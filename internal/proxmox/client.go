package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config contains settings for connecting to the Proxmox VE API.
type Config struct {
	BaseURL        string        `json:"baseUrl"`        // e.g. "https://192.168.88.169:8006"
	Node           string        `json:"node"`           // e.g. "pve"
	SkipTLSVerify  bool          `json:"skipTlsVerify"`  // true for self-signed certificates
	APIToken       string        `json:"apiToken"`       // "PVEAPIToken=USER@REALM!TOKENID=UUID" or "USER@REALM!TOKENID=UUID"
	Username       string        `json:"username"`       // e.g. "root@pam"
	Password       string        `json:"password"`       // e.g. "secret"
	DefaultStorage string        `json:"defaultStorage"` // e.g. "local-lvm"
	DefaultISO     string        `json:"defaultIso"`     // e.g. "data:iso/talos-v1.14.0-qemu-guest-agent.iso"
	DefaultBridge  string        `json:"defaultBridge"`  // e.g. "vmbr0"
	Timeout        time.Duration `json:"timeout"`
}

// MemoryStatus represents Proxmox host RAM statistics.
type MemoryStatus struct {
	Total        uint64  `json:"total"`        // Total memory in bytes
	Used         uint64  `json:"used"`         // Used memory in bytes
	Free         uint64  `json:"free"`         // Free memory in bytes
	Available    uint64  `json:"available"`    // Available memory in bytes
	UsagePercent float64 `json:"usagePercent"` // Usage in percentage (0 - 100)
}

// StorageStatus represents Proxmox storage pool statistics.
type StorageStatus struct {
	Name         string  `json:"name"`         // Storage ID (e.g. "local-lvm")
	Total        uint64  `json:"total"`        // Total storage in bytes
	Used         uint64  `json:"used"`         // Used storage in bytes
	Free         uint64  `json:"free"`         // Available/free storage in bytes
	UsagePercent float64 `json:"usagePercent"` // Usage in percentage (0 - 100)
	Type         string  `json:"type"`         // Storage type (e.g. "lvmthin")
}

// NodeStatus represents the overall host resource summary from Proxmox VE.
type NodeStatus struct {
	Node            string        `json:"node"`
	Uptime          int64         `json:"uptime"`
	CPU             float64       `json:"cpu"`             // CPU usage (0.0 to 1.0)
	CPUUsagePercent float64       `json:"cpuUsagePercent"` // CPU usage percentage (0 - 100)
	CPUCores        int           `json:"cpuCores"`
	CPUModel        string        `json:"cpuModel"`
	Memory          MemoryStatus  `json:"memory"`
	Storage         StorageStatus `json:"storage"`
}

// VMStatus represents current status of a QEMU VM.
type VMStatus struct {
	VMID    int    `json:"vmid"`
	Name    string `json:"name"`
	Status  string `json:"status"` // "running", "stopped"
	CPUs    int    `json:"cpus"`
	Memory  uint64 `json:"memory"`
	Uptime  int64  `json:"uptime"`
	NetIn   uint64 `json:"netin"`
	NetOut  uint64 `json:"netout"`
	DiskIn  uint64 `json:"diskread"`
	DiskOut uint64 `json:"diskwrite"`
}

// TaskStatus represents the status of an asynchronous Proxmox task.
type TaskStatus struct {
	UPID       string `json:"upid"`
	Node       string `json:"node"`
	Type       string `json:"type"`
	Status     string `json:"status"`     // "running", "stopped"
	ExitStatus string `json:"exitstatus"` // "OK" or error message
}

// CreateWorkerOpts defines options for creating a new Talos worker VM.
type CreateWorkerOpts struct {
	VMID     int    `json:"vmid,omitempty"`     // Target VMID (if <= 0, auto-allocated via GetNextVMID)
	Name     string `json:"name,omitempty"`     // VM name (defaults to "talos-worker-{vmid}")
	Cores    int    `json:"cores,omitempty"`    // vCPU count (defaults to 2)
	MemoryMB int    `json:"memoryMB,omitempty"` // Memory in MB (defaults to 3072 = 3GB)
	DiskGB   int    `json:"diskGB,omitempty"`   // Disk size in GB (defaults to 30 = 30GB)
	Storage  string `json:"storage,omitempty"`  // Target storage (defaults to "local-lvm")
	ISO      string `json:"iso,omitempty"`      // ISO path (defaults to "data:iso/talos-v1.14.0-qemu-guest-agent.iso")
	Bridge   string `json:"bridge,omitempty"`   // Network bridge (defaults to "vmbr0")
	MACAddr  string `json:"macAddr,omitempty"`  // Optional MAC address
	Start    *bool  `json:"start,omitempty"`    // Whether to start the VM after creation (defaults to true)
}

// CreateWorkerResult represents the outcome of worker creation.
type CreateWorkerResult struct {
	VMID    int    `json:"vmid"`
	Name    string `json:"name"`
	TaskID  string `json:"taskId,omitempty"`
	Status  string `json:"status"` // "running", "created"
	Message string `json:"message"`
}

// Client is an HTTP client for communicating with the Proxmox VE REST API.
type Client struct {
	cfg        Config
	httpClient *http.Client

	mu              sync.RWMutex
	ticket          string
	csrfToken       string
	ticketExpiresAt time.Time
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

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.SkipTLSVerify,
		},
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
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
	skipTLS := getEnvBool("PROXMOX_SKIP_TLS_VERIFY", getEnvBool("PVE_SKIP_TLS_VERIFY", true))

	// Fallback to cluster-config/proxmox.token if token not provided via env
	if apiToken == "" && username == "" {
		tokenPaths := []string{
			"/home/artem/laba-kuber/cluster-config/proxmox.token",
			"cluster-config/proxmox.token",
			"proxmox.token",
		}
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

// GetNextVMID gets the next free VMID from Proxmox cluster.
func (c *Client) GetNextVMID(ctx context.Context) (int, error) {
	if !c.IsConfigured() {
		return 0, errors.New("proxmox client is not configured")
	}

	var resp struct {
		Data json.RawMessage `json:"data"`
	}

	if err := c.getJSON(ctx, "/cluster/nextid", &resp); err != nil {
		return 0, fmt.Errorf("failed to get next VMID from proxmox: %w", err)
	}

	// Proxmox can return string "100" or number 100
	var strVal string
	if err := json.Unmarshal(resp.Data, &strVal); err == nil {
		vmid, err := strconv.Atoi(strVal)
		if err != nil {
			return 0, fmt.Errorf("unexpected non-numeric VMID string: %s", strVal)
		}
		return vmid, nil
	}

	var numVal int
	if err := json.Unmarshal(resp.Data, &numVal); err == nil {
		return numVal, nil
	}

	return 0, fmt.Errorf("unable to decode nextid response: %s", string(resp.Data))
}

// CreateTalosWorker creates a new worker VM, attaches Talos ISO, configures resources, and starts the VM.
func (c *Client) CreateTalosWorker(ctx context.Context, opts CreateWorkerOpts) (*CreateWorkerResult, error) {
	if !c.IsConfigured() {
		return nil, errors.New("proxmox client is not configured")
	}

	node := c.cfg.Node

	// Allocate VMID if not specified
	vmid := opts.VMID
	if vmid <= 0 {
		var err error
		vmid, err = c.GetNextVMID(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to auto-allocate VMID: %w", err)
		}
	}

	// Apply sensible defaults matching TalosDeck worker template
	name := opts.Name
	if name == "" {
		name = fmt.Sprintf("talos-worker-%d", vmid)
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
	form.Set("memory", strconv.Itoa(memoryMB))
	form.Set("scsihw", "virtio-scsi-single")
	form.Set("scsi0", fmt.Sprintf("%s:%d,ssd=1", storage, diskGB))
	form.Set("cdrom", iso)
	form.Set("boot", "order=ide2;scsi0")
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

	// If start was requested, ensure the VM is active
	finalStatus := "created"
	if shouldStart {
		vmStatus, err := c.GetVMStatus(ctx, vmid)
		if err == nil && vmStatus.Status == "running" {
			finalStatus = "running"
		} else {
			// Explicit start call if start=1 was delayed or didn't auto-start
			if startErr := c.StartVM(ctx, vmid); startErr == nil {
				finalStatus = "running"
			} else {
				finalStatus = "created"
			}
		}
	}

	return &CreateWorkerResult{
		VMID:    vmid,
		Name:    name,
		TaskID:  taskID,
		Status:  finalStatus,
		Message: fmt.Sprintf("Talos worker VM %d (%s) created successfully with 2 vCPU, 3GB RAM, 30GB disk", vmid, name),
	}, nil
}

// DeleteWorker stops and destroys the specified VM and its storage disks.
func (c *Client) DeleteWorker(ctx context.Context, vmid int) error {
	if !c.IsConfigured() {
		return errors.New("proxmox client is not configured")
	}

	node := c.cfg.Node

	// 1. Inspect current VM status
	vmStatus, err := c.GetVMStatus(ctx, vmid)
	if err != nil {
		return fmt.Errorf("failed to inspect VM %d before deletion: %w", vmid, err)
	}

	// 2. If running, stop VM and wait until stopped
	if vmStatus.Status == "running" {
		if err := c.StopVM(ctx, vmid); err != nil {
			return fmt.Errorf("failed to stop VM %d before deletion: %w", vmid, err)
		}

		// Wait up to 30 seconds for VM to halt
		stopped := false
		for i := 0; i < 30; i++ {
			time.Sleep(1 * time.Second)
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
			time.Sleep(1 * time.Second)
			continue
		}

		if task.Status == "stopped" {
			if task.ExitStatus == "OK" {
				return nil
			}
			return fmt.Errorf("task finished with error: %s", task.ExitStatus)
		}

		time.Sleep(1 * time.Second)
	}
}

// authenticate acquires ticket and CSRF token when username/password is used.
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

	c.ticket = authResp.Data.Ticket
	c.csrfToken = authResp.Data.CSRFPreventionToken
	// Tickets usually expire in 2 hours; refresh slightly earlier
	c.ticketExpiresAt = time.Now().Add(100 * time.Minute)

	return nil
}

// doRequest prepares and executes an HTTP request with proper authorization.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body io.Reader, contentType string) (*http.Response, error) {
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	fullURL := c.cfg.BaseURL + "/api2/json" + endpoint

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
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
