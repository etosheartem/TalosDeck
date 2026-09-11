package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"

	"talosdeck/internal/backup"
)

// RegisterBackupRoutes registers the cluster backup endpoints on the provided Fiber router.
func RegisterBackupRoutes(router fiber.Router, bm *backup.BackupManager) {
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

	// POST /api/backups/create -> triggers backup creation
	router.Post("/backups/create", func(c *fiber.Ctx) error {
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

		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("backup creation failed: %v", err),
			})
		}

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"status":  "success",
			"message": "Backup created successfully",
			"backup":  info,
		})
	})

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
	router.Delete("/backups/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")
		if err := bm.DeleteBackup(id); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to delete backup '%s': %v", id, err),
			})
		}

		return c.JSON(fiber.Map{
			"status":  "deleted",
			"id":      id,
			"message": "Backup removed successfully",
		})
	})
}
