package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestDelegatedAccountIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "panel.db"))
	db := database.GetDB()
	var root model.User
	if err := db.First(&root).Error; err != nil {
		t.Fatal(err)
	}

	engine := gin.New()
	engine.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("delegation-test-secret"))))
	engine.GET("/session/:id", func(c *gin.Context) {
		var u model.User
		if err := db.First(&u, c.Param("id")).Error; err != nil {
			t.Fatal(err)
		}
		if err := session.SetLoginUser(c, &u); err != nil {
			t.Fatal(err)
		}
		c.Status(http.StatusNoContent)
	})
	api := engine.Group("/panel/api")
	a := &APIController{}
	api.Use(a.checkAPIAuth, a.enforceTokenScope, enforcePanelRole)
	api.GET("/clients/list", func(c *gin.Context) { c.Status(http.StatusOK) })
	registerDelegation(api.Group("/delegation"))

	cookieFor := func(id int) *http.Cookie {
		t.Helper()
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("GET", "/session/"+strconv.Itoa(id), nil))
		if w.Code != http.StatusNoContent || len(w.Result().Cookies()) != 1 {
			t.Fatalf("no test session for %d", id)
		}
		return w.Result().Cookies()[0]
	}
	call := func(want int, user *http.Cookie, method, path, payload string) json.RawMessage {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/panel/api"+path, strings.NewReader(payload))
		r.AddCookie(user)
		r.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var envelope struct{ Obj json.RawMessage }
		_ = json.Unmarshal(w.Body.Bytes(), &envelope)
		return envelope.Obj
	}
	add := func(actor *http.Cookie, role, name string) delegatedAccountView {
		t.Helper()
		body := `{"username":"` + name + `","password":"test-only-password","role":"` + role + `"}`
		obj := call(200, actor, "POST", "/delegation/accounts", body)
		var account delegatedAccountView
		if err := json.Unmarshal(obj, &account); err != nil || account.ID < 1 {
			t.Fatalf("invalid account response %s: %v", obj, err)
		}
		return account
	}
	grant := func(actor *http.Cookie, to int, want int, body string) {
		t.Helper()
		call(want, actor, "POST", "/delegation/accounts/"+strconv.Itoa(to)+"/credit", body)
	}

	ownerSession := cookieFor(root.Id)
	admin := add(ownerSession, "admin", "branch-a")
	sudo := add(ownerSession, "sudo", "xray-reader")
	sudoSession := cookieFor(sudo.ID)
	adminSession := cookieFor(admin.ID)
	reseller := add(adminSession, "reseller", "shop-a")
	resellerSession := cookieFor(reseller.ID)
	const backupPath = "/delegation/telegram-backup"
	const botSecret = "12345:fake-test-token-0123456789"
	call(403, ownerSession, "GET", backupPath, "")
	call(400, resellerSession, "POST", backupPath, `{"enabled":true,"chatId":"-123456","botToken":"`+botSecret+`"}`)
	call(200, resellerSession, "POST", backupPath, `{"enabled":true,"chatId":"123456","botToken":"`+botSecret+`"}`)
	gotBackup := string(call(200, resellerSession, "GET", backupPath, ""))
	if !strings.Contains(gotBackup, `"hasToken":true`) || strings.Contains(gotBackup, botSecret) {
		t.Fatalf("unexpected backup settings: %s", gotBackup)
	}
	if other := string(call(200, adminSession, "GET", backupPath, "")); !strings.Contains(other, `"hasToken":false`) || strings.Contains(other, "123456") {
		t.Fatalf("backup settings exposed to parent: %s", other)
	}
	call(200, resellerSession, "POST", backupPath, `{"enabled":false,"chatId":"123456"}`)
	users := &panel.UserService{}
	if u, err := users.CheckUser(reseller.Username, "test-only-password", ""); err != nil || u.Id != reseller.ID {
		t.Fatalf("delegated user cannot authenticate via the panel login service: %v", err)
	}
	call(403, adminSession, "POST", "/delegation/accounts", `{"username":"elevated","password":"test-only-password","role":"sudo"}`)
	call(403, resellerSession, "GET", "/delegation/accounts", "")
	call(403, resellerSession, "GET", "/clients/list", "")
	call(403, adminSession, "GET", "/delegation/xray", "")
	call(200, sudoSession, "GET", "/delegation/xray", "")
	call(200, ownerSession, "GET", "/clients/list", "")

	grant(ownerSession, admin.ID, 200, `{"clients":2,"gib":5}`)
	grant(adminSession, sudo.ID, 404, `{"clients":1,"gib":1}`)
	grant(adminSession, reseller.ID, 409, `{"clients":1,"gib":6}`)
	grant(adminSession, reseller.ID, 200, `{"clients":2,"gib":5}`)
	grant(adminSession, reseller.ID, 409, `{"clients":1}`)
	var empty delegatedAccountView
	if err := json.Unmarshal(call(200, adminSession, "GET", "/delegation/me", ""), &empty); err != nil || empty.Clients != 0 || empty.GiB != 0 {
		t.Fatalf("transfer did not debit the parent: %+v %v", empty, err)
	}

	inbound := model.Inbound{
		UserId: root.Id, Remark: "Sales", Enable: true, Port: 34125, Protocol: model.VLESS,
		Tag: "inbound-34125", Settings: `{"clients":[],"decryption":"none"}`, StreamSettings: `{}`,
	}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	inboundID := strconv.Itoa(inbound.Id)
	makeClient := func(want int, name string, volume int) {
		t.Helper()
		call(want, resellerSession, "POST", "/delegation/clients",
			`{"email":"`+name+`","inboundId":`+inboundID+`,"gib":`+strconv.Itoa(volume)+`}`)
	}
	makeClient(404, "not-allowed", 1)
	call(403, adminSession, "POST", "/delegation/inbounds/"+inboundID, `{"allowed":true}`)
	call(200, ownerSession, "POST", "/delegation/inbounds/"+inboundID, `{"allowed":true}`)
	makeClient(404, "not-granted", 1)
	if string(call(200, resellerSession, "GET", "/delegation/inbound-options", "")) != "[]" {
		t.Fatal("ungranted inbound was exposed")
	}
	call(404, adminSession, "POST", "/delegation/accounts/"+strconv.Itoa(reseller.ID)+"/inbounds/"+inboundID, `{"allowed":true}`)
	call(200, ownerSession, "POST", "/delegation/accounts/"+strconv.Itoa(admin.ID)+"/inbounds/"+inboundID, `{"allowed":true}`)
	var options []service.InboundOption
	if err := json.Unmarshal(call(200, resellerSession, "GET", "/delegation/inbound-options", ""), &options); err != nil || len(options) != 1 || options[0].Id != inbound.Id {
		t.Fatalf("unexpected permitted options: %+v %v", options, err)
	}
	call(200, ownerSession, "POST", "/delegation/accounts/"+strconv.Itoa(admin.ID)+"/inbounds/"+inboundID, `{"allowed":false}`)
	var remainingGrants int64
	if err := db.Model(&model.DelegatedInboundGrant{}).Where("inbound_id = ?", inbound.Id).Count(&remainingGrants).Error; err != nil || remainingGrants != 0 {
		t.Fatalf("revocation left grants in the branch: %d %v", remainingGrants, err)
	}
	makeClient(404, "ancestor-revoked", 1)
	call(200, ownerSession, "POST", "/delegation/accounts/"+strconv.Itoa(admin.ID)+"/inbounds/"+inboundID, `{"allowed":true}`)
	makeClient(200, "shop-client-a", 3)
	makeClient(409, "over-traffic", 3)
	makeClient(409, "shop-client-a", 2)
	makeClient(200, "shop-client-b", 2)
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "shop-client-a").Updates(map[string]any{"up": 100, "down": 200}).Error; err != nil {
		t.Fatal(err)
	}
	grant(ownerSession, admin.ID, 200, "{\"gib\":1}")
	grant(adminSession, reseller.ID, 200, "{\"gib\":1}")
	makeClient(409, "over-count", 1)
	var clients []delegatedClientView
	if err := json.Unmarshal(call(200, resellerSession, "GET", "/delegation/clients", ""), &clients); err != nil || len(clients) != 2 {
		t.Fatalf("reseller client list: %+v %v", clients, err)
	}
	if clients[1].Email != "shop-client-a" || clients[1].Up != 100 || clients[1].Down != 200 {
		t.Fatalf("traffic missing from delegated list: %+v", clients)
	}
	if string(call(200, adminSession, "GET", "/delegation/clients", "")) != "[]" {
		t.Fatal("parent can see a subaccount's clients")
	}
	call(404, adminSession, "GET", "/delegation/clients/"+clients[0].SubID+"/links", "")
	grant(ownerSession, admin.ID, 200, `{"clients":30,"gib":3000}`)
	grant(adminSession, reseller.ID, 200, `{"clients":30,"gib":3000}`)
	var funded delegatedAccountView
	if err := json.Unmarshal(call(200, resellerSession, "GET", "/delegation/me", ""), &funded); err != nil || funded.Clients != 30 || funded.GiB != 3001 {
		t.Fatalf("large credit transfer failed: %+v %v", funded, err)
	}
	full := "/delegation/clients/full"
	call(400, resellerSession, "POST", full, `{"client":{"email":"unsafe","totalGB":1073741824,"reset":1},"inboundIds":[`+inboundID+`]}`)
	call(409, resellerSession, "POST", full, `{"client":{"email":"unlimited","totalGB":0},"inboundIds":[`+inboundID+`]}`)
	call(404, resellerSession, "POST", full, `{"client":{"email":"other-inbound","totalGB":1073741824},"inboundIds":[9999]}`)
	call(200, resellerSession, "POST", full, `{"client":{"email":"shared-editor","subId":"chosen-id","totalGB":1073741824,"enable":true,"comment":"test comment","expiryTime":2000000000000},"inboundIds":[`+inboundID+`]}`)
	var created model.ClientRecord
	if err := db.Where("email = ?", "shared-editor").First(&created).Error; err != nil || created.Comment != "test comment" || created.SubID == "chosen-id" || created.SubID == "" {
		t.Fatalf("shared editor client invalid: %+v %v", created, err)
	}
	if err := json.Unmarshal(call(200, resellerSession, "GET", "/delegation/me", ""), &funded); err != nil || funded.Clients != 29 || funded.GiB != 3000 {
		t.Fatalf("shared editor credit incorrect: %+v %v", funded, err)
	}
	shared := "/delegation/client-page"
	var page service.ClientPageResponse
	if err := json.Unmarshal(call(200, resellerSession, "GET", shared+"/list/paged?search=shop-client", ""), &page); err != nil || page.Total != 3 || page.Filtered != 2 || len(page.Items) != 2 {
		t.Fatalf("unexpected delegated page: %+v %v", page, err)
	}
	if err := json.Unmarshal(call(200, adminSession, "GET", shared+"/list/paged", ""), &page); err != nil || page.Total != 0 || len(page.Items) != 0 || page.Summary.Total != 0 {
		t.Fatalf("cross-account summary: %+v %v", page, err)
	}
	call(404, adminSession, "GET", shared+"/get/shop-client-a", "")
	call(404, adminSession, "POST", shared+"/update/shop-client-a", `{"email":"shop-client-a","totalGB":1073741824}`)
	call(404, adminSession, "POST", shared+"/del/shop-client-a", "")
	call(200, resellerSession, "POST", shared+"/update/shop-client-a", `{"email":"shop-client-a","enable":true,"totalGB":2147483648,"group":"Retail","subId":"not-the-original"}`)
	var record model.ClientRecord
	if err := db.Where("email = ?", "shop-client-a").First(&record).Error; err != nil || record.TotalGB != 2*gib || record.SubID != clients[1].SubID {
		t.Fatalf("unexpected client after edit: %+v %v", record, err)
	}
	var balance model.User
	if err := db.First(&balance, reseller.ID).Error; err != nil || balance.TrafficCredits != 3001*gib || balance.ClientCredits != 29 {
		t.Fatalf("edit refund incorrect: %+v %v", balance, err)
	}
	call(200, resellerSession, "POST", shared+"/del/shop-client-a", "")
	call(404, resellerSession, "POST", shared+"/del/shop-client-a", "")
	if err := db.First(&balance, reseller.ID).Error; err != nil || balance.TrafficCredits != 3003*gib-300 || balance.ClientCredits != 30 {
		t.Fatalf("delete refund incorrect: %+v %v", balance, err)
	}
	call(200, resellerSession, "POST", shared+"/groups/bulkAdd", `{"emails":["shop-client-b"],"group":"Retail"}`)
	if err := json.Unmarshal(call(200, resellerSession, "GET", shared+"/list/paged?group=Retail", ""), &page); err != nil || page.Filtered != 1 || page.Items[0].Group != "Retail" {
		t.Fatalf("scoped group filter: %+v %v", page, err)
	}
	call(200, adminSession, "POST", shared+"/bulkDisable", `{"emails":["shop-client-b"]}`)
	var enabled model.ClientRecord
	if err := db.Where("email = ?", "shop-client-b").First(&enabled).Error; err != nil || !enabled.Enable {
		t.Fatalf("unauthorized bulk disable changed record: %+v %v", enabled, err)
	}
	call(404, adminSession, "POST", shared+"/resetTraffic/shop-client-b", "")
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "shop-client-b").Update("up", 400).Error; err != nil {
		t.Fatal(err)
	}
	call(200, resellerSession, "POST", shared+"/resetTraffic/shop-client-b", "")
	var traffic xray.ClientTraffic
	if err := db.Where("email = ?", "shop-client-b").First(&traffic).Error; err != nil || traffic.Up != 0 {
		t.Fatalf("reset failed: %+v %v", traffic, err)
	}
	if err := db.First(&balance, reseller.ID).Error; err != nil || balance.TrafficCredits != 3003*gib-700 {
		t.Fatalf("reset debit incorrect: %+v %v", balance, err)
	}
	editPath := "/delegation/accounts/" + strconv.Itoa(reseller.ID) + "/edit"
	call(409, adminSession, "POST", editPath, `{"username":"shop-renamed","clients":1000000,"gib":1000000}`)
	call(200, adminSession, "POST", editPath, `{"username":"shop-managed","password":"managed-test-password"}`)
	call(404, resellerSession, "GET", "/delegation/me", "")
	if _, err := users.CheckUser("shop-managed", "managed-test-password", ""); err != nil {
		t.Fatalf("credentials set by parent rejected: %v", err)
	}
	leaf := add(adminSession, "admin", "branch-leaf")
	leafSession := cookieFor(leaf.ID)
	leafPath := "/delegation/accounts/" + strconv.Itoa(leaf.ID)
	call(404, sudoSession, "POST", leafPath+"/edit", `{"username":"other-branch"}`)
	call(404, sudoSession, "POST", leafPath+"/delete", `{}`)
	grant(ownerSession, admin.ID, 200, `{"clients":3,"gib":5}`)
	var before, after model.User
	if err := db.First(&before, admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	call(200, adminSession, "POST", leafPath+"/edit", `{"username":"branch-leaf","clients":3,"gib":5}`)
	call(200, adminSession, "POST", leafPath+"/edit", `{"username":"branch-leaf","clients":0,"gib":2}`)
	if err := db.First(&after, admin.ID).Error; err != nil || after.ClientCredits != before.ClientCredits || after.TrafficCredits != before.TrafficCredits-2*gib {
		t.Fatalf("parent balance after granting and reclaiming credit: %+v, %v", after, err)
	}
	var adjusted model.User
	if err := db.First(&adjusted, leaf.ID).Error; err != nil || adjusted.ClientCredits != 0 || adjusted.TrafficCredits != 2*gib {
		t.Fatalf("child balance after edit: %+v, %v", adjusted, err)
	}
	nested := add(leafSession, "reseller", "nested-shop")
	nestedSession := cookieFor(nested.ID)
	nestedPath := "/delegation/accounts/" + strconv.Itoa(nested.ID)
	if err := json.Unmarshal(call(200, nestedSession, "GET", "/delegation/inbound-options", ""), &options); err != nil || len(options) != 1 {
		t.Fatalf("new descendants did not inherit the grant: %+v %v", options, err)
	}
	grant(adminSession, leaf.ID, 200, `{"clients":1}`)
	grant(leafSession, nested.ID, 200, `{"clients":1,"gib":1}`)
	call(200, nestedSession, "POST", full, `{"client":{"email":"nested-branch-client","totalGB":1073741824},"inboundIds":[`+inboundID+`]}`)
	var nestedClient model.ClientRecord
	if err := db.Where("email = ?", "nested-branch-client").First(&nestedClient).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", nestedClient.Email).Update("up", 300).Error; err != nil {
		t.Fatal(err)
	}
	assertNestedAttachment := func(attached bool) {
		t.Helper()
		var count int64
		if err := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", nestedClient.Id, inbound.Id).Count(&count).Error; err != nil || (count == 1) != attached {
			t.Fatalf("nested client attachment: count %d, want attached %v: %v", count, attached, err)
		}
		var persisted model.Inbound
		if err := db.First(&persisted, inbound.Id).Error; err != nil || strings.Contains(persisted.Settings, "nested-branch-client") != attached || !strings.Contains(persisted.Settings, "shop-client-b") {
			t.Fatalf("unexpected Xray settings after branch change: %s: %v", persisted.Settings, err)
		}
		var row model.ClientRecord
		if err := db.First(&row, nestedClient.Id).Error; err != nil || row.SubID != nestedClient.SubID || row.TotalGB != gib {
			t.Fatalf("branch change modified the client identity or quota: %+v %v", row, err)
		}
		var usage xray.ClientTraffic
		if err := db.Where("email = ?", nestedClient.Email).First(&usage).Error; err != nil || usage.Up != 300 {
			t.Fatalf("branch change modified client traffic: %+v %v", usage, err)
		}
	}
	leafInboundPath := leafPath + "/inbounds/" + inboundID
	call(200, ownerSession, "POST", leafInboundPath, `{"allowed":false}`)
	assertNestedAttachment(false)
	if string(call(200, nestedSession, "GET", "/delegation/inbound-options", "")) != "[]" {
		t.Fatal("descendant retained revoked inbound")
	}
	if err := json.Unmarshal(call(200, adminSession, "GET", "/delegation/inbound-options", ""), &options); err != nil || len(options) != 1 {
		t.Fatalf("revocation affected the parent: %+v %v", options, err)
	}
	call(200, ownerSession, "POST", leafInboundPath, `{"allowed":true}`)
	assertNestedAttachment(true)
	if err := json.Unmarshal(call(200, nestedSession, "GET", "/delegation/inbound-options", ""), &options); err != nil || len(options) != 1 {
		t.Fatalf("descendant did not regain inbound: %+v %v", options, err)
	}
	var branch []delegatedAccountView
	if err := json.Unmarshal(call(200, adminSession, "GET", "/delegation/accounts", ""), &branch); err != nil ||
		len(branch) != 3 || branch[2].ID != nested.ID || branch[2].ParentID != leaf.ID {
		t.Fatalf("incorrect account tree: %+v, %v", branch, err)
	}
	call(200, adminSession, "POST", nestedPath+"/edit", `{"username":"nested-renamed"}`)
	call(404, adminSession, "POST", nestedPath+"/edit", `{"username":"nested-renamed","gib":10}`)
	call(404, sudoSession, "POST", leafPath+"/delete", `{"cascade":true}`)
	call(200, adminSession, "POST", leafPath+"/delete", `{"cascade":true}`)
	if err := db.First(&after, admin.ID).Error; err != nil || after.ClientCredits != before.ClientCredits || after.TrafficCredits != before.TrafficCredits-300 {
		t.Fatalf("balances from both levels were not returned to the surviving admin: %+v, %v", after, err)
	}
	call(404, leafSession, "GET", "/delegation/me", "")
	call(404, nestedSession, "GET", "/delegation/me", "")
	call(409, adminSession, "POST", "/delegation/accounts/"+strconv.Itoa(reseller.ID)+"/delete", `{}`)
	call(200, ownerSession, "POST", editPath, `{"username":"shop-renamed","password":"another-test-password","clients":12,"gib":100}`)
	if _, err := users.CheckUser("shop-renamed", "another-test-password", ""); err != nil {
		t.Fatalf("new credentials rejected: %v", err)
	}
	call(404, resellerSession, "GET", "/delegation/me", "")
	call(409, ownerSession, "POST", "/delegation/accounts/"+strconv.Itoa(reseller.ID)+"/delete", `{}`)
	call(409, ownerSession, "POST", "/delegation/accounts/"+strconv.Itoa(admin.ID)+"/delete", `{}`)
	activeResellerSession := cookieFor(reseller.ID)
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "shop-client-b").Update("up", 400).Error; err != nil {
		t.Fatal(err)
	}
	resellerDelete := "/delegation/accounts/" + strconv.Itoa(reseller.ID) + "/delete"
	call(200, ownerSession, "POST", resellerDelete, `{"cascade":true}`)
	call(404, ownerSession, "POST", resellerDelete, `{"cascade":true}`)
	if err := db.First(&after, admin.ID).Error; err != nil || after.ClientCredits != before.ClientCredits+14 || after.TrafficCredits != before.TrafficCredits+103*gib-700 {
		t.Fatalf("delete should refund free balances plus two slots and unused client traffic to the actual parent: %+v, %v", after, err)
	}
	for range 2 {
		call(200, adminSession, "POST", full, `{"client":{"displayName":"Same customer","totalGB":1073741824},"inboundIds":[`+inboundID+`]}`)
	}
	if err := json.Unmarshal(call(200, adminSession, "GET", shared+"/list/paged?search=Same+customer", ""), &page); err != nil ||
		page.Filtered != 2 || len(page.Items) != 2 || page.Items[0].Email == page.Items[1].Email ||
		page.Items[0].DisplayName != "Same customer" || page.Items[1].DisplayName != "Same customer" ||
		page.Items[0].OwnerLabel != admin.Username {
		t.Fatalf("duplicate names or automatic owner label: %+v %v", page, err)
	}
	var paging service.ClientService
	branchPage, err := paging.ListPaged(&service.InboundService{}, nil, service.ClientPageParams{Search: "owner:" + root.Username})
	if err != nil || branchPage.Filtered != 2 {
		t.Fatalf("owner branch filter: %+v %v", branchPage, err)
	}
	if err := json.Unmarshal(call(200, sudoSession, "GET", shared+"/list/paged?search=owner:"+admin.Username, ""), &page); err != nil || page.Filtered != 0 {
		t.Fatalf("branch filter exposed clients to another operator: %+v %v", page, err)
	}
	call(200, ownerSession, "POST", "/delegation/accounts/"+strconv.Itoa(admin.ID)+"/delete", `{"cascade":true}`)
	call(404, activeResellerSession, "GET", "/delegation/me", "")
	call(404, adminSession, "GET", "/delegation/me", "")
	for _, record := range []struct {
		model any
		where string
		arg   any
	}{
		{&model.ClientRecord{}, "email = ?", "shop-client-b"},
		{&xray.ClientTraffic{}, "email = ?", "shop-client-b"},
		{&model.DelegatedClient{}, "owner_id = ?", reseller.ID},
		{&model.DelegatedInboundGrant{}, "user_id = ?", reseller.ID},
	} {
		var count int64
		if err := db.Model(record.model).Where(record.where, record.arg).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("branch deletion left %T: %d, %v", record.model, count, err)
		}
	}
	call(200, sudoSession, "GET", "/delegation/me", "")
	sudoPath := "/delegation/accounts/" + strconv.Itoa(sudo.ID)
	call(200, ownerSession, "POST", sudoPath+"/inbounds/"+inboundID, `{"allowed":true}`)
	transferAdmin := add(sudoSession, "admin", "transfer-admin")
	transferAdminSession := cookieFor(transferAdmin.ID)
	destination := add(transferAdminSession, "reseller", "transfer-destination")
	destinationSession := cookieFor(destination.ID)
	grant(ownerSession, sudo.ID, 200, `{"clients":5,"gib":5}`)
	call(200, sudoSession, "POST", full, `{"client":{"displayName":"Transferred","totalGB":1073741824},"inboundIds":[`+inboundID+`]}`)
	var moved model.ClientRecord
	if err := db.Where("display_name = ?", "Transferred").First(&moved).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", moved.Email).Update("up", 123).Error; err != nil {
		t.Fatal(err)
	}
	call(200, sudoSession, "POST", shared+"/groups/bulkAdd", `{"emails":["`+moved.Email+`"],"group":"Source group"}`)
	targetsPath := shared + "/assign-targets/" + moved.Email
	assignPath := shared + "/assign/" + moved.Email
	moveTo := func(id int) string { return `{"accountId":` + strconv.Itoa(id) + `}` }
	var targets []struct {
		ID        int
		HasCredit bool
	}
	if err := json.Unmarshal(call(200, sudoSession, "GET", targetsPath, ""), &targets); err != nil ||
		len(targets) != 2 || targets[1].ID != destination.ID || targets[1].HasCredit {
		t.Fatalf("unexpected targets: %+v %v", targets, err)
	}
	call(403, transferAdminSession, "GET", targetsPath, "")
	call(403, destinationSession, "POST", assignPath, moveTo(transferAdmin.ID))
	call(404, sudoSession, "POST", assignPath, moveTo(root.Id))
	call(409, sudoSession, "POST", assignPath, moveTo(destination.ID))
	grant(sudoSession, transferAdmin.ID, 200, `{"clients":2,"gib":2}`)
	grant(transferAdminSession, destination.ID, 200, `{"clients":2,"gib":2}`)
	destinationInbound := "/delegation/accounts/" + strconv.Itoa(destination.ID) + "/inbounds/" + inboundID
	call(200, transferAdminSession, "POST", destinationInbound, `{"allowed":false}`)
	call(409, sudoSession, "POST", assignPath, moveTo(destination.ID))
	call(200, transferAdminSession, "POST", destinationInbound, `{"allowed":true}`)
	call(200, sudoSession, "POST", assignPath, moveTo(destination.ID))
	call(404, sudoSession, "GET", shared+"/get/"+moved.Email, "")
	call(200, destinationSession, "GET", shared+"/get/"+moved.Email, "")
	var saved model.ClientRecord
	var usage xray.ClientTraffic
	if err := db.First(&saved, moved.Id).Error; err != nil || saved.SubID != moved.SubID ||
		saved.UUID != moved.UUID || saved.TotalGB != gib || saved.Group != "" {
		t.Fatalf("transfer modified identity/quota or retained group: %+v, %v", saved, err)
	}
	if err := db.Where("email = ?", moved.Email).First(&usage).Error; err != nil || usage.Up != 123 {
		t.Fatalf("transfer modified usage: %+v, %v", usage, err)
	}
	var sourceBalance, destinationBalance model.User
	if err := db.First(&sourceBalance, sudo.ID).Error; err != nil || sourceBalance.ClientCredits != 3 || sourceBalance.TrafficCredits != 3*gib-123 {
		t.Fatalf("source refund incorrect: slots=%d traffic=%d (%v)", sourceBalance.ClientCredits, sourceBalance.TrafficCredits, err)
	}
	if err := db.First(&destinationBalance, destination.ID).Error; err != nil || destinationBalance.ClientCredits != 1 || destinationBalance.TrafficCredits != gib+123 {
		t.Fatalf("destination debit incorrect: slots=%d traffic=%d (%v)", destinationBalance.ClientCredits, destinationBalance.TrafficCredits, err)
	}
	call(200, ownerSession, "POST", assignPath, moveTo(root.Id))
	call(404, destinationSession, "GET", shared+"/get/"+moved.Email, "")
	call(200, ownerSession, "POST", assignPath, moveTo(transferAdmin.ID))
	call(200, transferAdminSession, "GET", shared+"/get/"+moved.Email, "")
	call(200, transferAdminSession, "POST", shared+"/del/"+moved.Email, "")
	var emptyBalance model.User
	if err := db.First(&emptyBalance, transferAdmin.ID).Error; err != nil || emptyBalance.ClientCredits != 0 || emptyBalance.TrafficCredits != 0 {
		t.Fatalf("free transfer created spendable credit: slots=%d traffic=%d (%v)", emptyBalance.ClientCredits, emptyBalance.TrafficCredits, err)
	}
	call(403, transferAdminSession, "POST", "/delegation/accounts", `{"role":"infinity","username":"invalid-infinity","password":"test-only-password"}`)
	infinity := add(sudoSession, "infinity", "infinity-parent")
	infinitySession := cookieFor(infinity.ID)
	call(200, infinitySession, "GET", "/delegation/xray", "")
	infinityAdmin := add(infinitySession, "admin", "infinity-admin")
	infinityReseller := add(infinitySession, "reseller", "infinity-reseller")
	infinityChild := add(infinitySession, "infinity", "infinity-child")
	infinityChildSession := cookieFor(infinityChild.ID)
	grant(infinitySession, infinityAdmin.ID, 200, `{"clients":5,"gib":5}`)
	call(200, infinitySession, "POST", "/delegation/accounts/"+strconv.Itoa(infinityReseller.ID)+"/edit",
		`{"username":"infinity-reseller","clients":4,"gib":4}`)
	call(200, infinitySession, "POST", full, `{"client":{"displayName":"Unlimited","totalGB":0},"inboundIds":[`+inboundID+`]}`)
	call(200, infinitySession, "POST", full, `{"client":{"displayName":"Limited","totalGB":1073741824},"inboundIds":[`+inboundID+`]}`)
	var freeClients []model.ClientRecord
	if err := ownedClients(db, infinity.ID).Select("clients.*").Order("clients.id").Find(&freeClients).Error; err != nil || len(freeClients) != 2 ||
		freeClients[0].TotalGB != 0 || freeClients[1].TotalGB != gib {
		t.Fatalf("limited and unlimited issuance: clients=%d (%v)", len(freeClients), err)
	}
	freeEmail := freeClients[0].Email
	limitedEmail := freeClients[1].Email
	call(200, infinitySession, "POST", shared+"/update/"+limitedEmail,
		`{"email":"`+limitedEmail+`","enable":true,"totalGB":2147483648}`)
	call(200, infinitySession, "POST", shared+"/assign/"+limitedEmail, moveTo(infinityReseller.ID))
	call(200, cookieFor(infinityReseller.ID), "GET", shared+"/get/"+limitedEmail, "")
	call(200, infinitySession, "POST", shared+"/assign/"+freeEmail, moveTo(infinityChild.ID))
	call(200, infinityChildSession, "GET", shared+"/get/"+freeEmail, "")
	call(200, infinitySession, "GET", shared+"/get/"+freeEmail, "")
	grandchild := add(cookieFor(infinityAdmin.ID), "reseller", "infinity-grandchild")
	grandchildSession := cookieFor(grandchild.ID)
	grant(cookieFor(infinityAdmin.ID), grandchild.ID, 200, `{"clients":1,"gib":1}`)
	call(200, grandchildSession, "POST", full, `{"client":{"email":"grandchild-client","group":"Branch group","totalGB":1073741824},"inboundIds":[`+inboundID+`]}`)
	if err := json.Unmarshal(call(200, infinitySession, "GET", shared+"/list/paged?group=Branch+group", ""), &page); err != nil ||
		page.Total != 3 || page.Filtered != 1 || len(page.Items) != 1 ||
		page.Items[0].Email != "grandchild-client" || page.Items[0].OwnerLabel != grandchild.Username ||
		!page.Items[0].ReadOnly || page.Items[0].SubID != "" || page.Items[0].Group != "Branch group" {
		t.Fatalf("infinity branch listing incorrect: %+v, %v", page, err)
	}
	if err := json.Unmarshal(call(200, infinitySession, "GET", shared+"/list/paged?search="+freeClients[0].SubID, ""), &page); err != nil || page.Filtered != 0 {
		t.Fatalf("branch search exposed a subscription token: %v", err)
	}
	for _, actorSession := range []*http.Cookie{cookieFor(infinityAdmin.ID), sudoSession} {
		if err := json.Unmarshal(call(200, actorSession, "GET", shared+"/list/paged", ""), &page); err != nil || page.Total != 0 {
			t.Fatalf("a non-infinity role saw descendants: total=%d, %v", page.Total, err)
		}
	}
	call(404, infinitySession, "POST", shared+"/del/grandchild-client", "")
	editGrandchild := shared + "/update/grandchild-client"
	assignGrandchild := shared + "/assign/grandchild-client"
	editTraffic := `{"email":"grandchild-client","totalGB":2147483648,"enable":true,"group":"Branch group"}`
	call(404, infinityChildSession, "POST", editGrandchild, editTraffic)
	call(404, infinityChildSession, "POST", assignGrandchild, moveTo(infinityChild.ID))
	call(404, sudoSession, "POST", editGrandchild, editTraffic)
	call(409, infinitySession, "POST", editGrandchild, editTraffic)
	grant(cookieFor(infinityAdmin.ID), grandchild.ID, 200, `{"clients":0,"gib":1}`)
	call(200, infinitySession, "POST", editGrandchild, editTraffic)
	call(200, infinitySession, "POST", shared+"/grandchild-client/externalLinks", `{"externalLinks":[]}`)
	call(200, infinitySession, "GET", shared+"/assign-targets/grandchild-client", "")
	call(404, infinitySession, "POST", assignGrandchild, moveTo(transferAdmin.ID))
	call(200, infinitySession, "POST", assignGrandchild, moveTo(infinity.ID))
	call(404, grandchildSession, "GET", shared+"/get/grandchild-client", "")
	call(200, infinitySession, "GET", shared+"/get/grandchild-client", "")
	var grandchildBalance model.User
	if err := db.First(&grandchildBalance, grandchild.ID).Error; err != nil || grandchildBalance.ClientCredits != 1 || grandchildBalance.TrafficCredits != 2*gib {
		t.Fatalf("transfer did not refund actual owner: slots=%d traffic=%d (%v)", grandchildBalance.ClientCredits, grandchildBalance.TrafficCredits, err)
	}
	call(200, sudoSession, "POST", "/delegation/accounts/"+strconv.Itoa(infinity.ID)+"/delete", `{"cascade":true}`)
	if err := db.First(&sourceBalance, sudo.ID).Error; err != nil || sourceBalance.ClientCredits != 3 || sourceBalance.TrafficCredits != 3*gib-123 {
		t.Fatalf("infinity branch increased metered credits: slots=%d traffic=%d (%v)", sourceBalance.ClientCredits, sourceBalance.TrafficCredits, err)
	}
	call(200, ownerSession, "POST", sudoPath+"/delete", `{"cascade":true}`)
	call(404, sudoSession, "GET", "/delegation/me", "")

	mixed := add(ownerSession, "admin", "mixed-quota")
	mixedSession := cookieFor(mixed.ID)
	mixedPath := "/delegation/accounts/" + strconv.Itoa(mixed.ID)
	call(200, mixedSession, "POST", shared+"/defaults", "")
	call(200, ownerSession, "POST", mixedPath+"/inbounds/"+inboundID, `{"allowed":true}`)
	call(200, ownerSession, "POST", mixedPath+"/edit",
		`{"username":"mixed-quota","clients":3,"gib":1,"unlimitedClientLimit":1}`)
	shop := add(mixedSession, "reseller", "volume-only")
	shopSession := cookieFor(shop.ID)
	shopPath := "/delegation/accounts/" + strconv.Itoa(shop.ID)
	call(403, mixedSession, "POST", shopPath+"/edit", `{"username":"volume-only","uncappedClientSlots":true}`)
	createQuotaClient := func(want int, actor *http.Cookie, name, bytes string) {
		t.Helper()
		call(want, actor, "POST", full, `{"client":{"email":"`+name+`","totalGB":`+bytes+`},"inboundIds":[`+inboundID+`]}`)
	}
	createQuotaClient(200, mixedSession, "unlimited-a", "0")
	createQuotaClient(409, mixedSession, "unlimited-b", "0")
	createQuotaClient(200, mixedSession, "finite-a", "1073741824")
	createQuotaClient(409, mixedSession, "finite-b", "1")
	call(200, mixedSession, "POST", shared+"/del/unlimited-a", "")
	createQuotaClient(200, mixedSession, "unlimited-b", "0")
	call(200, ownerSession, "POST", shopPath+"/edit", `{"username":"volume-only","uncappedClientSlots":true,"gib":1}`)
	createQuotaClient(409, shopSession, "volume-unlimited", "0")
	createQuotaClient(200, shopSession, "volume-a", "536870912")
	createQuotaClient(200, shopSession, "volume-b", "536870912")
	createQuotaClient(409, shopSession, "volume-c", "1")
	call(200, shopSession, "POST", shared+"/del/volume-a", "")
	var shopBalance model.User
	if err := db.First(&shopBalance, shop.ID).Error; err != nil || shopBalance.ClientCredits != 0 || shopBalance.TrafficCredits != gib/2 {
		t.Fatalf("volume-only refunds: slots=%d traffic=%d (%v)", shopBalance.ClientCredits, shopBalance.TrafficCredits, err)
	}
	call(409, mixedSession, "POST", shopPath+"/edit", `{"username":"volume-only","unlimitedClientLimit":1}`)
	call(200, ownerSession, "POST", mixedPath+"/edit", `{"username":"mixed-quota","unlimitedClientLimit":4}`)
	call(200, mixedSession, "POST", shopPath+"/edit", `{"username":"volume-only","unlimitedClientLimit":1}`)
	var delegatedChild delegatedAccountView
	createdChild := call(200, mixedSession, "POST", "/delegation/accounts",
		`{"username":"quota-child","role":"admin","password":"test-only-password","unlimitedClientLimit":2}`)
	if err := json.Unmarshal(createdChild, &delegatedChild); err != nil || delegatedChild.UnlimitedClientLimit != 2 {
		t.Fatalf("unexpected child allocation: %s (%v)", createdChild, err)
	}
	childSession := cookieFor(delegatedChild.ID)
	call(200, childSession, "POST", "/delegation/accounts",
		`{"username":"quota-grandchild","role":"reseller","password":"test-only-password","unlimitedClientLimit":2}`)
	call(409, ownerSession, "POST", mixedPath+"/edit", `{"username":"mixed-quota","unlimitedClientLimit":3}`)
	createQuotaClient(409, mixedSession, "mixed-at-capacity", "0")
	createQuotaClient(200, shopSession, "shop-allocated", "0")
	createQuotaClient(409, shopSession, "shop-over-capacity", "0")
	childPath := "/delegation/accounts/" + strconv.Itoa(delegatedChild.ID)
	call(200, mixedSession, "POST", childPath+"/delete", `{"cascade":true}`)
	createQuotaClient(200, mixedSession, "mixed-after-deletion", "0")
}
