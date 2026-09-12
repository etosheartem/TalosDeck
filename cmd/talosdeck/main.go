package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"talosdeck/internal/alerts"
	"talosdeck/internal/api"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
	"talosdeck/internal/proxmox"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	home, _ := os.UserHomeDir()
	configPath := flag.String("talosconfig", env("TALOSCONFIG", filepath.Join(home, ".talos", "config")), "Legacy talosconfig to import on first startup (optional)")
	kubeconfigPath := flag.String("kubeconfig", "", "Legacy kubeconfig (empty: KUBECONFIG or Talos-issued credentials)")
	dataDir := flag.String("data", env("TALOSDECK_DATA_DIR", "./data"), "Persistent data directory")
	keyPath := flag.String("encryption-key", os.Getenv("TALOSDECK_ENCRYPTION_KEY_FILE"), "Master key file outside SQLite (default: DATA/master.key)")
	backupDir := flag.String("backups", "", "Legacy backups directory (default: DATA/backups)")
	port := flag.String("port", env("PORT", ":8080"), "HTTP port")
	nodesFlag := flag.String("nodes", "", "Additional node addresses for legacy migration")
	dbBackup := flag.String("backup-database", "", "Write a consistent encrypted database snapshot and exit")
	rotateKey := flag.String("rotate-encryption-key", "", "Rotate credentials into a new master key file and exit (stop the server first)")
	flag.Parse()
	if *keyPath == "" {
		*keyPath = filepath.Join(*dataDir, "master.key")
	}
	store, err := clusters.Open(filepath.Join(*dataDir, "talosdeck.db"), *keyPath)
	if err != nil {
		log.Fatalf("Cannot open encrypted cluster registry: %v", err)
	}
	defer store.Close()
	if *dbBackup != "" {
		if err := store.Backup(context.Background(), *dbBackup); err != nil {
			log.Fatal(err)
		}
		log.Print("Encrypted database backup created; preserve the master key separately")
		return
	}
	if *rotateKey != "" {
		if err := store.RotateKey(context.Background(), *rotateKey); err != nil {
			log.Fatal(err)
		}
		log.Print("Encryption key rotated; preserve the updated keyring separately")
		return
	}
	var extraNodes []string
	for _, node := range strings.Split(*nodesFlag, ",") {
		if node = strings.TrimSpace(node); node != "" {
			extraNodes = append(extraNodes, node)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = api.MigrateLegacy(ctx, store, *configPath, *kubeconfigPath, extraNodes)
	cancel()
	if err != nil {
		log.Fatalf("Single-cluster migration could not finish: %v", err)
	}
	authMgr, err := auth.NewPersistentAuthManager(store, os.Getenv("TALOSDECK_ADMIN_PASSWORD"), os.Getenv("TALOSDECK_JWT_SECRET"))
	if err != nil {
		log.Fatalf("Cannot initialize persistent authentication: %v", err)
	}
	oidcCtx, oidcCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := authMgr.ConfigureOIDCFromEnv(oidcCtx); err != nil {
		log.Printf("OIDC unavailable: %v; local administrator authentication remains available", err)
	}
	oidcCancel()
	globalAudit, err := audit.NewAuditManager(filepath.Join(*dataDir, "global-audit.log"), 1000)
	if err != nil {
		log.Fatalf("Cannot open global audit: %v", err)
	}
	defer globalAudit.Close()
	fleet, err := api.OpenFleet(api.FleetOptions{Store: store, Auth: authMgr, Audit: globalAudit, DataDir: *dataDir, LegacyBackupDir: *backupDir, LegacyJobsDir: os.Getenv("TALOSDECK_JOBS_DIR")})
	if err != nil {
		log.Fatalf("Cannot open fleet: %v", err)
	}
	defer fleet.Close()
	app := api.SetupServer(api.ServerConfig{Fleet: fleet, Auth: authMgr, Audit: globalAudit, AlertService: alerts.NewTelegramService("", "", false), Proxmox: &proxmox.Client{}, Port: *port})
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Print("Stopping TalosDeck")
		_ = app.ShutdownWithTimeout(10 * time.Second)
		fleet.Close()
	}()
	addr := *port
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	log.Printf("TalosDeck listening on %s", addr)
	if err := app.Listen(addr); err != nil {
		log.Printf("Server stopped: %v", err)
	}
}
