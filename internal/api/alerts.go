package api

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"

	"talosdeck/internal/alerts"
)

// RegisterAlertRoutes sets up the REST endpoints for alerts management.
func RegisterAlertRoutes(r fiber.Router, alertSvc *alerts.TelegramService, watcher *alerts.Watcher) {
	group := r.Group("/alerts")

	// GET /api/alerts/config -> alerting status & masked config
	group.Get("/config", func(c *fiber.Ctx) error {
		if alertSvc == nil {
			return c.JSON(fiber.Map{
				"enabled":                false,
				"bot_configured":         false,
				"bot_token":              "",
				"bot_token_masked":       "",
				"botToken":               "",
				"chat_id":                "",
				"chat_id_masked":         "",
				"chatID":                 "",
				"min_level":              "INFO",
				"minLevel":               "INFO",
				"check_interval_seconds": 0,
				"watcher_running":        false,
				"monitored_nodes":        0,
				"active_alerts_count":    0,
				"activeAlertsCount":       0,
				"last_check_time":        nil,
				"lastCheckTime":          nil,
				"recent_alerts":          []alerts.AlertRecord{},
			})
		}

		cfg := alertSvc.GetConfig()
		maskedChat := alerts.MaskChatID(cfg.ChatID)
		maskedToken := cfg.BotToken

		var watcherStatus alerts.WatcherStatus
		activeAlertsCount := 0
		var lastCheckTime any = nil
		if watcher != nil {
			watcherStatus = watcher.GetStatus()
			activeAlertsCount = watcher.GetActiveAlertsCount()
			if !watcherStatus.LastCheckTime.IsZero() {
				lastCheckTime = watcherStatus.LastCheckTime
			}
		}

		minLvl := cfg.MinLevel
		if minLvl == "" {
			minLvl = "INFO"
		}

		recent := alertSvc.GetRecentAlerts()
		if recent == nil {
			recent = []alerts.AlertRecord{}
		}

		return c.JSON(fiber.Map{
			"enabled":                alertSvc.IsEnabled(),
			"bot_configured":         alertSvc.IsConfigured(),
			"bot_token":              maskedToken,
			"bot_token_masked":       maskedToken,
			"botToken":               maskedToken,
			"chat_id":                maskedChat,
			"chat_id_masked":         maskedChat,
			"chatID":                 maskedChat,
			"min_level":              minLvl,
			"minLevel":               minLvl,
			"check_interval_seconds": watcherStatus.IntervalSeconds,
			"watcher_running":        watcherStatus.Running,
			"monitored_nodes":        watcherStatus.MonitoredNodes,
			"active_alerts_count":    activeAlertsCount,
			"activeAlertsCount":       activeAlertsCount,
			"last_check_time":        lastCheckTime,
			"lastCheckTime":          lastCheckTime,
			"recent_alerts":          recent,
			"recentAlerts":           recent,
		})
	})

	// handleConfigSave handles POST and PUT to /api/alerts/config
	handleConfigSave := func(c *fiber.Ctx) error {
		if alertSvc == nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Alert service is not initialized",
			})
		}

		var req struct {
			BotToken      *string `json:"bot_token"`
			BotTokenCamel *string `json:"botToken"`
			ChatID        *string `json:"chat_id"`
			ChatIDCamel   *string `json:"chatId"`
			ChatIDUpper   *string `json:"chatID"`
			Enabled       *bool   `json:"enabled"`
			MinLevel      *string `json:"min_level"`
			MinLevelCamel *string `json:"minLevel"`
		}

		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid request payload",
			})
		}

		curr := alertSvc.GetConfig()
		newCfg := alerts.TelegramConfig{
			BotToken: curr.BotToken,
			ChatID:   curr.ChatID,
			Enabled:  curr.Enabled,
			MinLevel: curr.MinLevel,
		}

		token := req.BotToken
		if token == nil {
			token = req.BotTokenCamel
		}
		if token != nil && *token != "" {
			newCfg.BotToken = *token
		}

		chatID := req.ChatID
		if chatID == nil {
			chatID = req.ChatIDCamel
		}
		if chatID == nil {
			chatID = req.ChatIDUpper
		}
		if chatID != nil {
			newCfg.ChatID = *chatID
		}

		if req.Enabled != nil {
			newCfg.Enabled = *req.Enabled
		}

		minLvl := req.MinLevel
		if minLvl == nil {
			minLvl = req.MinLevelCamel
		}
		if minLvl != nil && *minLvl != "" {
			newCfg.MinLevel = strings.ToUpper(strings.TrimSpace(*minLvl))
		}

		if err := alertSvc.UpdateConfig(newCfg); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("Failed to update config: %v", err),
			})
		}

		updated := alertSvc.GetConfig()
		return c.JSON(fiber.Map{
			"success":        true,
			"enabled":        alertSvc.IsEnabled(),
			"bot_configured": alertSvc.IsConfigured(),
			"bot_token":      updated.BotToken,
			"botToken":       updated.BotToken,
			"chat_id_masked": alertSvc.GetMaskedChatID(),
			"chat_id":        alertSvc.GetMaskedChatID(),
			"chatID":         alertSvc.GetMaskedChatID(),
			"min_level":      updated.MinLevel,
			"minLevel":       updated.MinLevel,
		})
	}

	// POST /api/alerts/config -> save/update configuration
	group.Post("/config", handleConfigSave)
	// PUT /api/alerts/config -> update configuration
	group.Put("/config", handleConfigSave)

	// POST /api/alerts/test -> test message dispatch with token/chat validation
	group.Post("/test", func(c *fiber.Ctx) error {
		if alertSvc == nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"success": false,
				"error":   "Telegram alert service is not initialized.",
			})
		}

		var req struct {
			BotToken      *string `json:"bot_token"`
			BotTokenCamel *string `json:"botToken"`
			ChatID        *string `json:"chat_id"`
			ChatIDCamel   *string `json:"chatId"`
			Message       string  `json:"message"`
		}
		_ = c.BodyParser(&req)

		botToken := ""
		if req.BotToken != nil && *req.BotToken != "" && !strings.Contains(*req.BotToken, "*") {
			botToken = strings.TrimSpace(*req.BotToken)
		} else if req.BotTokenCamel != nil && *req.BotTokenCamel != "" && !strings.Contains(*req.BotTokenCamel, "*") {
			botToken = strings.TrimSpace(*req.BotTokenCamel)
		}

		chatID := ""
		if req.ChatID != nil && *req.ChatID != "" && !strings.Contains(*req.ChatID, "*") {
			chatID = strings.TrimSpace(*req.ChatID)
		} else if req.ChatIDCamel != nil && *req.ChatIDCamel != "" && !strings.Contains(*req.ChatIDCamel, "*") {
			chatID = strings.TrimSpace(*req.ChatIDCamel)
		}

		// If credentials are not provided in request body, check if already configured in service
		if botToken == "" && chatID == "" {
			if !alertSvc.IsConfigured() {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"success": false,
					"error":   "Telegram alerting is not configured. Please set Telegram Bot Token and Chat ID.",
				})
			}
		}

		err := alertSvc.SendTestNotification(botToken, chatID, req.Message)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"success": false,
				"error":   fmt.Sprintf("Failed to send test message: %v", err),
			})
		}

		targetChat := chatID
		if targetChat == "" {
			targetChat = alertSvc.GetMaskedChatID()
		} else {
			targetChat = alerts.MaskChatID(targetChat)
		}

		return c.JSON(fiber.Map{
			"success": true,
			"message": fmt.Sprintf("Test alert delivered to Telegram chat %s", targetChat),
		})
	})

	// GET /api/alerts/history -> recent alert records
	group.Get("/history", func(c *fiber.Ctx) error {
		if alertSvc == nil {
			return c.JSON([]alerts.AlertRecord{})
		}
		return c.JSON(alertSvc.GetRecentAlerts())
	})

	// GET /api/alerts/status -> detailed watcher state
	group.Get("/status", func(c *fiber.Ctx) error {
		if watcher == nil {
			return c.JSON(fiber.Map{
				"running": false,
			})
		}

		return c.JSON(fiber.Map{
			"status": watcher.GetStatus(),
			"nodes":  watcher.GetNodeSnapshots(),
		})
	})
}
