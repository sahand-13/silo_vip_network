package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type backupAccount struct {
	ID                   int    `json:"id"`
	ParentID             int    `json:"parentId"`
	Username             string `json:"username"`
	PanelRole            string `json:"role"`
	ClientCredits        int64  `json:"clientCredits"`
	TrafficCredits       int64  `json:"trafficCreditsBytes"`
	UncappedClientSlots  bool   `json:"uncappedClientSlots"`
	UnlimitedClientLimit int64  `json:"unlimitedClientLimit"`
}

func delegatedTelegramArchive(db *gorm.DB, userID int, now time.Time) ([]byte, error) {
	out := struct {
		Version   int                     `json:"version"`
		CreatedAt time.Time               `json:"createdAt"`
		Accounts  []backupAccount         `json:"accounts"`
		Clients   []ClientCreatePayload   `json:"clients"`
		Ownership []model.DelegatedClient `json:"ownership"`
	}{Version: 1, CreatedAt: now.UTC(), Accounts: []backupAccount{}, Ownership: []model.DelegatedClient{}}
	err := db.Transaction(func(tx *gorm.DB) error {
		branch := func() *gorm.DB {
			return tx.Raw(`WITH RECURSIVE branch(id) AS (
				SELECT id FROM users WHERE id = ? AND parent_id > 0
				UNION SELECT u.id FROM users u JOIN branch b ON u.parent_id = b.id
			) SELECT id FROM branch`, userID)
		}
		if err := tx.Table("users").Select("id, parent_id, username, panel_role, client_credits, traffic_credits, uncapped_client_slots, unlimited_client_limit").
			Where("id IN (?)", branch()).Order("id").Find(&out.Accounts).Error; err != nil {
			return err
		}
		if len(out.Accounts) == 0 {
			return gorm.ErrRecordNotFound
		}
		for i := range out.Accounts {
			if out.Accounts[i].ID == userID {
				out.Accounts[i].ParentID = 0
			}
		}
		if err := tx.Where("owner_id IN (?)", branch()).Order("sub_id").Find(&out.Ownership).Error; err != nil {
			return err
		}
		var err error
		selected := tx.Model(&model.ClientRecord{}).Where("sub_id IN (?)",
			tx.Model(&model.DelegatedClient{}).Select("sub_id").Where("owner_id IN (?)", branch()))
		out.Clients, err = (&ClientService{}).exportClients(tx, selected)
		return err
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

var delegatedBackupRun sync.Mutex

// RunDelegatedTelegramBackups attempts one send per account and local calendar day.
func RunDelegatedTelegramBackups() {
	if !delegatedBackupRun.TryLock() {
		return
	}
	defer delegatedBackupRun.Unlock()
	db := database.GetDB().Session(&gorm.Session{Logger: logger.Discard})
	now := time.Now()
	if loc, err := (&SettingService{}).GetTimeLocation(); err == nil && loc != nil {
		now = now.In(loc)
	}
	runDelegatedTelegramBackups(db, now, sendDelegatedTelegramBackup)
}

func runDelegatedTelegramBackups(db *gorm.DB, now time.Time, send func(model.DelegatedTelegramBackup, string, []byte) error) {
	day := now.Format(time.DateOnly)
	var settings []model.DelegatedTelegramBackup
	if err := db.Where("enabled = ? AND COALESCE(attempt_day, '') <> ?", true, day).Find(&settings).Error; err != nil {
		return
	}
	for _, cfg := range settings {
		current := func() *gorm.DB {
			return db.Model(&model.DelegatedTelegramBackup{}).Where("user_id = ? AND enabled = ? AND bot_token = ? AND chat_id = ?", cfg.UserID, true, cfg.BotToken, cfg.ChatID)
		}
		claim := current().Where("COALESCE(attempt_day, '') <> ?", day).
			Updates(map[string]any{"attempt_day": day, "status": "pending"})
		if claim.Error != nil || claim.RowsAffected != 1 {
			continue
		}
		data, err := delegatedTelegramArchive(db, cfg.UserID, now)
		var active int64
		if current().Count(&active).Error != nil || active != 1 {
			continue
		}
		update := map[string]any{"status": "failed"}
		if err == nil && send(cfg, day, data) == nil {
			update["status"] = "sent"
			update["last_sent_at"] = time.Now().Unix()
		}
		current().Where("attempt_day = ?", day).Updates(update)
	}
}

var delegatedTelegramHTTP = &http.Client{
	Timeout:       45 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func delegatedTelegramClient() (*http.Client, error) {
	settings := &SettingService{}
	proxy, err := settings.GetTgBotProxy()
	if err != nil {
		return nil, errors.New("could not read Telegram proxy settings")
	}
	if proxy == "" {
		proxy = settings.PanelEgressProxyURL()
	}
	if proxy == "" {
		return delegatedTelegramHTTP, nil
	}
	parsed, err := url.Parse(proxy)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "socks5" && parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("invalid Telegram proxy settings")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(parsed)
	client := *delegatedTelegramHTTP
	client.Transport = transport
	return &client, nil
}

func sendDelegatedTelegramBackup(cfg model.DelegatedTelegramBackup, day string, data []byte) error {
	client, err := delegatedTelegramClient()
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("chat_id", strconv.FormatInt(cfg.ChatID, 10)); err != nil {
		return err
	}
	if err := w.WriteField("protect_content", "true"); err != nil {
		return err
	}
	file, err := w.CreateFormFile("document", "panel-backup-"+day+".json")
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	res, err := client.Post("https://api.telegram.org/bot"+cfg.BotToken+"/sendDocument", w.FormDataContentType(), &body)
	if err != nil {
		return errors.New("telegram backup delivery failed")
	}
	defer res.Body.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&result) != nil || !result.OK {
		return errors.New("telegram backup delivery rejected")
	}
	return nil
}
