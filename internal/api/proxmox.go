package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/proxmox"
)

// RegisterProxmoxRoutes registers Proxmox VE endpoints under the provided router (e.g. /api).
func RegisterProxmoxRoutes(router fiber.Router, client *proxmox.Client, extra ...any) {
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
	group := router.Group("/proxmox")

	// GET /api/proxmox/status -> Returns Proxmox host resources and configuration status
	group.Get("/status", func(c *fiber.Ctx) error {
		if client == nil || !client.IsConfigured() {
			return c.JSON(fiber.Map{
				"configured": false,
				"message":    "Proxmox integration is not configured. Set PROXMOX_API_TOKEN or PROXMOX_USERNAME/PASSWORD.",
			})
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		status, err := client.GetNodeStatus(ctx)
		if err != nil {
			log.Printf("[Proxmox] Failed to get node status: %v", err)
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"configured": true,
				"error":      fmt.Sprintf("Failed to query Proxmox VE host: %v", err),
			})
		}

		return c.JSON(fiber.Map{
			"configured": true,
			"node":       status.Node,
			"status":     status,
		})
	})

	// GET /api/proxmox/next-vmid -> Gets next free VMID
	group.Get("/next-vmid", func(c *fiber.Ctx) error {
		if client == nil || !client.IsConfigured() {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "Proxmox integration is not configured",
			})
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 8*time.Second)
		defer cancel()

		vmid, err := client.GetNextVMID(ctx)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"error": fmt.Sprintf("Failed to get next VMID: %v", err),
			})
		}

		return c.JSON(fiber.Map{
			"vmid": vmid,
		})
	})

	// POST /api/proxmox/worker -> Triggers creation of a new Talos worker node
	workerHandlers := []fiber.Handler{}
	if authMgr != nil {
		workerHandlers = append(workerHandlers, auth.RequireAuth(authMgr))
	}
	workerHandlers = append(workerHandlers, func(c *fiber.Ctx) error {
		if client == nil || !client.IsConfigured() {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "Proxmox integration is not configured",
			})
		}

		var opts proxmox.CreateWorkerOpts
		if len(c.Body()) > 0 {
			if err := c.BodyParser(&opts); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": fmt.Sprintf("Invalid JSON request body: %v", err),
				})
			}
		}

		user := auth.GetContextUser(c, authMgr)
		ip := auth.GetClientIP(c)
		log.Printf("[Proxmox] Request to create Talos worker VM (requested vmid=%d, name=%s, user=%s)...", opts.VMID, opts.Name, user)

		ctx, cancel := context.WithTimeout(c.UserContext(), 120*time.Second)
		defer cancel()

		result, err := client.CreateTalosWorker(ctx, opts)
		if err != nil {
			log.Printf("[Proxmox] Failed to create Talos worker: %v", err)
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "worker.create",
					User:    user,
					IP:      ip,
					Status:  "failed",
					Details: map[string]any{"name": opts.Name, "vmid": opts.VMID, "error": err.Error()},
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("Failed to create worker VM: %v", err),
			})
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "worker.create",
				User:    user,
				IP:      ip,
				Status:  "success",
				Details: map[string]any{"name": result.Name, "vmid": result.VMID, "status": result.Status},
			})
		}

		log.Printf("[Proxmox] Worker VM %d (%s) created successfully, status: %s", result.VMID, result.Name, result.Status)
		return c.Status(fiber.StatusCreated).JSON(result)
	})
	group.Post("/worker", workerHandlers...)

	// DELETE /api/proxmox/worker/:vmid -> Stops and destroys a worker VM
	delWorkerHandlers := []fiber.Handler{}
	if authMgr != nil {
		delWorkerHandlers = append(delWorkerHandlers, auth.RequireAuth(authMgr))
	}
	delWorkerHandlers = append(delWorkerHandlers, func(c *fiber.Ctx) error {
		if client == nil || !client.IsConfigured() {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "Proxmox integration is not configured",
			})
		}

		vmidStr := c.Params("vmid")
		vmid, err := strconv.Atoi(vmidStr)
		if err != nil || vmid <= 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("Invalid VMID: %s", vmidStr),
			})
		}

		user := auth.GetContextUser(c, authMgr)
		ip := auth.GetClientIP(c)
		log.Printf("[Proxmox] Request to delete worker VM %d (user=%s)...", vmid, user)

		ctx, cancel := context.WithTimeout(c.UserContext(), 60*time.Second)
		defer cancel()

		if err := client.DeleteWorker(ctx, vmid); err != nil {
			log.Printf("[Proxmox] Failed to delete worker VM %d: %v", vmid, err)
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "worker.delete",
					User:    user,
					IP:      ip,
					Status:  "failed",
					Details: map[string]any{"vmid": vmid, "error": err.Error()},
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("Failed to delete VM %d: %v", vmid, err),
			})
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "worker.delete",
				User:    user,
				IP:      ip,
				Status:  "success",
				Details: map[string]any{"vmid": vmid},
			})
		}

		log.Printf("[Proxmox] Worker VM %d deleted successfully", vmid)
		return c.JSON(fiber.Map{
			"success": true,
			"vmid":    vmid,
			"message": fmt.Sprintf("Worker VM %d stopped and deleted successfully", vmid),
		})
	})
	group.Delete("/worker/:vmid", delWorkerHandlers...)
}
