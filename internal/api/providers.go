package api

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/jobs"
	"talosdeck/internal/operations"
	"talosdeck/internal/proxmox"
)

func provisionAdmin(manager *auth.AuthManager) []fiber.Handler {
	return []fiber.Handler{auth.RequireAuth(manager), func(c *fiber.Ctx) error {
		if c.Locals("role") != "admin" {
			return fiber.NewError(403, "Administrator access required")
		}
		return c.Next()
	}}
}
func RegisterProviderRoutes(router fiber.Router, store operations.ProvisionStore, manager *auth.AuthManager) {
	handlers := provisionAdmin(manager)
	group := router.Group("/providers", handlers...)
	group.Get("/", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		records, err := operations.ListProviders(ctx, store)
		if err != nil {
			return fiber.NewError(503, err.Error())
		}
		return c.JSON(records)
	})
	group.Post("/", func(c *fiber.Ctx) error {
		var request struct {
			Name   string         `json:"name"`
			Kind   string         `json:"kind"`
			Config proxmox.Config `json:"config"`
		}
		if c.BodyParser(&request) != nil {
			return fiber.NewError(400, "Invalid provider request")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		provider, err := operations.SaveProvider(ctx, store, request.Name, request.Kind, request.Config, true)
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		return c.Status(201).JSON(provider)
	})
	group.Delete("/:id", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		if err := operations.DeleteProvider(ctx, store, c.Params("id")); err != nil {
			return fiber.NewError(409, err.Error())
		}
		return c.SendStatus(204)
	})
}
func RegisterProvisionRoutes(router fiber.Router, manager *jobs.Manager, service *operations.ProvisionService, authManager *auth.AuthManager) {
	handlers := provisionAdmin(authManager)
	handlers = append(handlers, func(c *fiber.Ctx) error {
		if manager == nil || service == nil {
			return fiber.NewError(503, "Provisioning unavailable")
		}
		return c.Next()
	})
	group := router.Group("/provision", handlers...)
	group.Post("/plan", func(c *fiber.Ctx) error {
		var spec operations.ProvisionSpec
		if c.BodyParser(&spec) != nil {
			return fiber.NewError(400, "Invalid provisioning specification")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 40*time.Second)
		defer cancel()
		plan, err := service.Plan(ctx, spec, auth.GetContextUser(c, authManager))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		return c.JSON(plan)
	})
	group.Post("/", func(c *fiber.Ctx) error {
		var request struct {
			PlanID        string `json:"planId"`
			ConfirmedName string `json:"confirmedName"`
		}
		if c.BodyParser(&request) != nil {
			return fiber.NewError(400, "Invalid provisioning confirmation")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		jobRequest, err := service.Request(ctx, request.PlanID, request.ConfirmedName, auth.GetContextUser(c, authManager))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		job, err := manager.Submit(jobRequest, auth.GetContextUser(c, authManager))
		if err != nil {
			return jobError(err)
		}
		return c.Status(202).JSON(job)
	})
	machineHandlers := provisionAdmin(authManager)
	machineHandlers = append(machineHandlers, func(c *fiber.Ctx) error {
		if service == nil {
			return fiber.NewError(503, "Provisioning unavailable")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		machines, err := operations.ListOwnedMachines(ctx, service.Store, service.ClusterID)
		if err != nil {
			return fiber.NewError(503, err.Error())
		}
		return c.JSON(machines)
	})
	router.Get("/machines", machineHandlers...)
}
