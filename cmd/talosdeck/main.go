package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"talosdeck/internal/alerts"
	"talosdeck/internal/api"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/backup"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/operations"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/talos"
)

func main() {
	defaultConfig := os.Getenv("TALOSCONFIG")
	if defaultConfig == "" {
		defaultConfig = "/home/artem/laba-kuber/cluster-config/talosconfig"
	}

	// Empty means auto-discover (KUBECONFIG, in-cluster, then standard paths).
	// A machine-specific default silently binds this build to one developer's box
	// — or, worse, to a stale file that points at the wrong cluster.
	defaultKubeconfig := ""

	defaultPort := os.Getenv("PORT")
	if defaultPort == "" {
		defaultPort = ":8080"
	} else if !strings.HasPrefix(defaultPort, ":") {
		defaultPort = ":" + defaultPort
	}

	configPath := flag.String("talosconfig", defaultConfig, "Path to talosconfig file")
	kubeconfigPath := flag.String("kubeconfig", defaultKubeconfig, "Path to kubeconfig file (empty: auto-discover)")
	backupDirFlag := flag.String("backups", "./data/backups", "Storage directory for cluster backups and snapshots")
	port := flag.String("port", defaultPort, "HTTP server port (e.g. :8080)")
	nodesFlag := flag.String("nodes", "", "Comma-separated list of additional node IPs")
	flag.Parse()

	log.Printf("══════════════════════════════════════════════")
	log.Printf("         ⚡ TalosDeck Control Plane ⚡        ")
	log.Printf("══════════════════════════════════════════════")
	log.Printf("Talosconfig: %s", *configPath)
	if *kubeconfigPath != "" {
		log.Printf("Kubeconfig:  %s", *kubeconfigPath)
	} else {
		log.Printf("Kubeconfig:  (auto-discover)")
	}
	log.Printf("Listen port: %s", *port)

	var extraNodes []string
	if *nodesFlag != "" {
		for _, n := range strings.Split(*nodesFlag, ",") {
			n = strings.TrimSpace(n)
			if n != "" {
				extraNodes = append(extraNodes, n)
			}
		}
	}

	manager, err := talos.NewTalosManager(*configPath, extraNodes...)
	if err != nil {
		log.Fatalf("Failed to initialize Talos manager: %v", err)
	}
	defer manager.Close()

	endpoints := manager.GetEndpoints()
	nodes := manager.GetConfiguredNodes()
	log.Printf("Connected context: %s", manager.GetConfig().Context)
	log.Printf("Talos API endpoints: %v", endpoints)
	log.Printf("Configured cluster nodes: %v", nodes)

	// Initialize Kubernetes workloads manager
	k8sMgr, err := k8s.NewK8sManager(*kubeconfigPath, manager.GetClient().Kubeconfig)
	if err != nil {
		log.Printf("[Warning] Kubernetes workloads API disabled: %v", err)
	} else {
		log.Printf("Connected to Kubernetes API for workloads & namespaces")
	}

	// Initialize Telegram alerting and background health watcher
	alertSvc := alerts.NewTelegramServiceFromEnv()
	if alertSvc.IsConfigured() {
		log.Printf("Telegram alerts: ENABLED (Chat ID: %s)", alertSvc.GetMaskedChatID())
	} else {
		log.Printf("Telegram alerts: disabled (set TELEGRAM_BOT_TOKEN & TELEGRAM_CHAT_ID to enable)")
	}

	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	defer cancelWatcher()

	alertWatcher := alerts.NewWatcher(manager, alertSvc, 30*time.Second)
	alertWatcher.Start(watcherCtx)

	// Initialize Cluster Backup & Disaster Recovery manager
	backupMgr, err := backup.NewBackupManager(*backupDirFlag, manager)
	if err != nil {
		log.Printf("[Warning] Failed to initialize backup manager: %v", err)
	} else {
		log.Printf("Cluster Backup & DR manager initialized at: %s", *backupDirFlag)
	}

	// Initialize Proxmox VE integration client
	proxmoxClient := proxmox.NewClientFromEnv()
	if proxmoxClient.IsConfigured() {
		log.Printf("Proxmox VE integration: ENABLED (%s, node=%s)", proxmoxClient.GetBaseURL(), proxmoxClient.GetNode())
	} else {
		log.Printf("Proxmox VE integration: DISABLED (set PROXMOX_API_TOKEN or PROXMOX_USERNAME/PASSWORD to enable)")
	}

	// Initialize Audit Manager & Log
	auditMgr, err := audit.NewAuditManager("./data/audit.log", 1000)
	if err != nil {
		log.Printf("[Warning] Failed to initialize audit manager: %v", err)
	} else {
		log.Printf("Audit Trail manager initialized at ./data/audit.log (%d events in history)", auditMgr.TotalCount())
		defer auditMgr.Close()
	}

	// Initialize Authentication & RBAC manager
	authMgr := auth.NewAuthManagerFromEnv()
	log.Printf("Authentication & RBAC active (configured admin role)")

	ops := &operations.Service{Talos: manager, Kubernetes: k8sMgr, Backups: backupMgr, CLI: operations.CLI{Path: os.Getenv("TALOSDECK_TALOSCTL")}}
	jobDir := os.Getenv("TALOSDECK_JOBS_DIR")
	if jobDir == "" {
		jobDir = "./data/jobs"
	}
	jobManager, err := jobs.Open(jobDir, ops.Run)
	if err != nil {
		log.Fatalf("Failed to open durable job journal: %v", err)
	}
	defer jobManager.Close()

	app := api.SetupServer(api.ServerConfig{
		Manager:      manager,
		Jobs:         jobManager,
		Operations:   ops,
		K8s:          k8sMgr,
		Backup:       backupMgr,
		AlertService: alertSvc,
		AlertWatcher: alertWatcher,
		Proxmox:      proxmoxClient,
		Audit:        auditMgr,
		Auth:         authMgr,
		Port:         *port,
	})

	// Graceful shutdown channel
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-stopChan
		log.Println("Shutting down TalosDeck gracefully...")
		cancelWatcher()
		alertWatcher.Stop()
		_ = app.Shutdown()
		_ = jobManager.Close()
		_ = manager.Close()
		if auditMgr != nil {
			_ = auditMgr.Close()
		}
	}()

	addr := *port
	if !strings.HasPrefix(addr, ":") {
		addr = ":" + addr
	}

	fmt.Printf("\n🚀 TalosDeck server listening at http://0.0.0.0%s\n\n", addr)
	if err := app.Listen(addr); err != nil {
		log.Printf("Server stopped: %v", err)
	}
}
