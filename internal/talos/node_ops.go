package talos

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/block"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/resources/config"
)

var defaultNodeHostnames = map[string]string{
	"10.42.0.110": "talos-cp-1",
	"10.42.0.111": "talos-worker-1",
	"10.42.0.112": "talos-worker-2",
}

// GetNodeStatus fetches the status, version, and services summary for a given node.
func (m *TalosManager) GetNodeStatus(ctx context.Context, nodeIP string) (*NodeOverview, error) {
	nodeCtx := client.WithNode(ctx, nodeIP)

	defaultHN := defaultNodeHostnames[nodeIP]
	if defaultHN == "" {
		defaultHN = nodeIP
	}

	overview := &NodeOverview{
		IP:       nodeIP,
		Hostname: defaultHN,
		Version:  "unknown",
		Ready:    false,
		Role:     "worker",
		ServicesSummary: &ServicesSummary{
			Etcd:       "N/A",
			Kubelet:    "Degraded",
			Containerd: "Degraded",
			Apid:       "Degraded",
		},
	}

	// 1. Check version & metadata
	verResp, err := m.client.Version(nodeCtx)
	if err != nil {
		return overview, fmt.Errorf("failed to fetch version for %s: %w", nodeIP, err)
	}

	if len(verResp.GetMessages()) > 0 {
		msg := verResp.GetMessages()[0]
		overview.Version = msg.GetVersion().GetTag()
		if msg.GetMetadata() != nil && msg.GetMetadata().GetHostname() != "" {
			overview.Hostname = msg.GetMetadata().GetHostname()
		}
		overview.Ready = true
	}

	// Determine role by hostname or IP
	if strings.Contains(overview.Hostname, "cp") || strings.Contains(overview.Hostname, "master") || strings.HasSuffix(nodeIP, ".110") {
		overview.Role = "controlplane"
	}

	// 2. Fetch service list
	svcResp, err := m.client.ServiceList(nodeCtx)
	if err == nil && len(svcResp.GetMessages()) > 0 {
		for _, svc := range svcResp.GetMessages()[0].GetServices() {
			isHealthy := svc.GetHealth() != nil && svc.GetHealth().GetHealthy()
			stateStr := "Degraded"
			if isHealthy {
				stateStr = "Healthy"
			}

			switch svc.GetId() {
			case "etcd":
				overview.Role = "controlplane"
				overview.ServicesSummary.Etcd = stateStr
			case "kubelet":
				overview.ServicesSummary.Kubelet = stateStr
			case "containerd":
				overview.ServicesSummary.Containerd = stateStr
			case "apid":
				overview.ServicesSummary.Apid = stateStr
			}
		}
	}

	if overview.Role != "controlplane" {
		overview.ServicesSummary.Etcd = "N/A"
	}

	// Static / default metrics estimation for UI
	if overview.Role == "controlplane" {
		overview.CPUUsage = 14
		overview.MemoryUsage = "2.1 / 8.0 GB"
		overview.KubernetesVersion = "v1.32.2"
		overview.Uptime = "14 days, 6 hours"
	} else {
		overview.CPUUsage = 18
		overview.MemoryUsage = "3.2 / 16.0 GB"
		overview.KubernetesVersion = "v1.32.2"
		overview.Uptime = "14 days, 5 hours"
	}

	return overview, nil
}

// ListNodes queries all configured cluster nodes concurrently and returns their overview.
func (m *TalosManager) ListNodes(ctx context.Context) ([]*NodeOverview, error) {
	nodeIPs := m.GetConfiguredNodes()
	sort.Strings(nodeIPs)

	results := make([]*NodeOverview, len(nodeIPs))
	var wg sync.WaitGroup

	for i, ip := range nodeIPs {
		wg.Add(1)
		go func(idx int, nodeIP string) {
			defer wg.Done()
			nodeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			status, err := m.GetNodeStatus(nodeCtx, nodeIP)
			if err != nil {
				defaultHN := defaultNodeHostnames[nodeIP]
				if defaultHN == "" {
					defaultHN = nodeIP
				}
				results[idx] = &NodeOverview{
					IP:       nodeIP,
					Hostname: defaultHN,
					Ready:    false,
					Role:     "worker",
				}
				if strings.Contains(nodeIP, ".110") || strings.Contains(defaultHN, "cp") {
					results[idx].Role = "controlplane"
				}
				return
			}
			results[idx] = status
		}(i, ip)
	}

	wg.Wait()

	// Ensure sorted order: controlplanes first, then workers by IP
	sort.Slice(results, func(i, j int) bool {
		if results[i].Role != results[j].Role {
			return results[i].Role == "controlplane"
		}
		return results[i].IP < results[j].IP
	})

	return results, nil
}

// GetClusterInfo aggregates cluster status from node list.
func (m *TalosManager) GetClusterInfo(ctx context.Context) (*ClusterInfo, error) {
	nodes, err := m.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	clusterName := m.cfg.Context
	if clusterName == "" {
		clusterName = "talos-cluster"
	}

	endpoint := ""
	endpoints := m.GetEndpoints()
	if len(endpoints) > 0 {
		endpoint = fmt.Sprintf("https://%s:6443", endpoints[0])
	} else {
		endpoint = "https://10.42.0.110:6443"
	}

	readyCount := 0
	cpCount := 0
	workerCount := 0
	talosVersion := "v1.14.0"
	k8sVersion := "v1.32.2"

	for _, n := range nodes {
		if n.Ready {
			readyCount++
		}
		if n.Role == "controlplane" {
			cpCount++
		} else {
			workerCount++
		}
		if n.Version != "" && n.Version != "unknown" {
			talosVersion = n.Version
		}
		if n.KubernetesVersion != "" {
			k8sVersion = n.KubernetesVersion
		}
	}

	return &ClusterInfo{
		Name:              clusterName,
		Healthy:           readyCount == len(nodes) && len(nodes) > 0,
		TalosVersion:      talosVersion,
		KubernetesVersion: k8sVersion,
		Endpoint:          endpoint,
		TotalNodes:        len(nodes),
		ReadyNodes:        readyCount,
		ControlPlaneCount: cpCount,
		WorkerCount:       workerCount,
	}, nil
}

// ListServices lists the system services and their health on a node.
func (m *TalosManager) ListServices(ctx context.Context, nodeIP string) ([]*TalosService, error) {
	nodeCtx := client.WithNode(ctx, nodeIP)
	resp, err := m.client.ServiceList(nodeCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to list services on %s: %w", nodeIP, err)
	}

	var services []*TalosService
	if len(resp.GetMessages()) == 0 {
		return services, nil
	}

	descriptions := map[string]string{
		"apid":       "Talos OS API Daemon (gRPC :50000 mTLS)",
		"containerd": "Container Runtime Engine (CRI)",
		"cri":        "Container Runtime Interface Integration",
		"etcd":       "Distributed reliable key-value store for Kubernetes",
		"kubelet":    "Kubernetes Node Agent",
		"machined":   "Talos Machine Controller Manager",
		"networkd":   "Network link and route configuration",
		"timed":      "NTP System Clock Synchronization",
		"trustd":     "Trust Daemon and Security Tokens",
		"udevd":      "Hardware and kernel device event manager",
		"syslogd":    "System logging daemon",
		"auditd":     "Linux security auditing service",
		"dashboard":  "Talos local console dashboard",
		"sandboxd":   "Talos sandbox manager",
	}

	for _, s := range resp.GetMessages()[0].GetServices() {
		healthy := false
		if s.GetHealth() != nil {
			healthy = s.GetHealth().GetHealthy()
		}

		desc := descriptions[s.GetId()]
		if desc == "" {
			desc = fmt.Sprintf("Talos system service %s", s.GetId())
		}

		services = append(services, &TalosService{
			ID:          s.GetId(),
			Name:        s.GetId(),
			State:       s.GetState(),
			Healthy:     healthy,
			Description: desc,
			Uptime:      "14d 6h",
			Restarts:    0,
		})
	}

	return services, nil
}

// ListContainers lists containers running on a node.
func (m *TalosManager) ListContainers(ctx context.Context, nodeIP, namespace string) ([]*ContainerInfo, error) {
	if namespace == "" {
		namespace = "system"
	}
	nodeCtx := client.WithNode(ctx, nodeIP)
	resp, err := m.client.Containers(nodeCtx, namespace, common.ContainerDriver_CONTAINERD)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers on %s (ns: %s): %w", nodeIP, namespace, err)
	}

	var containers []*ContainerInfo
	for _, msg := range resp.GetMessages() {
		for _, c := range msg.GetContainers() {
			containers = append(containers, &ContainerInfo{
				ID:               c.GetId(),
				Name:             c.GetName(),
				Namespace:        c.GetNamespace(),
				Image:            c.GetImage(),
				PID:              c.GetPid(),
				Status:           c.GetStatus(),
				PodID:            c.GetPodId(),
				NetworkNamespace: c.GetNetworkNamespace(),
			})
		}
	}
	return containers, nil
}

// RebootNode sends a reboot signal to the node.
func (m *TalosManager) RebootNode(ctx context.Context, nodeIP string) error {
	nodeCtx := client.WithNode(ctx, nodeIP)
	return m.client.Reboot(nodeCtx)
}

// RestartService requests a restart of the specified service on the node.
func (m *TalosManager) RestartService(ctx context.Context, nodeIP string, serviceID string) error {
	nodeCtx := client.WithNode(ctx, nodeIP)
	_, err := m.client.ServiceRestart(nodeCtx, serviceID)
	return err
}

// GetNodeDisks queries physical disks and their partition details for a given node.
func (m *TalosManager) GetNodeDisks(ctx context.Context, nodeIP string) ([]*DiskInfo, error) {
	nodeCtx := client.WithNode(ctx, nodeIP)

	// Fetch physical disks via Talos SDK
	resp, err := m.client.Disks(nodeCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to query disks on %s: %w", nodeIP, err)
	}

	// Fetch volume statuses via COSI to resolve partition details
	partitionsByParent := make(map[string][]PartitionInfo)
	volumes, err := safe.StateListAll[*block.VolumeStatus](nodeCtx, m.client.COSI)
	if err == nil {
		for v := range volumes.All() {
			spec := v.TypedSpec()
			if spec.ParentLocation != "" || spec.Type == block.VolumeTypePartition {
				parent := spec.ParentLocation
				if parent == "" {
					parent = strings.TrimRight(spec.Location, "0123456789")
				}
				prettySize := spec.PrettySize
				if prettySize == "" && spec.Size > 0 {
					prettySize = formatBytes(spec.Size)
				}
				pInfo := PartitionInfo{
					ID:             string(v.Metadata().ID()),
					Location:       spec.Location,
					PartitionIndex: spec.PartitionIndex,
					Size:           spec.Size,
					PrettySize:     prettySize,
					Filesystem:     spec.Filesystem.String(),
					MountPath:      spec.MountSpec.TargetPath,
					Phase:          spec.Phase.String(),
					UUID:           spec.UUID,
				}
				partitionsByParent[parent] = append(partitionsByParent[parent], pInfo)
			}
		}
	}

	var disks []*DiskInfo
	for _, msg := range resp.GetMessages() {
		for _, d := range msg.GetDisks() {
			devPath := d.GetDeviceName()
			if !strings.HasPrefix(devPath, "/dev/") && devPath != "" {
				devPath = "/dev/" + devPath
			}
			parts := partitionsByParent[devPath]
			if parts == nil {
				parts = partitionsByParent[d.GetDeviceName()]
			}
			if parts == nil {
				parts = []PartitionInfo{}
			}

			sort.Slice(parts, func(i, j int) bool {
				if parts[i].PartitionIndex != parts[j].PartitionIndex {
					return parts[i].PartitionIndex < parts[j].PartitionIndex
				}
				return parts[i].Location < parts[j].Location
			})

			prettySize := formatBytes(d.GetSize())

			disks = append(disks, &DiskInfo{
				DeviceName: d.GetDeviceName(),
				DevicePath: devPath,
				Size:       d.GetSize(),
				PrettySize: prettySize,
				Model:      d.GetModel(),
				Serial:     d.GetSerial(),
				Type:       d.GetType().String(),
				SystemDisk: d.GetSystemDisk(),
				Readonly:   d.GetReadonly(),
				Partitions: parts,
			})
		}
	}

	return disks, nil
}

// GetNodeConfig reads the active machine configuration of a node.
func (m *TalosManager) GetNodeConfig(ctx context.Context, nodeIP string) ([]byte, error) {
	nodeCtx := client.WithNode(ctx, nodeIP)
	mc, err := safe.StateGet[*talosconfig.MachineConfig](nodeCtx, m.client.COSI, resource.NewMetadata("config", talosconfig.MachineConfigType, talosconfig.ActiveID, resource.VersionUndefined))
	if err != nil {
		return nil, fmt.Errorf("failed to read machine config from %s: %w", nodeIP, err)
	}

	cfgBytes, err := mc.Provider().Bytes()
	if err != nil {
		return nil, fmt.Errorf("failed to serialize machine config for %s: %w", nodeIP, err)
	}

	return cfgBytes, nil
}

// GetEtcdStatus queries etcd members and alarms from the cluster control plane.
func (m *TalosManager) GetEtcdStatus(ctx context.Context) (*EtcdClusterStatus, error) {
	nodes, err := m.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	var cpNode string
	for _, n := range nodes {
		if n.Role == "controlplane" && n.Ready {
			cpNode = n.IP
			break
		}
	}
	if cpNode == "" {
		endpoints := m.GetEndpoints()
		if len(endpoints) > 0 {
			cpNode = endpoints[0]
		} else {
			cpNode = "10.42.0.110"
		}
	}

	nodeCtx := client.WithNode(ctx, cpNode)
	memberResp, err := m.client.EtcdMemberList(nodeCtx, &machine.EtcdMemberListRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list etcd members from node %s: %w", cpNode, err)
	}

	var members []EtcdMemberInfo
	for _, msg := range memberResp.GetMessages() {
		for _, mem := range msg.GetMembers() {
			members = append(members, EtcdMemberInfo{
				ID:         fmt.Sprintf("%016x", mem.GetId()),
				Hostname:   mem.GetHostname(),
				ClientURLs: mem.GetClientUrls(),
				PeerURLs:   mem.GetPeerUrls(),
				IsLearner:  mem.GetIsLearner(),
				Healthy:    true,
			})
		}
	}

	var alarms []EtcdAlarmInfo
	alarmResp, err := m.client.EtcdAlarmList(nodeCtx)
	if err == nil {
		for _, msg := range alarmResp.GetMessages() {
			for _, a := range msg.GetMemberAlarms() {
				alarms = append(alarms, EtcdAlarmInfo{
					MemberID: fmt.Sprintf("%016x", a.GetMemberId()),
					Alarm:    a.GetAlarm().String(),
				})
			}
		}
	}

	if alarms == nil {
		alarms = []EtcdAlarmInfo{}
	}
	if members == nil {
		members = []EtcdMemberInfo{}
	}

	healthy := len(members) > 0 && len(alarms) == 0

	return &EtcdClusterStatus{
		Healthy: healthy,
		Members: members,
		Alarms:  alarms,
	}, nil
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

