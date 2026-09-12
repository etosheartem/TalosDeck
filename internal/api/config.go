package api

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/jobs"
	"talosdeck/internal/operations"
)

// RegisterConfigRoutes exposes only redacted previews and durable references.
// Patch bodies are never copied into jobs, audit entries or HTTP error text.
func RegisterConfigRoutes(router fiber.Router, manager *jobs.Manager, service *operations.ConfigService, authMgr *auth.AuthManager) {
	group := router.Group("/config/:node", auth.RequireAuth(authMgr), func(c *fiber.Ctx) error {
		if c.Locals("role") != "admin" {
			return fiber.NewError(403, "Administrator access required")
		}
		if manager == nil || service == nil {
			return fiber.NewError(503, "Configuration operations unavailable")
		}
		return c.Next()
	})
	group.Post("/plan", func(c *fiber.Ctx) error {
		var body struct {
			Patch string `json:"patch"`
			Mode  string `json:"mode"`
		}
		if c.BodyParser(&body) != nil {
			return fiber.NewError(400, "Invalid configuration request")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 50*time.Second)
		defer cancel()
		plan, err := service.Plan(ctx, c.Params("node"), body.Patch, body.Mode, auth.GetContextUser(c, authMgr))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		return c.JSON(plan)
	})
	group.Get("/history", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		revisions, err := service.History(ctx, c.Params("node"))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		return c.JSON(revisions)
	})
	group.Get("/history/:id", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		revision, err := service.Revision(ctx, c.Params("node"), c.Params("id"))
		if err != nil {
			return fiber.NewError(404, err.Error())
		}
		return c.JSON(revision)
	})
	group.Post("/apply", func(c *fiber.Ctx) error {
		var body struct {
			PlanID        string `json:"planId"`
			ConfirmedNode string `json:"confirmedNode"`
		}
		if c.BodyParser(&body) != nil || body.ConfirmedNode != c.Params("node") {
			return fiber.NewError(400, "Type the node address to confirm")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		request, err := service.Request(ctx, c.Params("node"), body.PlanID, auth.GetContextUser(c, authMgr))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		job, err := manager.Submit(request, auth.GetContextUser(c, authMgr))
		if err != nil {
			return jobError(err)
		}
		return c.Status(202).JSON(job)
	})
	group.Post("/restore-plan", func(c *fiber.Ctx) error {
		var body struct {
			RevisionID string `json:"revisionId"`
			Mode       string `json:"mode"`
		}
		if c.BodyParser(&body) != nil {
			return fiber.NewError(400, "Invalid restore request")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 50*time.Second)
		defer cancel()
		plan, err := service.RestorePlan(ctx, c.Params("node"), body.RevisionID, body.Mode, auth.GetContextUser(c, authMgr))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		return c.JSON(plan)
	})
	group.Post("/restore", func(c *fiber.Ctx) error {
		var body struct {
			PlanID        string `json:"planId"`
			ConfirmedNode string `json:"confirmedNode"`
		}
		if c.BodyParser(&body) != nil || body.ConfirmedNode != c.Params("node") {
			return fiber.NewError(400, "Type the node address to confirm")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		request, err := service.Request(ctx, c.Params("node"), body.PlanID, auth.GetContextUser(c, authMgr))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		request.Kind = "config-restore"
		job, err := manager.Submit(request, auth.GetContextUser(c, authMgr))
		if err != nil {
			return jobError(err)
		}
		return c.Status(202).JSON(job)
	})
}
