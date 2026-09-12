package api

import (
	"context"
	"errors"
	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/jobs"
	"talosdeck/internal/operations"
	"time"
)

func RegisterBackupLifecycleRoutes(router fiber.Router, s *operations.BackupService, jm *jobs.Manager, am *auth.AuthManager, auditLog *audit.AuditManager, tickets ...*DownloadTickets) {
	group := router.Group("/backups", auth.RequireAuth(am))
	record := func(c *fiber.Ctx, action string, details map[string]any) {
		if auditLog != nil {
			auditLog.Log(audit.AuditEvent{Action: action, User: auth.GetContextUser(c, am), IP: auth.GetClientIP(c), Status: "success", Details: details})
		}
	}
	group.Get("/", func(c *fiber.Ctx) error {
		items, err := s.List(c.UserContext())
		if err != nil {
			return fiber.NewError(503, "Cannot read backup catalog")
		}
		return c.JSON(items)
	})
	group.Get("/targets", func(c *fiber.Ctx) error {
		items, err := s.Targets(c.UserContext())
		if err != nil {
			return fiber.NewError(503, "Cannot read backup targets")
		}
		return c.JSON(fiber.Map{"targets": items})
	})
	group.Put("/targets", func(c *fiber.Ctx) error {
		var t operations.BackupTarget
		if err := c.BodyParser(&t); err != nil {
			return fiber.NewError(400, "Invalid target")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		saved, err := s.SaveTarget(ctx, t, jm)
		if errors.Is(err, jobs.ErrBusy) {
			return jobError(err)
		}
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		record(c, "backup.target.update", map[string]any{"targetId": saved.ID})
		return c.JSON(saved)
	})
	group.Get("/schedule", func(c *fiber.Ctx) error {
		q, err := s.Schedule(c.UserContext())
		if err != nil {
			return fiber.NewError(503, "Cannot read backup schedule")
		}
		return c.JSON(q)
	})
	group.Put("/schedule", func(c *fiber.Ctx) error {
		var q operations.BackupSchedule
		if err := c.BodyParser(&q); err != nil {
			return fiber.NewError(400, "Invalid schedule")
		}
		if err := s.SaveSchedule(c.UserContext(), q); err != nil {
			return fiber.NewError(422, err.Error())
		}
		record(c, "backup.schedule.update", map[string]any{"enabled": q.Enabled, "targetId": q.TargetID})
		saved, _ := s.Schedule(c.UserContext())
		return c.JSON(saved)
	})
	group.Post("/create", func(c *fiber.Ctx) error {
		var body struct {
			Type     string `json:"type"`
			Node     string `json:"node"`
			TargetID string `json:"targetId"`
		}
		if len(c.Body()) > 0 {
			if err := c.BodyParser(&body); err != nil {
				return fiber.NewError(400, "Invalid backup request")
			}
		}
		if body.Type == "" {
			body.Type = "full"
		}
		if body.Type != "etcd" && body.Type != "full" {
			return fiber.NewError(400, "Backup type must be etcd or full")
		}
		job, err := jm.Submit(jobs.Request{Kind: "backup-create", BackupType: body.Type, Node: body.Node, TargetID: body.TargetID}, auth.GetContextUser(c, am))
		if err != nil {
			return jobError(err)
		}
		record(c, "backup.create", map[string]any{"jobId": job.ID})
		return c.Status(202).JSON(job)
	})
	group.Post("/restore-plan", func(c *fiber.Ctx) error {
		var body struct {
			BackupID string `json:"backupId"`
		}
		if err := c.BodyParser(&body); err != nil || body.BackupID == "" {
			return fiber.NewError(400, "Backup ID required")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 3*time.Minute)
		defer cancel()
		plan, err := s.PlanRestore(ctx, body.BackupID, auth.GetContextUser(c, am))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		return c.JSON(plan)
	})
	group.Post("/restore", func(c *fiber.Ctx) error {
		var body struct {
			PlanID           string `json:"planId"`
			ConfirmedCluster string `json:"confirmedCluster"`
		}
		if err := c.BodyParser(&body); err != nil {
			return fiber.NewError(400, "Invalid restore request")
		}
		if body.ConfirmedCluster == "" || body.ConfirmedCluster != s.Operations.ConfirmationName() {
			return fiber.NewError(400, "Type the cluster name to confirm restore")
		}
		user := auth.GetContextUser(c, am)
		if err := s.CheckRestorePlan(c.UserContext(), body.PlanID, user); err != nil {
			return fiber.NewError(409, err.Error())
		}
		job, err := jm.Submit(jobs.Request{Kind: "backup-restore", RestorePlanID: body.PlanID}, user)
		if err != nil {
			return jobError(err)
		}
		record(c, "backup.restore", map[string]any{"jobId": job.ID, "planId": body.PlanID})
		return c.Status(202).JSON(job)
	})
	if len(tickets) > 0 && tickets[0] != nil {
		group.Post("/:id/download-ticket", func(c *fiber.Ctx) error {
			items, err := s.List(c.UserContext())
			if err != nil {
				return fiber.NewError(503, "Cannot read backup catalog")
			}
			for _, info := range items {
				if info.ID == c.Params("id") {
					return tickets[0].Issue(c, s.ClusterID, info.ID)
				}
			}
			return fiber.ErrNotFound
		})
	}
	group.Get("/:id/download", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Minute)
		defer cancel()
		info, file, cleanup, err := s.Materialize(ctx, c.Params("id"))
		if err != nil {
			return fiber.NewError(422, err.Error())
		}
		defer cleanup()
		record(c, "backup.download", map[string]any{"backupId": info.ID})
		return c.Download(file, info.Filename)
	})
	group.Delete("/:id", func(c *fiber.Ctx) error {
		release, err := jm.ReserveManual()
		if err != nil {
			return jobError(err)
		}
		defer release()
		if err = s.Delete(c.UserContext(), c.Params("id")); err != nil {
			return fiber.NewError(422, err.Error())
		}
		record(c, "backup.delete", map[string]any{"backupId": c.Params("id")})
		return c.JSON(fiber.Map{"status": "deleted"})
	})
}
