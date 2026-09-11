package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"

	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/backup"
)

// RegisterBackupRoutes registers the cluster backup endpoints on the provided Fiber router.
func RegisterBackupRoutes(router fiber.Router, bm *backup.BackupManager, extra ...any) {
	var authMgr *auth.AuthManager
	var auditMgr *audit.AuditManager
	for _, a := range extra {
		switch v := a.(type) {
		case *auth.AuthManager:
			authMgr = v
		case *audit.AuditManager:
			auditMgr = v
		}
	}
	_ = authMgr
	_ = auditMgr
	// GET /api/backups -> returns list of backups
	router.Get("/backups", func(c *fiber.Ctx) error {
		backups, err := bm.ListBackups()
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to list backups: %v", err),
			})
		}
		if backups == nil {
			backups = []*backup.BackupInfo{}
		}
		return c.JSON(backups)
	})

	// Handlers with optional auth middleware
	createHandlers := []fiber.Handler{}
	if authMgr != nil {
		createHandlers = append(createHandlers, auth.RequireAuth(authMgr))
	}
	createHandlers = append(createHandlers, func(c *fiber.Ctx) error {
		var req struct {
			Type string `json:"type"` // "etcd" or "full"
			Node string `json:"node"` // optional node IP for etcd
		}

		_ = c.BodyParser(&req)
		if req.Type == "" {
			req.Type = "full"
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 3*time.Minute)
		defer cancel()

		var info *backup.BackupInfo
		var err error

		switch req.Type {
		case "etcd", "snapshot":
			info, err = bm.CreateEtcdSnapshot(ctx, req.Node)
		case "full", "archive", "cluster":
			info, err = bm.CreateFullClusterBackup(ctx)
		default:
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("unsupported backup type '%s'; valid values are 'etcd' or 'full'", req.Type),
			})
		}

		user := auth.GetContextUser(c, authMgr)
		ip := auth.GetClientIP(c)

		if err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "backup.create",
					User:    user,
					IP:      ip,
					Status:  "failed",
					Details: map[string]any{"type": req.Type, "node": req.Node, "error": err.Error()},
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("backup creation failed: %v", err),
			})
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "backup.create",
				User:    user,
				IP:      ip,
				Status:  "success",
				Details: map[string]any{"id": info.ID, "filename": info.Filename, "size": info.HumanSize, "type": info.Type},
			})
		}

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"status":  "success",
			"message": "Backup created successfully",
			"backup":  info,
		})
	})
	router.Post("/backups/create", createHandlers...)

	// GET /api/backups/:id/download -> downloads the backup archive file
	router.Get("/backups/:id/download", func(c *fiber.Ctx) error {
		id := c.Params("id")
		info, filePath, err := bm.GetBackup(id)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": fmt.Sprintf("backup '%s' not found: %v", id, err),
			})
		}

		return c.Download(filePath, info.Filename)
	})

	// DELETE /api/backups/:id -> removes backup
	deleteHandlers := []fiber.Handler{}
	if authMgr != nil {
		deleteHandlers = append(deleteHandlers, auth.RequireAuth(authMgr))
	}
	deleteHandlers = append(deleteHandlers, func(c *fiber.Ctx) error {
		id := c.Params("id")
		user := auth.GetContextUser(c, authMgr)
		ip := auth.GetClientIP(c)

		if err := bm.DeleteBackup(id); err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "backup.delete",
					User:    user,
					IP:      ip,
					Status:  "failed",
					Details: map[string]any{"id": id, "error": err.Error()},
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to delete backup '%s': %v", id, err),
			})
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "backup.delete",
				User:    user,
				IP:      ip,
				Status:  "success",
				Details: map[string]any{"id": id},
			})
		}

		return c.JSON(fiber.Map{
			"status":  "deleted",
			"id":      id,
			"message": "Backup removed successfully",
		})
	})
	router.Delete("/backups/:id", deleteHandlers...)
}
