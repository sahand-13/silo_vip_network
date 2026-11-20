package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"gorm.io/gorm"
)

var errAssignmentInbound = errors.New("assignment_inbound_unavailable")

func canAssignClients(actor *model.User) bool {
	return actor.IsPanelOwner() || actor.HasDelegatedSudoAccess()
}

func editableClients(db *gorm.DB, actor *model.User) *gorm.DB {
	if actor.HasUnlimitedClients() {
		return db.Model(&model.ClientRecord{}).Where(
			"EXISTS (SELECT 1 FROM delegated_clients dc WHERE dc.sub_id = clients.sub_id AND (dc.owner_id = ? OR dc.owner_id IN (?)))",
			actor.Id, accountDescendants(db, actor.Id))
	}
	return ownedClients(db, actor.Id)
}

func editableClient(db *gorm.DB, actor *model.User, email string) (*model.ClientRecord, *model.User, error) {
	var rec model.ClientRecord
	if err := editableClients(db, actor).Select("clients.*").Where("clients.email = ?", email).First(&rec).Error; err != nil {
		return nil, nil, err
	}
	var owner model.User
	if !actor.HasUnlimitedClients() {
		err := db.First(&owner, actor.Id).Error
		return &rec, &owner, err
	}
	err := db.Model(&model.User{}).Joins("JOIN delegated_clients dc ON dc.owner_id = users.id").
		Where("dc.sub_id = ? AND (users.id = ? OR users.id IN (?))", rec.SubID, actor.Id, accountDescendants(db, actor.Id)).
		First(&owner).Error
	return &rec, &owner, err
}

func assignmentSource(db *gorm.DB, actor *model.User, email string) (*model.ClientRecord, model.DelegatedClient, error) {
	var rec model.ClientRecord
	var link model.DelegatedClient
	query := db.Model(&model.ClientRecord{})
	if !actor.IsPanelOwner() {
		query = editableClients(db, actor).Select("clients.*")
	}
	if err := query.Where("clients.email = ?", email).First(&rec).Error; err != nil {
		return nil, link, err
	}
	if rec.SubID == "" {
		return nil, link, errDelegationInput
	}
	linkQuery := db.Where("sub_id = ?", rec.SubID)
	if actor.HasUnlimitedClients() {
		linkQuery = linkQuery.Where("owner_id = ? OR owner_id IN (?)", actor.Id, accountDescendants(db, actor.Id))
	}
	err := linkQuery.First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && actor.IsPanelOwner() {
		err = nil
	}
	return &rec, link, err
}

func assignmentAccounts(db *gorm.DB, actor *model.User, link model.DelegatedClient) *gorm.DB {
	query := db.Model(&model.User{}).Where("id <> ?", link.OwnerID)
	if actor.HasUnlimitedClients() {
		return query.Where("(id = ? OR id IN (?)) AND panel_role IN ?", actor.Id, accountDescendants(db, actor.Id), managedPanelRoles(actor))
	}
	if !actor.IsPanelOwner() {
		return query.Where("id IN (?) AND panel_role IN ?", accountDescendants(db, actor.Id), managedPanelRoles(actor))
	}
	if link.OwnerID != 0 {
		return query.Where("parent_id > 0 OR id = ?", actor.Id)
	}
	return query.Where("parent_id > 0")
}

func assignmentAllowed(db *gorm.DB, target *model.User, inboundIDs []int) (bool, error) {
	if target.IsPanelOwner() {
		return true, nil
	}
	for _, id := range inboundIDs {
		allowed, err := inboundAccess(db, target, id)
		if err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}

func assignmentInbounds(db *gorm.DB, rec *model.ClientRecord) ([]int, error) {
	var ids []int
	err := db.Model(&model.ClientInbound{}).Where("client_id = ?", rec.Id).Pluck("inbound_id", &ids).Error
	return ids, err
}

func (d *delegationController) assignmentTargets(c *gin.Context) {
	actor := delegatedUser(c)
	if !canAssignClients(actor) {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	db := database.GetDB()
	rec, link, err := assignmentSource(db, actor, c.Param("email"))
	if err != nil {
		delegationError(c, err)
		return
	}
	ids, err := assignmentInbounds(db, rec)
	if err != nil {
		delegationError(c, err)
		return
	}
	remaining, err := delegatedTrafficRemaining(db, rec)
	if err != nil {
		delegationError(c, err)
		return
	}
	var targets []model.User
	if err := assignmentAccounts(db, actor, link).Order("username, id").Find(&targets).Error; err != nil {
		delegationError(c, err)
		return
	}
	options := make([]gin.H, 0, len(targets))
	for _, target := range targets {
		allowed, err := assignmentAllowed(db, &target, ids)
		if err != nil {
			delegationError(c, err)
			return
		}
		if allowed {
			hasCredit := actor.IsPanelOwner() || target.HasUnlimitedClients() ||
				((target.UncappedClientSlots || target.ClientCredits > 0) && target.TrafficCredits >= remaining)
			if hasCredit && !actor.IsPanelOwner() && rec.TotalGB == 0 {
				err := checkUnlimitedClientCapacity(db, &target)
				if err != nil && !errors.Is(err, errDelegationCredit) {
					delegationError(c, err)
					return
				}
				hasCredit = err == nil
			}
			options = append(options, gin.H{"id": target.Id, "username": target.Username,
				"role": accountView(target).Role, "hasCredit": hasCredit})
		}
	}
	jsonObj(c, options, nil)
}

func (d *delegationController) assignScopedClient(c *gin.Context) {
	actor := delegatedUser(c)
	if !canAssignClients(actor) {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		AccountID int `json:"accountId"`
	}
	if c.ShouldBindJSON(&body) != nil || body.AccountID <= 0 {
		delegationError(c, errDelegationInput)
		return
	}
	delegatedClientWrite.Lock()
	defer delegatedClientWrite.Unlock()
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		rec, link, err := assignmentSource(tx, actor, c.Param("email"))
		if err != nil {
			return err
		}
		var target model.User
		if err := assignmentAccounts(tx, actor, link).First(&target, body.AccountID).Error; err != nil {
			return err
		}
		ids, err := assignmentInbounds(tx, rec)
		if err != nil {
			return err
		}
		allowed, err := assignmentAllowed(tx, &target, ids)
		if err != nil {
			return err
		}
		if !allowed {
			return errAssignmentInbound
		}
		remaining, err := delegatedTrafficRemaining(tx, rec)
		if err != nil {
			return err
		}
		free := actor.IsPanelOwner() || target.HasUnlimitedClients()
		freeSlot := free || target.UncappedClientSlots
		if !free {
			if rec.TotalGB == 0 {
				if err := checkUnlimitedClientCapacity(tx, &target); err != nil {
					return err
				}
			}
			var slots int64
			if !freeSlot {
				slots = 1
			}
			if err := debitCredits(tx, target.Id, slots, remaining); err != nil {
				return err
			}
		}
		if !actor.IsPanelOwner() {
			if err := addDelegatedBalance(tx, link.OwnerID, refundableClientSlots(link), max(0, remaining-link.FreeTraffic)); err != nil {
				return err
			}
		}
		if rec.Group != "" {
			if err := service.ClearClientGroupOnTransfer(tx, rec.Email); err != nil {
				return err
			}
		}
		if target.IsPanelOwner() {
			return tx.Delete(&link).Error
		}
		next := model.DelegatedClient{SubID: rec.SubID, OwnerID: target.Id, FreeSlot: freeSlot}
		if free {
			next.FreeTraffic = rec.TotalGB
		}
		if link.OwnerID == 0 {
			return tx.Create(&next).Error
		}
		result := tx.Model(&model.DelegatedClient{}).Where("sub_id = ? AND owner_id = ?", rec.SubID, link.OwnerID).
			Select("OwnerID", "FreeSlot", "FreeTraffic").Updates(&next)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if errors.Is(err, errAssignmentInbound) {
		delegationFailure(c, http.StatusConflict, errAssignmentInbound.Error())
	} else if err != nil {
		delegationError(c, err)
	} else {
		notifyClientsChanged()
		jsonObj(c, true, nil)
	}
}
