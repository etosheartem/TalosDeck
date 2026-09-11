package proxmox

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var validNodeNameRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

func isValidNodeName(name string) bool {
	return validNodeNameRegex.MatchString(name)
}

func isTalosWorker(name string) bool {
	lower := strings.ToLower(name)
	// Explicitly protect master / control-plane nodes
	if strings.HasPrefix(lower, "talos-cp") || strings.Contains(lower, "controlplane") || strings.Contains(lower, "master") {
		return false
	}
	// Explicitly protect user workstations, production servers and Proxmox infrastructure
	if lower == "win11" || lower == "ubuntu-server" || strings.Contains(lower, "pve") || strings.Contains(lower, "proxmox") {
		return false
	}
	// Destructive deletion is allowed only for the TalosDeck worker naming scheme.
	return lower == "talos-worker" || strings.HasPrefix(lower, "talos-worker-")
}

// NodeDrainer is an interface to gracefully cordon and drain workloads from a Kubernetes node before VM destruction (PVE-07).
type NodeDrainer interface {
	CordonAndDrainNode(ctx context.Context, nodeName string) error
}

// Client is an HTTP client for communicating with the Proxmox VE REST API.
type Client struct {
	cfg        Config
	httpClient *http.Client

	mu              sync.RWMutex
	ticket          string
	csrfToken       string
	ticketExpiresAt time.Time

	vmidMu        sync.Mutex
	inFlightVMIDs map[int]bool
	drainer       NodeDrainer
}

// NewClient creates a new Proxmox VE client from the provided configuration.
func NewClient(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://192.168.88.169:8006"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	if cfg.Node == "" {
		cfg.Node = "pve"
	}
	if cfg.DefaultStorage == "" {
		cfg.DefaultStorage = "local-lvm"
	}
	if cfg.DefaultISO == "" {
		cfg.DefaultISO = "data:iso/talos-v1.14.0-qemu-guest-agent.iso"
	}
	if cfg.DefaultBridge == "" {
		cfg.DefaultBridge = "vmbr0"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.SkipTLSVerify,
	}

	// Support custom CA certificates (TLS-02)
	if cfg.CACertFile != "" || cfg.CACert != "" {
		certPool, err := x509.SystemCertPool()
		if err != nil || certPool == nil {
			certPool = x509.NewCertPool()
		}
		if cfg.CACertFile != "" {
			caBytes, err := os.ReadFile(cfg.CACertFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read Proxmox CA certificate %s: %w", cfg.CACertFile, err)
			}
			if ok := certPool.AppendCertsFromPEM(caBytes); !ok {
				return nil, fmt.Errorf("failed to parse Proxmox CA certificate from %s", cfg.CACertFile)
			}
		}
		if cfg.CACert != "" {
			if ok := certPool.AppendCertsFromPEM([]byte(cfg.CACert)); !ok {
				return nil, errors.New("failed to parse inline Proxmox CA certificate")
			}
		}
		tlsConfig.RootCAs = certPool
	}

	transport := &http.Transport{
		TLSClientConfig:    tlsConfig,
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: false,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
	}

	return &Client{
		cfg:        cfg,
		httpClient: httpClient,
	}, nil
}

// NewClientFromEnv initializes a Proxmox client using environment variables and fallback files.
func NewClientFromEnv() *Client {
	baseURL := getEnv("PROXMOX_URL", getEnv("PVE_URL", "https://192.168.88.169:8006"))
	node := getEnv("PROXMOX_NODE", getEnv("PVE_NODE", "pve"))
	apiToken := getEnv("PROXMOX_API_TOKEN", getEnv("PVE_API_TOKEN", getEnv("PROXMOX_TOKEN", "")))
	username := getEnv("PROXMOX_USERNAME", getEnv("PVE_USERNAME", getEnv("PROXMOX_USER", "")))
	password := getEnv("PROXMOX_PASSWORD", getEnv("PVE_PASSWORD", ""))
	storage := getEnv("PROXMOX_STORAGE", getEnv("PVE_STORAGE", "local-lvm"))
	iso := getEnv("PROXMOX_ISO", getEnv("PVE_ISO", "data:iso/talos-v1.14.0-qemu-guest-agent.iso"))
	bridge := getEnv("PROXMOX_BRIDGE", getEnv("PVE_BRIDGE", "vmbr0"))

	// PVE-05: InsecureSkipVerify must default to false (safe by default)
	skipTLS := getEnvBool("PROXMOX_SKIP_TLS_VERIFY", getEnvBool("PVE_SKIP_TLS_VERIFY", false))
	caCert := getEnv("PROXMOX_CA_CERT", getEnv("PVE_CA_CERT", ""))
	caFile := getEnv("PROXMOX_CA_FILE", getEnv("PVE_CA_FILE", getEnv("PROXMOX_CA_PATH", "")))

	// PVE-12: Dynamic token path search
	if apiToken == "" && username == "" {
		tokenFileEnv := getEnv("PROXMOX_TOKEN_FILE", getEnv("PVE_TOKEN_FILE", ""))
		var tokenPaths []string
		if tokenFileEnv != "" {
			tokenPaths = append(tokenPaths, tokenFileEnv)
		}
		tokenPaths = append(tokenPaths,
			"cluster-config/proxmox.token",
			"./proxmox.token",
			"proxmox.token",
			"/etc/talosdeck/proxmox.token",
			"/home/artem/laba-kuber/cluster-config/proxmox.token",
		)
		for _, p := range tokenPaths {
			if content, err := os.ReadFile(p); err == nil {
				token := strings.TrimSpace(string(content))
				if token != "" {
					apiToken = token
					break
				}
			}
		}
	}

	cfg := Config{
		BaseURL:        baseURL,
		Node:           node,
		SkipTLSVerify:  skipTLS,
		CACert:         caCert,
		CACertFile:     caFile,
		APIToken:       apiToken,
		Username:       username,
		Password:       password,
		DefaultStorage: storage,
		DefaultISO:     iso,
		DefaultBridge:  bridge,
		Timeout:        45 * time.Second,
	}

	client, _ := NewClient(cfg)
	return client
}

// IsConfigured returns true if minimum authentication credentials are provided.
func (c *Client) IsConfigured() bool {
	if c == nil {
		return false
	}
	return c.cfg.BaseURL != "" && (c.cfg.APIToken != "" || (c.cfg.Username != "" && c.cfg.Password != ""))
}

// GetBaseURL returns the configured base URL.
func (c *Client) GetBaseURL() string {
	if c == nil {
		return ""
	}
	return c.cfg.BaseURL
}

// GetNode returns the configured Proxmox target node name.
func (c *Client) GetNode() string {
	if c == nil {
		return ""
	}
	return c.cfg.Node
}

// GetNodeStatus returns CPU, memory total/used/free, and local-lvm storage status.
func (c *Client) GetNodeStatus(ctx context.Context) (*NodeStatus, error) {
	if !c.IsConfigured() {
		return nil, errors.New("proxmox client is not configured")
	}

	node := c.cfg.Node

	// 1. Fetch Node Status (CPU, Memory, Uptime)
	var nodeResp struct {
		Data struct {
			CPU     float64 `json:"cpu"`
			Uptime  int64   `json:"uptime"`
			CPUInfo struct {
				Model   string `json:"model"`
				Cores   int    `json:"cores"`
				CPUs    int    `json:"cpus"`
				Sockets int    `json:"sockets"`
			} `json:"cpuinfo"`
			Memory struct {
				Total     uint64 `json:"total"`
				Used      uint64 `json:"used"`
				Free      uint64 `json:"free"`
				Available uint64 `json:"available"`
			} `json:"memory"`
		} `json:"data"`
	}

	if err := c.getJSON(ctx, fmt.Sprintf("/nodes/%s/status", node), &nodeResp); err != nil {
		return nil, fmt.Errorf("failed to get node status for %s: %w", node, err)
	}

	// 2. Fetch Storage Status for local-lvm
	storageName := c.cfg.DefaultStorage
	var storageResp struct {
		Data struct {
			Type    string `json:"type"`
			Total   uint64 `json:"total"`
			Used    uint64 `json:"used"`
			Avail   uint64 `json:"avail"`
			Active  int    `json:"active"`
			Enabled int    `json:"enabled"`
		} `json:"data"`
	}

	if err := c.getJSON(ctx, fmt.Sprintf("/nodes/%s/storage/%s/status", node, storageName), &storageResp); err != nil {
		return nil, fmt.Errorf("failed to get storage status for %s/%s: %w", node, storageName, err)
	}

	// Calculate usage percentages
	cpuUsagePct := math.Round(nodeResp.Data.CPU*10000) / 100
	memTotal := nodeResp.Data.Memory.Total
	memUsed := nodeResp.Data.Memory.Used
	var memUsagePct float64
	if memTotal > 0 {
		memUsagePct = math.Round(float64(memUsed)/float64(memTotal)*10000) / 100
	}

	storTotal := storageResp.Data.Total
	storUsed := storageResp.Data.Used
	var storUsagePct float64
	if storTotal > 0 {
		storUsagePct = math.Round(float64(storUsed)/float64(storTotal)*10000) / 100
	}

	cores := nodeResp.Data.CPUInfo.CPUs
	if cores <= 0 {
		cores = nodeResp.Data.CPUInfo.Cores
	}

	return &NodeStatus{
		Node:            node,
		Uptime:          nodeResp.Data.Uptime,
		CPU:             nodeResp.Data.CPU,
		CPUUsagePercent: cpuUsagePct,
		CPUCores:        cores,
		CPUModel:        nodeResp.Data.CPUInfo.Model,
		Memory: MemoryStatus{
			Total:        memTotal,
			Used:         memUsed,
			Free:         nodeResp.Data.Memory.Free,
			Available:    nodeResp.Data.Memory.Available,
			UsagePercent: memUsagePct,
		},
		Storage: StorageStatus{
			Name:         storageName,
			Total:        storTotal,
			Used:         storUsed,
			Free:         storageResp.Data.Avail,
			UsagePercent: storUsagePct,
			Type:         storageResp.Data.Type,
		},
	}, nil
}

func (c *Client) fetchNextVMID(ctx context.Context) (int, error) {
	var resp struct {
		Data json.RawMessage `json:"data"`
	}

	if err := c.getJSON(ctx, "/cluster/nextid", &resp); err != nil {
		return 0, fmt.Errorf("failed to get next VMID from proxmox: %w", err)
	}

	var vmid int
	var strVal string
	if err := json.Unmarshal(resp.Data, &strVal); err == nil {
		var errConv error
		vmid, errConv = strconv.Atoi(strVal)
		if errConv != nil {
			return 0, fmt.Errorf("unexpected non-numeric VMID string: %s", strVal)
		}
	} else {
		var numVal int
		if err := json.Unmarshal(resp.Data, &numVal); err == nil {
			vmid = numVal
		} else {
			return 0, fmt.Errorf("unable to decode nextid response: %s", string(resp.Data))
		}
	}

	return vmid, nil
}

// GetNextVMID queries the next available VMID without locking it permanently.
func (c *Client) GetNextVMID(ctx context.Context) (int, error) {
	if !c.IsConfigured() {
		return 0, errors.New("proxmox client is not configured")
	}

	c.vmidMu.Lock()
	defer c.vmidMu.Unlock()

	vmid, err := c.fetchNextVMID(ctx)
	if err != nil {
		return 0, err
	}

	for c.inFlightVMIDs != nil && c.inFlightVMIDs[vmid] {
		vmid++
	}

	if vmid < 100 {
		vmid = 100
		for c.inFlightVMIDs != nil && c.inFlightVMIDs[vmid] {
			vmid++
		}
	}

	return vmid, nil
}

// allocateVMID reserves a unique VMID to prevent TOCTOU collisions during concurrent creations (PVE-04).
func (c *Client) allocateVMID(ctx context.Context) (int, error) {
	c.vmidMu.Lock()
	defer c.vmidMu.Unlock()

	vmid, err := c.fetchNextVMID(ctx)
	if err != nil {
		return 0, err
	}

	if c.inFlightVMIDs == nil {
		c.inFlightVMIDs = make(map[int]bool)
	}

	for c.inFlightVMIDs[vmid] {
		vmid++
	}

	if vmid < 100 {
		vmid = 100
		for c.inFlightVMIDs[vmid] {
			vmid++
		}
	}

	c.inFlightVMIDs[vmid] = true
	return vmid, nil
}

// reserveVMID protects explicitly requested IDs with the same in-flight
// reservation used by automatic allocation.
func (c *Client) reserveVMID(vmid int) error {
	c.vmidMu.Lock()
	defer c.vmidMu.Unlock()

	if c.inFlightVMIDs == nil {
		c.inFlightVMIDs = make(map[int]bool)
	}
	if c.inFlightVMIDs[vmid] {
		return fmt.Errorf("VMID %d is already being created", vmid)
	}
	c.inFlightVMIDs[vmid] = true

	return nil
}

func (c *Client) releaseVMID(vmid int) {
	c.vmidMu.Lock()
	defer c.vmidMu.Unlock()
	if c.inFlightVMIDs != nil {
		delete(c.inFlightVMIDs, vmid)
	}
}

// CreateTalosWorker creates a new worker VM, attaches Talos ISO, configures resources, and starts the VM.
func (c *Client) CreateTalosWorker(ctx context.Context, opts CreateWorkerOpts) (*CreateWorkerResult, error) {
	if !c.IsConfigured() {
		return nil, errors.New("proxmox client is not configured")
	}

	node := c.cfg.Node

	// PVE-10: Validate VMID range if explicitly specified
	if opts.VMID > 0 {
		if opts.VMID < 100 || opts.VMID > 999999999 {
			return nil, fmt.Errorf("invalid VMID %d: Proxmox VMID must be between 100 and 999999999", opts.VMID)
		}
	}

	// Reserve both automatic and explicit VMIDs for the entire create operation.
	vmid := opts.VMID
	if vmid <= 0 {
		var err error
		vmid, err = c.allocateVMID(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to auto-allocate VMID: %w", err)
		}
	} else if err := c.reserveVMID(vmid); err != nil {
		return nil, err
	}
	defer c.releaseVMID(vmid)

	// Apply sensible defaults matching TalosDeck worker template
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = fmt.Sprintf("talos-worker-%d", vmid)
	}

	// QEMU-04: Validate hostname according to RFC 1123
	if !isValidNodeName(name) {
		return nil, fmt.Errorf("invalid VM name %q: must adhere to RFC 1123 (lowercase alphanumeric characters or '-', max 63 chars, must start and end with an alphanumeric character)", name)
	}

	cores := opts.Cores
	if cores <= 0 {
		cores = 2
	}
	memoryMB := opts.MemoryMB
	if memoryMB <= 0 {
		memoryMB = 3072 // 3GB RAM
	}
	diskGB := opts.DiskGB
	if diskGB <= 0 {
		diskGB = 30 // 30GB Disk
	}
	storage := opts.Storage
	if storage == "" {
		storage = c.cfg.DefaultStorage
	}
	iso := opts.ISO
	if iso == "" {
		iso = c.cfg.DefaultISO
	}
	bridge := opts.Bridge
	if bridge == "" {
		bridge = c.cfg.DefaultBridge
	}
	shouldStart := true
	if opts.Start != nil {
		shouldStart = *opts.Start
	}

	// Form body matching `qm create` parameters
	form := url.Values{}
	form.Set("vmid", strconv.Itoa(vmid))
	form.Set("name", name)
	form.Set("cores", strconv.Itoa(cores))
	form.Set("sockets", "1")
	form.Set("cpu", "host")
	// PVE-09: Specify ostype l26 (Linux 2.6/3.x/4.x/5.x/6.x) for kernel optimizations
	form.Set("ostype", "l26")
	form.Set("memory", strconv.Itoa(memoryMB))
	form.Set("scsihw", "virtio-scsi-single")
	form.Set("scsi0", fmt.Sprintf("%s:%d,ssd=1", storage, diskGB))

	// PVE-06: Use REST API standard ide2 parameter with media=cdrom instead of CLI alias cdrom.
	// Modern PVE API2 rejects the CLI-only 'cdrom' parameter.
	form.Set("ide2", fmt.Sprintf("%s,media=cdrom", iso))

	// PVE-02: Boot order must prioritize scsi0 before ide2 (order=scsi0;ide2).
	// On first boot, scsi0 is unformatted, so BIOS falls back to ide2 (Live ISO).
	// Once Talos installs to scsi0, subsequent reboots boot directly from scsi0 without Live ISO boot-loop.
	form.Set("boot", "order=scsi0;ide2")

	form.Set("agent", "enabled=1")
	form.Set("onboot", "1")

	netVal := "virtio,bridge=" + bridge
	if opts.MACAddr != "" {
		netVal = fmt.Sprintf("virtio=%s,bridge=%s", opts.MACAddr, bridge)
	}
	form.Set("net0", netVal)

	if shouldStart {
		form.Set("start", "1")
	}

	// Execute VM Creation
	var createResp struct {
		Data string `json:"data"` // UPID of task
	}

	endpoint := fmt.Sprintf("/nodes/%s/qemu", node)
	if err := c.postForm(ctx, endpoint, form, &createResp); err != nil {
		return nil, fmt.Errorf("failed to create VM %d: %w", vmid, err)
	}

	taskID := createResp.Data

	// Wait for creation task to complete (timeout: 90 seconds)
	if taskID != "" {
		if err := c.WaitForTask(ctx, taskID, 90*time.Second); err != nil {
			return nil, fmt.Errorf("VM %d creation task failed: %w", vmid, err)
		}
	}

	// PVE-08: Handle autostart cleanly without lock contention
	finalStatus := "created"
	if shouldStart {
		started := false
		// Poll for up to 10 seconds for VM to be in running state (handled by start=1 in Proxmox)
		for i := 0; i < 20; i++ {
			vmStatus, err := c.GetVMStatus(ctx, vmid)
			if err == nil && vmStatus.Status == "running" {
				started = true
				finalStatus = "running"
				break
			}

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}

		// If still stopped (e.g. start=1 was omitted or delayed), explicitly trigger start
		if !started {
			if startErr := c.StartVM(ctx, vmid); startErr == nil {
				finalStatus = "running"
			} else {
				log.Printf("[Proxmox] Note: explicit start for VM %d returned: %v", vmid, startErr)
			}
		}
	}

	return &CreateWorkerResult{
		VMID:    vmid,
		Name:    name,
		TaskID:  taskID,
		Status:  finalStatus,
		Message: fmt.Sprintf("Talos worker VM %d (%s) created successfully with %d vCPU, %dMB RAM, %dGB disk", vmid, name, cores, memoryMB, diskGB),
	}, nil
}

// SetDrainer registers a NodeDrainer (e.g. K8sManager) to drain workloads prior to VM deletion (PVE-07).
func (c *Client) SetDrainer(drainer NodeDrainer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drainer = drainer
}

// DeleteWorker stops and destroys the specified VM and its storage disks.
// PVE-01: Validates that the target VM is strictly a Talos worker node to prevent accidental deletion of Control Plane or host VMs.
// PVE-07: Cordons and drains the node in Kubernetes, then gracefully shuts down VM via ACPI before forcing stop.
func (c *Client) DeleteWorker(ctx context.Context, vmid int) error {
	if !c.IsConfigured() {
		return errors.New("proxmox client is not configured")
	}

	if vmid < 100 {
		return fmt.Errorf("invalid VMID %d: VMID must be >= 100", vmid)
	}

	node := c.cfg.Node

	// 1. Inspect current VM status
	vmStatus, err := c.GetVMStatus(ctx, vmid)
	if err != nil {
		return fmt.Errorf("failed to inspect VM %d before deletion: %w", vmid, err)
	}

	// PVE-01: Safety check: protect Control Plane, win11 and non-worker VMs
	if !isTalosWorker(vmStatus.Name) {
		return fmt.Errorf("safety check violation: VM %d (%q) is protected or not a Talos worker node; deletion aborted", vmid, vmStatus.Name)
	}

	// 2. Cordon & Drain node in Kubernetes before shutting down and deleting (PVE-07)
	c.mu.RLock()
	drainer := c.drainer
	c.mu.RUnlock()
	if drainer == nil {
		return errors.New("refusing to delete worker VM: Kubernetes drain service is unavailable")
	}
	log.Printf("[Proxmox] Cordoning and draining node %s before destroying VM %d (PVE-07)...", vmStatus.Name, vmid)
	drainCtx, drainCancel := context.WithTimeout(ctx, 45*time.Second)
	drainErr := drainer.CordonAndDrainNode(drainCtx, vmStatus.Name)
	drainCancel()
	if drainErr != nil {
		return fmt.Errorf("refusing to delete worker VM %d because Kubernetes drain failed: %w", vmid, drainErr)
	}

	// 3. If running, first attempt graceful ACPI shutdown (PVE-07)
	if vmStatus.Status == "running" {
		stopped := false

		// Attempt graceful ACPI shutdown
		if shutdownErr := c.ShutdownVM(ctx, vmid); shutdownErr == nil {
			// Wait up to 15 seconds for VM to gracefully halt
			for i := 0; i < 15; i++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(1 * time.Second):
				}

				st, err := c.GetVMStatus(ctx, vmid)
				if err == nil && st.Status == "stopped" {
					stopped = true
					break
				}
			}
		}

		// Fall back to hard stop if ACPI shutdown timed out or failed
		if !stopped {
			if err := c.StopVM(ctx, vmid); err != nil {
				return fmt.Errorf("failed to stop VM %d before deletion: %w", vmid, err)
			}

			// Wait up to 30 seconds for VM to halt
			for i := 0; i < 30; i++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(1 * time.Second):
				}

				st, err := c.GetVMStatus(ctx, vmid)
				if err == nil && st.Status == "stopped" {
					stopped = true
					break
				}
			}
			if !stopped {
				return fmt.Errorf("VM %d failed to stop within 30 seconds", vmid)
			}
		}
	}

	// 3. Delete VM with purge and destroy-unreferenced-disks
	deleteEndpoint := fmt.Sprintf("/nodes/%s/qemu/%d?purge=1&destroy-unreferenced-disks=1", node, vmid)
	var deleteResp struct {
		Data string `json:"data"` // UPID
	}

	if err := c.deleteJSON(ctx, deleteEndpoint, &deleteResp); err != nil {
		return fmt.Errorf("failed to delete VM %d: %w", vmid, err)
	}

	if deleteResp.Data != "" {
		if err := c.WaitForTask(ctx, deleteResp.Data, 60*time.Second); err != nil {
			return fmt.Errorf("delete task failed for VM %d: %w", vmid, err)
		}
	}

	return nil
}

// GetVMStatus retrieves the current runtime status of a VM.
func (c *Client) GetVMStatus(ctx context.Context, vmid int) (*VMStatus, error) {
	node := c.cfg.Node
	var resp struct {
		Data struct {
			VMID      int    `json:"vmid"`
			Name      string `json:"name"`
			Status    string `json:"status"`
			CPUs      int    `json:"cpus"`
			MaxMem    uint64 `json:"maxmem"`
			Uptime    int64  `json:"uptime"`
			NetIn     uint64 `json:"netin"`
			NetOut    uint64 `json:"netout"`
			DiskRead  uint64 `json:"diskread"`
			DiskWrite uint64 `json:"diskwrite"`
		} `json:"data"`
	}

	endpoint := fmt.Sprintf("/nodes/%s/qemu/%d/status/current", node, vmid)
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}

	return &VMStatus{
		VMID:    resp.Data.VMID,
		Name:    resp.Data.Name,
		Status:  resp.Data.Status,
		CPUs:    resp.Data.CPUs,
		Memory:  resp.Data.MaxMem,
		Uptime:  resp.Data.Uptime,
		NetIn:   resp.Data.NetIn,
		NetOut:  resp.Data.NetOut,
		DiskIn:  resp.Data.DiskRead,
		DiskOut: resp.Data.DiskWrite,
	}, nil
}

// ShutdownVM issues a graceful ACPI shutdown signal to a VM (PVE-07).
func (c *Client) ShutdownVM(ctx context.Context, vmid int) error {
	node := c.cfg.Node
	endpoint := fmt.Sprintf("/nodes/%s/qemu/%d/status/shutdown", node, vmid)
	var resp struct {
		Data string `json:"data"` // UPID
	}
	if err := c.postForm(ctx, endpoint, url.Values{}, &resp); err != nil {
		return fmt.Errorf("failed to shutdown VM %d: %w", vmid, err)
	}
	return nil
}

// StartVM issues a power on command to a VM.
func (c *Client) StartVM(ctx context.Context, vmid int) error {
	node := c.cfg.Node
	endpoint := fmt.Sprintf("/nodes/%s/qemu/%d/status/start", node, vmid)
	var resp struct {
		Data string `json:"data"` // UPID
	}
	if err := c.postForm(ctx, endpoint, url.Values{}, &resp); err != nil {
		return fmt.Errorf("failed to start VM %d: %w", vmid, err)
	}
	if resp.Data != "" {
		_ = c.WaitForTask(ctx, resp.Data, 30*time.Second)
	}
	return nil
}

// StopVM issues an immediate stop command to a VM.
func (c *Client) StopVM(ctx context.Context, vmid int) error {
	node := c.cfg.Node
	endpoint := fmt.Sprintf("/nodes/%s/qemu/%d/status/stop", node, vmid)
	var resp struct {
		Data string `json:"data"` // UPID
	}
	if err := c.postForm(ctx, endpoint, url.Values{}, &resp); err != nil {
		return fmt.Errorf("failed to stop VM %d: %w", vmid, err)
	}
	if resp.Data != "" {
		_ = c.WaitForTask(ctx, resp.Data, 30*time.Second)
	}
	return nil
}

// GetTaskStatus retrieves task state and completion status.
func (c *Client) GetTaskStatus(ctx context.Context, upid string) (*TaskStatus, error) {
	node := c.cfg.Node
	escapedUPID := url.PathEscape(upid)
	endpoint := fmt.Sprintf("/nodes/%s/tasks/%s/status", node, escapedUPID)

	var resp struct {
		Data struct {
			UPID       string `json:"upid"`
			Node       string `json:"node"`
			Type       string `json:"type"`
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		} `json:"data"`
	}

	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}

	return &TaskStatus{
		UPID:       resp.Data.UPID,
		Node:       resp.Data.Node,
		Type:       resp.Data.Type,
		Status:     resp.Data.Status,
		ExitStatus: resp.Data.ExitStatus,
	}, nil
}

// WaitForTask polls a task UPID until completed or until timeout occurs.
// TASK-01: Uses interruptible select on ctx.Done() rather than blocking time.Sleep.
func (c *Client) WaitForTask(ctx context.Context, upid string, timeout time.Duration) error {
	if upid == "" {
		return nil
	}

	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for proxmox task %s", upid)
		}

		task, err := c.GetTaskStatus(ctx, upid)
		if err != nil {
			// If temporary error, wait and retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
			continue
		}

		if task.Status == "stopped" {
			if task.ExitStatus == "OK" {
				return nil
			}
			return fmt.Errorf("task finished with error: %s", task.ExitStatus)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// authenticate acquires ticket and CSRF token when username/password is used.
// PVE-11: Validates against empty ticket response.
func (c *Client) authenticate(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if already valid
	if c.ticket != "" && time.Now().Before(c.ticketExpiresAt) {
		return nil
	}

	authEndpoint := c.cfg.BaseURL + "/api2/json/access/ticket"
	form := url.Values{}
	form.Set("username", c.cfg.Username)
	form.Set("password", c.cfg.Password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("auth failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var authResp struct {
		Data struct {
			Ticket              string `json:"ticket"`
			CSRFPreventionToken string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}

	if err := json.Unmarshal(bodyBytes, &authResp); err != nil {
		return fmt.Errorf("failed to parse auth response: %w", err)
	}

	// PVE-11: Explicitly verify ticket is non-empty
	if authResp.Data.Ticket == "" {
		return errors.New("authentication failed: Proxmox returned empty ticket (TFA/2FA or authentication failure)")
	}

	c.ticket = authResp.Data.Ticket
	c.csrfToken = authResp.Data.CSRFPreventionToken
	// Tickets usually expire in 2 hours; refresh slightly earlier
	c.ticketExpiresAt = time.Now().Add(100 * time.Minute)

	return nil
}

// doRequest prepares and executes an HTTP request with proper authorization.
// PVE-03: Refreshes session and retries request on HTTP 401/403.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body io.Reader, contentType string) (*http.Response, error) {
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	fullURL := c.cfg.BaseURL + "/api2/json" + endpoint

	// Buffer body to allow replay on re-authentication if necessary
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("failed to buffer request body: %w", err)
		}
	}

	createRequest := func() (*http.Request, error) {
		var r io.Reader
		if bodyBytes != nil {
			r = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, r)
		if err != nil {
			return nil, err
		}

		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Accept", "application/json")

		// Apply Authentication
		if c.cfg.APIToken != "" {
			token := c.cfg.APIToken
			if !strings.HasPrefix(token, "PVEAPIToken=") {
				token = "PVEAPIToken=" + token
			}
			req.Header.Set("Authorization", token)
		} else if c.cfg.Username != "" && c.cfg.Password != "" {
			c.mu.RLock()
			validTicket := c.ticket != "" && time.Now().Before(c.ticketExpiresAt)
			c.mu.RUnlock()

			if !validTicket {
				if err := c.authenticate(ctx); err != nil {
					return nil, fmt.Errorf("ticket authentication failed: %w", err)
				}
			}

			c.mu.RLock()
			req.AddCookie(&http.Cookie{
				Name:  "PVEAuthCookie",
				Value: c.ticket,
			})
			if method != http.MethodGet && method != http.MethodHead {
				req.Header.Set("CSRFPreventionToken", c.csrfToken)
			}
			c.mu.RUnlock()
		}
		return req, nil
	}

	req, err := createRequest()
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// PVE-03: If ticket-based auth receives 401 or 403, invalidate ticket and retry once
	if (c.cfg.APIToken == "" && c.cfg.Username != "" && c.cfg.Password != "") &&
		(resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		resp.Body.Close()

		c.mu.Lock()
		c.ticket = ""
		c.csrfToken = ""
		c.ticketExpiresAt = time.Time{}
		c.mu.Unlock()

		if err := c.authenticate(ctx); err != nil {
			return nil, fmt.Errorf("ticket re-authentication after %d failed: %w", resp.StatusCode, err)
		}

		retryReq, err := createRequest()
		if err != nil {
			return nil, err
		}
		return c.httpClient.Do(retryReq)
	}

	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target interface{}) error {
	resp, err := c.doRequest(ctx, http.MethodGet, endpoint, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("proxmox API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if err := json.Unmarshal(bodyBytes, target); err != nil {
		return fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return nil
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values, target interface{}) error {
	resp, err := c.doRequest(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("proxmox API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if target != nil && len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, target); err != nil {
			return fmt.Errorf("failed to parse JSON response: %w", err)
		}
	}

	return nil
}

func (c *Client) deleteJSON(ctx context.Context, endpoint string, target interface{}) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, endpoint, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("proxmox API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if target != nil && len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, target); err != nil {
			return fmt.Errorf("failed to parse JSON response: %w", err)
		}
	}

	return nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		val = strings.ToLower(strings.TrimSpace(val))
		return val == "true" || val == "1" || val == "yes"
	}
	return defaultVal
}
