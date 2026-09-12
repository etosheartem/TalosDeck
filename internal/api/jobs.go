package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/jobs"
	"talosdeck/internal/operations"
)

// Job reads include operational details and are admin-only, just like submission.
func RegisterJobRoutes(router fiber.Router, manager *jobs.Manager, service *operations.Service, authMgr *auth.AuthManager) {
	group := router.Group("/jobs", auth.RequireAuth(authMgr), func(c *fiber.Ctx) error {
		if c.Locals("role") != "admin" {
			return fiber.NewError(fiber.StatusForbidden, "Administrator access required")
		}
		if manager == nil || service == nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, "Background jobs are unavailable")
		}
		return c.Next()
	})
	group.Get("/", func(c *fiber.Ctx) error { return c.JSON(manager.List()) })
	group.Post("/plan", func(c *fiber.Ctx) error {
		var r jobs.Request
		if err := c.BodyParser(&r); err != nil {
			return fiber.NewError(400, "Invalid operation request")
		}
		if err := operations.Validate(r); err != nil {
			return fiber.NewError(400, err.Error())
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 50*time.Second)
		defer cancel()
		plan, err := service.Preflight(ctx, r)
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		return c.JSON(fiber.Map{"plan": plan, "cluster": service.Talos.GetClusterName()})
	})
	group.Post("/", func(c *fiber.Ctx) error {
		var body struct {
			jobs.Request
			ConfirmedCluster string `json:"confirmedCluster"`
		}
		if err := c.BodyParser(&body); err != nil {
			return fiber.NewError(400, "Invalid operation request")
		}
		if err := operations.Validate(body.Request); err != nil {
			return fiber.NewError(400, err.Error())
		}
		if service.Talos == nil || body.ConfirmedCluster == "" || body.ConfirmedCluster != service.Talos.GetClusterName() {
			return fiber.NewError(400, "Type the cluster name to confirm the operation")
		}
		job, err := manager.Submit(body.Request, auth.GetContextUser(c, authMgr))
		if err != nil {
			return jobError(err)
		}
		return c.Status(fiber.StatusAccepted).JSON(job)
	})
	group.Get("/:id", func(c *fiber.Ctx) error {
		job, err := manager.Get(c.Params("id"))
		if err != nil {
			return jobError(err)
		}
		return c.JSON(job)
	})
	group.Post("/:id/stop", func(c *fiber.Ctx) error {
		if err := manager.Stop(c.Params("id")); err != nil {
			return jobError(err)
		}
		return c.SendStatus(fiber.StatusAccepted)
	})
	group.Post("/:id/acknowledge", func(c *fiber.Ctx) error {
		var body struct {
			Reviewed bool `json:"reviewed"`
		}
		if err := c.BodyParser(&body); err != nil || !body.Reviewed {
			return fiber.NewError(400, "Cluster state review must be explicitly acknowledged")
		}
		if err := manager.Acknowledge(c.Params("id"), auth.GetContextUser(c, authMgr)); err != nil {
			return jobError(err)
		}
		return c.SendStatus(fiber.StatusOK)
	})
}
func jobError(err error) error {
	switch {
	case errors.Is(err, jobs.ErrBusy):
		return fiber.NewError(409, err.Error())
	case errors.Is(err, jobs.ErrNotFound):
		return fiber.NewError(404, err.Error())
	default:
		return fiber.NewError(503, err.Error())
	}
}

// Protect legacy node, backup and provisioning mutations with the same cluster
// lock as asynchronous upgrades. Reads and stop/review actions stay available.
func jobMutationGuard(manager *jobs.Manager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if manager == nil || c.Method() == fiber.MethodGet || c.Method() == fiber.MethodHead || c.Method() == fiber.MethodOptions {
			return c.Next()
		}
		path := c.Path()
		if !strings.HasPrefix(path, "/api/nodes/") && !strings.HasPrefix(path, "/api/proxmox/") && !strings.HasPrefix(path, "/api/backups") {
			return c.Next()
		}
		release, err := manager.ReserveManual()
		if err != nil {
			return jobError(err)
		}
		defer release()
		return c.Next()
	}
}
