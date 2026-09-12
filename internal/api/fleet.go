package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"

	"talosdeck/internal/alerts"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/backup"
	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/operations"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/talos"
)

type FleetOptions struct {
	Audit                                   *audit.AuditManager
	Store                                   *clusters.Store
	Auth                                    *auth.AuthManager
	DataDir, LegacyBackupDir, LegacyJobsDir string
}

type clusterRuntime struct {
	config         ServerConfig
	handler        fasthttp.RequestHandler
	credentialsDir string
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

// Fleet owns a complete set of clients, caches and journals per cluster. Runtime
// handlers are published atomically; Fiber's live route tables are never mutated.
type Fleet struct {
	options          FleetOptions
	mu               sync.RWMutex
	importMu         sync.Mutex
	runtimes         map[string]*clusterRuntime
	legacyID         string
	closed           bool
	globalJobs       *jobs.Manager
	globalOperations *operations.Service
	downloadTickets  *DownloadTickets
}

func OpenFleet(opts FleetOptions) (*Fleet, error) {
	if opts.Store == nil || opts.Auth == nil {
		return nil, errors.New("cluster store and authentication required")
	}
	if opts.DataDir == "" {
		opts.DataDir = "./data"
	}
	if opts.LegacyBackupDir == "" {
		opts.LegacyBackupDir = filepath.Join(opts.DataDir, "backups")
	}
	if opts.LegacyJobsDir == "" {
		opts.LegacyJobsDir = filepath.Join(opts.DataDir, "jobs")
	}
	f := &Fleet{options: opts, runtimes: make(map[string]*clusterRuntime)}
	f.downloadTickets = NewDownloadTickets(opts.Auth)
	globalOps := &operations.Service{ClusterName: "TalosDeck fleet"}
	globalOps.Provision = &operations.ProvisionService{ClusterID: operations.FleetScope, Store: opts.Store, RegisterCluster: func(ctx context.Context, name string, talosconfig, kubeconfig []byte, providerID string) (string, error) {
		cluster, err := f.Import(ctx, ImportClusterRequest{Name: name, Talosconfig: string(talosconfig), Kubeconfig: string(kubeconfig), Provider: providerID})
		return cluster.ID, err
	}}
	globalJobs, err := jobs.OpenCluster(filepath.Join(opts.DataDir, "fleet-jobs"), operations.FleetScope, globalOps.Run)
	if err != nil {
		return nil, err
	}
	f.globalJobs, f.globalOperations = globalJobs, globalOps
	if opts.Audit != nil {
		globalJobs.SetCompletionHandler(func(j jobs.Job) {
			opts.Audit.Log(audit.AuditEvent{Action: "job." + j.Request.Kind, User: j.User, Status: j.Status, Details: map[string]any{"jobId": j.ID, "scope": "fleet"}})
		})
	}
	if err := globalOps.Provision.ReconcileInterrupted(context.Background()); err != nil {
		f.Close()
		return nil, err
	}
	list, err := opts.Store.List(context.Background())
	if err != nil {
		f.Close()
		return nil, err
	}
	for _, cluster := range list {
		creds, err := opts.Store.Credentials(context.Background(), cluster.ID)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("cluster %s credentials cannot be decrypted", cluster.ID)
		}
		rt, err := f.newRuntime(cluster, creds)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("cluster %s runtime initialization failed: %w", cluster.ID, err)
		}
		f.runtimes[cluster.ID] = rt
		if cluster.Legacy {
			f.legacyID = cluster.ID
		}
	}
	return f, nil
}

type providerCredentials struct {
	Proxmox  proxmox.Config        `json:"proxmox"`
	Telegram alerts.TelegramConfig `json:"telegram"`
}

func runtimeConfigFile(raw []byte) (string, error) {
	base := os.Getenv("TALOSDECK_RUNTIME_DIR")
	if base == "" {
		base = "/dev/shm"
		if _, err := os.Stat(base); err != nil {
			base = os.TempDir()
		}
	}
	dir, err := os.MkdirTemp(base, "talosdeck-credentials-")
	if err != nil {
		return "", errors.New("cannot create private runtime credentials directory; configure TALOSDECK_RUNTIME_DIR")
	}
	if err = os.WriteFile(filepath.Join(dir, "talosconfig"), raw, 0600); err != nil {
		os.RemoveAll(dir)
		return "", errors.New("cannot write runtime credentials")
	}
	return dir, nil
}

func (f *Fleet) newRuntime(cluster clusters.Cluster, creds clusters.Credentials) (_ *clusterRuntime, resultErr error) {
	dir, err := runtimeConfigFile(creds.Talosconfig)
	if err != nil {
		return nil, err
	}
	rt := &clusterRuntime{credentialsDir: dir}
	defer func() {
		if resultErr != nil {
			rt.close()
		}
	}()
	tm, err := talos.NewTalosManager(filepath.Join(dir, "talosconfig"))
	if err != nil {
		return nil, errors.New("invalid Talos credentials")
	}
	rt.config.Manager = tm
	owned, err := operations.ListOwnedMachines(context.Background(), f.options.Store, cluster.ID)
	if err != nil {
		return nil, err
	}
	liveAddresses := map[string]bool{}
	for _, record := range owned {
		if record.Status != "deleted" && record.Address != "" {
			liveAddresses[record.Address] = true
		}
	}
	for _, record := range owned {
		if record.Status == "deleted" && record.Address != "" && !liveAddresses[record.Address] {
			tm.ForgetNode(record.Address, record.Name)
		}
	}
	km, err := k8s.NewK8sManagerFromBytes(creds.Kubeconfig)
	if err != nil {
		return nil, err
	}
	rt.config.K8s = km
	dataDir := filepath.Join(f.options.DataDir, "clusters", cluster.ID)
	backupDir, jobDir, auditPath := filepath.Join(dataDir, "backups"), filepath.Join(dataDir, "jobs"), filepath.Join(dataDir, "audit.log")
	if cluster.Legacy {
		backupDir, jobDir, auditPath = f.options.LegacyBackupDir, f.options.LegacyJobsDir, filepath.Join(f.options.DataDir, "audit.log")
	}
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return nil, errors.New("cannot create cluster data directory")
	}
	bm, err := backup.NewBackupManager(backupDir, tm)
	if err != nil {
		return nil, errors.New("cannot open cluster backups")
	}
	rt.config.Backup = bm
	am, err := audit.NewAuditManager(auditPath, 1000)
	if err != nil {
		return nil, errors.New("cannot open cluster audit journal")
	}
	rt.config.Audit = am
	am.BindCluster(cluster.ID)
	rt.config.ClusterName = cluster.Name
	var providers providerCredentials
	if len(creds.ProviderSecrets) > 0 && json.Unmarshal(creds.ProviderSecrets, &providers) != nil {
		return nil, errors.New("invalid encrypted provider configuration")
	}
	if cluster.Legacy && (cluster.Provider == "" || cluster.Provider == "proxmox") && (providers.Proxmox.APIToken != "" || providers.Proxmox.Username != "") {
		providerID, err := operations.ImportLegacyProvider(context.Background(), f.options.Store, providers.Proxmox)
		if err != nil {
			return nil, fmt.Errorf("cannot migrate encrypted Proxmox provider: %w", err)
		}
		cluster.Provider = providerID
		if err := f.options.Store.Update(context.Background(), cluster); err != nil {
			return nil, err
		}
	}
	pc, err := proxmox.NewClient(providers.Proxmox)
	if _, providerErr := uuid.Parse(cluster.Provider); providerErr == nil {
		pc, err = operations.ProviderClient(context.Background(), f.options.Store, cluster.Provider)
	}
	if err != nil {
		return nil, errors.New("invalid provider TLS configuration")
	}
	rt.config.Proxmox = pc
	svc := alerts.NewTelegramService(providers.Telegram.BotToken, providers.Telegram.ChatID, providers.Telegram.Enabled)
	if err = svc.UpdateConfig(providers.Telegram); err != nil {
		return nil, err
	}
	svc.SetPersistence(func(cfg alerts.TelegramConfig) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		current, err := f.options.Store.Credentials(ctx, cluster.ID)
		if err != nil {
			return errors.New("cannot read encrypted alert settings")
		}
		var saved providerCredentials
		if len(current.ProviderSecrets) > 0 && json.Unmarshal(current.ProviderSecrets, &saved) != nil {
			return errors.New("invalid encrypted settings")
		}
		saved.Telegram = cfg
		current.ProviderSecrets, err = json.Marshal(saved)
		if err != nil {
			return err
		}
		if err = f.options.Store.UpdateCredentials(ctx, cluster.ID, current); err != nil {
			return errors.New("cannot persist encrypted alert settings")
		}
		return nil
	})
	svc.SetClusterLabel(cluster.Name)
	rt.config.AlertService = svc
	ops := &operations.Service{Talos: tm, Kubernetes: km, Backups: bm, CLI: operations.CLI{Path: os.Getenv("TALOSDECK_TALOSCTL")}}
	ops.ClusterName = cluster.Name
	ops.Config = &operations.ConfigService{ClusterID: cluster.ID, Store: f.options.Store, NodeClient: tm, Audit: func(action, user, node, status, revision string) {
		am.Log(audit.AuditEvent{Action: action, User: user, Status: status, Details: map[string]any{"clusterId": cluster.ID, "node": node, "revision": revision}})
	}}
	ops.Provision = &operations.ProvisionService{ClusterID: cluster.ID, Store: f.options.Store, Talos: tm, Kubernetes: km, Audit: func(action, user, status, id string) {
		am.Log(audit.AuditEvent{Action: action, User: user, Status: status, Details: map[string]any{"clusterId": cluster.ID, "provisionId": id}})
	}}
	ops.BackupLifecycle = &operations.BackupService{ClusterID: cluster.ID, Store: f.options.Store, Manager: bm, Operations: ops}
	ops.Diagnostics = &operations.DiagnosticsService{ClusterID: cluster.ID, Store: f.options.Store, Talos: tm, Kubernetes: km, Backups: ops.BackupLifecycle}
	bm.SetMaxBackups(0)
	rt.config.Operations = ops
	jm, err := jobs.OpenCluster(jobDir, cluster.ID, ops.Run)
	if err != nil {
		return nil, err
	}
	rt.config.Jobs = jm
	if err := ops.Provision.ReconcileInterrupted(context.Background()); err != nil {
		return nil, err
	}
	jm.SetCompletionHandler(func(job jobs.Job) {
		am.Log(audit.AuditEvent{Action: "job." + job.Request.Kind, User: job.User, Status: job.Status, Details: map[string]any{"jobId": job.ID}})
		if !svc.IsEnabled() {
			return
		}
		level := alerts.LevelInfo
		if job.Status != "succeeded" {
			level = alerts.LevelWarning
		}
		notifyCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = svc.SendAlertWithContext(notifyCtx, level, "Operation "+job.Status, "Job "+job.ID+" ("+job.Request.Kind+")")
	})
	rt.config.Auth = f.options.Auth
	rt.config.DownloadTickets = f.downloadTickets
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	rt.wg.Add(1)
	go func() { defer rt.wg.Done(); ops.BackupLifecycle.Scheduler(ctx, jm) }()
	watcher := alerts.NewWatcher(tm, svc, 30*time.Second)
	rt.config.AlertWatcher = watcher
	app := SetupServer(rt.config)
	rt.handler = app.Handler()
	watcher.Start(ctx)
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		// Wait until an import is committed before observing its metadata.
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, cancel := context.WithTimeout(ctx, 20*time.Second)
				info, err := tm.GetClusterInfo(check)
				record, e := f.options.Store.Get(check, cluster.ID)
				if e == nil {
					record.Health = "unknown"
					if err == nil && info != nil {
						record.TalosVersion = info.TalosVersion
						record.Health = "degraded"
						version, knodes, kerr := km.UpgradeInventory(check)
						khealthy := kerr == nil && len(knodes) == info.TotalNodes
						for _, node := range knodes {
							khealthy = khealthy && node.Ready
						}
						if kerr == nil {
							record.KubernetesVersion = version
						}
						if info.Healthy && khealthy {
							record.Health = "healthy"
						}
					}
					_ = f.options.Store.Update(check, record)
				}
				cancel()
			}
		}
	}()
	return rt, nil
}

func (rt *clusterRuntime) close() {
	if rt.cancel != nil {
		rt.cancel()
	}
	if rt.config.AlertWatcher != nil {
		rt.config.AlertWatcher.Stop()
	}
	rt.wg.Wait()
	if rt.config.Jobs != nil {
		_ = rt.config.Jobs.Close()
	}
	if rt.config.Manager != nil {
		_ = rt.config.Manager.Close()
	}
	if rt.config.Audit != nil {
		_ = rt.config.Audit.Close()
	}
	if rt.credentialsDir != "" {
		_ = os.RemoveAll(rt.credentialsDir)
	}
}

func (f *Fleet) Close() {
	// Cancel global provisioning before taking importMu: a finishing job may be
	// importing its new cluster and must be allowed to leave the callback.
	if f.globalJobs != nil {
		_ = f.globalJobs.Close()
	}
	f.importMu.Lock()
	defer f.importMu.Unlock()
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	runtimes := f.runtimes
	f.runtimes = map[string]*clusterRuntime{}
	f.mu.Unlock()
	for _, rt := range runtimes {
		rt.close()
	}
}

type ImportClusterRequest struct {
	Name        string `json:"name"`
	Talosconfig string `json:"talosconfig"`
	Kubeconfig  string `json:"kubeconfig"`
	Provider    string `json:"provider"`
}

// inspectImportedCluster proves the supplied credentials address the same fleet
// before committing them. It performs only read operations.
type importTalosInspector interface {
	ListNodes(context.Context) ([]*talos.NodeOverview, error)
	GetEtcdStatus(context.Context) (*talos.EtcdClusterStatus, error)
	GetKubernetesConfig(context.Context, string) ([]byte, error)
}
type importKubernetesInspector interface {
	UpgradeInventory(context.Context) (string, []k8s.UpgradeNode, error)
}

func inspectImportedCluster(ctx context.Context, tm importTalosInspector, km importKubernetesInspector, cluster *clusters.Cluster, importedKubeconfig []byte) error {
	nodes, err := tm.ListNodes(ctx)
	if err != nil || len(nodes) == 0 {
		return errors.New("Talos API unavailable: check endpoints, network and client certificate")
	}
	version, knodes, err := km.UpgradeInventory(ctx)
	if err != nil {
		return errors.New("Kubernetes API unavailable: check kubeconfig, network and permission to list nodes")
	}
	if len(knodes) != len(nodes) {
		return errors.New("Talos and Kubernetes discovery disagree: verify both configurations belong to the same cluster")
	}
	cp, healthy := 0, true
	controlPlane := ""
	seen := map[string]bool{}
	for _, node := range nodes {
		if node == nil || node.Version == "" || (node.Role != "controlplane" && node.Role != "worker") {
			return errors.New("Talos discovery incomplete: credentials must reach every node and identify its role")
		}
		match := ""
		for _, kn := range knodes {
			for _, addr := range kn.Addresses {
				a, ae := netip.ParseAddr(addr)
				b, be := netip.ParseAddr(node.IP)
				if ae == nil && be == nil && a.Unmap() == b.Unmap() {
					if match != "" && match != kn.Name {
						return errors.New("ambiguous Kubernetes node identity")
					}
					match = kn.Name
					healthy = healthy && kn.Ready
				}
			}
		}
		if match == "" || seen[match] {
			return errors.New("Talos and Kubernetes node addresses do not match; check selected contexts")
		}
		seen[match] = true
		if node.Role == "controlplane" {
			cp++
			controlPlane = node.IP
		}
		healthy = healthy && node.Ready
		if cluster.TalosVersion == "" {
			cluster.TalosVersion = node.Version
		} else if cluster.TalosVersion != node.Version {
			cluster.TalosVersion = "mixed"
		}
	}
	if cp == 0 {
		return errors.New("no Talos control plane discovered")
	}
	trustedKubeconfig, err := tm.GetKubernetesConfig(ctx, controlPlane)
	if err != nil {
		return errors.New("cannot verify Kubernetes identity through Talos: control-plane kubeconfig access required")
	}
	want, err := k8s.ConfigIdentity(trustedKubeconfig)
	if err != nil {
		return errors.New("cannot identify the Kubernetes CA managed by Talos")
	}
	got, err := k8s.ConfigIdentity(importedKubeconfig)
	if err != nil || got != want {
		return errors.New("kubeconfig belongs to a different Kubernetes cluster: CA does not match Talos")
	}
	etcd, err := tm.GetEtcdStatus(ctx)
	if err != nil || etcd == nil || !etcd.Healthy {
		healthy = false
	}
	cluster.KubernetesVersion = version
	cluster.Health = "degraded"
	if healthy {
		cluster.Health = "healthy"
	}
	return nil
}

func (f *Fleet) Import(ctx context.Context, req ImportClusterRequest) (clusters.Cluster, error) {
	f.importMu.Lock()
	defer f.importMu.Unlock()
	f.mu.RLock()
	closed := f.closed
	f.mu.RUnlock()
	if closed {
		return clusters.Cluster{}, errors.New("cluster registry is shutting down")
	}
	if len(req.Name) == 0 || len(req.Name) > 80 || strings.ContainsAny(req.Name, "\r\n\t") || strings.TrimSpace(req.Name) != req.Name {
		return clusters.Cluster{}, errors.New("cluster name must contain 1–80 characters without surrounding whitespace")
	}
	if len(req.Talosconfig) > 1024*1024 || len(req.Kubeconfig) > 1024*1024 {
		return clusters.Cluster{}, errors.New("configuration exceeds 1 MiB limit")
	}
	raw, identity, _, endpoints, err := normalizeTalosConfig([]byte(req.Talosconfig), nil)
	if err != nil {
		return clusters.Cluster{}, err
	}
	kube, err := k8s.NormalizeConfig([]byte(req.Kubeconfig))
	if err != nil {
		return clusters.Cluster{}, err
	}
	list, err := f.options.Store.List(ctx)
	if err != nil {
		return clusters.Cluster{}, errors.New("cannot read cluster registry")
	}
	for _, c := range list {
		if c.Identity == identity {
			return clusters.Cluster{}, clusters.ErrDuplicate
		}
	}
	cluster := clusters.Cluster{ID: uuid.NewString(), Name: req.Name, Identity: identity, Endpoints: endpoints, Provider: req.Provider, Health: "unknown"}
	dir, err := runtimeConfigFile(raw)
	if err != nil {
		return cluster, err
	}
	defer os.RemoveAll(dir)
	tm, err := talos.NewTalosManager(filepath.Join(dir, "talosconfig"))
	if err != nil {
		return cluster, errors.New("talosconfig: invalid TLS credentials")
	}
	defer tm.Close()
	km, err := k8s.NewK8sManagerFromBytes(kube)
	if err != nil {
		return cluster, err
	}
	if err = inspectImportedCluster(ctx, tm, km, &cluster, kube); err != nil {
		return cluster, err
	}
	creds := clusters.Credentials{Talosconfig: raw, Kubeconfig: kube}
	// Initialize journals before committing: a failed import leaves no database
	// record that cannot be opened on the next startup.
	rt, err := f.newRuntime(cluster, creds)
	if err != nil {
		return cluster, err
	}
	cluster, err = f.options.Store.Create(ctx, cluster, creds)
	if err != nil {
		rt.close()
		return cluster, err
	}
	f.mu.Lock()
	f.runtimes[cluster.ID] = rt
	f.mu.Unlock()
	return cluster, nil
}

func (f *Fleet) register(app *fiber.App) {
	app.Use(func(c *fiber.Ctx) error {
		path := c.Path()
		action := ""
		if path == "/api/clusters" {
			action = "clusters"
		} else if path == "/api/providers" || strings.HasPrefix(path, "/api/providers/") {
			action = "providers"
		} else if path == "/api/provision" || strings.HasPrefix(path, "/api/provision/") {
			action = "provision"
		}
		if f.options.Audit == nil || action == "" || c.Method() == "GET" || c.Method() == "HEAD" || c.Method() == "OPTIONS" {
			return c.Next()
		}
		err := c.Next()
		status := "success"
		if err != nil || c.Response().StatusCode() >= 400 {
			status = "failed"
		}
		user, _ := c.Locals("user").(string)
		if user == "" {
			user = "anonymous"
		}
		f.options.Audit.Log(audit.AuditEvent{Action: action + "." + strings.ToLower(c.Method()), User: user, IP: auth.GetClientIP(c), Status: status})
		return err
	})
	app.Get("/readyz", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok", "registry": "available"}) })
	app.Get("/api/health", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok"}) })
	global := app.Group("/api")
	if f.options.Audit != nil {
		global.Get("/audit", auth.RequireAuth(f.options.Auth), func(c *fiber.Ctx) error {
			limit := c.QueryInt("limit", 100)
			if limit < 1 || limit > 1000 {
				limit = 100
			}
			return c.JSON(fiber.Map{"events": f.options.Audit.GetEvents(limit, c.Query("action"), c.Query("search")), "total": f.options.Audit.TotalCount()})
		})
	}
	RegisterProviderRoutes(global, f.options.Store, f.options.Auth)
	RegisterProvisionRoutes(global, f.globalJobs, f.globalOperations.Provision, f.options.Auth)
	RegisterJobRoutes(app.Group("/api/provision"), f.globalJobs, f.globalOperations, f.options.Auth)
	group := app.Group("/api/clusters")
	group.Get("/", auth.RequireAuth(f.options.Auth), func(c *fiber.Ctx) error {
		list, err := f.options.Store.List(c.UserContext())
		if err != nil {
			return fiber.NewError(503, "cluster registry unavailable")
		}
		if list == nil {
			list = []clusters.Cluster{}
		}
		return c.JSON(fiber.Map{"clusters": list})
	})
	group.Post("/", auth.RequireAuth(f.options.Auth), func(c *fiber.Ctx) error {
		if c.Locals("role") != "admin" {
			return fiber.ErrForbidden
		}
		release, err := f.globalJobs.ReserveManual()
		if err != nil {
			return jobError(err)
		}
		defer release()
		var req ImportClusterRequest
		if c.BodyParser(&req) != nil {
			return fiber.NewError(400, "invalid import request")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 45*time.Second)
		defer cancel()
		cluster, err := f.Import(ctx, req)
		if errors.Is(err, clusters.ErrDuplicate) {
			return fiber.NewError(409, "this Talos cluster is already imported")
		}
		if err != nil {
			return fiber.NewError(400, err.Error())
		}
		return c.Status(201).JSON(fiber.Map{"cluster": cluster})
	})
	// Dispatch precedes the legacy routes and works for WebSocket upgrades too.
	// WebSockets authenticate in the child app through Sec-WebSocket-Protocol.
	app.Use(func(c *fiber.Ctx) error {
		path := c.Path()
		if strings.HasPrefix(path, "/api/clusters/") {
			rest := strings.TrimPrefix(path, "/api/clusters/")
			id, suffix, _ := strings.Cut(rest, "/")
			if _, err := uuid.Parse(id); err != nil {
				return fiber.ErrNotFound
			}
			f.mu.RLock()
			rt := f.runtimes[id]
			f.mu.RUnlock()
			if rt == nil {
				return fiber.ErrNotFound
			}
			if suffix == "" {
				if err := f.authenticate(c); err != nil {
					return err
				}
				if c.Method() != fiber.MethodGet && c.Method() != fiber.MethodHead {
					c.Set(fiber.HeaderAllow, "GET, HEAD")
					return fiber.ErrMethodNotAllowed
				}
				cluster, err := f.options.Store.Get(c.UserContext(), id)
				if err != nil {
					return fiber.ErrNotFound
				}
				return c.JSON(fiber.Map{"cluster": cluster})
			}
			if strings.HasPrefix(suffix, "ws/") {
				return dispatchCluster(c, rt, "/"+suffix)
			}
			if err := f.authenticate(c); err != nil {
				return err
			}
			if strings.HasPrefix(suffix, "auth/") {
				return fiber.ErrNotFound
			}
			return dispatchCluster(c, rt, "/api/"+suffix)
		}
		if strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/api/auth/") && path != "/api/healthz" && path != "/api/readyz" {
			if err := f.authenticate(c); err != nil {
				return err
			}
			f.mu.RLock()
			rt := f.runtimes[f.legacyID]
			f.mu.RUnlock()
			if rt == nil {
				return fiber.NewError(409, "select a cluster using /api/clusters/:id")
			}
			return dispatchCluster(c, rt, path)
		}
		if strings.HasPrefix(path, "/ws/") {
			f.mu.RLock()
			rt := f.runtimes[f.legacyID]
			f.mu.RUnlock()
			if rt == nil {
				return fiber.ErrNotFound
			}
			return dispatchCluster(c, rt, path)
		}
		return c.Next()
	})
}

func (f *Fleet) authenticate(c *fiber.Ctx) error {
	header := strings.Fields(c.Get("Authorization"))
	if len(header) != 2 || !strings.EqualFold(header[0], "Bearer") {
		return fiber.ErrUnauthorized
	}
	claims, err := f.options.Auth.ValidateToken(header[1])
	if err != nil {
		return fiber.ErrUnauthorized
	}
	if !auth.Can(claims.Role, c.Method(), c.Path()) {
		return fiber.ErrForbidden
	}
	return nil
}

func dispatchCluster(c *fiber.Ctx, rt *clusterRuntime, path string) error {
	c.Request().URI().SetPath(path)
	rt.handler(c.Context())
	return nil
}
