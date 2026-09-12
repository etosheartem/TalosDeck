package operations

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"sort"
	"strings"
	"sync"
	"talosdeck/internal/backup"
	"talosdeck/internal/certificates"
	"talosdeck/internal/health"
	"talosdeck/internal/k8s"
	"talosdeck/internal/talos"
	"time"
)

type HealthTalos interface {
	ListNodes(context.Context) ([]*talos.NodeOverview, error)
	ListServices(context.Context, string) ([]*talos.TalosService, error)
	GetNodeDisks(context.Context, string) ([]*talos.DiskInfo, error)
	GetEtcdStatus(context.Context) (*talos.EtcdClusterStatus, error)
	GetNodeConfig(context.Context, string) ([]byte, error)
}
type HealthKubernetes interface {
	HealthReady(context.Context) error
	UpgradeInventory(context.Context) (string, []k8s.UpgradeNode, error)
	ListPods(context.Context, string, string) ([]k8s.PodInfo, error)
	HealthNetworking(context.Context) ([]k8s.HealthWorkload, error)
	HealthClaims(context.Context) ([]k8s.HealthClaim, error)
}
type HealthBackups interface {
	List(context.Context) ([]*backup.BackupInfo, error)
}
type HealthCollector struct {
	ClusterID    string
	Talos        HealthTalos
	Kubernetes   HealthKubernetes
	Backups      HealthBackups
	Certificates interface {
		Check(context.Context) certificates.Report
	}
	Now func() time.Time
}

func (s *HealthCollector) Collect(parent context.Context) health.Snapshot {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}
	snapshot := health.Snapshot{ID: uuid.NewString(), ClusterID: s.ClusterID, CheckedAt: now, Checks: []health.Check{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	overflow := false
	add := func(category, rule, resource, node, state, reason, details string) {
		mu.Lock()
		defer mu.Unlock()
		if len(snapshot.Checks) >= 2000 {
			overflow = true
			return
		}
		snapshot.Checks = append(snapshot.Checks, health.Check{ID: rule + "/" + resource, Category: category, Resource: resource, Node: node, State: state, Reason: reason, Title: healthTitle(rule), Details: details, SuggestedAction: healthAction(category), ObservedAt: now})
	}
	run := func(fn func(context.Context)) {
		wg.Add(1)
		go func() { defer wg.Done(); c, stop := context.WithTimeout(ctx, 35*time.Second); defer stop(); fn(c) }()
	}
	run(func(c context.Context) { s.collectNodes(c, add) })
	run(func(c context.Context) {
		if unavailable(s.Talos) {
			add("etcd", "etcd.health", "cluster", "", "unknown", "unavailable", "Etcd client unavailable")
			return
		}
		e, err := s.Talos.GetEtcdStatus(c)
		state, reason := "healthy", "healthy"
		if err != nil || e == nil {
			state, reason = "unknown", "unavailable"
		} else if len(e.Errors) > 0 || len(e.Members) == 0 {
			state, reason = "unknown", "partial-inventory"
		} else if !e.Healthy {
			state, reason = "critical", "etcd-degraded"
		}

		if e != nil {
			if len(e.Alarms) > 0 {
				add("etcd", "etcd.alarms", "cluster", "", "critical", "etcd-alarm", "Etcd reports active alarms")
			}
			if len(e.Members) > 0 && len(e.Errors) == 0 {
				healthy := 0
				for _, m := range e.Members {
					if m.Healthy {
						healthy++
					}
				}
				if healthy < len(e.Members)/2+1 {
					add("etcd", "etcd.quorum", "cluster", "", "critical", "quorum-lost", "Healthy members are below quorum")
				}
			}
		}
		add("etcd", "etcd.health", "cluster", "", state, reason, "Etcd member, leader and alarm inspection")
	})
	run(func(c context.Context) { s.collectKubernetes(c, now, add) })
	run(func(c context.Context) {
		if unavailable(s.Backups) {
			add("backups", "backups.age", "cluster", "", "unknown", "unavailable", "Backup catalog unavailable")
		} else {
			items, err := s.Backups.List(c)
			receivedAt := time.Now().UTC()
			if s.Now != nil {
				receivedAt = s.Now()
			}
			last := latestCompleteBackup(items)
			state, reason := "healthy", "recent-complete-backup"
			if err != nil {
				state, reason = "unknown", "unavailable"
			} else if last == nil {
				state, reason = "warning", "no-complete-backup"
			} else if last.Timestamp.IsZero() || last.Timestamp.After(receivedAt) {
				state, reason = "unknown", "invalid-timestamp"
			} else if now.Sub(last.Timestamp) > 24*time.Hour {
				state, reason = "warning", "backup-old"
			}
			add("backups", "backups.age", "cluster", "", state, reason, "Complete backup catalog age; this does not certify a tested restore")
		}
	})
	run(func(c context.Context) {
		if unavailable(s.Certificates) {
			add("certificates", "certificates.inventory", "cluster", "", "unknown", "unavailable", "Certificate monitor unavailable")
			return
		}
		report := s.Certificates.Check(c)
		receivedAt := time.Now().UTC()
		if s.Now != nil {
			receivedAt = s.Now()
		}
		if len(report.Certificates) == 0 {
			add("certificates", "certificates.inventory", "cluster", "", "unknown", "unavailable", "No certificates inspected")
		}
		for _, cert := range report.Certificates {
			reason := cert.Reason
			if reason == "expired" {
				reason = "certificate-expired"
			}
			state := cert.Status
			if report.CheckedAt.IsZero() || report.CheckedAt.After(receivedAt.Add(time.Minute)) || receivedAt.Sub(report.CheckedAt) > health.Freshness {
				state, reason = "unknown", "stale"
			}
			add("certificates", "certificate", cert.ID, cert.Node, state, reason, cert.Name)
		}
	})
	wg.Wait()
	mu.Lock()
	if overflow {
		for _, category := range []string{"control-plane", "etcd", "nodes", "storage", "networking", "workloads", "backups", "certificates"} {
			snapshot.Checks = append(snapshot.Checks, health.Check{ID: "collection/overflow/" + category, Category: category, State: "unknown", Reason: "limit-exceeded", Title: "Health detail limit reached", ObservedAt: now})
		}
	}
	mu.Unlock()
	sort.Slice(snapshot.Checks, func(i, j int) bool { return snapshot.Checks[i].ID < snapshot.Checks[j].ID })
	return snapshot
}

type healthAdd func(category, rule, resource, node, state, reason, details string)

func (s *HealthCollector) collectNodes(ctx context.Context, add healthAdd) {
	if unavailable(s.Talos) {
		add("nodes", "nodes.inventory", "cluster", "", "unknown", "unavailable", "Talos inventory unavailable")
		add("storage", "disks.inventory", "cluster", "", "unknown", "unavailable", "Disk inventory unavailable")
		return
	}
	nodes, err := s.Talos.ListNodes(ctx)
	if err != nil || len(nodes) == 0 {
		add("nodes", "nodes.inventory", "cluster", "", "unknown", "unavailable", "Talos inventory unavailable")
		add("storage", "disks.inventory", "cluster", "", "unknown", "unavailable", "Disk inventory unavailable")
		return
	}

	controlPlanes := 0
	for _, node := range nodes {
		if node != nil && node.Role == "controlplane" {
			controlPlanes++
		}
	}
	if controlPlanes == 0 {
		add("control-plane", "control-plane.inventory", "cluster", "", "unknown", "unavailable", "No control-plane identity in Talos inventory")
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, n := range nodes {
		if n == nil {
			continue
		}
		wg.Add(1)
		go func(n *talos.NodeOverview) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				add("nodes", "node.ready", n.IP, n.IP, "unknown", "deadline", "Node inspection incomplete")
				add("storage", "disk.usage", n.IP, n.IP, "unknown", "deadline", "Disk inspection incomplete")
				return
			}
			c, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			state := "healthy"
			reason := "ready"
			if !n.Ready {
				state, reason = "unknown", "unreachable"
			}
			add("nodes", "node.ready", n.IP, n.IP, state, reason, "Talos API node readiness")
			services, se := s.Talos.ListServices(c, n.IP)
			if se != nil {
				add("nodes", "node.services", n.IP, n.IP, "unknown", "unavailable", "Talos service health unavailable")
			} else {
				known := 0
				for _, svc := range services {
					if svc == nil {
						continue
					}
					st, rs := "healthy", "healthy"
					if svc.State == "Failed" || svc.State == "Crashed" {
						st, rs = "warning", "service-failed"
					} else if !svc.HealthKnown {
						continue
					} else if !svc.Healthy {
						st, rs = "warning", "service-unhealthy"
					}
					known++
					category := "nodes"
					if n.Role == "controlplane" && (svc.ID == "apid" || svc.ID == "etcd") {
						category = "control-plane"
					}
					add(category, "service", n.IP+"/"+svc.ID, n.IP, st, rs, "Talos service "+svc.ID)
				}
				if known == 0 {
					add("nodes", "node.services", n.IP, n.IP, "unknown", "unavailable", "No service health samples")
				}
			}
			if !n.MemoryUsageKnown || n.MemoryTotalBytes == 0 {
				add("nodes", "node.memory", n.IP, n.IP, "unknown", "unmeasured", "Memory usage unavailable")
			} else {
				add("nodes", "node.memory", n.IP, n.IP, "healthy", "measured", "Memory usage measured; pressure is evaluated from Kubernetes conditions")
			}
			disks, de := s.Talos.GetNodeDisks(c, n.IP)
			measured := 0
			if de == nil {
				for _, d := range disks {
					if d == nil {
						continue
					}
					for _, p := range d.Partitions {
						if p.MountPath == "" {
							continue
						}
						st, rs := "healthy", "measured"
						if p.Used == "" {
							st, rs = "unknown", "unmeasured"
						} else {
							measured++
							if p.UsedPercent >= 95 {
								st, rs = "critical", "disk-full"
							} else if p.UsedPercent >= 85 {
								st, rs = "warning", "disk-high"
							}
						}
						add("storage", "filesystem", n.IP+"/"+p.ID, n.IP, st, rs, fmt.Sprintf("%s: %d%% used", p.MountPath, p.UsedPercent))
					}
				}
			}
			if de != nil || measured == 0 {
				add("storage", "disk.usage", n.IP, n.IP, "unknown", "unmeasured", "No filesystem usage samples")
			}
		}(n)
	}
	wg.Wait()
}
func (s *HealthCollector) collectKubernetes(ctx context.Context, now time.Time, add healthAdd) {
	if unavailable(s.Kubernetes) {
		for _, category := range []string{"control-plane", "networking", "workloads", "storage"} {
			add(category, category+".api", "cluster", "", "unknown", "unavailable", "Kubernetes client unavailable")
		}
		return
	}
	state, reason := "healthy", "ready"
	if err := s.Kubernetes.HealthReady(ctx); err != nil {
		state, reason = "unknown", "unavailable"
	}
	add("control-plane", "apiserver.ready", "cluster", "", state, reason, "Live Kubernetes readiness endpoint")
	_, nodes, err := s.Kubernetes.UpgradeInventory(ctx)
	if err != nil || len(nodes) == 0 {
		add("nodes", "kubernetes.inventory", "cluster", "", "unknown", "unavailable", "Kubernetes nodes unavailable")
	} else {
		for _, n := range nodes {
			st, rs := "healthy", "ready"
			if !n.Ready {
				st, rs = "critical", "node-not-ready"
			} else if n.Pressure {
				st, rs = "warning", "node-pressure"
			}
			add("nodes", "kubernetes.node", n.Name, n.Name, st, rs, "Kubernetes Ready and pressure conditions")
		}
	}
	pods, err := s.Kubernetes.ListPods(ctx, "", "")
	if err != nil {
		add("workloads", "pods.inventory", "cluster", "", "unknown", "unavailable", "Pod inventory unavailable")
	} else if len(pods) == 0 {
		add("workloads", "pods.inventory", "cluster", "", "not-applicable", "empty", "No pods returned by successful inspection")
	} else {
		for _, p := range pods {
			st, rs := "healthy", "ready"
			if podNeedsDiagnosticAttention(p) {
				if p.Status == "Pending" && !p.CreatedAt.IsZero() && now.Sub(p.CreatedAt) < 5*time.Minute {
					rs = "pending-grace"
				} else {
					st, rs = "warning", "pod-not-ready"
				}
			}
			add("workloads", "pod", p.Namespace+"/"+p.Name, p.NodeName, st, rs, p.Status)
		}
	}
	workloads, err := s.Kubernetes.HealthNetworking(ctx)
	if err != nil {
		add("networking", "cni", "cluster", "", "unknown", "unavailable", "CNI workload inventory unavailable")
		add("networking", "dns", "cluster", "", "unknown", "unavailable", "DNS workload inventory unavailable")
	} else {
		configured := "unknown"
		if !unavailable(s.Talos) {
			nodes, e := s.Talos.ListNodes(ctx)
			if e == nil {
				for _, n := range nodes {
					if n != nil && n.Role == "controlplane" && n.Ready {
						data, ce := s.Talos.GetNodeConfig(ctx, n.IP)
						if ce == nil {
							provider, pe := configloader.NewFromBytes(data)
							if pe == nil {
								configured = "custom"
								if provider.K8sFlannelCNIConfig() != nil {
									configured = "flannel"
								}
								break
							}
						}
					}
				}
			}
		}
		collectNetworking(workloads, configured, add)
	}
	claims, err := s.Kubernetes.HealthClaims(ctx)
	if err != nil {
		add("storage", "claims", "cluster", "", "unknown", "unavailable", "Claim inventory unavailable")
	} else if len(claims) == 0 {
		add("storage", "claims", "cluster", "", "not-applicable", "empty", "No persistent volume claims")
	} else {
		for _, p := range claims {
			st, rs := "healthy", "bound"
			if p.Phase != "Bound" {
				if p.Phase == "Pending" && ((p.WaitForConsumer && !p.HasConsumer) || (!p.CreatedAt.IsZero() && now.Sub(p.CreatedAt) < 5*time.Minute)) {
					rs = "pending-grace"
				} else {
					st, rs = "warning", "claim-unbound"
				}
			}
			add("storage", "claim", p.Namespace+"/"+p.Name, "", st, rs, p.Phase)
		}
	}
}
func collectNetworking(workloads []k8s.HealthWorkload, configured string, add healthAdd) {
	providers := map[string][]k8s.HealthWorkload{}
	dns := []k8s.HealthWorkload{}
	for _, w := range workloads {
		for _, image := range w.Images {
			if w.Kind == "DaemonSet" {
				if strings.Contains(image, "/cilium/cilium:") || strings.Contains(image, "/cilium/cilium@") {
					providers["cilium"] = append(providers["cilium"], w)
				}
				if strings.Contains(image, "/flannel:") || strings.Contains(image, "/flannel@") {
					providers["flannel"] = append(providers["flannel"], w)
				}
			}
			if w.Kind == "Deployment" && (strings.Contains(image, "/coredns:") || strings.Contains(image, "/coredns@")) {
				dns = append(dns, w)
			}
		}
	}
	if configured == "unknown" || len(providers) != 1 || (configured == "flannel" && len(providers["flannel"]) == 0) || (configured == "custom" && len(providers["flannel"]) > 0) {
		add("networking", "cni", "cluster", "", "unknown", "unsupported-or-ambiguous", "Recognized CNI identity is unavailable or ambiguous")
	} else {
		for name, ws := range providers {
			for _, w := range ws {
				st, rs := "healthy", "ready"
				if w.Desired == 0 {
					st, rs = "unknown", "no-desired-pods"
				} else if w.Ready < w.Desired {
					st, rs = "critical", "cni-not-ready"
				}
				add("networking", "cni", w.Namespace+"/"+w.Name, "", st, rs, name)
			}
		}
	}
	if len(dns) == 0 {
		add("networking", "dns", "cluster", "", "unknown", "unavailable", "CoreDNS identity not found")
	} else {
		for _, w := range dns {
			st, rs := "healthy", "ready"
			if w.Desired == 0 || w.Ready < w.Desired {
				st, rs = "warning", "dns-not-ready"
			}
			add("networking", "dns", w.Namespace+"/"+w.Name, "", st, rs, "CoreDNS")
		}
	}
}

func healthTitle(rule string) string {
	titles := map[string]string{"node.ready": "Talos node readiness", "node.services": "Talos services", "node.memory": "Node memory measurement", "kubernetes.node": "Kubernetes node readiness", "apiserver.ready": "Kubernetes API readiness", "etcd.health": "Etcd health", "etcd.quorum": "Etcd quorum", "etcd.alarms": "Etcd alarms", "filesystem": "Filesystem usage", "disk.usage": "Disk usage measurement", "pod": "Pod readiness", "cni": "Container network readiness", "dns": "Cluster DNS readiness", "claim": "Persistent volume claim", "backups.age": "Backup freshness", "certificate": "Certificate validity"}
	if title := titles[rule]; title != "" {
		return title
	}
	return strings.ReplaceAll(rule, ".", " ")
}
func healthAction(category string) string {
	switch category {
	case "etcd":
		return "Inspect control-plane connectivity, member health and alarms before maintenance."
	case "storage":
		return "Inspect node filesystems, storage provisioner and volume events."
	case "networking":
		return "Inspect the configured CNI and DNS workload status and events."
	case "workloads":
		return "Open the workload inspector for current container status and events."
	case "backups":
		return "Inspect backup schedules and jobs; create a complete backup if needed."
	case "certificates":
		return "Open certificate monitoring for renewal guidance and verification details."
	default:
		return "Inspect API connectivity, permissions, node conditions and service logs."
	}
}
