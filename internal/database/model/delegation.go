package model

import (
	"strconv"
	"strings"
)

const (
	PanelRoleOwner    = "owner"
	PanelRoleSudo     = "sudo"
	PanelRoleAdmin    = "admin"
	PanelRoleReseller = "reseller"
	PanelRoleInfinity = "infinity"
)

func (u User) IsPanelOwner() bool {
	return u.ParentID == 0 && (u.PanelRole == "" || u.PanelRole == PanelRoleOwner)
}

func (u User) CanManagePanelAccounts() bool {
	return u.IsPanelOwner() || u.PanelRole == PanelRoleSudo || u.PanelRole == PanelRoleAdmin || u.HasUnlimitedClients()
}

func (u User) HasUnlimitedClients() bool { return u.PanelRole == PanelRoleInfinity }

func (u User) HasDelegatedSudoAccess() bool {
	return u.PanelRole == PanelRoleSudo || u.HasUnlimitedClients()
}

func (u User) CanCreatePanelRole(role string) bool {
	switch role {
	case PanelRoleSudo:
		return u.IsPanelOwner()
	case PanelRoleAdmin, PanelRoleReseller:
		return u.CanManagePanelAccounts()
	case PanelRoleInfinity:
		return u.IsPanelOwner() || u.HasDelegatedSudoAccess()
	default:
		return false
	}
}

// DelegatedInbound records the local inbounds the owner has made available for sales.
type DelegatedInbound struct {
	InboundID int `gorm:"column:inbound_id;primaryKey;autoIncrement:false"`
}

// DelegatedClient ties an issued account to the creator and the generated
// subscription identity. Reusing a client's email does not transfer ownership.
type DelegatedClient struct {
	SubID   string `gorm:"column:sub_id;primaryKey"`
	OwnerID int    `gorm:"column:owner_id;index;not null"`
	// Grants without a debit cannot be converted to reusable credits.
	FreeSlot    bool  `gorm:"not null;default:false"`
	FreeTraffic int64 `gorm:"not null;default:0"`
}

func (u User) IsDelegatedOperator() bool {
	return u.ParentID > 0 && (u.PanelRole == PanelRoleSudo || u.PanelRole == PanelRoleAdmin || u.PanelRole == PanelRoleReseller || u.HasUnlimitedClients())
}

// DelegatedInboundGrant authorizes a direct child to use an inbound, provided
// each ancestor still has access to it.
type DelegatedInboundGrant struct {
	UserID    int `gorm:"primaryKey;autoIncrement:false;column:user_id"`
	InboundID int `gorm:"primaryKey;autoIncrement:false;column:inbound_id"`
}

// DelegatedTelegramBackup stores an account's independent delivery settings.
type DelegatedTelegramBackup struct {
	UserID      int    `gorm:"primaryKey;autoIncrement:false;column:user_id"`
	Enabled     bool
	BotToken    string `json:"-"`
	ChatID      int64
	AttemptDay  string
	LastSentAt  int64
	Status      string
}

// Delegated groups are client labels and must not join an owner's group
// with shared accounting, even when the displayed name is identical.
func DelegatedGroupName(ownerID int, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "delegated/" + strconv.Itoa(ownerID) + "/" + name
}

func DisplayDelegatedGroupName(ownerID int, name string) string {
	return strings.TrimPrefix(name, "delegated/"+strconv.Itoa(ownerID)+"/")
}
