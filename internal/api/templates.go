package api

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/operations"
	"talosdeck/internal/templates"
)

func templateError(err error) error {
	switch {
	case errors.Is(err, templates.ErrNotFound):
		return fiber.NewError(404, "Template or revision not found")
	case errors.Is(err, templates.ErrConflict):
		return fiber.NewError(409, "Template changed or is archived; reload before continuing")
	case errors.Is(err, templates.ErrInvalid):
		return fiber.NewError(422, "Invalid template parameters")
	default:
		return fiber.NewError(503, "Template storage unavailable")
	}
}

type templatePlanner interface {
	PlanFromTemplate(context.Context, operations.ProvisionSpec, string, operations.TemplateReference) (*operations.ProvisionPlan, error)
}

func RegisterTemplateRoutes(router fiber.Router, service *templates.Service, provision templatePlanner, am *auth.AuthManager) {
	group := router.Group("/templates", auth.RequireAuth(am))
	group.Use(func(c *fiber.Ctx) error {
		if service == nil {
			return fiber.NewError(503, "Templates unavailable")
		}
		c.Set("Cache-Control", "no-store")
		return c.Next()
	})
	group.Get("/", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"templates": service.List(c.Query("archived") == "true")})
	})
	group.Get("/:id/revisions", func(c *fiber.Ctx) error {
		value, err := service.Revisions(c.Params("id"))
		if err != nil {
			return templateError(err)
		}
		return c.JSON(fiber.Map{"revisions": value})
	})
	admin := func(c *fiber.Ctx) error {
		if c.Locals("role") != "admin" {
			return fiber.ErrForbidden
		}
		return c.Next()
	}
	group.Post("/", admin, func(c *fiber.Ctx) error {
		var input struct {
			Name string         `json:"name"`
			Spec templates.Spec `json:"spec"`
		}
		if err := strictBody(c, &input); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		value, err := service.Create(ctx, input.Name, input.Spec, auth.GetContextUser(c, am))
		if err != nil {
			return templateError(err)
		}
		return c.Status(201).JSON(value)
	})
	group.Post("/:id/revisions", admin, func(c *fiber.Ctx) error {
		var input struct {
			ExpectedRevision int            `json:"expectedRevision"`
			Name             string         `json:"name"`
			Spec             templates.Spec `json:"spec"`
		}
		if err := strictBody(c, &input); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		value, err := service.Revise(ctx, c.Params("id"), input.ExpectedRevision, input.Name, input.Spec, auth.GetContextUser(c, am))
		if err != nil {
			return templateError(err)
		}
		return c.Status(201).JSON(value)
	})
	group.Put("/:id/archive", admin, func(c *fiber.Ctx) error {
		var input struct {
			ExpectedRevision int  `json:"expectedRevision"`
			Archived         bool `json:"archived"`
		}
		if err := strictBody(c, &input); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		if err := service.Archive(ctx, c.Params("id"), input.ExpectedRevision, input.Archived); err != nil {
			return templateError(err)
		}
		return c.JSON(fiber.Map{"success": true})
	})
	group.Post("/:id/revisions/:revision/plans", admin, func(c *fiber.Ctx) error {
		if provision == nil {
			return fiber.NewError(503, "Provisioning unavailable")
		}
		revision, err := strconv.Atoi(c.Params("revision"))
		if err != nil || revision < 1 {
			return fiber.ErrBadRequest
		}
		current, err := service.Get(c.Params("id"))
		if err != nil {
			return templateError(err)
		}
		if current.Archived {
			return templateError(templates.ErrConflict)
		}
		pinned, err := service.GetRevision(current.ID, revision)
		if err != nil {
			return templateError(err)
		}
		var input templates.Input
		if err := strictBody(c, &input); err != nil {
			return err
		}
		spec, err := templates.InstantiateSpec(pinned, input)
		if err != nil {
			return templateError(err)
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 45*time.Second)
		defer cancel()
		plan, err := provision.PlanFromTemplate(ctx, spec, auth.GetContextUser(c, am), operations.TemplateReference{ID: pinned.TemplateID, Revision: pinned.Revision, SpecHash: pinned.SpecHash, Name: pinned.Name})
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		return c.JSON(plan)
	})
}
