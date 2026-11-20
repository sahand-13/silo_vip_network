package controller

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var delegatedBotToken = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,100}$`)

func (d *delegationController) telegramBackup(c *gin.Context) {
	u := delegatedUser(c)
	if !u.IsDelegatedOperator() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	var cfg model.DelegatedTelegramBackup
	err := database.GetDB().Session(&gorm.Session{Logger: logger.Discard}).First(&cfg, u.Id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		delegationFailure(c, 500, "request_failed")
		return
	}
	chatID := ""
	if cfg.ChatID != 0 {
		chatID = strconv.FormatInt(cfg.ChatID, 10)
	}
	jsonObj(c, gin.H{"enabled": cfg.Enabled, "chatId": chatID, "hasToken": cfg.BotToken != "", "lastSentAt": cfg.LastSentAt, "status": cfg.Status}, nil)
}

func (d *delegationController) saveTelegramBackup(c *gin.Context) {
	u := delegatedUser(c)
	if !u.IsDelegatedOperator() {
		delegationFailure(c, http.StatusForbidden, "forbidden")
		return
	}
	var in struct {
		Enabled  bool   `json:"enabled"`
		BotToken string `json:"botToken"`
		ChatID   string `json:"chatId"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if c.ShouldBindJSON(&in) != nil {
		delegationFailure(c, 400, "invalid_input")
		return
	}
	in.BotToken = strings.TrimSpace(in.BotToken)
	in.ChatID = strings.TrimSpace(in.ChatID)
	chatID, parseErr := strconv.ParseInt(in.ChatID, 10, 64)
	if (in.BotToken != "" && !delegatedBotToken.MatchString(in.BotToken)) ||
		(in.ChatID != "" && (parseErr != nil || chatID <= 0)) || (in.Enabled && chatID <= 0) {
		delegationFailure(c, 400, "invalid_input")
		return
	}
	db := database.GetDB().Session(&gorm.Session{Logger: logger.Discard})
	err := db.Transaction(func(tx *gorm.DB) error {
		var account model.User
		if err := tx.First(&account, u.Id).Error; err != nil {
			return err
		}
		if !account.IsDelegatedOperator() {
			return errDelegationInput
		}
		cfg := model.DelegatedTelegramBackup{UserID: u.Id}
		if err := tx.First(&cfg, u.Id).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if in.BotToken != "" {
			cfg.BotToken = in.BotToken
		}
		if in.Enabled && cfg.BotToken == "" {
			return errDelegationInput
		}
		cfg.Enabled, cfg.ChatID = in.Enabled, chatID
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"enabled", "bot_token", "chat_id"})}).Create(&cfg).Error
	})
	switch {
	case errors.Is(err, errDelegationInput):
		delegationFailure(c, 400, "invalid_input")
	case err != nil:
		// Do not return or log database errors from operations carrying bot tokens.
		delegationFailure(c, 500, "request_failed")
	default:
		jsonObj(c, true, nil)
	}
}
