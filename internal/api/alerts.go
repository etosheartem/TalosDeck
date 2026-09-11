package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"

	"talosdeck/internal/alerts"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
)

// RegisterAlertRoutes sets up the REST endpoints for alerts management.
// API-03: Secures sensitive config & test endpoints with RequireAuth when authMgr is provided.
// API-09: Adds rate limiting to /test to prevent spamming Telegram API.
func RegisterAlertRoutes(r fiber.Router, alertSvc *alerts.TelegramService, watcher *alerts.Watcher, extra ...any) {
	var authMgr *auth.AuthManager
	var auditMgr *audit.AuditManager
	for _, a := range extra {
		switch v := a.(type) {
		case *auth.AuthManager:
			authMgr = v
		case *audit.AuditManager:
			auditMgr = v
		}
	}

	group := r.Group("/alerts")
	if authMgr != nil {
		group.Use(auth.RequireAuth(authMgr))
	}

	// GET /api/alerts/config -> alerting status & masked config (API-03: Always masked)
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
		maskedToken := cfg.BotToken // GetConfig() already masks the bot token via alerts.MaskToken

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

		if len(c.Body()) > 0 {
			if err := c.BodyParser(&req); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": fmt.Sprintf("Invalid JSON request body: %v", err),
				})
			}
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
		// API-03: If token contains asterisks, do not overwrite the existing valid token
		if token != nil && *token != "" && !strings.Contains(*token, "*") {
			newCfg.BotToken = strings.TrimSpace(*token)
		}

		chatID := req.ChatID
		if chatID == nil {
			chatID = req.ChatIDCamel
		}
		if chatID == nil {
			chatID = req.ChatIDUpper
		}
		if chatID != nil && !strings.Contains(*chatID, "*") {
			newCfg.ChatID = strings.TrimSpace(*chatID)
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

		user := auth.GetContextUser(c, authMgr)
		clientIP := auth.GetClientIP(c)

		if err := alertSvc.UpdateConfig(newCfg); err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "alert.config.update",
					User:    user,
					IP:      clientIP,
					Status:  "failed",
					Details: map[string]any{"error": err.Error()},
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("Failed to update config: %v", err),
			})
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "alert.config.update",
				User:    user,
				IP:      clientIP,
				Status:  "success",
				Details: map[string]any{"enabled": newCfg.Enabled, "min_level": newCfg.MinLevel},
			})
		}

		updated := alertSvc.GetConfig()
		maskedChat := alerts.MaskChatID(updated.ChatID)
		maskedToken := updated.BotToken // already masked
		return c.JSON(fiber.Map{
			"success":          true,
			"enabled":          alertSvc.IsEnabled(),
			"bot_configured":   alertSvc.IsConfigured(),
			"bot_token":        maskedToken,
			"bot_token_masked": maskedToken,
			"botToken":         maskedToken,
			"chat_id_masked":   maskedChat,
			"chat_id":          maskedChat,
			"chatID":           maskedChat,
			"min_level":        updated.MinLevel,
			"minLevel":         updated.MinLevel,
		})
	}

	// POST /api/alerts/config -> save/update configuration
	group.Post("/config", handleConfigSave)
	// PUT /api/alerts/config -> update configuration
	group.Put("/config", handleConfigSave)

	// API-09: Rate limiting on test alert notifications to prevent Telegram API spam
	testAlertLimiter := limiter.New(limiter.Config{
		Max:        30,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return auth.GetClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "Too many test alert requests. Please try again later.",
			})
		},
	})

	// POST /api/alerts/test -> test message dispatch with token/chat validation
	group.Post("/test", testAlertLimiter, func(c *fiber.Ctx) error {
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
		if len(c.Body()) > 0 {
			if err := c.BodyParser(&req); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": fmt.Sprintf("Invalid JSON request body: %v", err),
				})
			}
		}

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

		user := auth.GetContextUser(c, authMgr)
		clientIP := auth.GetClientIP(c)

		err := alertSvc.SendTestNotification(botToken, chatID, req.Message)
		if err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "alert.test",
					User:    user,
					IP:      clientIP,
					Status:  "failed",
					Details: map[string]any{"error": err.Error()},
				})
			}
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

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "alert.test",
				User:    user,
				IP:      clientIP,
				Status:  "success",
				Details: map[string]any{"target_chat": targetChat},
			})
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
