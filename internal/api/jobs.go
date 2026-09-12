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

// Reads are available to authenticated roles; mutation checks include job kind.
func RegisterJobRoutes(router fiber.Router, manager *jobs.Manager, service *operations.Service, authMgr *auth.AuthManager) {
	group := router.Group("/jobs", auth.RequireAuth(authMgr), func(c *fiber.Ctx) error {
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
		if !auth.CanJob(roleName(c), r.Kind) {
			return fiber.NewError(403, "Operation not permitted for this role")
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
		return c.JSON(fiber.Map{"plan": plan, "cluster": service.ConfirmationName()})
	})
	group.Post("/", func(c *fiber.Ctx) error {
		var body struct {
			jobs.Request
			ConfirmedCluster string `json:"confirmedCluster"`
		}
		if err := c.BodyParser(&body); err != nil {
			return fiber.NewError(400, "Invalid operation request")
		}
		if !auth.CanJob(roleName(c), body.Kind) {
			return fiber.NewError(403, "Operation not permitted for this role")
		}
		if err := operations.Validate(body.Request); err != nil {
			return fiber.NewError(400, err.Error())
		}
		if service.Talos == nil || body.ConfirmedCluster == "" || body.ConfirmedCluster != service.ConfirmationName() {
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
	group.Get("/:id/export", func(c *fiber.Ctx) error {
		job, err := manager.Get(c.Params("id"))
		if err != nil {
			return jobError(err)
		}
		c.Set("Content-Disposition", `attachment; filename="job-`+job.ID+`.json"`)
		return c.JSON(job)
	})
	group.Post("/:id/reconcile", func(c *fiber.Ctx) error {
		j, err := manager.Get(c.Params("id"))
		if err != nil {
			return jobError(err)
		}
		if !auth.CanJob(roleName(c), j.Request.Kind) {
			return fiber.NewError(403, "Operation not permitted for this role")
		}
		if len(c.Body()) != 0 {
			return fiber.NewError(400, "Reconciliation accepts no client-supplied evidence")
		}
		if service.Provision == nil {
			return fiber.NewError(409, "Provider reconciliation unavailable")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		observed, err := service.Provision.ReconcileJob(ctx, manager, j.ID)
		if err != nil {
			return fiber.NewError(409, "Job reconciliation requires review; no infrastructure operation was started")
		}
		return c.JSON(observed)
	})
	group.Post("/:id/stop", func(c *fiber.Ctx) error {
		j, err := manager.Get(c.Params("id"))
		if err != nil {
			return jobError(err)
		}
		if !auth.CanJob(roleName(c), j.Request.Kind) {
			return fiber.NewError(403, "Operation not permitted for this role")
		}
		if err := manager.Stop(c.Params("id")); err != nil {
			return jobError(err)
		}
		return c.SendStatus(fiber.StatusAccepted)
	})
	group.Post("/:id/acknowledge", func(c *fiber.Ctx) error {
		j, err := manager.Get(c.Params("id"))
		if err != nil {
			return jobError(err)
		}
		if !auth.CanJob(roleName(c), j.Request.Kind) {
			return fiber.NewError(403, "Operation not permitted for this role")
		}
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
func roleName(c *fiber.Ctx) string { role, _ := c.Locals("role").(string); return role }
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
		if strings.HasPrefix(path, "/api/backups") {
			return c.Next()
		}
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
