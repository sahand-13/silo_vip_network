package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

// Serialize delegated mutations that span the accounting database and the
// existing client service's inbound/runtime writes.
var delegatedClientWrite sync.Mutex

func (d *delegationController) sharedClientRoutes(g *gin.RouterGroup) {
	g.GET("/list/paged", d.scopedClientPage)
	g.GET("/get/:email", d.scopedClientDetails)
	g.GET("/assign-targets/:email", d.assignmentTargets)
	g.POST("/assign/:email", d.assignScopedClient)
	g.GET("/subLinks/:subId", d.links)
	g.POST("/add", d.createClientFull)
	g.POST("/update/:email", d.editScopedClient)
	g.POST("/del/:email", d.deleteScopedClient)
	g.POST("/onlines", d.scopedOnlines)
	g.POST("/defaults", d.scopedClientDefaults)
	g.POST("/bulkDel", d.bulkDeleteScoped)
	g.POST("/resetTraffic/:email", d.scopedReset(false))
	g.POST("/resetAllTraffics", d.scopedReset(true))
	g.POST("/bulkCreate", d.scopedBatchCreate(false))
	g.POST("/import", d.scopedBatchCreate(true))
	g.GET("/export", d.scopedExport)
	g.POST("/bulkEnable", d.scopedBatch("enable"))
	g.POST("/bulkDisable", d.scopedBatch("disable"))
	g.POST("/bulkAdjust", d.scopedBatch("adjust"))
	g.POST("/groups/bulkAdd", d.scopedBatch("group"))
	g.POST("/groups/bulkRemove", d.scopedBatch("ungroup"))
	g.POST("/bulkAttach", d.scopedBatch("attach"))
	g.POST("/bulkDetach", d.scopedBatch("detach"))
	g.POST("/:email/attach", d.scopedAttachments(true))
	g.POST("/:email/detach", d.scopedAttachments(false))
	g.POST("/delOrphans", d.scopedCleanup(true))
	g.POST("/delDepleted", d.scopedCleanup(false))
	a := &ClientController{}
	g.POST("/renewalPreview", a.renewalPreview)
	a.happGenerator = service.NewHappService(&a.clientService, &a.settingService)
	g.POST("/happLink/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id < 1 {
			delegationError(c, errDelegationInput)
			return
		}
		var count int64
		if err := ownedClients(database.GetDB(), delegatedUser(c).Id).Where("clients.id = ?", id).Count(&count).Error; err != nil {
			delegationError(c, err)
			return
		}
		if count != 1 {
			delegationError(c, gorm.ErrRecordNotFound)
			return
		}
		a.generateHappLink(c)
	})
	g.POST("/ips/:email", onOwnedClient(a.getIps))
	g.POST("/clearIps/:email", onOwnedClient(a.clearIps))
	g.POST("/hwids/:email", onOwnedClient(a.getHwids))
	g.DELETE("/hwids/:email", onOwnedClient(a.clearHwids))
	g.DELETE("/hwids/:email/:id", onOwnedClient(a.deleteHwid))
	g.POST("/:email/externalLinks", d.scopedExternalLinks)
}

func (d *delegationController) scopedClientPage(c *gin.Context) {
	var params service.ClientPageParams
	if c.ShouldBindQuery(&params) != nil {
		delegationFailure(c, 400, "invalid_input")
		return
	}
	actor := delegatedUser(c)
	var descendants *gorm.DB
	if actor.HasUnlimitedClients() {
		descendants = accountDescendants(database.GetDB(), actor.Id)
	}
	rows, err := d.ClientService.ListPagedForOwnerBranch(&d.InboundService, &d.SettingService, params, actor.Id, descendants)
	if err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, rows, nil)
}

func (d *delegationController) scopedClientDefaults(c *gin.Context) {
	raw, err := d.GetDelegatedClientDefaults(c.Request.Host)
	if err != nil {
		delegationError(c, err)
		return
	}
	source, _ := raw.(map[string]any)
	out := gin.H{}
	for _, key := range []string{"happLinkEnable", "ipLimitEnable", "datepicker", "subEnable", "subURI", "subJsonURI", "subJsonEnable", "subClashURI", "subClashEnable", "expireDiff", "trafficDiff", "pageSize"} {
		if v, ok := source[key]; ok {
			out[key] = v
		}
	}
	jsonObj(c, out, nil)
}

func (d *delegationController) scopedOnlines(c *gin.Context) {
	online := d.GetOnlineClients()
	out := []string{}
	if len(online) > 0 {
		scope := editableClients(database.GetDB(), delegatedUser(c))
		if err := scope.Where("clients.email IN ?", online).Pluck("clients.email", &out).Error; err != nil {
			delegationError(c, err)
			return
		}
	}
	jsonObj(c, out, nil)
}

func findScopedClient(c *gin.Context) (*model.ClientRecord, error) {
	return findOwnedClient(database.GetDB(), delegatedUser(c).Id, c.Param("email"))
}

func findOwnedClient(db *gorm.DB, owner int, email string) (*model.ClientRecord, error) {
	var rec model.ClientRecord
	err := ownedClients(db, owner).Select("clients.*").Where("clients.email = ?", email).First(&rec).Error
	return &rec, err
}

func (d *delegationController) scopedClientDetails(c *gin.Context) {
	rec, owner, err := editableClient(database.GetDB(), delegatedUser(c), c.Param("email"))
	if err != nil {
		delegationError(c, err)
		return
	}
	a := &ClientController{}
	rec.Group = model.DisplayDelegatedGroupName(owner.Id, rec.Group)
	payload, err := a.buildClientPayload(rec)
	if err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, payload, nil)
}

func availableTraffic(total, up, down int64) int64 {
	return max(0, max(0, total-max(0, up))-max(0, down))
}

func addDelegatedBalance(tx *gorm.DB, owner int, slots, bytes int64) error {
	if slots == 0 && bytes == 0 {
		return nil
	}
	return tx.Model(&model.User{}).Where("id = ?", owner).Updates(map[string]any{
		"client_credits":  gorm.Expr("client_credits + ?", slots),
		"traffic_credits": gorm.Expr("traffic_credits + ?", bytes),
	}).Error
}

func refundableClientSlots(link model.DelegatedClient) int64 {
	if link.FreeSlot {
		return 0
	}
	return 1
}

func (d *delegationController) editScopedClient(c *gin.Context) {
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	rec, owner, err := editableClient(database.GetDB(), delegatedUser(c), c.Param("email"))
	if err != nil {
		delegationError(c, err)
		return
	}
	var body struct {
		model.Client
		LimitHwid int `json:"limitHwid"`
	}
	if c.ShouldBindJSON(&body) != nil ||
		body.Reset != 0 || body.ResetDay != 0 || body.ResetWeekday != 0 || body.ResetMax != 0 ||
		(body.TrafficReset != "" && body.TrafficReset != "never") || len(body.ClientHostRuleIds) != 0 {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	if err := d.updateOwnedClient(owner, rec, body.Client, body.LimitHwid); err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, nil, nil)
}

func (d *delegationController) updateOwnedClient(u *model.User, rec *model.ClientRecord, body model.Client, limitHwid int) error {
	if body.TotalGB < 0 || body.TotalGB > maxTraffic {
		return errDelegationInput
	}
	if body.TotalGB == 0 && rec.TotalGB > 0 {
		if err := checkUnlimitedClientCapacity(database.GetDB(), u); err != nil {
			return err
		}
	}
	body.SubID = rec.SubID
	body.ClientHostRuleIds = nil
	body.Reverse = nil
	body.Reset, body.ResetDay, body.ResetWeekday, body.ResetMax = 0, 0, 0, 0
	body.TrafficReset, body.TrafficResetDay = "never", 1
	body.Group = model.DelegatedGroupName(u.Id, model.DisplayDelegatedGroupName(u.Id, body.Group))
	owner := u.Id
	db := database.GetDB()
	var link model.DelegatedClient
	if err := db.First(&link, "sub_id = ? AND owner_id = ?", rec.SubID, owner).Error; err != nil {
		return err
	}
	reserve := max(0, body.TotalGB-rec.TotalGB)
	if reserve > 0 || (body.TotalGB == 0 && rec.TotalGB > 0) {
		ids, err := d.GetInboundIdsForRecord(rec.Id)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return gorm.ErrRecordNotFound
		}
		for _, id := range ids {
			allowed, err := inboundAccess(db, u, id)
			if err != nil {
				return err
			}
			if !allowed {
				return gorm.ErrRecordNotFound
			}
		}
		if !u.HasUnlimitedClients() {
			if err := debitCredits(db, owner, 0, reserve); err != nil {
				return err
			}
		}
	}
	restart, updateErr := d.ClientService.Update(&d.InboundService, rec.Id, body, limitHwid)
	if restart {
		d.SetToNeedRestart()
	}
	persisted, err := d.GetByID(rec.Id)
	if err != nil {
		return err
	}
	if u.HasUnlimitedClients() {
		if err := db.Model(&link).Update("free_traffic", persisted.TotalGB).Error; err != nil {
			return err
		}
		if updateErr != nil {
			return errDelegationRejected
		}
		notifyClientsChanged()
		return nil
	}
	var refund int64
	if persisted.TotalGB < rec.TotalGB {
		traffic, err := d.GetClientTrafficByEmail(persisted.Email)
		if err != nil {
			return err
		}
		if traffic != nil {
			refund = max(0, availableTraffic(rec.TotalGB, traffic.Up, traffic.Down)-link.FreeTraffic) -
				max(0, availableTraffic(persisted.TotalGB, traffic.Up, traffic.Down)-link.FreeTraffic)
		}
		if persisted.TotalGB < link.FreeTraffic {
			if err := db.Model(&link).Update("free_traffic", persisted.TotalGB).Error; err != nil {
				return err
			}
		}
	} else {
		refund = max(0, reserve-max(0, persisted.TotalGB-rec.TotalGB))
	}
	if refund > 0 {
		if err := addDelegatedBalance(db, owner, 0, refund); err != nil {
			return err
		}
	}
	if updateErr != nil {
		return errDelegationRejected
	}
	notifyClientsChanged()
	return nil
}

func (d *delegationController) removeOwnedClient(rec *model.ClientRecord, owner int) error {
	restart, err := d.ClientService.DeleteWithFinalizer(&d.InboundService, rec.Id, false, func(tx *gorm.DB) error {
		var link model.DelegatedClient
		if err := tx.First(&link, "sub_id = ? AND owner_id = ?", rec.SubID, owner).Error; err != nil {
			return err
		}
		res := tx.Where("sub_id = ? AND owner_id = ?", rec.SubID, owner).Delete(&model.DelegatedClient{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		unused, err := delegatedTrafficRemaining(tx, rec)
		if err != nil {
			return err
		}
		return addDelegatedBalance(tx, owner, refundableClientSlots(link), max(0, unused-link.FreeTraffic))
	})
	if restart {
		d.SetToNeedRestart()
	}
	if err == nil || restart {
		notifyClientsChanged()
	}
	return err
}

func (d *delegationController) deleteScopedClient(c *gin.Context) {
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	rec, err := findScopedClient(c)
	if err == nil {
		err = d.removeOwnedClient(rec, delegatedUser(c).Id)
	}
	if err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, nil, nil)
}

func (d *delegationController) bulkDeleteScoped(c *gin.Context) {
	var body struct {
		Emails []string `json:"emails"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.Emails) > 200 {
		delegationFailure(c, 400, "invalid_input")
		return
	}
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	skipped := []gin.H{}
	owner := delegatedUser(c).Id
	ok := 0
	for _, email := range uniqueClientEmails(body.Emails) {
		var rec model.ClientRecord
		err := ownedClients(database.GetDB(), owner).Select("clients.*").Where("clients.email = ?", email).First(&rec).Error
		if err == nil {
			err = d.removeOwnedClient(&rec, owner)
		}
		if err == nil {
			ok++
		} else {
			skipped = append(skipped, gin.H{"email": email, "reason": "client_rejected"})
		}
	}
	jsonObj(c, gin.H{"deleted": ok, "skipped": skipped}, nil)
}

var (
	errDelegationInput    = errors.New("invalid_input")
	errDelegationRejected = errors.New("client_rejected")
)

func onOwnedClient(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		delegatedClientWrite.Lock()
		defer delegatedClientWrite.Unlock()
		if _, err := findScopedClient(c); err != nil {
			delegationError(c, err)
			return
		}
		handler(c)
	}
}

func (d *delegationController) scopedAttachments(attach bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		delegatedClientWrite.Lock()
		defer delegatedClientWrite.Unlock()
		rec, owner, err := editableClient(database.GetDB(), delegatedUser(c), c.Param("email"))
		if err != nil {
			delegationError(c, err)
			return
		}
		var body attachDetachBody
		if c.ShouldBindJSON(&body) != nil || len(body.InboundIds) > 32 {
			delegationError(c, errDelegationInput)
			return
		}
		err = d.changeAttachments(owner, rec, body.InboundIds, attach)
		if err != nil {
			delegationError(c, err)
			return
		}
		jsonObj(c, nil, nil)
	}
}

func (d *delegationController) changeAttachments(u *model.User, rec *model.ClientRecord, ids []int, attach bool) error {
	if len(ids) > 32 {
		return errDelegationInput
	}
	var restart bool
	var err error
	if attach {
		for _, id := range ids {
			allowed, err := inboundAccess(database.GetDB(), u, id)
			if err != nil {
				return err
			}
			if !allowed {
				return gorm.ErrRecordNotFound
			}
		}
		restart, err = d.Attach(&d.InboundService, rec.Id, ids)
	} else {
		restart, err = d.Detach(&d.InboundService, rec.Id, ids)
	}
	if restart {
		d.SetToNeedRestart()
	}
	if err == nil || restart {
		notifyClientsChanged()
	}
	return err
}

func (d *delegationController) scopedBatchCreate(importing bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var payloads []service.ClientCreatePayload
		var err error
		if importing {
			var body struct {
				Data string `json:"data"`
			}
			if c.ShouldBindJSON(&body) != nil || len(body.Data) > 2<<20 {
				delegationError(c, errDelegationInput)
				return
			}
			err = json.Unmarshal([]byte(body.Data), &payloads)
			// Import grants a new prepaid allowance; old counters are never restored.
			for i := range payloads {
				payloads[i].Traffic = nil
			}
		} else {
			err = c.ShouldBindJSON(&payloads)
		}
		if err != nil || len(payloads) > 200 {
			delegationError(c, errDelegationInput)
			return
		}
		delegatedClientWrite.Lock()
		defer delegatedClientWrite.Unlock()
		skipped := []gin.H{}
		created := 0
		for _, p := range payloads {
			if err := d.issueDelegated(delegatedUser(c), p); err != nil {
				skipped = append(skipped, gin.H{"email": p.Client.Email, "reason": "client_rejected"})
			} else {
				created++
			}
		}
		jsonObj(c, gin.H{"created": created, "skipped": skipped}, nil)
	}
}

func (d *delegationController) scopedExport(c *gin.Context) {
	var rows []model.ClientRecord
	if err := ownedClients(database.GetDB(), delegatedUser(c).Id).Select("clients.*").Order("clients.id").Find(&rows).Error; err != nil {
		delegationError(c, err)
		return
	}
	out := make([]service.ClientCreatePayload, 0, len(rows))
	for _, rec := range rows {
		ids, err := d.GetInboundIdsForRecord(rec.Id)
		if err != nil {
			delegationError(c, err)
			return
		}
		rec.Group = model.DisplayDelegatedGroupName(delegatedUser(c).Id, rec.Group)
		out = append(out, service.ClientCreatePayload{Client: *rec.ToClient(), InboundIds: ids, LimitHwid: rec.LimitHwid})
	}
	jsonObj(c, out, nil)
}

func (d *delegationController) scopedBatch(operation string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Emails     []string `json:"emails"`
			Group      string   `json:"group"`
			InboundIDs []int    `json:"inboundIds"`
			AddDays    int      `json:"addDays"`
			AddBytes   int64    `json:"addBytes"`
			Flow       string   `json:"flow"`
			LimitHwid  *int     `json:"limitHwid"`
		}
		if c.ShouldBindJSON(&body) != nil || len(body.Emails) > 200 || len(body.Group) > 100 || len(body.InboundIDs) > 32 ||
			body.AddBytes < -maxTraffic || body.AddBytes > maxTraffic || body.AddDays < -36500 || body.AddDays > 36500 {
			delegationError(c, errDelegationInput)
			return
		}
		delegatedClientWrite.Lock()
		defer delegatedClientWrite.Unlock()
		u := delegatedUser(c)
		db := database.GetDB()
		skipped := []gin.H{}
		failed := []string{}
		done := []string{}
		for _, email := range uniqueClientEmails(body.Emails) {
			var rec model.ClientRecord
			err := ownedClients(db, u.Id).Select("clients.*").Where("clients.email = ?", email).First(&rec).Error
			if err == nil {
				switch operation {
				case "enable", "disable":
					_, restart, e := d.BulkSetEnable(&d.InboundService, []string{rec.Email}, operation == "enable")
					if restart {
						d.SetToNeedRestart()
					}
					err = e
				case "group", "ungroup":
					group := strings.TrimSpace(body.Group)
					if operation == "ungroup" {
						group = ""
					}
					err = db.Model(&model.ClientRecord{}).Where("id = ?", rec.Id).UpdateColumn("group_name", model.DelegatedGroupName(u.Id, group)).Error
				case "attach", "detach":
					err = d.changeAttachments(u, &rec, body.InboundIDs, operation == "attach")
				case "adjust":
					updated := *rec.ToClient()
					updated.TotalGB += body.AddBytes
					if body.AddDays != 0 {
						days := int64(body.AddDays) * 86400000
						if updated.ExpiryTime < 0 {
							updated.ExpiryTime = min(-1, updated.ExpiryTime-days)
						} else {
							start := updated.ExpiryTime
							if start == 0 {
								start = time.Now().UnixMilli()
							}
							updated.ExpiryTime = max(1, start+days)
						}
					}
					if body.Flow != "" {
						updated.Flow = body.Flow
					}
					hwid := rec.LimitHwid
					if body.LimitHwid != nil {
						hwid = *body.LimitHwid
					}
					err = d.updateOwnedClient(u, &rec, updated, hwid)
				}
			}
			if err == nil {
				done = append(done, email)
			} else {
				failed = append(failed, email)
				skipped = append(skipped, gin.H{"email": email, "reason": "client_rejected"})
			}
		}
		if len(done) > 0 {
			notifyClientsChanged()
		}
		switch operation {
		case "attach":
			jsonObj(c, gin.H{"attached": done, "skipped": []string{}, "errors": failed}, nil)
		case "detach":
			jsonObj(c, gin.H{"detached": done, "skipped": []string{}, "errors": failed}, nil)
		case "adjust":
			jsonObj(c, gin.H{"adjusted": len(done), "skipped": skipped}, nil)
		default:
			jsonObj(c, gin.H{"changed": len(done), "affected": len(done), "skipped": skipped}, nil)
		}
	}
}

func (d *delegationController) scopedCleanup(orphan bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		delegatedClientWrite.Lock()
		defer delegatedClientWrite.Unlock()
		owner := delegatedUser(c).Id
		q := ownedClients(database.GetDB(), owner).Select("clients.*")
		if orphan {
			q = q.Where("NOT EXISTS (SELECT 1 FROM client_inbounds ci WHERE ci.client_id = clients.id)")
		} else {
			q = q.Where("(clients.expiry_time > 0 AND clients.expiry_time <= ?) OR (clients.total_gb > 0 AND EXISTS (SELECT 1 FROM client_traffics ct WHERE ct.email = clients.email AND ct.up + ct.down >= clients.total_gb))", time.Now().UnixMilli())
		}
		var rows []model.ClientRecord
		if err := q.Find(&rows).Error; err != nil {
			delegationError(c, err)
			return
		}
		deleted := 0
		for _, row := range rows {
			if err := d.removeOwnedClient(&row, owner); err != nil {
				delegationError(c, err)
				return
			}
			deleted++
		}
		jsonObj(c, gin.H{"deleted": deleted}, nil)
	}
}

func (d *delegationController) scopedExternalLinks(c *gin.Context) {
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	var body externalLinksBody
	if c.ShouldBindJSON(&body) != nil || len(body.ExternalLinks) > 32 {
		delegationError(c, errDelegationInput)
		return
	}
	for _, link := range body.ExternalLinks {
		if link.Kind != "link" {
			delegationError(c, errDelegationInput)
			return
		}
	}
	rec, _, err := editableClient(database.GetDB(), delegatedUser(c), c.Param("email"))
	if err == nil {
		err = d.SetExternalLinksForRecord(rec.Id, body.ExternalLinks)
	}
	if err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, nil, nil)
}

func delegatedTrafficRemaining(tx *gorm.DB, rec *model.ClientRecord) (int64, error) {
	var traffic xray.ClientTraffic
	err := tx.Where("email = ?", rec.Email).First(&traffic).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var totals struct{ Up, Down int64 }
	if err := tx.Model(&model.ClientGlobalTraffic{}).Where("email = ?", rec.Email).Select("COALESCE(MAX(up),0) AS up, COALESCE(MAX(down),0) AS down").Scan(&totals).Error; err != nil {
		return 0, err
	}
	return availableTraffic(rec.TotalGB, max(traffic.Up, totals.Up), max(traffic.Down, totals.Down)), nil
}

func (d *delegationController) scopedReset(all bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		delegatedClientWrite.Lock()
		defer delegatedClientWrite.Unlock()
		u := delegatedUser(c)
		var rows []model.ClientRecord
		if all {
			if err := ownedClients(database.GetDB(), u.Id).Select("clients.*").Find(&rows).Error; err != nil {
				delegationError(c, err)
				return
			}
		} else {
			row, err := findScopedClient(c)
			if err != nil {
				delegationError(c, err)
				return
			}
			rows = append(rows, *row)
		}
		changed := 0
		for _, rec := range rows {
			ids, err := d.GetInboundIdsForRecord(rec.Id)
			if err != nil {
				delegationError(c, err)
				return
			}
			for _, id := range ids {
				ok, err := inboundAccess(database.GetDB(), u, id)
				if err != nil {
					delegationError(c, err)
					return
				}
				if !ok {
					delegationError(c, gorm.ErrRecordNotFound)
					return
				}
			}
			debit := func(tx *gorm.DB) error {
				if rec.TotalGB == 0 || u.HasUnlimitedClients() {
					return nil
				}
				remaining, err := delegatedTrafficRemaining(tx, &rec)
				if err != nil {
					return err
				}
				return debitCredits(tx, u.Id, 0, rec.TotalGB-remaining)
			}
			if len(ids) == 0 {
				err = d.ResetClientTrafficByEmailWithDebit(rec.Email, debit)
			} else {
				var restart bool
				restart, err = d.ResetClientTrafficWithDebit(ids[0], rec.Email, debit)
				if restart || (err == nil && len(ids) > 1) {
					d.SetToNeedRestart()
				}
			}
			if err != nil {
				delegationError(c, err)
				return
			}
			changed++
		}
		if changed > 0 {
			notifyClientsChanged()
		}
		jsonObj(c, gin.H{"changed": changed}, nil)
	}
}

func uniqueClientEmails(emails []string) []string {
	out := make([]string, 0, len(emails))
	seen := make(map[string]bool, len(emails))
	for _, email := range emails {
		if !seen[email] {
			seen[email] = true
			out = append(out, email)
		}
	}
	return out
}
