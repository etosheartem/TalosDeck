package api

import (
	"fmt"

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
				"chat_id":                "",
				"chat_id_masked":         "",
				"check_interval_seconds": 0,
				"watcher_running":        false,
				"monitored_nodes":        0,
			})
		}

		cfg := alertSvc.GetConfig()
		maskedChat := alerts.MaskChatID(cfg.ChatID)

		var watcherStatus alerts.WatcherStatus
		if watcher != nil {
			watcherStatus = watcher.GetStatus()
		}

		return c.JSON(fiber.Map{
			"enabled":                alertSvc.IsEnabled(),
			"bot_configured":         alertSvc.IsConfigured(),
			"chat_id":                maskedChat,
			"chat_id_masked":         maskedChat,
			"check_interval_seconds": watcherStatus.IntervalSeconds,
			"watcher_running":        watcherStatus.Running,
			"monitored_nodes":        watcherStatus.MonitoredNodes,
			"last_check_time":        watcherStatus.LastCheckTime,
		})
	})

	// POST /api/alerts/test -> test message dispatch
	group.Post("/test", func(c *fiber.Ctx) error {
		if alertSvc == nil || !alertSvc.IsConfigured() {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"success": false,
				"error":   "Telegram alerting is not configured. Please set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID.",
			})
		}

		if !alertSvc.IsEnabled() {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"success": false,
				"error":   "Telegram alerting is disabled in settings.",
			})
		}

		var req struct {
			Message string `json:"message"`
		}
		_ = c.BodyParser(&req)

		if err := alertSvc.SendTestMessage(req.Message); err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"success": false,
				"error":   fmt.Sprintf("Failed to send test message: %v", err),
			})
		}

		return c.JSON(fiber.Map{
			"success": true,
			"message": fmt.Sprintf("Test alert delivered to Telegram chat %s", alertSvc.GetMaskedChatID()),
		})
	})

	// PUT /api/alerts/config -> update configuration at runtime
	group.Put("/config", func(c *fiber.Ctx) error {
		if alertSvc == nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Alert service is not initialized",
			})
		}

		var req struct {
			BotToken *string `json:"bot_token"`
			ChatID   *string `json:"chat_id"`
			Enabled  *bool   `json:"enabled"`
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
		}

		if req.BotToken != nil && *req.BotToken != "" {
			newCfg.BotToken = *req.BotToken
		}
		if req.ChatID != nil {
			newCfg.ChatID = *req.ChatID
		}
		if req.Enabled != nil {
			newCfg.Enabled = *req.Enabled
		}

		if err := alertSvc.UpdateConfig(newCfg); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("Failed to update config: %v", err),
			})
		}

		return c.JSON(fiber.Map{
			"success":        true,
			"enabled":        alertSvc.IsEnabled(),
			"bot_configured": alertSvc.IsConfigured(),
			"chat_id_masked": alertSvc.GetMaskedChatID(),
		})
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
