package api

import (
	"context"
	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/operations"
	"time"
)

func RegisterInspectorRoutes(router fiber.Router, km *k8s.K8sManager, ds *operations.DiagnosticsService, jm *jobs.Manager, am *auth.AuthManager) {
	group := router.Group("/k8s", auth.RequireAuth(am))
	group.Get("/workloads", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		v, err := km.Workloads(ctx, c.Query("namespace"))
		if err != nil {
			return fiber.NewError(503, "Workload inventory unavailable")
		}
		return c.JSON(v)
	})
	group.Get("/events", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		v, err := km.Events(ctx, c.Query("namespace"), "")
		if err != nil {
			return fiber.NewError(503, "Events unavailable")
		}
		return c.JSON(fiber.Map{"events": v})
	})
	group.Get("/storage", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		v, err := km.Storage(ctx)
		if err != nil {
			return fiber.NewError(503, "Storage inventory unavailable")
		}
		return c.JSON(v)
	})
	group.Get("/pods/:namespace/:name", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		v, err := km.InspectPod(ctx, c.Params("namespace"), c.Params("name"), c.Query("container"))
		if err != nil {
			return fiber.NewError(503, "Pod inspection unavailable")
		}
		return c.JSON(v)
	})
	if ds == nil {
		return
	}
	dx := router.Group("/diagnostics", auth.RequireAuth(am))
	dx.Get("/", func(c *fiber.Ctx) error {
		v, err := ds.Latest(c.UserContext())
		if err != nil {
			return fiber.NewError(503, "Diagnostics unavailable")
		}
		return c.JSON(v)
	})
	dx.Post("/run", func(c *fiber.Ctx) error {
		job, err := jm.Submit(jobs.Request{Kind: "diagnostics"}, auth.GetContextUser(c, am))
		if err != nil {
			return jobError(err)
		}
		return c.Status(202).JSON(job)
	})
	dx.Get("/bundle", func(c *fiber.Ctx) error {
		data, err := ds.Bundle(c.UserContext())
		if err != nil {
			return fiber.NewError(409, err.Error())
		}
		c.Set("Content-Type", "application/gzip")
		c.Set("Content-Disposition", `attachment; filename="talosdeck-support.tar.gz"`)
		return c.Send(data)
	})
}
