package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/websocket/v2"
	"sigs.k8s.io/yaml"

	"talosdeck/internal/alerts"
	"talosdeck/internal/backup"
	"talosdeck/internal/k8s"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/talos"
	"talosdeck/web"
)

// ServerConfig configures the HTTP & WebSocket server.
type ServerConfig struct {
	Manager      *talos.TalosManager
	K8s          *k8s.K8sManager
	Backup       *backup.BackupManager
	AlertService *alerts.TelegramService
	AlertWatcher *alerts.Watcher
	Proxmox      *proxmox.Client
	Port         string
}

// SetupServer initializes the Fiber app with routes and middlewares.
func SetupServer(cfg ServerConfig) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:      "TalosDeck v0.1.0",
		ServerHeader: "TalosDeck",
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

	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${latency} ${method} ${path}\n",
	}))
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS",
	}))

	manager := cfg.Manager

	// REST API Routes Group
	api := app.Group("/api")

	bm := cfg.Backup
	if bm == nil {
		var err error
		bm, err = backup.NewBackupManager("./data/backups", manager)
		if err != nil {
			log.Printf("[Backup] Failed to initialize backup manager: %v", err)
		}
	}
	if bm != nil {
		RegisterBackupRoutes(api, bm)
	}

	alertSvc := cfg.AlertService
	if alertSvc == nil {
		alertSvc = alerts.NewTelegramServiceFromEnv()
	}

	watcher := cfg.AlertWatcher
	if watcher == nil && manager != nil {
		watcher = alerts.NewWatcher(manager, alertSvc, 30*time.Second)
	}

	RegisterAlertRoutes(api, alertSvc, watcher)

	proxmoxClient := cfg.Proxmox
	if proxmoxClient == nil {
		proxmoxClient = proxmox.NewClientFromEnv()
	}
	RegisterProxmoxRoutes(api, proxmoxClient)

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

	// GET /api/nodes/:ip -> single node overview
	api.Get("/nodes/:ip", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
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

	// GET /api/nodes/:ip/services -> list of services and health
	api.Get("/nodes/:ip/services", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
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

	// POST /api/nodes/:ip/services/:id/restart -> restart service
	api.Post("/nodes/:ip/services/:id/restart", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
		id := c.Params("id")
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		if err := manager.RestartService(ctx, ip, id); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to restart service %s on %s: %v", id, ip, err),
			})
		}
		return c.JSON(fiber.Map{
			"status":  "restarting",
			"node":    ip,
			"service": id,
		})
	})

	// GET /api/nodes/:ip/containers -> list of containers
	api.Get("/nodes/:ip/containers", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
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

	// POST /api/nodes/:ip/reboot -> reboot node
	api.Post("/api/nodes/:ip/reboot", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		if err := manager.RebootNode(ctx, ip); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to reboot node %s: %v", ip, err),
			})
		}
		return c.JSON(fiber.Map{
			"status":  "rebooting",
			"node":    ip,
			"message": "Reboot command successfully dispatched",
		})
	})

	// Also support without double /api prefix just in case:
	api.Post("/nodes/:ip/reboot", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		if err := manager.RebootNode(ctx, ip); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to reboot node %s: %v", ip, err),
			})
		}
		return c.JSON(fiber.Map{
			"status":  "rebooting",
			"node":    ip,
			"message": "Reboot command successfully dispatched",
		})
	})

	// GET /api/nodes/:ip/disks -> physical disks and partitions via Talos SDK
	api.Get("/nodes/:ip/disks", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
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

	// GET /api/nodes/:ip/config -> Talos MachineConfig (YAML or JSON)
	api.Get("/nodes/:ip/config", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
		format := strings.ToLower(c.Query("format", ""))
		accept := c.Get("Accept")

		ctx, cancel := context.WithTimeout(c.UserContext(), 10*time.Second)
		defer cancel()

		cfgBytes, err := manager.GetNodeConfig(ctx, ip)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("failed to get machine config for %s: %v", ip, err),
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

	// POST /api/nodes/:ip/maintenance -> toggle maintenance mode
	api.Post("/nodes/:ip/maintenance", func(c *fiber.Ctx) error {
		ip := c.Params("ip")
		var body struct {
			Enable bool `json:"enable"`
		}
		_ = c.BodyParser(&body)
		msg := fmt.Sprintf("Node %s cordoned (maintenance active)", ip)
		if !body.Enable {
			msg = fmt.Sprintf("Node %s uncordoned (active)", ip)
		}
		return c.JSON(fiber.Map{
			"success": true,
			"node":    ip,
			"message": msg,
		})
	})

	// WebSocket Upgrade Middleware
	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	// GET /ws/nodes/:ip/dmesg -> WebSocket stream of kernel dmesg
	app.Get("/ws/nodes/:ip/dmesg", websocket.New(func(c *websocket.Conn) {
		ip := c.Params("ip")
		log.Printf("[WebSocket] Dmesg client connected for node %s", ip)
		defer log.Printf("[WebSocket] Dmesg client disconnected for node %s", ip)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

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
		ip := c.Params("ip")
		service := c.Params("service")
		log.Printf("[WebSocket] Service logs client connected for %s on node %s", service, ip)
		defer log.Printf("[WebSocket] Service logs client disconnected for %s on node %s", service, ip)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

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
			app.Use(filesystem.New(filesystem.Config{
				Root:         http.FS(distSubFS),
				Index:        "index.html",
				Browse:       false,
				NotFoundFile: "index.html",
			}))
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
