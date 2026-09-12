package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"talosdeck/internal/alertcenter"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
)

func strictBody(c *fiber.Ctx, value any) error {
	if len(c.Body()) > 64<<10 {
		return fiber.ErrRequestEntityTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return fiber.NewError(400, "Invalid request fields")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fiber.NewError(400, "Expected one JSON object")
	}
	return nil
}
func centerError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, alertcenter.ErrNotFound):
		return fiber.NewError(404, "Alert Center record not found")
	case errors.Is(err, alertcenter.ErrConflict):
		return fiber.NewError(409, "Settings changed or operation conflicts; reload and retry")
	case errors.Is(err, alertcenter.ErrInvalid):
		return fiber.NewError(422, "Invalid notification settings or alert operation")
	default:
		return fiber.NewError(503, "Alert Center storage or delivery unavailable")
	}
}
func pageBounds(c *fiber.Ctx, total int) (int, int, any, error) {
	offset, limit := 0, 100
	var err error
	if c.Query("offset") != "" {
		offset, err = strconv.Atoi(c.Query("offset"))
		if err != nil || offset < 0 {
			return 0, 0, nil, fiber.ErrBadRequest
		}
	}
	if c.Query("limit") != "" {
		limit, err = strconv.Atoi(c.Query("limit"))
		if err != nil || limit < 1 || limit > 500 {
			return 0, 0, nil, fiber.ErrBadRequest
		}
	}
	if offset > total {
		offset = total
	}
	end := min(total, offset+limit)
	var next any
	if end < total {
		next = end
	}
	return offset, end, next, nil
}

func RegisterAlertCenterRoutes(router fiber.Router, center *alertcenter.Center, am *auth.AuthManager, journal *audit.AuditManager) {
	if center == nil {
		return
	}
	record := func(c *fiber.Ctx, action, id string) {
		if journal != nil {
			journal.Log(audit.AuditEvent{Action: action, User: auth.GetContextUser(c, am), IP: auth.GetClientIP(c), Status: "success", Details: map[string]any{"id": id}})
		}
	}
	alerts := router.Group("/alerts", auth.RequireAuth(am))
	alerts.Get("/", func(c *fiber.Ctx) error {
		snapshot := center.Snapshot()
		state, severity := c.Query("state", "active"), c.Query("severity")
		if state != "active" && state != "resolved" && state != "all" {
			return fiber.ErrBadRequest
		}
		if severity != "" && severity != "info" && severity != "warning" && severity != "critical" {
			return fiber.ErrBadRequest
		}
		rows := []alertcenter.Alert{}
		for _, a := range snapshot.Alerts {
			if (state == "all" || a.State == state) && (severity == "" || a.Severity == severity) {
				rows = append(rows, a)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].LastChanged.Equal(rows[j].LastChanged) {
				return rows[i].ID < rows[j].ID
			}
			return rows[i].LastChanged.After(rows[j].LastChanged)
		})
		start, end, next, err := pageBounds(c, len(rows))
		if err != nil {
			return err
		}
		c.Set("Cache-Control", "no-store")
		return c.JSON(fiber.Map{"alerts": rows[start:end], "summary": snapshot.Summary, "health": snapshot.Health, "lastCheckAt": snapshot.LastCheckAt, "total": len(rows), "offset": start, "limit": c.QueryInt("limit", 100), "nextOffset": next})
	})
	alerts.Get("/silences", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"silences": center.Silences()}) })
	alerts.Post("/silences", func(c *fiber.Ctx) error {
		var input alertcenter.Silence
		if err := strictBody(c, &input); err != nil {
			return err
		}
		input.ID = ""
		input.CreatedBy = auth.GetContextUser(c, am)
		input.CreatedAt = time.Now().UTC()
		value, err := center.AddSilence(c.UserContext(), input)
		if err != nil {
			return centerError(err)
		}
		record(c, "alert.silence.create", value.ID)
		return c.Status(201).JSON(value)
	})
	alerts.Delete("/silences/:id", func(c *fiber.Ctx) error {
		if err := center.DeleteSilence(c.UserContext(), c.Params("id")); err != nil {
			return centerError(err)
		}
		record(c, "alert.silence.delete", c.Params("id"))
		return c.JSON(fiber.Map{"success": true})
	})
	// The old Telegram-only settings must not remain a second sending pipeline.
	alerts.All("/config", func(c *fiber.Ctx) error {
		return fiber.NewError(410, "Use notification channels and routes in cluster settings")
	})
	alerts.All("/test", func(c *fiber.Ctx) error { return fiber.NewError(410, "Use an explicit notification channel test") })
	alerts.Get("/:id", func(c *fiber.Ctx) error {
		value, ok := center.Alert(c.Params("id"))
		if !ok {
			return fiber.ErrNotFound
		}
		return c.JSON(value)
	})
	notifications := router.Group("/notifications", auth.RequireAuth(am))
	notifications.Get("/status", func(c *fiber.Ctx) error {
		snapshot := center.Snapshot()
		return c.JSON(fiber.Map{"health": snapshot.Health, "lastCheckAt": snapshot.LastCheckAt, "summary": snapshot.Summary})
	})
	notifications.Get("/channels", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"channels": center.Channels()}) })
	saveChannel := func(c *fiber.Ctx) error {
		var input alertcenter.ChannelInput
		if err := strictBody(c, &input); err != nil {
			return err
		}
		if c.Method() == "POST" {
			if input.ID != "" {
				return fiber.ErrBadRequest
			}
		} else {
			if input.ID != "" && input.ID != c.Params("id") {
				return fiber.ErrBadRequest
			}
			input.ID = c.Params("id")
		}
		if input.RecipientUser != "" {
			found := false
			for _, user := range am.ListUsers() {
				if user.Username == input.RecipientUser && !user.Disabled {
					found = true
					break
				}
			}
			if !found {
				return fiber.NewError(422, "Notification recipient must be an active user")
			}
		}
		value, err := center.SaveChannel(c.UserContext(), input)
		if err != nil {
			return centerError(err)
		}
		record(c, "notification.channel.save", value.ID)
		return c.JSON(value)
	}
	notifications.Post("/channels", saveChannel)
	notifications.Put("/channels/:id", saveChannel)
	notifications.Delete("/channels/:id", func(c *fiber.Ctx) error {
		if err := center.DeleteChannel(c.UserContext(), c.Params("id")); err != nil {
			return centerError(err)
		}
		record(c, "notification.channel.delete", c.Params("id"))
		return c.JSON(fiber.Map{"success": true})
	})
	testLimit := limiter.New(limiter.Config{Max: 5, Expiration: time.Minute, KeyGenerator: auth.GetClientIP})
	notifications.Post("/channels/:id/test", testLimit, func(c *fiber.Ctx) error {
		if err := center.TestChannel(c.UserContext(), c.Params("id"), auth.GetContextUser(c, am)); err != nil {
			return centerError(err)
		}
		record(c, "notification.channel.test", c.Params("id"))
		return c.Status(202).JSON(fiber.Map{"queued": true})
	})
	notifications.Get("/routes", func(c *fiber.Ctx) error { return c.JSON(center.Routes()) })
	notifications.Put("/routes", func(c *fiber.Ctx) error {
		var input alertcenter.RoutesConfig
		if err := strictBody(c, &input); err != nil {
			return err
		}
		if err := center.SaveRoutes(c.UserContext(), input); err != nil {
			return centerError(err)
		}
		record(c, "notification.routes.save", "")
		return c.JSON(center.Routes())
	})
	notifications.Get("/deliveries", func(c *fiber.Ctx) error {
		rows := []alertcenter.Delivery{}
		for _, d := range center.Deliveries() {
			if c.Query("state") == "" || c.Query("state") == d.State {
				rows = append(rows, d)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
		start, end, next, err := pageBounds(c, len(rows))
		if err != nil {
			return err
		}
		return c.JSON(fiber.Map{"deliveries": rows[start:end], "total": len(rows), "offset": start, "limit": c.QueryInt("limit", 100), "nextOffset": next})
	})
	notifications.Post("/deliveries/:id/retry", testLimit, func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
		defer cancel()
		if err := center.RetryDelivery(ctx, c.Params("id")); err != nil {
			return centerError(err)
		}
		record(c, "notification.delivery.retry", c.Params("id"))
		return c.Status(202).JSON(fiber.Map{"queued": true})
	})
}
