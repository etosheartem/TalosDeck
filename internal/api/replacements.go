package api

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/jobs"
	"talosdeck/internal/operations"
)

type replacementPlanner interface {
	PlanReplacement(context.Context, operations.ReplacementSpec, string) (*operations.WorkerReplacementPlan, error)
	ListReplacements(context.Context) ([]operations.WorkerReplacementPlan, error)
	ApproveReplacement(context.Context, string, string, string, string, bool) (jobs.Request, error)
	PlanReplacementResume(context.Context, string, string) (*operations.WorkerReplacementPlan, error)
}

// RegisterReplacementRoutes is intentionally cluster-only. Fleet provisioning
// cannot supply the identity, Kubernetes API or storage impact of a worker.
func RegisterReplacementRoutes(router fiber.Router, manager *jobs.Manager, service *operations.ProvisionService, authManager *auth.AuthManager) {
	var planner replacementPlanner
	scope := ""
	if service != nil {
		planner = service
		scope = service.ClusterID
	}
	registerReplacementRoutes(router, manager, planner, scope, authManager)
}
func registerReplacementRoutes(router fiber.Router, manager *jobs.Manager, service replacementPlanner, clusterID string, authManager *auth.AuthManager) {
	handlers := provisionAdmin(authManager)
	handlers = append(handlers, func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		if clusterID == "" || clusterID == operations.FleetScope {
			return fiber.NewError(409, "Worker replacement requires an explicit cluster scope")
		}
		if service == nil || manager == nil {
			return fiber.NewError(503, "Worker replacement unavailable")
		}
		return c.Next()
	})
	group := router.Group("/replacements", handlers...)
	group.Get("/", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		plans, e := service.ListReplacements(ctx)
		if e != nil {
			return fiber.NewError(503, "Replacement plan storage unavailable")
		}
		if plans == nil {
			plans = []operations.WorkerReplacementPlan{}
		}
		return c.JSON(plans)
	})
	group.Post("/plan", func(c *fiber.Ctx) error {
		var spec operations.ReplacementSpec
		if e := replacementBody(c, &spec); e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 90*time.Second)
		defer cancel()
		plan, e := service.PlanReplacement(ctx, spec, auth.GetContextUser(c, authManager))
		if e != nil {
			return fiber.NewError(422, e.Error())
		}
		return c.JSON(plan)
	})
	group.Post("/", func(c *fiber.Ctx) error {
		var input struct {
			PlanID                   string `json:"planId"`
			ConfirmedName            string `json:"confirmedName"`
			ImpactHash               string `json:"impactHash"`
			AcknowledgeStorageImpact bool   `json:"acknowledgeStorageImpact"`
		}
		if e := replacementBody(c, &input); e != nil {
			return e
		}
		if strings.TrimSpace(input.PlanID) == "" || strings.TrimSpace(input.ConfirmedName) == "" || strings.TrimSpace(input.ImpactHash) == "" {
			return fiber.NewError(400, "Plan identity, confirmed node name and reviewed storage impact hash are required")
		}
		if !manager.HasExecutionAuthority() {
			return fiber.NewError(503, "Worker replacement requires an independent execution authority; configure it before approval")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 90*time.Second)
		defer cancel()
		request, e := service.ApproveReplacement(ctx, input.PlanID, input.ConfirmedName, input.ImpactHash, auth.GetContextUser(c, authManager), input.AcknowledgeStorageImpact)
		if e != nil {
			return fiber.NewError(409, e.Error())
		}
		if request.Kind != "worker-replace" {
			return fiber.NewError(503, "Invalid replacement operation")
		}
		job, e := manager.Submit(request, auth.GetContextUser(c, authManager))
		if e != nil {
			return jobError(e)
		}
		return c.Status(fiber.StatusAccepted).JSON(job)
	})
	group.Post("/:id/resume-plan", func(c *fiber.Ctx) error {
		var input struct{}
		if len(c.Body()) > 0 {
			if e := replacementBody(c, &input); e != nil {
				return e
			}
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 90*time.Second)
		defer cancel()
		plan, e := service.PlanReplacementResume(ctx, strings.Clone(c.Params("id")), auth.GetContextUser(c, authManager))
		if e != nil {
			return fiber.NewError(409, e.Error())
		}
		return c.JSON(plan)
	})
}

func replacementBody(c *fiber.Ctx, value any) error {
	if !strings.HasPrefix(strings.TrimSpace(string(c.Body())), "{") {
		return fiber.NewError(400, "Replacement request must be a JSON object")
	}
	return strictBody(c, value)
}
