package talos

// NodeOverview represents the summary of a single Talos node.
type NodeOverview struct {
	IP                string           `json:"ip"`
	Hostname          string           `json:"hostname"`
	Version           string           `json:"version"`
	Ready             bool             `json:"ready"`
	Role              string           `json:"role"` // "controlplane" or "worker"
	Uptime            string           `json:"uptime,omitempty"`
	CPUUsage          int              `json:"cpuUsage,omitempty"`
	MemoryUsage       string           `json:"memoryUsage,omitempty"`
	KubernetesVersion string           `json:"kubernetesVersion,omitempty"`
	ServicesSummary   *ServicesSummary `json:"servicesSummary,omitempty"`
}

// ServicesSummary summarizes the health of core services.
type ServicesSummary struct {
	Etcd       string `json:"etcd"`       // "Healthy", "Degraded", or "N/A"
	Kubelet    string `json:"kubelet"`    // "Healthy", "Degraded"
	Containerd string `json:"containerd"` // "Healthy", "Degraded"
	Apid       string `json:"apid"`       // "Healthy", "Degraded"
}

// ClusterInfo represents an overview of the Talos/K8s cluster.
type ClusterInfo struct {
	Name              string `json:"name"`
	Healthy           bool   `json:"healthy"`
	TalosVersion      string `json:"talosVersion"`
	KubernetesVersion string `json:"kubernetesVersion"`
	Endpoint          string `json:"endpoint"`
	TotalNodes        int    `json:"totalNodes"`
	ReadyNodes        int    `json:"readyNodes"`
	ControlPlaneCount int    `json:"controlPlaneCount"`
	WorkerCount       int    `json:"workerCount"`
}

// TalosService represents an OS or system service on a node.
type TalosService struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Healthy     bool   `json:"healthy"`
	Description string `json:"description"`
	Uptime      string `json:"uptime,omitempty"`
	Restarts    int    `json:"restarts"`
}

// ContainerInfo represents a running container on a node.
type ContainerInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Namespace        string `json:"namespace"`
	Image            string `json:"image"`
	PID              uint32 `json:"pid"`
	Status           string `json:"status"`
	PodID            string `json:"podId,omitempty"`
	NetworkNamespace string `json:"networkNamespace,omitempty"`
}

// DiskInfo represents physical disk and its partitions.
type DiskInfo struct {
	DeviceName string          `json:"deviceName"`
	DevicePath string          `json:"devicePath"`
	Size       uint64          `json:"size"`
	PrettySize string          `json:"prettySize"`
	Model      string          `json:"model"`
	Serial     string          `json:"serial"`
	Bus        string          `json:"bus,omitempty"`
	Type       string          `json:"type"`
	SystemDisk bool            `json:"systemDisk"`
	Readonly   bool            `json:"readonly"`
	Partitions []PartitionInfo `json:"partitions"`
}

// PartitionInfo represents a partition on a physical disk.
type PartitionInfo struct {
	ID             string `json:"id"`
	Location       string `json:"location"`
	PartitionIndex int    `json:"partitionIndex"`
	Size           uint64 `json:"size"`
	PrettySize     string `json:"prettySize"`
	Filesystem     string `json:"filesystem"`
	MountPath      string `json:"mountPath"`
	Used           string `json:"used,omitempty"`
	UsedBytes      uint64 `json:"usedBytes,omitempty"`
	UsedPercent    int    `json:"usedPercent,omitempty"`
	Phase          string `json:"phase"`
	UUID           string `json:"uuid,omitempty"`
}

// EtcdClusterStatus represents the health and topology of the etcd cluster.
type EtcdClusterStatus struct {
	Healthy     bool             `json:"healthy"`
	Members     []EtcdMemberInfo `json:"members"`
	Alarms      []EtcdAlarmInfo  `json:"alarms"`
	Errors      []string         `json:"errors,omitempty"`
	LeaderID    string           `json:"leaderId,omitempty"`
	LeaderName  string           `json:"leaderName,omitempty"`
	TotalDBSize string           `json:"totalDbSize,omitempty"`
	RaftTerm    uint64           `json:"raftTerm,omitempty"`
	RaftIndex   uint64           `json:"raftIndex,omitempty"`
}

// EtcdMemberInfo represents a member in the etcd cluster.
type EtcdMemberInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Hostname   string   `json:"hostname"`
	ClientURLs []string `json:"clientUrls"`
	PeerURLs   []string `json:"peerUrls"`
	IsLearner  bool     `json:"isLearner"`
	Healthy    bool     `json:"healthy"`
	Leader     bool     `json:"leader"`
	DBSize     string   `json:"dbSize,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// EtcdAlarmInfo represents an active alarm in etcd.
type EtcdAlarmInfo struct {
	MemberID string `json:"memberId"`
	Alarm    string `json:"alarm"`
}
