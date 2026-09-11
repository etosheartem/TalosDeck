package api

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/backup"
)

// RegisterBackupRoutes registers the cluster backup endpoints on the provided Fiber router.
// BKP-02 / BKP-14: Secures all mutating and sensitive download/listing endpoints with RequireAuth.
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

	// GET /api/backups -> returns list of backups
	listHandlers := []fiber.Handler{}
	if authMgr != nil {
		listHandlers = append(listHandlers, auth.RequireAuth(authMgr))
	}
	listHandlers = append(listHandlers, func(c *fiber.Ctx) error {
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
	router.Get("/backups", listHandlers...)

	// POST /api/backups/create -> creates a new backup
	createHandlers := []fiber.Handler{}
	if authMgr != nil {
		createHandlers = append(createHandlers, auth.RequireAuth(authMgr))
	}
	createHandlers = append(createHandlers, func(c *fiber.Ctx) error {
		var req struct {
			Type string `json:"type"` // "etcd" or "full"
			Node string `json:"node"` // optional node IP for etcd
		}

		if len(c.Body()) > 0 {
			if err := c.BodyParser(&req); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": fmt.Sprintf("invalid request payload: %v", err),
				})
			}
		}
		if req.Type == "" {
			req.Type = "full"
		}

		// BKP-03: Validate IP format if provided
		if req.Node != "" && net.ParseIP(strings.TrimSpace(req.Node)) == nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("invalid control plane node IP address: %q", req.Node),
			})
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

			statusCode := fiber.StatusInternalServerError
			if strings.Contains(err.Error(), "already in progress") {
				statusCode = fiber.StatusConflict
			} else if strings.Contains(err.Error(), "insufficient disk space") {
				statusCode = fiber.StatusInsufficientStorage
			} else if strings.Contains(err.Error(), "invalid control plane IP") {
				statusCode = fiber.StatusBadRequest
			}

			return c.Status(statusCode).JSON(fiber.Map{
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

	// GET /api/backups/:id/download -> downloads the backup archive file (BKP-02 / BKP-14: Protected by RequireAuth)
	downloadHandlers := []fiber.Handler{}
	if authMgr != nil {
		downloadHandlers = append(downloadHandlers, auth.RequireAuth(authMgr))
	}
	downloadHandlers = append(downloadHandlers, func(c *fiber.Ctx) error {
		id := c.Params("id")
		user := auth.GetContextUser(c, authMgr)
		ip := auth.GetClientIP(c)
		info, filePath, err := bm.GetBackup(id)
		if err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "backup.download",
					User:    user,
					IP:      ip,
					Status:  "failed",
					Details: map[string]any{"id": id, "error": err.Error()},
				})
			}

			statusCode := fiber.StatusNotFound
			if strings.Contains(err.Error(), "path traversal") || strings.Contains(err.Error(), "invalid backup ID") {
				statusCode = fiber.StatusBadRequest
			}
			return c.Status(statusCode).JSON(fiber.Map{
				"error": fmt.Sprintf("backup '%s' not found: %v", id, err),
			})
		}

		if err := c.Download(filePath, info.Filename); err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "backup.download",
					User:    user,
					IP:      ip,
					Status:  "failed",
					Details: map[string]any{"id": id, "filename": info.Filename, "error": err.Error()},
				})
			}
			return err
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action: "backup.download",
				User:   user,
				IP:     ip,
				Status: "success",
				Details: map[string]any{
					"id": id, "filename": info.Filename, "size": info.HumanSize, "type": info.Type,
				},
			})
		}

		return nil
	})
	router.Get("/backups/:id/download", downloadHandlers...)

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

			statusCode := fiber.StatusInternalServerError
			if strings.Contains(err.Error(), "path traversal") || strings.Contains(err.Error(), "invalid backup ID") {
				statusCode = fiber.StatusBadRequest
			} else if strings.Contains(err.Error(), "not found") {
				statusCode = fiber.StatusNotFound
			}

			return c.Status(statusCode).JSON(fiber.Map{
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
