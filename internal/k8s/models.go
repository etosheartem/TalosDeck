package k8s

import "time"

// PodInfo represents a single Kubernetes pod workload summary.
type PodInfo struct {
	Name            string    `json:"name"`
	Namespace       string    `json:"namespace"`
	Node            string    `json:"node"`
	NodeIP          string    `json:"nodeIp,omitempty"`
	Status          string    `json:"status"` // "Running", "Pending", "CrashLoopBackOff", etc.
	Phase           string    `json:"phase"`  // Raw phase: "Running", "Failed", etc.
	ContainerCount  string    `json:"containerCount"` // e.g. "1/1"
	ReadyContainers int       `json:"readyContainers"`
	TotalContainers int       `json:"totalContainers"`
	Restarts        int32     `json:"restarts"`
	Age             string    `json:"age"` // e.g. "36m", "2h", "5d"
	CreatedAt       time.Time `json:"createdAt"`
	PodIP           string    `json:"podIp,omitempty"`
}

// NamespaceInfo represents a Kubernetes namespace.
type NamespaceInfo struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"` // "Active", "Terminating"
	Age       string    `json:"age"`
	CreatedAt time.Time `json:"createdAt"`
}
