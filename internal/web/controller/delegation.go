package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	gib               = int64(1 << 30)
	maxCredits        = int64(1_000_000)
	maxTraffic        = maxCredits * gib
	delegationUserKey = "delegation_user"
)

var (
	errDelegationCredit      = errors.New("insufficient_credit")
	errDelegationLimit       = errors.New("credit_limit")
	errDelegationAccount     = errors.New("account_unavailable")
	errUsernameTaken         = errors.New("username_taken")
	errDelegationInboundSync = errors.New("inbound_sync_failed")
)

// Delegated sessions have no access to legacy configuration, export, or
// client endpoints, which were designed for the owner of the whole panel.
func enforcePanelRole(c *gin.Context) {
	delegation := strings.HasPrefix(relAPIPath(c.FullPath()), "/delegation/")
	if c.GetBool("api_authed") {
		if delegation {
			delegationFailure(c, http.StatusForbidden, "forbidden")
		}
		return
	}
	u := session.GetLoginUser(c)
	if u == nil || (!u.IsPanelOwner() && !delegation) {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	c.Set(delegationUserKey, u)
}

func delegationFailure(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"success": false, "msg": code})
}

func delegatedUser(c *gin.Context) *model.User {
	return c.MustGet(delegationUserKey).(*model.User)
}

func delegationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errDelegationInput):
		delegationFailure(c, 400, "invalid_input")
	case errors.Is(err, errDelegationRejected):
		delegationFailure(c, 409, "client_rejected")
	case errors.Is(err, errDelegationInboundSync):
		delegationFailure(c, http.StatusConflict, err.Error())
	case errors.Is(err, errDelegationCredit), errors.Is(err, errDelegationLimit):
		delegationFailure(c, http.StatusConflict, err.Error())
	case errors.Is(err, errDelegationAccount), errors.Is(err, gorm.ErrRecordNotFound):
		delegationFailure(c, http.StatusNotFound, "not_found")
	default:
		logger.Warningf("delegation %s: %v", c.FullPath(), err)
		delegationFailure(c, http.StatusInternalServerError, "request_failed")
	}
}

type delegationController struct {
	service.ClientService
	service.InboundService
	service.XrayService
	service.SettingService
}

func registerDelegation(g *gin.RouterGroup) {
	d := &delegationController{}
	g.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
	})
	g.GET("/me", d.me)
	g.GET("/telegram-backup", d.telegramBackup)
	g.POST("/telegram-backup", d.saveTelegramBackup)
	g.GET("/accounts", d.accounts)
	g.POST("/accounts", d.createAccount)
	g.POST("/accounts/:id/edit", d.editAccount)
	g.POST("/accounts/:id/delete", d.deleteAccount)
	g.POST("/accounts/:id/credit", d.transfer)
	g.GET("/accounts/:id/inbounds", d.accountInbounds)
	g.POST("/accounts/:id/inbounds/:inboundId", d.setAccountInbound)
	g.GET("/inbounds", d.inbounds)
	g.GET("/inbound-options", d.inboundOptions)
	g.POST("/inbounds/:id", d.setInbound)
	g.GET("/clients", d.listClients)
	g.POST("/clients", d.createClient)
	g.POST("/clients/full", d.createClientFull)
	g.GET("/clients/:subId/links", d.links)
	g.GET("/xray", d.xray)
	d.sharedClientRoutes(g.Group("/client-page"))
}

type delegatedAccountView struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Clients  int64  `json:"clients"`
	GiB      int64  `json:"gib"`
	ParentID int    `json:"parentId"`
	UncappedClientSlots bool `json:"uncappedClientSlots"`
	UnlimitedClientLimit int64 `json:"unlimitedClientLimit"`
	UnlimitedClientAvailable *int64 `json:"unlimitedClientAvailable,omitempty"`
}

func accountView(u model.User) delegatedAccountView {
	role := u.PanelRole
	if u.IsPanelOwner() {
		role = model.PanelRoleOwner
	}
	return delegatedAccountView{
		ID: u.Id, Username: u.Username, Role: role, Clients: u.ClientCredits, GiB: u.TrafficCredits / gib,
		ParentID: u.ParentID, UncappedClientSlots: u.UncappedClientSlots, UnlimitedClientLimit: u.UnlimitedClientLimit,
	}
}

func (d *delegationController) me(c *gin.Context) {
	account := delegatedUser(c)
	view := accountView(*account)
	if !account.IsPanelOwner() && !account.HasUnlimitedClients() && account.UnlimitedClientLimit > 0 {
		used, err := committedUnlimitedClients(database.GetDB(), account.Id)
		if err != nil {
			delegationError(c, err)
			return
		}
		available := max(0, account.UnlimitedClientLimit-used)
		view.UnlimitedClientAvailable = &available
	}
	jsonObj(c, view, nil)
}

func (d *delegationController) accounts(c *gin.Context) {
	u := delegatedUser(c)
	if !u.CanManagePanelAccounts() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	var users []model.User
	db := database.GetDB()
	query := db.Where("id IN (?) AND id <> ?", accountDescendants(db, u.Id), u.Id)
	if u.IsPanelOwner() {
		query = database.GetDB().Where("parent_id > 0")
	}
	if err := query.Order("id").Find(&users).Error; err != nil {
		delegationError(c, err)
		return
	}
	rows := make([]delegatedAccountView, 0, len(users))
	for _, child := range users {
		rows = append(rows, accountView(child))
	}
	jsonObj(c, rows, nil)
}

func accountDescendants(db *gorm.DB, id int) *gorm.DB {
	return db.Raw(`WITH RECURSIVE descendants(id) AS (
		SELECT id FROM users WHERE parent_id = ?
		UNION
		SELECT users.id FROM users JOIN descendants ON users.parent_id = descendants.id
	) SELECT id FROM descendants`, id)
}

func manageableAccount(tx *gorm.DB, actor *model.User, id int) *gorm.DB {
	query := tx.Model(&model.User{}).Where("id = ?", id)
	if actor.IsPanelOwner() {
		return query.Where("parent_id > 0")
	}
	return query.Where("id IN (?) AND panel_role IN ?", accountDescendants(tx, actor.Id), managedPanelRoles(actor))
}

func managedPanelRoles(actor *model.User) []string {
	roles := []string{model.PanelRoleAdmin, model.PanelRoleReseller}
	if actor.HasDelegatedSudoAccess() {
		roles = append(roles, model.PanelRoleInfinity)
	}
	return roles
}

func (d *delegationController) editAccount(c *gin.Context) {
	actor := delegatedUser(c)
	if !actor.CanManagePanelAccounts() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Clients  *int64 `json:"clients"`
		GiB      *int64 `json:"gib"`
		UncappedClientSlots *bool `json:"uncappedClientSlots"`
		UnlimitedClientLimit *int64 `json:"unlimitedClientLimit"`
	}
	if err != nil || id < 1 || c.ShouldBindJSON(&body) != nil {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	validCredit := func(n *int64) bool { return n == nil || (*n >= 0 && *n <= maxCredits) }
	quotaPolicy := body.UncappedClientSlots != nil || body.UnlimitedClientLimit != nil
	if body.UncappedClientSlots != nil && !actor.IsPanelOwner() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	if len(body.Username) < 3 || len(body.Username) > 80 || (body.Password != "" && (len(body.Password) < 12 || len(body.Password) > 72)) || !validCredit(body.Clients) || !validCredit(body.GiB) || !validCredit(body.UnlimitedClientLimit) {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	db := database.GetDB()
	if quotaPolicy {
		delegatedClientWrite.Lock()
		defer delegatedClientWrite.Unlock()
	}
	updates := map[string]any{"username": body.Username}
	if body.Password != "" {
		hash, hashErr := crypto.HashPasswordAsBcrypt(body.Password)
		if hashErr != nil {
			delegationError(c, hashErr)
			return
		}
		updates["password"] = hash
		updates["login_epoch"] = gorm.Expr("login_epoch + 1")
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var target model.User
		if e := manageableAccount(tx, actor, id).First(&target).Error; e != nil {
			return e
		}
		if quotaPolicy && target.PanelRole != model.PanelRoleAdmin && target.PanelRole != model.PanelRoleReseller {
			return errDelegationInput
		}
		if body.UncappedClientSlots != nil {
			updates["uncapped_client_slots"] = *body.UncappedClientSlots
		}
		if body.UnlimitedClientLimit != nil {
			if !actor.IsPanelOwner() && target.ParentID != actor.Id {
				return errDelegationAccount
			}
			if err := validateUnlimitedAllocation(tx, &target, *body.UnlimitedClientLimit); err != nil {
				return err
			}
			updates["unlimited_client_limit"] = *body.UnlimitedClientLimit
		}
		if target.HasUnlimitedClients() && (body.Clients != nil || body.GiB != nil) {
			return errDelegationInput
		}
		if !actor.IsPanelOwner() && target.ParentID != actor.Id && (body.Clients != nil || body.GiB != nil) {
			return errDelegationAccount
		}
		var n int64
		if e := tx.Model(&model.User{}).Where("username = ? AND id <> ?", body.Username, id).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return errUsernameTaken
		}
		var clientsDelta, trafficDelta int64
		if body.Clients != nil {
			clientsDelta = *body.Clients - target.ClientCredits
			updates["client_credits"] = *body.Clients
		}
		if body.GiB != nil {
			traffic := *body.GiB * gib
			trafficDelta = traffic - target.TrafficCredits
			updates["traffic_credits"] = traffic
		}
		if !actor.IsPanelOwner() && !actor.HasUnlimitedClients() && (clientsDelta != 0 || trafficDelta != 0) {
			parent := tx.Model(&model.User{}).Where("id = ?", actor.Id)
			res := parent.Session(&gorm.Session{}).
				Where("client_credits >= ? AND traffic_credits >= ?", max(0, clientsDelta), max(0, trafficDelta)).
				Where("client_credits <= ? AND traffic_credits <= ?", maxCredits+clientsDelta, maxTraffic+trafficDelta).
				Updates(map[string]any{
					"client_credits":  gorm.Expr("client_credits - ?", clientsDelta),
					"traffic_credits": gorm.Expr("traffic_credits - ?", trafficDelta),
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				var enough int64
				if e := parent.Where("client_credits >= ? AND traffic_credits >= ?", max(0, clientsDelta), max(0, trafficDelta)).Count(&enough).Error; e != nil {
					return e
				}
				if enough == 0 {
					return errDelegationCredit
				}
				return errDelegationLimit
			}
		}
		res := manageableAccount(tx, actor, id).
			Where("client_credits = ? AND traffic_credits = ?", target.ClientCredits, target.TrafficCredits).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errUsernameTaken) {
			delegationFailure(c, http.StatusConflict, "username_taken")
		} else {
			delegationError(c, err)
		}
		return
	}
	jsonObj(c, nil, nil)
}

func (d *delegationController) deleteAccount(c *gin.Context) {
	actor := delegatedUser(c)
	if !actor.CanManagePanelAccounts() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	var body struct {
		Cascade bool `json:"cascade"`
	}
	if err != nil || id < 1 || (c.Request.ContentLength != 0 && c.ShouldBindJSON(&body) != nil) {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	if body.Cascade {
		if err := d.deleteAccountBranch(actor, id); err != nil {
			delegationError(c, err)
			return
		}
		jsonObj(c, nil, nil)
		return
	}
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		var user model.User
		if e := manageableAccount(tx, actor, id).First(&user).Error; e != nil {
			return e
		}
		var n int64
		if e := tx.Model(&model.User{}).Where("parent_id = ?", id).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return errors.New("account_has_dependents")
		}
		if e := tx.Model(&model.DelegatedClient{}).Where("owner_id = ?", id).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return errors.New("account_has_dependents")
		}
		return removeAccountsAndReturnCredit(tx, user.ParentID, []int{id})
	})
	if err != nil {
		if err.Error() == "account_has_dependents" {
			delegationFailure(c, http.StatusConflict, "account_has_dependents")
		} else {
			delegationError(c, err)
		}
		return
	}
	jsonObj(c, nil, nil)
}

func (d *delegationController) deleteAccountBranch(actor *model.User, id int) error {
	db := database.GetDB()
	var root model.User
	if err := manageableAccount(db, actor, id).First(&root).Error; err != nil {
		return err
	}
	var ids []int
	if err := db.Model(&model.User{}).Where("id IN (?)", accountDescendants(db, id)).Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, child := range ids {
		if child == id || child == actor.Id {
			return errDelegationInput
		}
	}
	ids = append(ids, id)
	var links []model.DelegatedClient
	if err := db.Where("owner_id IN ?", ids).Find(&links).Error; err != nil {
		return err
	}
	for _, link := range links {
		var records []model.ClientRecord
		if err := db.Where("sub_id = ?", link.SubID).Limit(2).Find(&records).Error; err != nil {
			return err
		}
		if len(records) == 0 {
			continue
		}
		if len(records) != 1 {
			return errDelegationRejected
		}
		if err := d.removeOwnedClient(&records[0], link.OwnerID); err != nil {
			return err
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("owner_id IN ?", ids).Delete(&model.DelegatedClient{}).Error; err != nil {
			return err
		}
		return removeAccountsAndReturnCredit(tx, root.ParentID, ids)
	})
}

// Client deletion has already credited each client's owner. Move the balances
// of the whole deleted branch to its surviving parent exactly once.
func removeAccountsAndReturnCredit(tx *gorm.DB, parentID int, ids []int) error {
	if err := tx.Where("user_id IN ?", ids).Delete(&model.DelegatedTelegramBackup{}).Error; err != nil {
		return err
	}
	if err := tx.Where("user_id IN ?", ids).Delete(&model.DelegatedInboundGrant{}).Error; err != nil {
		return err
	}
	var accounts []model.User
	deleted := tx.Clauses(clause.Returning{Columns: []clause.Column{
		{Name: "id"}, {Name: "parent_id"}, {Name: "panel_role"}, {Name: "client_credits"}, {Name: "traffic_credits"},
	}}).
		Where("id IN ?", ids).Delete(&accounts)
	if deleted.Error != nil {
		return deleted.Error
	}
	if deleted.RowsAffected != int64(len(ids)) {
		return errDelegationAccount
	}
	var slots, traffic int64
	branch := make(map[int]model.User, len(accounts))
	for _, account := range accounts {
		branch[account.Id] = account
	}
	for _, account := range accounts {
		// Credits issued by an Infinity account must not become a refund
		// to a metered ancestor when the branch is removed.
		unlimited := false
		for ancestor := account; ancestor.Id != 0; ancestor = branch[ancestor.ParentID] {
			if ancestor.HasUnlimitedClients() {
				unlimited = true
				break
			}
		}
		if !unlimited {
			slots += account.ClientCredits
			traffic += account.TrafficCredits
		}
	}
	if slots == 0 && traffic == 0 {
		return nil
	}
	var parent model.User
	if err := tx.First(&parent, parentID).Error; err != nil {
		return err
	}
	if parent.HasUnlimitedClients() {
		return nil
	}
	refund := tx.Model(&model.User{}).Where("id = ?", parentID).Updates(map[string]any{
		"client_credits":  gorm.Expr("client_credits + ?", slots),
		"traffic_credits": gorm.Expr("traffic_credits + ?", traffic),
	})
	if refund.Error != nil {
		return refund.Error
	}
	if refund.RowsAffected != 1 {
		return errDelegationAccount
	}
	return nil
}

func (d *delegationController) createAccount(c *gin.Context) {
	u := delegatedUser(c)
	if !u.CanManagePanelAccounts() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		UnlimitedClientLimit int64 `json:"unlimitedClientLimit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if !u.CanCreatePanelRole(body.Role) {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	if body.UnlimitedClientLimit < 0 || body.UnlimitedClientLimit > maxCredits ||
		(body.UnlimitedClientLimit != 0 && body.Role != model.PanelRoleAdmin && body.Role != model.PanelRoleReseller) {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	if len(body.Username) < 3 || len(body.Username) > 80 || len(body.Password) < 12 || len(body.Password) > 72 {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	hash, err := crypto.HashPasswordAsBcrypt(body.Password)
	if err != nil {
		delegationError(c, err)
		return
	}
	child := model.User{Username: body.Username, Password: hash, ParentID: u.Id, PanelRole: body.Role}
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&model.User{}, u.Id).Error; err != nil {
			return err
		}
		if body.UnlimitedClientLimit > 0 {
			if err := validateUnlimitedAllocation(tx, &child, body.UnlimitedClientLimit); err != nil {
				return err
			}
			child.UnlimitedClientLimit = body.UnlimitedClientLimit
		}
		res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "username"}}, DoNothing: true}).Create(&child)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errUsernameTaken
		}
		if !u.IsPanelOwner() {
			return tx.Exec(`INSERT INTO delegated_inbound_grants (user_id, inbound_id)
				SELECT ?, inbound_id FROM delegated_inbound_grants WHERE user_id = ?`, child.Id, u.Id).Error
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errUsernameTaken) {
			delegationFailure(c, http.StatusConflict, "username_taken")
		} else {
			delegationError(c, err)
		}
		return
	}
	jsonObj(c, accountView(child), nil)
}

func (d *delegationController) transfer(c *gin.Context) {
	u := delegatedUser(c)
	if !u.CanManagePanelAccounts() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	var body struct {
		Clients int64 `json:"clients"`
		GiB     int64 `json:"gib"`
	}
	if err != nil || id < 1 || c.ShouldBindJSON(&body) != nil || body.Clients < 0 || body.GiB < 0 ||
		body.Clients > maxCredits || body.GiB > maxCredits || (body.Clients == 0 && body.GiB == 0) {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	if err := transferCredits(database.GetDB(), u, id, body.Clients, body.GiB*gib); err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, nil, nil)
}

func debitCredits(tx *gorm.DB, id int, clients, traffic int64) error {
	res := tx.Model(&model.User{}).Where("id = ? AND client_credits >= ? AND traffic_credits >= ?", id, clients, traffic).
		Updates(map[string]any{
			"client_credits":  gorm.Expr("client_credits - ?", clients),
			"traffic_credits": gorm.Expr("traffic_credits - ?", traffic),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errDelegationCredit
	}
	return nil
}

func transferCredits(db *gorm.DB, u *model.User, id int, clients, traffic int64) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if !u.IsPanelOwner() && !u.HasUnlimitedClients() {
			if err := debitCredits(tx, u.Id, clients, traffic); err != nil {
				return err
			}
		}
		target := tx.Model(&model.User{}).Where("id = ? AND parent_id = ? AND panel_role <> ?", id, u.Id, model.PanelRoleInfinity)
		res := target.Session(&gorm.Session{}).Where("client_credits <= ? AND traffic_credits <= ?", maxCredits-clients, maxTraffic-traffic).
			Updates(map[string]any{
				"client_credits":  gorm.Expr("client_credits + ?", clients),
				"traffic_credits": gorm.Expr("traffic_credits + ?", traffic),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			var n int64
			if err := target.Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				return errDelegationAccount
			}
			return errDelegationLimit
		}
		return nil
	})
}

type delegatedInboundView struct {
	ID       int            `json:"id"`
	Remark   string         `json:"remark"`
	Protocol model.Protocol `json:"protocol"`
	Allowed  bool           `json:"allowed"`
}

func eligibleInbounds(db *gorm.DB) *gorm.DB {
	return db.Model(&model.Inbound{}).Where("node_id IS NULL AND enable = ? AND protocol IN ?", true,
		[]model.Protocol{model.VLESS, model.VMESS, model.Trojan})
}

// inboundAccess walks the hierarchy, so removing a parent's grant also
// removes effective access for every descendant without changing stored grants.
func inboundAccess(db *gorm.DB, user *model.User, inboundID int) (bool, error) {
	var count int64
	if err := eligibleInbounds(db).Joins("JOIN delegated_inbounds ON delegated_inbounds.inbound_id = inbounds.id").
		Where("inbounds.id = ?", inboundID).Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	current := *user
	visited := map[int]bool{}
	for !current.IsPanelOwner() {
		if current.Id < 1 || current.ParentID < 1 || visited[current.Id] {
			return false, nil
		}
		visited[current.Id] = true
		if err := db.Model(&model.DelegatedInboundGrant{}).
			Where("user_id = ? AND inbound_id = ?", current.Id, inboundID).Count(&count).Error; err != nil {
			return false, err
		}
		if count == 0 {
			return false, nil
		}
		parentID := current.ParentID
		current = model.User{}
		if err := db.First(&current, parentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func listDelegatedInbounds(db *gorm.DB, user *model.User) ([]delegatedInboundView, error) {
	var rows []delegatedInboundView
	err := eligibleInbounds(db).
		Joins("LEFT JOIN delegated_inbounds ON delegated_inbounds.inbound_id = inbounds.id").
		Select("inbounds.id, inbounds.remark, inbounds.protocol, delegated_inbounds.inbound_id IS NOT NULL AS allowed").
		Order("inbounds.id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]delegatedInboundView, 0, len(rows))
	for _, row := range rows {
		if user.IsPanelOwner() {
			result = append(result, row)
			continue
		}
		allowed, err := inboundAccess(db, user, row.ID)
		if err != nil {
			return nil, err
		}
		if allowed {
			row.Allowed = true
			result = append(result, row)
		}
	}
	return result, nil
}

func (d *delegationController) inbounds(c *gin.Context) {
	rows, err := listDelegatedInbounds(database.GetDB(), delegatedUser(c))
	if err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, rows, nil)
}

func (d *delegationController) inboundOptions(c *gin.Context) {
	db := database.GetDB()
	rows, err := listDelegatedInbounds(db, delegatedUser(c))
	if err != nil {
		delegationError(c, err)
		return
	}
	allowed := make(map[int]bool, len(rows))
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.Allowed {
			allowed[row.ID] = true
			ids = append(ids, row.ID)
		}
	}
	options := make([]service.InboundOption, 0, len(ids))
	if len(ids) == 0 {
		jsonObj(c, options, nil)
		return
	}
	var ownerIDs []int
	if err := db.Model(&model.Inbound{}).Where("id IN ?", ids).Distinct().Pluck("user_id", &ownerIDs).Error; err != nil {
		delegationError(c, err)
		return
	}
	for _, ownerID := range ownerIDs {
		all, err := d.InboundService.GetInboundOptions(ownerID)
		if err != nil {
			delegationError(c, err)
			return
		}
		for _, option := range all {
			if allowed[option.Id] {
				options = append(options, option)
			}
		}
	}
	jsonObj(c, options, nil)
}

func directChild(db *gorm.DB, user *model.User, id int) (*model.User, error) {
	if !user.CanManagePanelAccounts() {
		return nil, errDelegationAccount
	}
	var child model.User
	if err := db.Where("id = ? AND parent_id = ?", id, user.Id).First(&child).Error; err != nil {
		return nil, err
	}
	return &child, nil
}

func inboundGrantParent(db *gorm.DB, actor *model.User, id int) (*model.User, error) {
	if !actor.IsPanelOwner() {
		_, err := directChild(db, actor, id)
		return actor, err
	}
	var target model.User
	if err := manageableAccount(db, actor, id).First(&target).Error; err != nil {
		return nil, err
	}
	var parent model.User
	if err := db.First(&parent, target.ParentID).Error; err != nil {
		return nil, err
	}
	return &parent, nil
}

func (d *delegationController) accountInbounds(c *gin.Context) {
	user := delegatedUser(c)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	db := database.GetDB()
	parent, err := inboundGrantParent(db, user, id)
	if err != nil {
		delegationError(c, err)
		return
	}
	rows, err := listDelegatedInbounds(db, parent)
	if err != nil {
		delegationError(c, err)
		return
	}
	result := make([]delegatedInboundView, 0, len(rows))
	for _, row := range rows {
		if !row.Allowed {
			continue
		}
		var count int64
		if err := db.Model(&model.DelegatedInboundGrant{}).Where("user_id = ? AND inbound_id = ?", id, row.ID).Count(&count).Error; err != nil {
			delegationError(c, err)
			return
		}
		row.Allowed = count > 0
		result = append(result, row)
	}
	jsonObj(c, result, nil)
}

func (d *delegationController) setAccountInbound(c *gin.Context) {
	user := delegatedUser(c)
	id, err := strconv.Atoi(c.Param("id"))
	inboundID, inboundErr := strconv.Atoi(c.Param("inboundId"))
	var body struct {
		Allowed bool `json:"allowed"`
	}
	if err != nil || inboundErr != nil || id < 1 || inboundID < 1 || c.ShouldBindJSON(&body) != nil {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	db := database.GetDB()
	parent, err := inboundGrantParent(db, user, id)
	if err != nil {
		delegationError(c, err)
		return
	}
	if body.Allowed {
		allowed, err := inboundAccess(db, parent, inboundID)
		if err != nil {
			delegationError(c, err)
			return
		}
		if !allowed {
			delegationError(c, gorm.ErrRecordNotFound)
			return
		}
	}
	if err := d.syncBranchInboundClients(db, id, inboundID, body.Allowed); err != nil {
		delegationError(c, err)
		return
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if !body.Allowed {
			return tx.Where("inbound_id = ? AND (user_id = ? OR user_id IN (?))", inboundID, id, accountDescendants(tx, id)).
				Delete(&model.DelegatedInboundGrant{}).Error
		}
		var ids []int
		if err := tx.Model(&model.User{}).Where("id = ? OR id IN (?)", id, accountDescendants(tx, id)).Pluck("id", &ids).Error; err != nil {
			return err
		}
		grants := make([]model.DelegatedInboundGrant, 0, len(ids))
		for _, userID := range ids {
			grants = append(grants, model.DelegatedInboundGrant{UserID: userID, InboundID: inboundID})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(grants, 200).Error
	})
	if err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, nil, nil)
}

// Reuse the normal attachment services so changes reach Xray and subscriptions.
// Detaching the last inbound intentionally preserves the client and its usage.
func (d *delegationController) syncBranchInboundClients(db *gorm.DB, accountID, inboundID int, attach bool) error {
	attachment := "EXISTS (SELECT 1 FROM client_inbounds ci WHERE ci.client_id = clients.id AND ci.inbound_id = ?)"
	if attach {
		attachment = "NOT " + attachment
	}
	pending := func() *gorm.DB {
		return db.Model(&model.ClientRecord{}).
			Joins("JOIN delegated_clients dc ON dc.sub_id = clients.sub_id").
			Where("(dc.owner_id = ? OR dc.owner_id IN (?))", accountID, accountDescendants(db, accountID)).
			Where(attachment, inboundID)
	}
	var emails []string
	if err := pending().Order("clients.id").Pluck("clients.email", &emails).Error; err != nil || len(emails) == 0 {
		return err
	}
	defer notifyClientsChanged()
	var restart bool
	var err error
	var failures []string
	if attach {
		var result *service.BulkAttachResult
		result, restart, err = d.BulkAttach(&d.InboundService, emails, []int{inboundID})
		if result != nil {
			failures = result.Errors
		}
	} else {
		var result *service.BulkDetachResult
		result, restart, err = d.BulkDetach(&d.InboundService, emails, []int{inboundID})
		if result != nil {
			failures = result.Errors
		}
	}
	if restart {
		d.SetToNeedRestart()
	}
	if err != nil {
		return err
	}
	var remaining int64
	if err := pending().Count(&remaining).Error; err != nil {
		return err
	}
	if len(failures) > 0 || remaining > 0 {
		logger.Warningf("delegation inbound %d account %d: %d client attachments still pending; %v", inboundID, accountID, remaining, failures)
		return errDelegationInboundSync
	}
	return nil
}

func (d *delegationController) setInbound(c *gin.Context) {
	if !delegatedUser(c).IsPanelOwner() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	var body struct {
		Allowed bool `json:"allowed"`
	}
	if err != nil || id < 1 || c.ShouldBindJSON(&body) != nil {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	db := database.GetDB()
	item := model.DelegatedInbound{InboundID: id}
	if body.Allowed {
		var count int64
		if err := eligibleInbounds(db).Where("id = ?", id).Count(&count).Error; err != nil {
			delegationError(c, err)
			return
		}
		if count == 0 {
			delegationFailure(c, http.StatusNotFound, "not_found")
			return
		}
		err = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
	} else {
		err = db.Delete(&item).Error
	}
	if err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, nil, nil)
}

type delegatedClientView struct {
	Email   string `json:"email"`
	SubID   string `json:"subId"`
	TotalGB int64  `json:"trafficBytes"`
	Enable  bool   `json:"enable"`
	Up      int64  `json:"up"`
	Down    int64  `json:"down"`
}

func ownedClients(db *gorm.DB, id int) *gorm.DB {
	return db.Model(&model.ClientRecord{}).
		Joins("JOIN delegated_clients ON delegated_clients.sub_id = clients.sub_id").
		Where("delegated_clients.owner_id = ?", id)
}

func checkUnlimitedClientCapacity(db *gorm.DB, account *model.User) error {
	if account.HasUnlimitedClients() {
		return nil
	}
	if account.UnlimitedClientLimit <= 0 {
		return errDelegationCredit
	}
	used, err := committedUnlimitedClients(db, account.Id)
	if err != nil {
		return err
	}
	if used >= account.UnlimitedClientLimit {
		return errDelegationCredit
	}
	return nil
}

// A direct child's whole budget is already reserved, including what it
// passes down further. Counting direct allocations avoids double counting.
func committedUnlimitedClients(db *gorm.DB, id int) (int64, error) {
	var used, allocated int64
	if err := ownedClients(db, id).Where("clients.total_gb = 0").Count(&used).Error; err != nil {
		return 0, err
	}
	err := db.Model(&model.User{}).Where("parent_id = ?", id).
		Select("COALESCE(SUM(unlimited_client_limit), 0)").Scan(&allocated).Error
	return used + allocated, err
}

func validateUnlimitedAllocation(db *gorm.DB, target *model.User, limit int64) error {
	delta := limit - target.UnlimitedClientLimit
	if delta == 0 {
		return nil
	}
	if delta < 0 {
		used, err := committedUnlimitedClients(db, target.Id)
		if err != nil {
			return err
		}
		if limit < used {
			return errDelegationLimit
		}
		return nil
	}
	var parent model.User
	if err := db.First(&parent, target.ParentID).Error; err != nil {
		return err
	}
	if parent.IsPanelOwner() || parent.HasUnlimitedClients() {
		return nil
	}
	used, err := committedUnlimitedClients(db, parent.Id)
	if err != nil {
		return err
	}
	if delta > parent.UnlimitedClientLimit-used {
		return errDelegationLimit
	}
	return nil
}

func (d *delegationController) listClients(c *gin.Context) {
	rows := make([]delegatedClientView, 0)
	if err := ownedClients(database.GetDB(), delegatedUser(c).Id).
		Select("clients.email, clients.sub_id, clients.total_gb, clients.enable").
		Order("clients.id DESC").Scan(&rows).Error; err != nil {
		delegationError(c, err)
		return
	}
	for i := range rows {
		traffic, err := d.InboundService.GetClientTrafficByEmail(rows[i].Email)
		if err != nil {
			delegationError(c, err)
			return
		}
		if traffic != nil {
			rows[i].Up, rows[i].Down = traffic.Up, traffic.Down
		}
	}
	jsonObj(c, rows, nil)
}

func (d *delegationController) createClient(c *gin.Context) {
	u := delegatedUser(c)
	if u.IsPanelOwner() || !u.IsDelegatedOperator() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Email     string `json:"email"`
		InboundID int    `json:"inboundId"`
		GiB       int64  `json:"gib"`
	}
	if c.ShouldBindJSON(&body) != nil || body.InboundID < 1 || body.GiB < 0 || body.GiB > maxCredits {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if len(body.Email) == 0 || len(body.Email) > 100 {
		delegationFailure(c, http.StatusBadRequest, "invalid_input")
		return
	}
	d.issueClient(c, u, service.ClientCreatePayload{
		Client:     model.Client{Email: body.Email, TotalGB: body.GiB * gib, Enable: true},
		InboundIds: []int{body.InboundID},
	})
}

// The shared editor may create only prepaid clients on authorized inbounds.
// Traffic renewal would grant data beyond the amount reserved at creation.
func (d *delegationController) createClientFull(c *gin.Context) {
	u := delegatedUser(c)
	if u.IsPanelOwner() || !u.IsDelegatedOperator() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	var payload service.ClientCreatePayload
	if c.ShouldBindJSON(&payload) != nil {
		delegationFailure(c, 400, "invalid_input")
		return
	}
	d.issueClient(c, u, payload)
}

func validateDelegatedCreate(payload *service.ClientCreatePayload) bool {
	if len(payload.InboundIds) == 0 || len(payload.InboundIds) > 32 ||
		payload.Client.TotalGB < 0 || payload.Client.TotalGB > maxTraffic || payload.Client.Reset != 0 ||
		payload.Client.ResetDay != 0 || payload.Client.ResetWeekday != 0 || payload.Client.ResetMax != 0 ||
		(payload.Client.TrafficReset != "" && payload.Client.TrafficReset != "never") ||
		len(payload.Client.ClientHostRuleIds) != 0 || payload.Traffic != nil {
		return false
	}
	payload.Client.Email = strings.TrimSpace(payload.Client.Email)
	payload.Client.DisplayName = strings.TrimSpace(payload.Client.DisplayName)
	if len([]rune(payload.Client.DisplayName)) > 100 {
		return false
	}
	if payload.Client.Email == "" && payload.Client.DisplayName != "" {
		payload.Client.Email = "client-" + uuid.NewString()
	}
	if len(payload.Client.Email) == 0 || len(payload.Client.Email) > 100 {
		return false
	}
	return true
}

func (d *delegationController) issueClient(c *gin.Context, u *model.User, payload service.ClientCreatePayload) {
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	if err := d.issueDelegated(u, payload); err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, nil, nil)
}

func (d *delegationController) issueDelegated(u *model.User, payload service.ClientCreatePayload) error {
	if !validateDelegatedCreate(&payload) {
		return errDelegationInput
	}
	db := database.GetDB()
	var fresh model.User
	if err := db.First(&fresh, u.Id).Error; err != nil {
		return err
	}
	u = &fresh
	owner := model.DelegatedClient{SubID: uuid.NewString(), OwnerID: u.Id, FreeSlot: u.HasUnlimitedClients() || u.UncappedClientSlots}
	bytes := payload.Client.TotalGB
	if u.HasUnlimitedClients() {
		owner.FreeTraffic = bytes
	}
	payload.Client.SubID = owner.SubID
	payload.Client.Reverse = nil
	payload.Client.Group = model.DelegatedGroupName(u.Id, payload.Client.Group)
	err := db.Transaction(func(tx *gorm.DB) error {
		// Acquire the credit row before reads; all issuance and transfers
		// must pass a single conditional debit on this database row.
		if !u.HasUnlimitedClients() {
			if bytes == 0 {
				if err := checkUnlimitedClientCapacity(tx, u); err != nil {
					return err
				}
			}
			if err := debitCredits(tx, u.Id, refundableClientSlots(owner), bytes); err != nil {
				return err
			}
		}
		for _, id := range payload.InboundIds {
			allowed, err := inboundAccess(tx, u, id)
			if err != nil {
				return err
			}
			if !allowed {
				return gorm.ErrRecordNotFound
			}
		}
		return tx.Create(&owner).Error
	})
	if err != nil {
		return err
	}
	needRestart, err := d.ClientService.Create(&d.InboundService, &payload)
	if needRestart {
		d.SetToNeedRestart()
	}
	if err == nil {
		notifyClientsChanged()
		return nil
	}
	// The existing create can report an error after committing the client.
	// Refund only if the reserved identity was never persisted.
	var committed bool
	refundErr := db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.ClientRecord{}).Where("sub_id = ?", owner.SubID).Count(&n).Error; err != nil {
			return err
		}
		committed = n > 0
		if committed {
			return nil
		}
		if err := tx.Delete(&owner).Error; err != nil {
			return err
		}
		return addDelegatedBalance(tx, u.Id, refundableClientSlots(owner), bytes-owner.FreeTraffic)
	})
	if refundErr != nil {
		return refundErr
	}
	if committed {
		d.SetToNeedRestart()
		notifyClientsChanged()
		return nil
	}
	logger.Warningf("delegated client creation failed: %v", err)
	return errDelegationRejected
}

func (d *delegationController) links(c *gin.Context) {
	var n int64
	subID := c.Param("subId")
	if err := ownedClients(database.GetDB(), delegatedUser(c).Id).Where("clients.sub_id = ?", subID).Count(&n).Error; err != nil {
		delegationError(c, err)
		return
	}
	if n != 1 {
		delegationFailure(c, http.StatusNotFound, "not_found")
		return
	}
	links, err := d.InboundService.GetSubLinks(resolveHost(c), subID)
	if err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, links, nil)
}

func (d *delegationController) xray(c *gin.Context) {
	u := delegatedUser(c)
	if !u.IsPanelOwner() && !u.HasDelegatedSudoAccess() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	raw, err := d.GetXrayConfigTemplate()
	if err != nil {
		delegationError(c, err)
		return
	}
	// Project only fields that don't carry client records or credentials.
	var summary struct {
		Log struct {
			Level string `json:"loglevel"`
		} `json:"log"`
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"outbounds"`
		Routing struct {
			Strategy string `json:"domainStrategy"`
			Rules    []struct {
				InboundTags []string `json:"inboundTag,omitempty"`
				OutboundTag string   `json:"outboundTag,omitempty"`
				BalancerTag string   `json:"balancerTag,omitempty"`
				Network     string   `json:"network,omitempty"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal([]byte(service.UnwrapXrayTemplateConfig(raw)), &summary); err != nil {
		delegationError(c, err)
		return
	}
	jsonObj(c, summary, nil)
}
