package talos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/block"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/resources/config"
	talosk8s "github.com/siderolabs/talos/pkg/machinery/resources/k8s"
	"github.com/siderolabs/talos/pkg/machinery/resources/perf"
)

type cpuSnapshot struct {
	busy  float64
	total float64
	usage int
}

func (m *TalosManager) calculateCPUUsage(nodeIP string, busy, total float64) int {
	m.metricsMu.Lock()
	defer m.metricsMu.Unlock()

	if m.cpuSamples == nil {
		m.cpuSamples = make(map[string]cpuSnapshot)
	}

	usage := 0
	previous, hasPrevious := m.cpuSamples[nodeIP]
	if hasPrevious {
		usage = previous.usage
		if total > previous.total && busy >= previous.busy {
			usage = int(((busy - previous.busy) / (total - previous.total)) * 100)
		}
	} else if total > 0 {
		// On the first sample, use the counters accumulated since boot. Later
		// samples use deltas between polls for a responsive dashboard value.
		usage = int((busy / total) * 100)
	}

	if usage < 0 {
		usage = 0
	} else if usage > 100 {
		usage = 100
	}

	m.cpuSamples[nodeIP] = cpuSnapshot{busy: busy, total: total, usage: usage}

	return usage
}

// GetNodeStatus fetches the runtime status, version, and health of a single node.
func (m *TalosManager) GetNodeStatus(ctx context.Context, nodeIP string) (*NodeOverview, error) {
	talosClient := m.GetClient()
	if talosClient == nil {
		return nil, errors.New("talos client is not initialized")
	}

	nodeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	nodeCtx = client.WithNode(nodeCtx, nodeIP)

	overview := newUnreachableNode(nodeIP)

	// 1. Check version & metadata
	verResp, err := talosClient.Version(nodeCtx)
	if err != nil {
		return overview, fmt.Errorf("failed to fetch version for %s: %w", nodeIP, err)
	}

	if len(verResp.GetMessages()) > 0 {
		msg := verResp.GetMessages()[0]
		if msg.GetVersion() != nil {
			overview.Version = msg.GetVersion().GetTag()
		}
		if msg.GetMetadata() != nil && msg.GetMetadata().GetHostname() != "" {
			overview.Hostname = msg.GetMetadata().GetHostname()
		}
	}

	// Role comes from the authoritative MachineType resource. Hostname/IP
	// heuristics misclassify any worker whose name merely contains "cp".
	if mt, err := safe.StateGet[*talosconfig.MachineType](nodeCtx, talosClient.COSI, resource.NewMetadata(talosconfig.NamespaceName, talosconfig.MachineTypeType, talosconfig.MachineTypeID, resource.VersionUndefined)); err == nil && mt != nil {
		if mt.MachineType().IsControlPlane() {
			overview.Role = "controlplane"
		} else {
			overview.Role = "worker"
		}
	}

	// 2. Fetch service list (handle all chunked messages)
	svcResp, err := talosClient.ServiceList(nodeCtx)
	serviceListOK := err == nil
	if err == nil {
		for _, msg := range svcResp.GetMessages() {
			for _, svc := range msg.GetServices() {
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
	}

	if overview.Role != "controlplane" {
		overview.ServicesSummary.Etcd = "N/A"
	}

	// A reachable Talos API only proves apid is up. Readiness must also require the
	// services that actually run workloads, otherwise a node with a dead kubelet
	// reports green. An unreadable service list leaves readiness unproven.
	overview.Ready = serviceListOK &&
		overview.ServicesSummary.Kubelet == "Healthy" &&
		overview.ServicesSummary.Containerd == "Healthy" &&
		overview.ServicesSummary.Apid == "Healthy"
	if overview.Role == "controlplane" {
		overview.Ready = overview.Ready && overview.ServicesSummary.Etcd == "Healthy"
	}

	// Kubernetes may still mark the node NotReady (CNI down, disk pressure) while
	// every Talos service is healthy, so honour its verdict when it is available.
	if nsList, err := safe.StateListAll[*talosk8s.NodeStatus](nodeCtx, talosClient.COSI); err == nil {
		for ns := range nsList.All() {
			if ns == nil || ns.TypedSpec() == nil {
				continue
			}
			if ns.TypedSpec().Nodename != "" {
				overview.Hostname = ns.TypedSpec().Nodename
			}
			overview.Ready = overview.Ready && ns.TypedSpec().NodeReady
			break
		}
	}

	// TALOS-18: Query real node runtime metrics (Memory, CPU, Uptime, Kubernetes version) dynamically
	// 1. Real Memory from Talos machine Memory API
	if memResp, err := talosClient.Memory(nodeCtx); err == nil && len(memResp.GetMessages()) > 0 {
		if meminfo := memResp.GetMessages()[0].GetMeminfo(); meminfo != nil {
			totalBytes := meminfo.GetMemtotal() * 1024
			availBytes := meminfo.GetMemavailable() * 1024
			if availBytes == 0 && meminfo.GetMemfree() > 0 {
				availBytes = meminfo.GetMemfree() * 1024
			}
			// These are unsigned: a partial or transitional MemInfo where available
			// exceeds total would otherwise underflow to exabytes of "used" memory.
			if availBytes > totalBytes {
				availBytes = totalBytes
			}
			if totalBytes > 0 {
				usedBytes := totalBytes - availBytes
				totalGB := float64(totalBytes) / (1024 * 1024 * 1024)
				usedGB := float64(usedBytes) / (1024 * 1024 * 1024)
				overview.MemoryUsage = fmt.Sprintf("%.1f / %.1f GB", usedGB, totalGB)
			}
		}
	}

	// 2. Real CPU usage from COSI perf.CPU
	if cpuRes, err := safe.StateGet[*perf.CPU](nodeCtx, talosClient.COSI, resource.NewMetadata(perf.NamespaceName, perf.CPUType, perf.CPUID, resource.VersionUndefined)); err == nil && cpuRes != nil && cpuRes.TypedSpec() != nil {
		stat := cpuRes.TypedSpec().CPUTotal
		total := stat.User + stat.Nice + stat.System + stat.Idle + stat.Iowait + stat.Irq + stat.SoftIrq + stat.Steal
		if total > 0 {
			idle := stat.Idle + stat.Iowait
			busy := total - idle
			if busy < 0 {
				busy = 0
			}

			// CPUStat contains counters accumulated since boot. Calculate usage from
			// the delta between dashboard polls instead of reporting a lifetime average.
			overview.CPUUsage = m.calculateCPUUsage(nodeIP, busy, total)
		}
	}

	// 3. Real Uptime from /proc/uptime via client.Read
	if uptimeReader, err := talosClient.Read(nodeCtx, "/proc/uptime"); err == nil {
		var upSec, idleSec float64
		if data, err := io.ReadAll(uptimeReader); err == nil {
			if _, err := fmt.Sscanf(string(data), "%f %f", &upSec, &idleSec); err == nil && upSec > 0 {
				overview.Uptime = formatDuration(time.Duration(upSec) * time.Second)
			}
		}
		_ = uptimeReader.Close()
	}

	// 4. Real Kubernetes Version from COSI k8s.KubeletStatus
	if k8sList, err := safe.StateListAll[*talosk8s.KubeletStatus](nodeCtx, talosClient.COSI); err == nil {
		for ks := range k8sList.All() {
			if ks == nil || ks.TypedSpec() == nil {
				continue
			}
			if version := kubernetesVersionFromImage(ks.TypedSpec().Image); version != "" {
				overview.KubernetesVersion = version
				break
			}
		}
	}

	return overview, nil
}

// ListNodes queries all configured cluster nodes concurrently and returns their overview.
func (m *TalosManager) ListNodes(ctx context.Context) ([]*NodeOverview, error) {
	nodeIPs := m.clusterNodes(ctx)
	sort.Strings(nodeIPs)

	results := make([]*NodeOverview, len(nodeIPs))
	var wg sync.WaitGroup

	for i, ip := range nodeIPs {
		wg.Add(1)
		go func(idx int, nodeIP string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[ERROR] panic recovered in ListNodes for %s: %v", nodeIP, r)
					results[idx] = newUnreachableNode(nodeIP)
				}
			}()

			nodeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			status, err := m.GetNodeStatus(nodeCtx, nodeIP)
			if err != nil {
				// The role of an unreachable node is genuinely unknown; guessing it
				// from the IP suffix invented control planes on other clusters.
				results[idx] = newUnreachableNode(nodeIP)
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

	clusterName := m.GetClusterName()

	// The Kubernetes endpoint is a property of the cluster config, not something
	// derivable from the Talos API address: that may use a different port, be a
	// separate load balancer, or not be the control-plane endpoint at all.
	endpoint := m.kubernetesEndpoint(ctx, nodes)

	readyCount := 0
	cpCount := 0
	workerCount := 0
	talosVersion := ""
	k8sVersion := ""

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

	// Report unknown rather than a plausible-looking guess when no node answered.
	if talosVersion == "" {
		talosVersion = "unknown"
	}
	if k8sVersion == "" {
		k8sVersion = "unknown"
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
	talosClient := m.GetClient()
	if talosClient == nil {
		return nil, errors.New("talos client is not initialized")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	nodeCtx := client.WithNode(reqCtx, nodeIP)

	resp, err := talosClient.ServiceList(nodeCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to list services on %s: %w", nodeIP, err)
	}

	services := make([]*TalosService, 0)
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

	for _, msg := range resp.GetMessages() {
		for _, s := range msg.GetServices() {
			healthy := false
			if s.GetHealth() != nil {
				healthy = s.GetHealth().GetHealthy()
			}

			desc := descriptions[s.GetId()]
			if desc == "" {
				desc = fmt.Sprintf("Talos system service %s", s.GetId())
			}

			uptime, restarts := serviceRuntimeFromEvents(s.GetEvents())

			services = append(services, &TalosService{
				HealthKnown: s.GetHealth() != nil && !s.GetHealth().GetUnknown(),
				ID:          s.GetId(),
				Name:        s.GetId(),
				State:       s.GetState(),
				Healthy:     healthy,
				Description: desc,
				Uptime:      uptime,
				Restarts:    restarts,
			})
		}
	}

	return services, nil
}

// ListContainers lists containers running on a node.
func (m *TalosManager) ListContainers(ctx context.Context, nodeIP, namespace string) ([]*ContainerInfo, error) {
	talosClient := m.GetClient()
	if talosClient == nil {
		return nil, errors.New("talos client is not initialized")
	}

	if namespace == "" {
		namespace = "system"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	nodeCtx := client.WithNode(reqCtx, nodeIP)

	resp, err := talosClient.Containers(nodeCtx, namespace, common.ContainerDriver_CONTAINERD)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers on %s (ns: %s): %w", nodeIP, namespace, err)
	}

	containers := make([]*ContainerInfo, 0)
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

// RebootNode sends a reboot signal to the node with timeout.
func (m *TalosManager) RebootNode(ctx context.Context, nodeIP string) error {
	talosClient := m.GetClient()
	if talosClient == nil {
		return errors.New("talos client is not initialized")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	nodeCtx := client.WithNode(reqCtx, nodeIP)
	return talosClient.Reboot(nodeCtx)
}

// RestartService requests a restart of the specified service on the node.
func (m *TalosManager) RestartService(ctx context.Context, nodeIP string, serviceID string) error {
	talosClient := m.GetClient()
	if talosClient == nil {
		return errors.New("talos client is not initialized")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	nodeCtx := client.WithNode(reqCtx, nodeIP)
	_, err := talosClient.ServiceRestart(nodeCtx, serviceID)
	return err
}

// GetNodeDisks queries physical disks and their partition details for a given node.
func (m *TalosManager) GetNodeDisks(ctx context.Context, nodeIP string) ([]*DiskInfo, error) {
	talosClient := m.GetClient()
	if talosClient == nil {
		return nil, errors.New("talos client is not initialized")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	nodeCtx := client.WithNode(reqCtx, nodeIP)

	// Fetch physical disks via Talos SDK
	resp, err := talosClient.Disks(nodeCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to query disks on %s: %w", nodeIP, err)
	}

	type filesystemUsage struct {
		used    uint64
		percent int
	}
	usageByPath := make(map[string]filesystemUsage)
	if mountsResp, mountsErr := talosClient.Mounts(nodeCtx); mountsErr != nil {
		log.Printf("[WARN] Talos Mounts returned error for %s: %v", nodeIP, mountsErr)
	} else {
		for _, msg := range mountsResp.GetMessages() {
			for _, stat := range msg.GetStats() {
				used, percent := calculateFilesystemUsage(stat.GetSize(), stat.GetAvailable())
				usage := filesystemUsage{used: used, percent: percent}
				if filesystem := stat.GetFilesystem(); filesystem != "" {
					usageByPath[filesystem] = usage
				}
				if mountPath := stat.GetMountedOn(); mountPath != "" {
					usageByPath[mountPath] = usage
				}
			}
		}
	}

	// Fetch volume statuses via COSI to resolve partition details
	partitionsByParent := make(map[string][]PartitionInfo)
	volumes, err := safe.StateListAll[*block.VolumeStatus](nodeCtx, talosClient.COSI)
	if err != nil {
		log.Printf("[WARN] COSI StateListAll VolumeStatus returned error for %s: %v", nodeIP, err)
	} else {
		for v := range volumes.All() {
			if v == nil || v.TypedSpec() == nil {
				continue
			}
			spec := v.TypedSpec()
			if spec.ParentLocation != "" || spec.Type == block.VolumeTypePartition {
				parent := spec.ParentLocation
				if parent == "" {
					// Handle NVMe/eMMC parent paths (e.g. /dev/nvme0n1p1 -> /dev/nvme0n1)
					trimmed := strings.TrimRight(spec.Location, "0123456789")
					if strings.HasSuffix(trimmed, "p") && len(trimmed) > 1 && unicode.IsDigit(rune(trimmed[len(trimmed)-2])) {
						parent = strings.TrimSuffix(trimmed, "p")
					} else {
						parent = trimmed
					}
				}
				prettySize := spec.PrettySize
				if prettySize == "" && spec.Size > 0 {
					prettySize = formatBytes(spec.Size)
				}
				mountPath := spec.MountSpec.TargetPath
				var metaID string
				if v.Metadata() != nil {
					metaID = string(v.Metadata().ID())
				}
				pInfo := PartitionInfo{
					ID:             metaID,
					Location:       spec.Location,
					PartitionIndex: spec.PartitionIndex,
					Size:           spec.Size,
					PrettySize:     prettySize,
					Filesystem:     spec.Filesystem.String(),
					MountPath:      mountPath,
					Phase:          spec.Phase.String(),
					UUID:           spec.UUID,
				}
				usage, ok := usageByPath[spec.Location]
				if !ok && mountPath != "" {
					usage, ok = usageByPath[mountPath]
				}
				if ok {
					pInfo.UsedBytes = usage.used
					pInfo.Used = formatBytes(usage.used)
					pInfo.UsedPercent = usage.percent
				}
				partitionsByParent[parent] = append(partitionsByParent[parent], pInfo)
			}
		}
	}

	disks := make([]*DiskInfo, 0)
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
			bus := detectDiskBus(d.GetDeviceName(), d.GetType().String(), d.GetBusPath(), d.GetSubsystem(), d.GetModalias())

			disks = append(disks, &DiskInfo{
				DeviceName: d.GetDeviceName(),
				DevicePath: devPath,
				Size:       d.GetSize(),
				PrettySize: prettySize,
				Model:      d.GetModel(),
				Serial:     d.GetSerial(),
				Bus:        bus,
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
	talosClient := m.GetClient()
	if talosClient == nil {
		return nil, errors.New("talos client is not initialized")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	nodeCtx := client.WithNode(reqCtx, nodeIP)

	mc, err := safe.StateGet[*talosconfig.MachineConfig](nodeCtx, talosClient.COSI, resource.NewMetadata("config", talosconfig.MachineConfigType, talosconfig.ActiveID, resource.VersionUndefined))
	if err != nil {
		return nil, fmt.Errorf("failed to read machine config from %s: %w", nodeIP, err)
	}

	if mc == nil || mc.Provider() == nil {
		return nil, fmt.Errorf("no machine config provider available for %s", nodeIP)
	}

	cfgBytes, err := mc.Provider().Bytes()
	if err != nil {
		return nil, fmt.Errorf("failed to serialize machine config for %s: %w", nodeIP, err)
	}

	return cfgBytes, nil
}

// GetEtcdStatus queries etcd members and alarms from the cluster control plane with multi-node failover.
func (m *TalosManager) GetEtcdStatus(ctx context.Context) (*EtcdClusterStatus, error) {
	talosClient := m.GetClient()
	if talosClient == nil {
		return nil, errors.New("talos client is not initialized")
	}

	// The budget must cover ListNodes plus a failover sweep over every control
	// plane, twice (members, then status), plus alarms. Each attempt below is
	// additionally clamped to what is actually left, so one black-holed node can
	// no longer consume the whole deadline and starve the remaining candidates.
	reqCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	nodes, err := m.ListNodes(reqCtx)
	if err != nil {
		return nil, err
	}

	var cpCandidates []string
	for _, n := range nodes {
		if n.Role == "controlplane" && n.Ready {
			cpCandidates = append(cpCandidates, n.IP)
		}
	}
	// A control plane that is up but NotReady still serves etcd, so fall back to
	// every known control plane before falling back to the configured endpoints.
	if len(cpCandidates) == 0 {
		for _, n := range nodes {
			if n.Role == "controlplane" {
				cpCandidates = append(cpCandidates, n.IP)
			}
		}
	}
	if len(cpCandidates) == 0 {
		cpCandidates = append(cpCandidates, m.GetEndpoints()...)
	}
	if len(cpCandidates) == 0 {
		return nil, errors.New("no control plane nodes or endpoints configured to query etcd")
	}

	var memberResp *machine.EtcdMemberListResponse
	var activeCP string
	var lastErr error

	for _, cp := range cpCandidates {
		budget, ok := attemptBudget(reqCtx, 4*time.Second)
		if !ok {
			break
		}
		subCtx, subCancel := context.WithTimeout(reqCtx, budget)
		nodeCtx := client.WithNode(subCtx, cp)
		resp, err := talosClient.EtcdMemberList(nodeCtx, &machine.EtcdMemberListRequest{})
		subCancel()
		if err == nil && resp != nil {
			memberResp = resp
			activeCP = cp
			break
		}
		lastErr = err
	}

	if memberResp == nil {
		return nil, fmt.Errorf("failed to list etcd members from any CP node (tried %v): %w", cpCandidates, lastErr)
	}

	members := make([]EtcdMemberInfo, 0)
	for _, msg := range memberResp.GetMessages() {
		for _, mem := range msg.GetMembers() {
			members = append(members, EtcdMemberInfo{
				ID:         fmt.Sprintf("%016x", mem.GetId()),
				Name:       mem.GetHostname(),
				Hostname:   mem.GetHostname(),
				ClientURLs: mem.GetClientUrls(),
				PeerURLs:   mem.GetPeerUrls(),
				IsLearner:  mem.GetIsLearner(),
				Healthy:    false,
			})
		}
	}

	// EtcdMemberList describes membership, not health. Query each reachable
	// control-plane node for its actual member status and correlate by member ID.
	type memberRuntimeStatus struct {
		leader    uint64
		dbSize    int64
		raftTerm  uint64
		raftIndex uint64
		errors    []string
	}
	statuses := make(map[string]memberRuntimeStatus)
	var totalDBSize uint64
	var leaderID string
	var raftTerm, raftIndex uint64
	for _, cp := range cpCandidates {
		budget, ok := attemptBudget(reqCtx, 3*time.Second)
		if !ok {
			break
		}
		statusCtx, statusCancel := context.WithTimeout(reqCtx, budget)
		statusResp, statusErr := talosClient.EtcdStatus(client.WithNode(statusCtx, cp))
		statusCancel()
		if statusErr != nil || statusResp == nil {
			continue
		}
		for _, msg := range statusResp.GetMessages() {
			status := msg.GetMemberStatus()
			if status == nil {
				continue
			}
			id := fmt.Sprintf("%016x", status.GetMemberId())
			statuses[id] = memberRuntimeStatus{
				leader: status.GetLeader(), dbSize: status.GetDbSize(),
				raftTerm: status.GetRaftTerm(), raftIndex: status.GetRaftIndex(),
				errors: append([]string(nil), status.GetErrors()...),
			}
			if status.GetDbSize() > 0 {
				totalDBSize += uint64(status.GetDbSize())
			}
			if status.GetLeader() != 0 {
				leaderID = fmt.Sprintf("%016x", status.GetLeader())
			}
			if status.GetRaftTerm() > raftTerm {
				raftTerm = status.GetRaftTerm()
			}
			if status.GetRaftIndex() > raftIndex {
				raftIndex = status.GetRaftIndex()
			}
		}
	}

	leaderName := ""
	for i := range members {
		status, ok := statuses[members[i].ID]
		if !ok {
			continue
		}
		members[i].Healthy = len(status.errors) == 0
		members[i].Leader = members[i].ID == leaderID
		members[i].Errors = status.errors
		if status.dbSize > 0 {
			members[i].DBSize = formatBytes(uint64(status.dbSize))
		}
		if members[i].Leader {
			leaderName = members[i].Hostname
		}
	}

	alarms := make([]EtcdAlarmInfo, 0)
	alarmCheckSuccess := false

	if alarmBudget, ok := attemptBudget(reqCtx, 4*time.Second); ok {
		alarmCtx, alarmCancel := context.WithTimeout(reqCtx, alarmBudget)
		alarmNodeCtx := client.WithNode(alarmCtx, activeCP)

		alarmResp, alarmErr := talosClient.EtcdAlarmList(alarmNodeCtx)
		alarmCancel()
		if alarmErr == nil && alarmResp != nil {
			alarmCheckSuccess = true
			for _, msg := range alarmResp.GetMessages() {
				for _, a := range msg.GetMemberAlarms() {
					alarms = append(alarms, EtcdAlarmInfo{
						MemberID: fmt.Sprintf("%016x", a.GetMemberId()),
						Alarm:    a.GetAlarm().String(),
					})
				}
			}
		} else {
			log.Printf("[WARN] EtcdAlarmList failed on %s: %v", activeCP, alarmErr)
		}
	} else {
		log.Printf("[WARN] no time budget left to check etcd alarms on %s", activeCP)
	}

	healthyMembers := len(members) > 0
	for _, member := range members {
		healthyMembers = healthyMembers && member.Healthy
	}
	healthy := healthyMembers && len(alarms) == 0 && alarmCheckSuccess

	return &EtcdClusterStatus{
		Healthy:     healthy,
		Members:     members,
		Alarms:      alarms,
		LeaderID:    leaderID,
		LeaderName:  leaderName,
		TotalDBSize: formatBytes(totalDBSize),
		RaftTerm:    raftTerm,
		RaftIndex:   raftIndex,
	}, nil
}

func detectDiskBus(deviceName, diskType, busPath, subsystem, modalias string) string {
	haystack := strings.ToLower(strings.Join([]string{deviceName, diskType, busPath, subsystem, modalias}, " "))
	for _, candidate := range []struct {
		needle string
		label  string
	}{
		{"nvme", "NVMe"},
		{"virtio", "VirtIO"},
		{"/vd", "VirtIO"},
		{"scsi", "SCSI"},
		{"sata", "SATA"},
		{"ata", "SATA"},
		{"usb", "USB"},
		{"mmc", "MMC"},
	} {
		if strings.Contains(haystack, candidate.needle) {
			return candidate.label
		}
	}
	return "Unknown"
}

func calculateFilesystemUsage(size, available uint64) (uint64, int) {
	if size == 0 {
		return 0, 0
	}
	if available > size {
		available = size
	}
	used := size - available
	return used, int(float64(used) / float64(size) * 100)
}

// newUnreachableNode builds the overview used when a node cannot be polled.
// Everything beyond its address is genuinely unknown at that point.
func newUnreachableNode(nodeIP string) *NodeOverview {
	return &NodeOverview{
		IP:       nodeIP,
		Hostname: nodeIP,
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
}

// attemptBudget clamps a per-attempt timeout to the time actually left on ctx,
// reporting false when too little remains for the attempt to be worth starting.
func attemptBudget(ctx context.Context, want time.Duration) (time.Duration, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return want, true
	}

	remaining := time.Until(deadline)
	if remaining < time.Second {
		return 0, false
	}
	if remaining < want {
		return remaining, true
	}
	return want, true
}

// formatDuration renders a coarse human-readable duration (e.g. "3d 4h").
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// serviceRuntimeFromEvents derives a service's uptime and restart count from its
// event history. Talos keeps only a bounded window of events, so the restart
// count is a lower bound rather than a lifetime total.
func serviceRuntimeFromEvents(events *machine.ServiceEvents) (string, int) {
	if events == nil {
		return "", 0
	}

	runs := 0
	var lastRun time.Time
	for _, ev := range events.GetEvents() {
		if ev == nil || ev.GetState() != "Running" {
			continue
		}
		runs++
		if ts := ev.GetTs(); ts != nil {
			lastRun = ts.AsTime()
		}
	}

	uptime := ""
	if !lastRun.IsZero() {
		uptime = formatDuration(time.Since(lastRun))
	}

	restarts := 0
	if runs > 1 {
		restarts = runs - 1
	}

	return uptime, restarts
}

// kubernetesVersionFromImage extracts the tag from an OCI reference. A digest
// pin carries no version, and neither does an untagged image: both report empty
// instead of passing a hash fragment off as a version number.
func kubernetesVersionFromImage(img string) string {
	if at := strings.IndexByte(img, '@'); at != -1 {
		img = img[:at]
	}

	colon := strings.LastIndexByte(img, ':')
	if colon == -1 {
		return ""
	}
	// A colon preceding the final path separator is a registry port, not a tag.
	if slash := strings.LastIndexByte(img, '/'); slash > colon {
		return ""
	}

	return img[colon+1:]
}

// kubernetesEndpoint reads the control-plane endpoint from the cluster config of
// a reachable control-plane node, returning empty when none can be consulted.
func (m *TalosManager) kubernetesEndpoint(ctx context.Context, nodes []*NodeOverview) string {
	talosClient := m.GetClient()
	if talosClient == nil {
		return ""
	}

	for _, n := range nodes {
		if n == nil || n.Role != "controlplane" {
			continue
		}

		nodeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		mc, err := safe.StateGet[*talosconfig.MachineConfig](
			client.WithNode(nodeCtx, n.IP),
			talosClient.COSI,
			resource.NewMetadata(talosconfig.NamespaceName, talosconfig.MachineConfigType, talosconfig.ActiveID, resource.VersionUndefined),
		)
		cancel()

		if err != nil || mc == nil || mc.Provider() == nil {
			continue
		}
		k8sCfg := mc.Provider().K8sClusterConfig()
		if k8sCfg == nil {
			continue
		}
		if ep := k8sCfg.ClusterEndpoint(); ep != nil && ep.String() != "" {
			return ep.String()
		}
	}

	return ""
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
	units := "KMGTPE"
	if exp >= len(units) {
		exp = len(units) - 1
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), units[exp])
}
