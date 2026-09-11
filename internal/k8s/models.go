package k8s

import "time"

// PodInfo represents a single Kubernetes pod workload summary.
type PodInfo struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Namespace       string    `json:"namespace"`
	Node            string    `json:"node"`
	NodeName        string    `json:"nodeName"`
	NodeIP          string    `json:"nodeIP"`
	Status          string    `json:"status"` // "Running", "Pending", "CrashLoopBackOff", "Terminating", "OOMKilled", etc.
	Phase           string    `json:"phase"`  // Raw phase: "Running", "Failed", etc.
	ContainerCount  string    `json:"containerCount"` // e.g. "1/1"
	ReadyContainers string    `json:"readyContainers"` // e.g. "1/1" for frontend compatibility
	ReadyCount      int       `json:"readyCount"`
	TotalContainers int       `json:"totalContainers"`
	Restarts        int32     `json:"restarts"`
	Age             string    `json:"age"` // e.g. "36m", "2h", "5d"
	CreatedAt       time.Time `json:"createdAt"`
	PodIP           string    `json:"podIp,omitempty"`
	IP              string    `json:"ip"`
}

// NamespaceInfo represents a Kubernetes namespace.
type NamespaceInfo struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"` // "Active", "Terminating"
	Age       string    `json:"age"`
	CreatedAt time.Time `json:"createdAt"`
}
