package service

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestDelegatedTelegramArchiveScope(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "panel.db"))
	db := database.GetDB()
	var root model.User
	if err := db.First(&root).Error; err != nil {
		t.Fatal(err)
	}
	add := func(name, role string, parent int) int {
		t.Helper()
		u := model.User{Username: name, PanelRole: role, ParentID: parent, Password: "secret-account-hash"}
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ClientRecord{Email: name + "-client", SubID: name + "-sub", TotalGB: 123}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.DelegatedClient{SubID: name + "-sub", OwnerID: u.Id}).Error; err != nil {
			t.Fatal(err)
		}
		return u.Id
	}
	admin := add("branch-admin", model.PanelRoleAdmin, root.Id)
	child := add("branch-child", model.PanelRoleAdmin, admin)
	add("branch-grandchild", model.PanelRoleReseller, child)
	add("sibling-outside", model.PanelRoleReseller, root.Id)
	if err := db.Create(&model.DelegatedTelegramBackup{UserID: admin, Enabled: true, ChatID: 1234, BotToken: "secret-bot-token"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientRecord{Email: "unassigned-outside", SubID: "unassigned-sub"}).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := delegatedTelegramArchive(db, admin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Accounts  []backupAccount
		Clients   []ClientCreatePayload
		Ownership []model.DelegatedClient
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 3 || len(got.Clients) != 3 || len(got.Ownership) != 3 ||
		got.Accounts[0].ParentID != 0 || got.Accounts[2].ParentID != child {
		t.Fatalf("unexpected branch: %+v", got)
	}
	for _, excluded := range []string{"secret-account-hash", "secret-bot-token", "sibling-outside", "unassigned-outside"} {
		if strings.Contains(string(raw), excluded) {
			t.Fatalf("archive included %q", excluded)
		}
	}
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	attempts := 0
	send := func(cfg model.DelegatedTelegramBackup, day string, data []byte) error {
		attempts++
		if cfg.UserID != admin || cfg.ChatID != 1234 || !strings.Contains(string(data), "branch-grandchild-client") {
			t.Fatal("wrong archive or recipient")
		}
		return nil
	}
	runDelegatedTelegramBackups(db, now, send)
	runDelegatedTelegramBackups(db, now.Add(time.Hour), send)
	if attempts != 1 {
		t.Fatalf("same day attempts = %d, want 1", attempts)
	}
	runDelegatedTelegramBackups(db, now.AddDate(0, 0, 1), send)
	var cfg model.DelegatedTelegramBackup
	if err := db.First(&cfg, admin).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || cfg.Status != "sent" || cfg.LastSentAt == 0 || cfg.AttemptDay != "2026-11-21" {
		t.Fatalf("second day: attempts=%d status=%s lastSent=%d", attempts, cfg.Status, cfg.LastSentAt)
	}
}
