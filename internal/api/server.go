package api

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/websocket/v2"
	"sigs.k8s.io/yaml"

	"talosdeck/internal/alertcenter"
	"talosdeck/internal/alerts"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/backup"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/operations"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/talos"
	"talosdeck/web"
)

// ServerConfig configures the HTTP & WebSocket server.
type ServerConfig struct {
	AutomationPaused bool
	RecoverySafeMode bool
	Health           HealthSnapshotProvider
	AlertCenter      *alertcenter.Center
	Certificates     CertificateInspector
	Fleet            *Fleet
	ClusterName      string
	Manager          *talos.TalosManager
	K8s              *k8s.K8sManager
	Backup           *backup.BackupManager
	AlertService     *alerts.TelegramService
	AlertWatcher     *alerts.Watcher
	Proxmox          *proxmox.Client
	Audit            *audit.AuditManager
	Auth             *auth.AuthManager
	DownloadTickets  *DownloadTickets
	Jobs             *jobs.Manager
	Operations       *operations.Service
	Port             string
}

// SetupServer initializes the Fiber app with routes and middlewares.
func SetupServer(cfg ServerConfig) *fiber.App {
	// API-08: Enforce server timeouts in fiber.Config to prevent Slowloris DoS attacks
	app := fiber.New(fiber.Config{
		Immutable:    true,
		AppName:      "TalosDeck v0.1.0",
		ServerHeader: "TalosDeck",
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(fiber.Map{
				"error": err.Error(),
			})
		},
	})
	// Keep normal responses bounded while allowing large authenticated backup
	// bodies to stream directly to disk in the browser.
	app.Server().HeaderReceived = backupRequestConfig

	app.Use(recover.New())
	app.Use(func(c *fiber.Ctx) error {
		c.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("Referrer-Policy", "no-referrer")
		return c.Next()
	})
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${latency} ${method} ${path}\n",
	}))
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS",
	}))

	manager := cfg.Manager

	// Audit Manager
	auditMgr := cfg.Audit
	ownsAuditMgr := false
	if auditMgr == nil {
		var err error
		auditMgr, err = audit.NewAuditManager("./data/audit.log", 1000)
		if err != nil {
			log.Printf("[Audit] Warning: Failed to initialize audit manager: %v", err)
		} else {
			ownsAuditMgr = true
		}
	}
	if ownsAuditMgr && auditMgr != nil {
		app.Hooks().OnShutdown(func() error {
			return auditMgr.Close()
		})
	}

	// Auth Manager
	authMgr := cfg.Auth
	if authMgr == nil {
		authMgr = auth.NewAuthManagerFromEnv()
	}
	if cfg.DownloadTickets == nil {
		if cfg.Fleet != nil {
			cfg.DownloadTickets = cfg.Fleet.downloadTickets
		} else {
			cfg.DownloadTickets = NewDownloadTickets(authMgr)
		}
	}
	safeMode := cfg.RecoverySafeMode || (cfg.Fleet != nil && cfg.Fleet.recoverySafeMode)
	app.Use(recoveryGuard(safeMode))
	app.Get("/api/recovery/status", func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		return c.JSON(fiber.Map{"safeMode": safeMode, "requiresReview": safeMode, "automaticResume": false, "automationPaused": cfg.AutomationPaused || (cfg.Fleet != nil && cfg.Fleet.automationPaused)})
	})
	app.Use(cfg.DownloadTickets.Authenticate)
	app.Use(auditMutationGuard(auditMgr, authMgr, cfg.Fleet != nil))
	RegisterSecurityRoutes(app, authMgr, auditMgr)
	if cfg.Fleet != nil {
		cfg.Fleet.register(app)
	}

	// REST API Routes Group
	api := app.Group("/api")
	api.Use(jobMutationGuard(cfg.Jobs))
	RegisterJobRoutes(api, cfg.Jobs, cfg.Operations, authMgr)
	RegisterCertificateRoutes(api, cfg.Certificates, authMgr)
	RegisterHealthScoreRoutes(api, cfg.Health, authMgr)
	if manager != nil {
		RegisterNodeImageRoutes(api, manager, authMgr)
	}
	if cfg.Operations != nil && cfg.Operations.Config != nil {
		RegisterConfigRoutes(api, cfg.Jobs, cfg.Operations.Config, authMgr)
	}
	if cfg.Operations != nil && cfg.Operations.Provision != nil {
		RegisterProvisionRoutes(api, cfg.Jobs, cfg.Operations.Provision, authMgr)
	}
	if cfg.K8s != nil && cfg.Operations != nil {
		RegisterInspectorRoutes(api, cfg.K8s, cfg.Operations.Diagnostics, cfg.Jobs, authMgr)
	}

	// OPS-08: Health and Readiness Probes (GET /healthz, GET /readyz, GET /api/health)
	healthzHandler := func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"status": "ok",
		})
	}
	app.Get("/healthz", healthzHandler)
	api.Get("/healthz", healthzHandler)

	readyzHandler := func(c *fiber.Ctx) error {
		if manager == nil || manager.GetClient() == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "not ready",
				"error":  "talos manager not initialized",
			})
		}

		// A node upgrade must not remove the management UI from Service endpoints.
		// With the job engine enabled, readiness measures the local control plane,
		// while managed-cluster health remains available through /api/cluster.
		if cfg.Jobs != nil {
			return c.JSON(fiber.Map{"status": "ok", "jobs": "available"})
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
		defer cancel()

		info, err := manager.GetClusterInfo(ctx)
		if err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "not ready",
				"error":  fmt.Sprintf("failed to reach cluster: %v", err),
			})
		}

		if !info.Healthy || info.ReadyNodes == 0 {
			status := "degraded"
			if info.ReadyNodes == 0 {
				status = "not ready"
			}
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status":     status,
				"healthy":    false,
				"readyNodes": info.ReadyNodes,
				"totalNodes": info.TotalNodes,
			})
		}

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"status":     "ok",
			"healthy":    true,
			"readyNodes": info.ReadyNodes,
			"totalNodes": info.TotalNodes,
		})
	}
	app.Get("/readyz", readyzHandler)
	api.Get("/readyz", readyzHandler)
	api.Get("/health", readyzHandler)

	// API-09: Rate limiting on login attempts to mitigate brute-force attacks
	loginLimiter := limiter.New(limiter.Config{
		Max:        30,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return auth.GetClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "Too many login attempts. Please try again later.",
			})
		},
	})

	// Auth Endpoints
	// POST /api/auth/login -> accepts {"password": "..."}, returns {"token": "...", "user": {"role": "admin"}}
	api.Post("/auth/login", loginLimiter, func(c *fiber.Ctx) error {
		var req struct {
			Password string `json:"password"`
		}
		if err := c.BodyParser(&req); err != nil || req.Password == "" {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "auth.login",
					User:    "admin",
					IP:      auth.GetClientIP(c),
					Status:  "failed",
					Details: map[string]any{"reason": "missing password"},
				})
			}
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Password is required",
			})
		}

		if !authMgr.VerifyPassword(req.Password) {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "auth.login",
					User:    "admin",
					IP:      auth.GetClientIP(c),
					Status:  "failed",
					Details: map[string]any{"reason": "invalid password"},
				})
			}
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Invalid password",
			})
		}

		token, err := authMgr.GenerateToken("admin", "admin")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to generate authentication token",
			})
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "auth.login",
				User:    "admin",
				IP:      auth.GetClientIP(c),
				Status:  "success",
				Details: map[string]any{"role": "admin"},
			})
		}

		return c.JSON(fiber.Map{
			"token": token,
			"user": fiber.Map{
				"username": "admin",
				"role":     "admin",
			},
			"expiresIn": 86400,
		})
	})

	// POST /api/auth/logout (SEC-08, SEC-15: authenticate before revoking)
	api.Post("/auth/logout", auth.RequireAuth(authMgr), func(c *fiber.Ctx) error {
		user := auth.GetContextUser(c, authMgr)
		authHeader := c.Get("Authorization")
		if authHeader != "" && authMgr != nil {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				if err := authMgr.RevokeToken(parts[1]); err != nil {
					return fiber.NewError(fiber.StatusUnauthorized, "invalid token")
				}
			}
		}
		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action: "auth.logout",
				User:   user,
				IP:     auth.GetClientIP(c),
				Status: "success",
			})
		}
		return c.JSON(fiber.Map{
			"success": true,
			"message": "Logged out successfully",
		})
	})

	// GET /api/auth/me -> checks token from Authorization header
	api.Get("/auth/me", func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				if claims, err := authMgr.ValidateToken(parts[1]); err == nil && claims != nil {
					return c.JSON(fiber.Map{
						"authenticated": true,
						"user": fiber.Map{
							"username": claims.Username,
							"role":     claims.Role,
						},
					})
				}
			}
		}

		return c.JSON(fiber.Map{
			"authenticated": false,
			"user": fiber.Map{
				"username": "guest",
				"role":     "viewer",
			},
		})
	})

	// GET /api/audit -> list of audit events (SEC-02: RequireAuth protected)
	auditHandlers := []fiber.Handler{}
	if authMgr != nil {
		auditHandlers = append(auditHandlers, auth.RequireAuth(authMgr))
	}
	auditHandlers = append(auditHandlers, func(c *fiber.Ctx) error {
		limit := c.QueryInt("limit", 50)
		action := c.Query("action", "")
		search := c.Query("search", "")

		var events []audit.AuditEvent
		if auditMgr != nil {
			events = auditMgr.GetEvents(limit, action, search)
		}
		if events == nil {
			events = []audit.AuditEvent{}
		}
		return c.JSON(events)
	})
	api.Get("/audit", auditHandlers...)

	bm := cfg.Backup
	if bm == nil && manager != nil {
		var err error
		bm, err = backup.NewBackupManager("./data/backups", manager)
		if err != nil {
			log.Printf("[Backup] Failed to initialize backup manager: %v", err)
		}
	}
	if bm != nil {
		if cfg.Operations != nil && cfg.Operations.BackupLifecycle != nil {
			RegisterBackupLifecycleRoutes(api, cfg.Operations.BackupLifecycle, cfg.Jobs, authMgr, auditMgr, cfg.DownloadTickets)
		}
		RegisterBackupRoutes(api, bm, authMgr, auditMgr)
	}

	alertSvc := cfg.AlertService
	if alertSvc == nil {
		alertSvc = alerts.NewTelegramServiceFromEnv()
	}

	watcher := cfg.AlertWatcher
	ownsWatcher := false
	if watcher == nil && manager != nil && cfg.AlertCenter == nil {
		watcher = alerts.NewWatcher(manager, alertSvc, 30*time.Second)
		watcher.Start(context.Background())
		ownsWatcher = true
	}
	if ownsWatcher {
		app.Hooks().OnShutdown(func() error {
			watcher.Stop()
			return nil
		})
	}

	if cfg.AlertCenter != nil {
		RegisterAlertCenterRoutes(api, cfg.AlertCenter, authMgr, auditMgr)
	} else {
		RegisterAlertRoutes(api, alertSvc, watcher, authMgr, auditMgr)
	}

	proxmoxClient := cfg.Proxmox
	if proxmoxClient == nil {
		proxmoxClient = proxmox.NewClientFromEnv()
	}
	if cfg.K8s != nil && proxmoxClient != nil {
		proxmoxClient.SetDrainer(cfg.K8s)
	}
	api.Use("/proxmox/worker", func(c *fiber.Ctx) error {
		if c.Method() != fiber.MethodGet && c.Method() != fiber.MethodHead && c.Method() != fiber.MethodOptions {
			return fiber.NewError(410, "Use provisioning plans and background jobs; legacy VM mutations are disabled")
		}
		return c.Next()
	})
	RegisterProxmoxRoutes(api, proxmoxClient, authMgr, auditMgr)

	// validateNodeIP checks that :ip parameter is a valid IPv4 or IPv6 address (API-11)
	validateNodeIP := func(c *fiber.Ctx) (string, error) {
		ip := strings.TrimSpace(c.Params("ip"))
		if ip == "" || net.ParseIP(ip) == nil {
			return "", fmt.Errorf("invalid node IP address: %q", ip)
		}
		return ip, nil
	}

	// GET /api/cluster -> cluster overview
	api.Get("/cluster", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 12*time.Second)
		defer cancel()

		info, err := manager.GetClusterInfo(ctx)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to get cluster info: %v", err),
			})
		}
		if cfg.ClusterName != "" {
			info.Name = cfg.ClusterName
		}
		if cfg.K8s != nil {
			version, nodes, kerr := cfg.K8s.UpgradeInventory(ctx)
			if kerr == nil {
				info.KubernetesVersion = version
				info.Healthy = info.Healthy && len(nodes) == info.TotalNodes
				for _, node := range nodes {
					info.Healthy = info.Healthy && node.Ready
				}
			} else {
				info.Healthy = false
			}
		}
		return c.JSON(info)
	})

	// GET /api/nodes -> list of nodes with status, version, role
	api.Get("/nodes", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 12*time.Second)
		defer cancel()

		nodes, err := manager.ListNodes(ctx)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to list nodes: %v", err),
			})
		}
		return c.JSON(nodes)
	})

	// GET /api/nodes/:ip -> single node overview (API-11: IP validation)
	api.Get("/nodes/:ip", func(c *fiber.Ctx) error {
		ip, err := validateNodeIP(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 8*time.Second)
		defer cancel()

		status, err := manager.GetNodeStatus(ctx, ip)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to get node status for %s: %v", ip, err),
			})
		}
		return c.JSON(status)
	})

	// GET /api/nodes/:ip/services -> list of services and health (API-11: IP validation)
	api.Get("/nodes/:ip/services", func(c *fiber.Ctx) error {
		ip, err := validateNodeIP(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 8*time.Second)
		defer cancel()

		services, err := manager.ListServices(ctx, ip)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to list services on %s: %v", ip, err),
			})
		}
		return c.JSON(services)
	})

	// POST /api/nodes/:ip/services/:id/restart -> restart service (API-01: RequireAuth, IP validation & audit log)
	restartHandlers := []fiber.Handler{}
	if authMgr != nil {
		restartHandlers = append(restartHandlers, auth.RequireAuth(authMgr))
	}
	restartHandlers = append(restartHandlers, func(c *fiber.Ctx) error {
		ip, err := validateNodeIP(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		id := strings.TrimSpace(c.Params("id"))
		if id == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "service id is required"})
		}
		user := auth.GetContextUser(c, authMgr)
		clientIP := auth.GetClientIP(c)

		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		if err := manager.RestartService(ctx, ip, id); err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "service.restart",
					User:    user,
					IP:      clientIP,
					Status:  "failed",
					Details: map[string]any{"node": ip, "service": id, "error": err.Error()},
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to restart service %s on %s: %v", id, ip, err),
			})
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "service.restart",
				User:    user,
				IP:      clientIP,
				Status:  "success",
				Details: map[string]any{"node": ip, "service": id},
			})
		}

		return c.JSON(fiber.Map{
			"status":  "restarting",
			"node":    ip,
			"service": id,
		})
	})
	api.Post("/nodes/:ip/services/:id/restart", restartHandlers...)

	// GET /api/nodes/:ip/containers -> list of containers (API-11: IP validation)
	api.Get("/nodes/:ip/containers", func(c *fiber.Ctx) error {
		ip, err := validateNodeIP(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		ns := c.Query("namespace", "system")
		ctx, cancel := context.WithTimeout(c.UserContext(), 8*time.Second)
		defer cancel()

		containers, err := manager.ListContainers(ctx, ip, ns)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to list containers on %s: %v", ip, err),
			})
		}
		return c.JSON(containers)
	})

	// POST /api/nodes/:ip/reboot -> reboot node (protected with auth & IP validation; API-12 removed duplicate route)
	rebootHandler := func(c *fiber.Ctx) error {
		ip, err := validateNodeIP(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		user := auth.GetContextUser(c, authMgr)
		clientIP := auth.GetClientIP(c)

		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		if err := manager.RebootNode(ctx, ip); err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "node.reboot",
					User:    user,
					IP:      clientIP,
					Status:  "failed",
					Details: map[string]any{"node": ip, "error": err.Error()},
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to reboot node %s: %v", ip, err),
			})
		}

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "node.reboot",
				User:    user,
				IP:      clientIP,
				Status:  "success",
				Details: map[string]any{"node": ip},
			})
		}

		return c.JSON(fiber.Map{
			"status":  "rebooting",
			"node":    ip,
			"message": "Reboot command successfully dispatched",
		})
	}

	rebootMiddlewares := []fiber.Handler{}
	if authMgr != nil {
		rebootMiddlewares = append(rebootMiddlewares, auth.RequireAuth(authMgr))
	}
	rebootHandlers := append(rebootMiddlewares, rebootHandler)

	api.Post("/nodes/:ip/reboot", rebootHandlers...)

	// GET /api/nodes/:ip/disks -> physical disks and partitions via Talos SDK (API-11: IP validation)
	api.Get("/nodes/:ip/disks", func(c *fiber.Ctx) error {
		ip, err := validateNodeIP(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		disks, err := manager.GetNodeDisks(ctx, ip)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to get disks for %s: %v", ip, err),
			})
		}
		return c.JSON(disks)
	})

	// GET /api/nodes/:ip/config -> Talos MachineConfig (API-02: RequireAuth, IP validation & audit log)
	configHandlers := []fiber.Handler{}
	if authMgr != nil {
		configHandlers = append(configHandlers, auth.RequireAuth(authMgr))
	}
	configHandlers = append(configHandlers, func(c *fiber.Ctx) error {
		ip, err := validateNodeIP(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		format := strings.ToLower(c.Query("format", ""))
		accept := c.Get("Accept")
		user := auth.GetContextUser(c, authMgr)
		clientIP := auth.GetClientIP(c)

		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		cfgBytes, err := manager.GetNodeConfig(ctx, ip)
		if err != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "node.config.export",
					User:    user,
					IP:      clientIP,
					Status:  "failed",
					Details: map[string]any{"node": ip, "format": format, "error": "configuration unavailable"},
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "failed to get machine configuration",
			})
		}
		redacted, err := operations.RedactedConfig(cfgBytes)
		if err != nil {
			return fiber.NewError(502, "cannot safely redact machine configuration")
		}
		cfgBytes = []byte(redacted)

		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "node.config.export",
				User:    user,
				IP:      clientIP,
				Status:  "success",
				Details: map[string]any{"node": ip, "format": format},
			})
		}

		if format == "yaml" || format == "raw" || strings.Contains(accept, "text/yaml") || strings.Contains(accept, "application/x-yaml") {
			c.Set("Content-Type", "text/yaml; charset=utf-8")
			return c.Send(cfgBytes)
		}

		var parsedConfig any
		if err := yaml.Unmarshal(cfgBytes, &parsedConfig); err != nil {
			parsedConfig = nil
		}

		return c.JSON(fiber.Map{
			"node":       ip,
			"nodeIP":     ip,
			"configYaml": string(cfgBytes),
			"yaml":       string(cfgBytes),
			"config":     parsedConfig,
		})
	})
	api.Get("/nodes/:ip/config", configHandlers...)

	// GET /api/cluster/etcd -> etcd cluster health, members, and alarms via Talos SDK
	api.Get("/cluster/etcd", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		status, err := manager.GetEtcdStatus(ctx)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to get etcd status: %v", err),
			})
		}
		return c.JSON(status)
	})

	// GET /api/k8s/pods -> kubernetes pods across namespaces
	api.Get("/k8s/pods", func(c *fiber.Ctx) error {
		if cfg.K8s == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "Kubernetes client is not available",
			})
		}

		namespace := c.Query("namespace", "")
		node := c.Query("node", "")

		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		pods, err := cfg.K8s.ListPods(ctx, namespace, node)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to list pods: %v", err),
			})
		}
		return c.JSON(pods)
	})

	// GET /api/k8s/namespaces -> active kubernetes namespaces
	api.Get("/k8s/namespaces", func(c *fiber.Ctx) error {
		if cfg.K8s == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "Kubernetes client is not available",
			})
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		namespaces, err := cfg.K8s.ListNamespaces(ctx)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to list namespaces: %v", err),
			})
		}
		return c.JSON(namespaces)
	})

	// POST /api/nodes/:ip/maintenance -> toggle maintenance mode (API-05: RequireAuth, IP & payload validation)
	maintHandlers := []fiber.Handler{}
	if authMgr != nil {
		maintHandlers = append(maintHandlers, auth.RequireAuth(authMgr))
	}
	maintHandlers = append(maintHandlers, func(c *fiber.Ctx) error {
		ip, err := validateNodeIP(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		var body struct {
			Enable bool `json:"enable"`
		}
		if len(c.Body()) > 0 {
			if err := c.BodyParser(&body); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": fmt.Sprintf("invalid request payload: %v", err),
				})
			}
		}
		if cfg.K8s == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "Kubernetes client is not available",
			})
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()
		nodeName, operationErr := cfg.K8s.SetNodeMaintenance(ctx, ip, body.Enable)
		if operationErr != nil {
			if auditMgr != nil {
				auditMgr.Log(audit.AuditEvent{
					Action:  "node.maintenance",
					User:    auth.GetContextUser(c, authMgr),
					IP:      auth.GetClientIP(c),
					Status:  "failed",
					Details: map[string]any{"node": ip, "enable": body.Enable, "error": operationErr.Error()},
				})
			}
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to update maintenance mode for %s: %v", ip, operationErr),
			})
		}

		msg := fmt.Sprintf("Node %s cordoned (maintenance active)", nodeName)
		if !body.Enable {
			msg = fmt.Sprintf("Node %s uncordoned (active)", nodeName)
		}
		if auditMgr != nil {
			auditMgr.Log(audit.AuditEvent{
				Action:  "node.maintenance",
				User:    auth.GetContextUser(c, authMgr),
				IP:      auth.GetClientIP(c),
				Status:  "success",
				Details: map[string]any{"node": nodeName, "nodeIP": ip, "enable": body.Enable},
			})
		}
		return c.JSON(fiber.Map{
			"success": true,
			"node":    nodeName,
			"message": msg,
		})
	})
	api.Post("/nodes/:ip/maintenance", maintHandlers...)

	// API-07: Catch-all 404 for unmatched /api routes
	api.All("/*", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": fmt.Sprintf("API route %s not found", c.Path()),
		})
	})

	// WebSocket Upgrade Middleware (API-06: WebSocket authentication & upgrade check)
	app.Use("/ws", func(c *fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}

		if authMgr != nil {
			token := ""
			authHeader := c.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
					token = strings.TrimSpace(parts[1])
				}
			}
			if token == "" {
				proto := c.Get("Sec-WebSocket-Protocol")
				if proto != "" {
					parts := strings.Split(proto, ",")
					for _, p := range parts {
						p = strings.TrimSpace(p)
						if claims, err := authMgr.ValidateToken(p); err == nil && claims != nil {
							token = p
							c.Set("Sec-WebSocket-Protocol", token)
							break
						}
					}
				}
			}

			if token == "" {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
					"error": "Authentication required for WebSocket connection",
				})
			}

			claims, err := authMgr.ValidateToken(token)
			if err != nil || claims == nil {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
					"error": "Invalid or expired token for WebSocket connection",
				})
			}
			if !auth.Can(claims.Role, fiber.MethodGet, c.Path()) {
				return fiber.ErrForbidden
			}
			c.Locals("authToken", token)
			c.Locals("authPath", c.Path())
		}

		return c.Next()
	})

	// GET /ws/nodes/:ip/dmesg -> WebSocket stream of kernel dmesg
	app.Get("/ws/nodes/:ip/dmesg", websocket.New(func(c *websocket.Conn) {
		ip := strings.TrimSpace(c.Params("ip"))
		if net.ParseIP(ip) == nil {
			_ = c.WriteMessage(websocket.TextMessage, []byte("Error: invalid node IP address"))
			_ = c.Close()
			return
		}
		log.Printf("[WebSocket] Dmesg client connected for node %s", ip)
		defer log.Printf("[WebSocket] Dmesg client disconnected for node %s", ip)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stopSessionWatch := watchWebSocketSession(ctx, c, authMgr, cancel)
		defer stopSessionWatch()

		logChan := make(chan string, 100)

		// Reader goroutine to detect client disconnect
		go func() {
			for {
				if _, _, err := c.ReadMessage(); err != nil {
					cancel()
					return
				}
			}
		}()

		// Talos streamer goroutine
		go func() {
			if err := manager.StreamDmesg(ctx, ip, logChan, true); err != nil {
				log.Printf("[WebSocket] StreamDmesg ended for %s: %v", ip, err)
			}
			close(logChan)
		}()

		// Writer loop to WebSocket
		for {
			select {
			case <-ctx.Done():
				return
			case line, ok := <-logChan:
				if !ok {
					return
				}
				if err := c.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
					cancel()
					return
				}
			}
		}
	}))

	// GET /ws/nodes/:ip/logs/:service -> WebSocket stream of service logs
	app.Get("/ws/nodes/:ip/logs/:service", websocket.New(func(c *websocket.Conn) {
		ip := strings.TrimSpace(c.Params("ip"))
		if net.ParseIP(ip) == nil {
			_ = c.WriteMessage(websocket.TextMessage, []byte("Error: invalid node IP address"))
			_ = c.Close()
			return
		}
		service := strings.TrimSpace(c.Params("service"))
		if service == "" {
			_ = c.WriteMessage(websocket.TextMessage, []byte("Error: service parameter is required"))
			_ = c.Close()
			return
		}
		log.Printf("[WebSocket] Service logs client connected for %s on node %s", service, ip)
		defer log.Printf("[WebSocket] Service logs client disconnected for %s on node %s", service, ip)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stopSessionWatch := watchWebSocketSession(ctx, c, authMgr, cancel)
		defer stopSessionWatch()

		logChan := make(chan string, 100)

		go func() {
			for {
				if _, _, err := c.ReadMessage(); err != nil {
					cancel()
					return
				}
			}
		}()

		go func() {
			if err := manager.StreamServiceLogs(ctx, ip, service, logChan, 100, true); err != nil {
				log.Printf("[WebSocket] StreamServiceLogs ended for %s/%s: %v", ip, service, err)
			}
			close(logChan)
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case line, ok := <-logChan:
				if !ok {
					return
				}
				if err := c.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
					cancel()
					return
				}
			}
		}
	}))

	// API-07: Catch-all 404 for unmatched /ws routes
	app.All("/ws/*", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": fmt.Sprintf("WebSocket route %s not found", c.Path()),
		})
	})

	// Embed Static Files from web/dist with fallback
	distSubFS, err := web.GetDistFS()
	hasStatic := false
	if err != nil {
		log.Printf("[Static] GetDistFS returned error: %v", err)
	} else {
		f, fErr := distSubFS.Open("index.html")
		if fErr != nil {
			log.Printf("[Static] distSubFS.Open(index.html) returned error: %v", fErr)
		} else {
			_ = f.Close()
			hasStatic = true
			log.Printf("[Static] Serving embedded frontend from web/dist")
			// API-07: Skip static file middleware for /api and /ws routes
			app.Use(filesystem.New(filesystem.Config{
				Root:   http.FS(distSubFS),
				Index:  "index.html",
				Browse: false,
				Next: func(c *fiber.Ctx) bool {
					path := c.Path()
					return strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/ws") || path == "/healthz" || path == "/readyz"
				},
			}))
			// SPA client-side fallback: serve index.html for non-API/non-WS paths
			app.Get("/*", func(c *fiber.Ctx) error {
				path := c.Path()
				if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/ws") {
					return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
						"error": "not found",
					})
				}
				idxContent, err := distSubFS.Open("index.html")
				if err != nil {
					return c.Status(fiber.StatusInternalServerError).SendString("index.html not found")
				}
				defer idxContent.Close()
				data, err := io.ReadAll(idxContent)
				if err != nil {
					return c.Status(fiber.StatusInternalServerError).SendString("failed to read index.html")
				}
				c.Set("Content-Type", "text/html; charset=utf-8")
				return c.Send(data)
			})
		}
	}

	if !hasStatic {
		log.Printf("[Static] Using fallback HTML handler")
		setupFallbackHandler(app)
	}

	return app
}

func setupFallbackHandler(app *fiber.App) {
	app.Get("/*", func(c *fiber.Ctx) error {
		path := c.Path()
		if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/ws") {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "not found",
			})
		}
		return c.Type("html").SendString(`<!DOCTYPE html>
<html>
<head><title>TalosDeck MVP</title></head>
<body style="font-family: sans-serif; background: #09090b; color: #f4f4f5; padding: 40px; text-align: center;">
  <h1>⚡ TalosDeck Control Plane</h1>
  <p>Backend API is active and connected to cluster.</p>
  <p>Frontend static files were not built into <code>web/dist</code>.</p>
  <p>Explore API: <a style="color: #38bdf8;" href="/api/cluster">/api/cluster</a> | <a style="color: #38bdf8;" href="/api/nodes">/api/nodes</a></p>
</body>
</html>`)
	})
}
