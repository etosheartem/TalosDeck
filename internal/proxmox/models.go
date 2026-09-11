package proxmox

import "time"

// Config contains settings for connecting to the Proxmox VE API.
type Config struct {
	BaseURL        string        `json:"baseUrl"`        // e.g. "https://192.168.88.169:8006"
	Node           string        `json:"node"`           // e.g. "pve"
	SkipTLSVerify  bool          `json:"skipTlsVerify"`  // true for self-signed certificates (default: false)
	CACert         string        `json:"caCert,omitempty"`     // PEM-encoded CA certificate string
	CACertFile     string        `json:"caCertFile,omitempty"` // Path to custom CA certificate file
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
