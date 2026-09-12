package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"talosdeck/internal/alertcenter"
	"talosdeck/internal/backup"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/operations"
	"talosdeck/internal/talos"
)

type alertTalos interface {
	ListNodes(context.Context) ([]*talos.NodeOverview, error)
	GetEtcdStatus(context.Context) (*talos.EtcdClusterStatus, error)
	GetNodeDisks(context.Context, string) ([]*talos.DiskInfo, error)
}
type alertKubernetes interface {
	UpgradeInventory(context.Context) (string, []k8s.UpgradeNode, error)
}
type alertBackups interface {
	List(context.Context) ([]*backup.BackupInfo, error)
}
type alertMonitor struct {
	center       *alertcenter.Center
	talos        alertTalos
	kubernetes   alertKubernetes
	certificates CertificateInspector
	backups      alertBackups
	jobs         *jobs.Manager
	store        operations.ProvisionStore
	clusterID    string
	mu           sync.Mutex
	highCounts   map[string]int
}

func observationKey(rule, resource string) string { return rule + "\x00" + resource }
func (m *alertMonitor) high(key string, value, trigger, recover float64, previous map[string]bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.highCounts == nil {
		m.highCounts = map[string]int{}
	}
	if value < recover {
		delete(m.highCounts, key)
		return false
	}
	if previous[key] {
		return true
	}
	if value < trigger {
		delete(m.highCounts, key)
		return false
	}
	m.highCounts[key] = min(2, m.highCounts[key]+1)
	return m.highCounts[key] >= 2
}
func (m *alertMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, 25*time.Second)
		observations := m.collect(check, m.center.Snapshot().Alerts)
		cancel()
		save, stop := context.WithTimeout(ctx, 5*time.Second)
		_ = m.center.Observe(save, observations)
		stop()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *alertMonitor) collect(ctx context.Context, previous []alertcenter.Alert) []alertcenter.Observation {
	active := map[string]bool{}
	for _, a := range previous {
		if a.State == "active" {
			active[observationKey(a.RuleID, a.ResourceID)] = true
		}
	}
	var mu sync.Mutex
	rows := []alertcenter.Observation{}
	seen := map[string]bool{}
	add := func(o alertcenter.Observation) {
		mu.Lock()
		defer mu.Unlock()
		if !o.Known {
			m.mu.Lock()
			delete(m.highCounts, observationKey(o.RuleID, o.ResourceID))
			m.mu.Unlock()
		}
		rows = append(rows, o)
		seen[observationKey(o.RuleID, o.ResourceID)] = true
	}
	unavailable := func(component, resource, node string, failed bool) {
		add(alertcenter.Observation{RuleID: "runtime.inspection." + component, ResourceID: resource, Node: node, Component: component, Known: true, Active: failed, Severity: "warning", Title: "Inspection unavailable", Details: component + " observation is incomplete or unavailable.", SuggestedAction: "Check API connectivity, credentials and the last successful observation."})
	}
	var wg sync.WaitGroup
	launch := func(fn func()) { wg.Add(1); go func() { defer wg.Done(); fn() }() }
	if m.talos != nil {
		launch(func() {
			nodes, err := m.talos.ListNodes(ctx)
			unavailable("talos", "cluster", "", err != nil || len(nodes) == 0)
			var disks sync.WaitGroup
			sem := make(chan struct{}, 4)
			for _, n := range nodes {
				if n == nil || n.IP == "" {
					continue
				}
				add(alertcenter.Observation{RuleID: "runtime.node.ready", ResourceID: n.IP, Node: n.IP, Component: "talos", Known: err == nil, Active: !n.Ready, Severity: "critical", Title: "Talos node not ready", Details: "Node " + n.Hostname + " is unreachable or reports degraded core services.", SuggestedAction: "Open the node inspector and Talos service logs."})
				for _, metric := range []struct {
					name  string
					known bool
					value float64
				}{{"cpu", n.CPUUsageKnown, float64(n.CPUUsage)}, {"memory", n.MemoryUsageKnown && n.MemoryTotalBytes > 0, 100 * float64(n.MemoryUsedBytes) / max(1, float64(n.MemoryTotalBytes))}} {
					rule := "runtime.resource." + metric.name
					enabled := false
					metric.known = metric.known && err == nil
					if metric.known {
						enabled = m.high(observationKey(rule, n.IP), metric.value, 90, 85, active)
					}
					add(alertcenter.Observation{RuleID: rule, ResourceID: n.IP, Node: n.IP, Component: metric.name, Known: metric.known, Active: enabled, Severity: "warning", Title: "High " + metric.name + " usage", Details: fmt.Sprintf("Observed usage %.0f%%; trigger 90%% for two observations, recover below 85%%.", metric.value), SuggestedAction: "Inspect workloads, capacity and node pressure."})
				}
				node := n
				disks.Add(1)
				go func() {
					defer disks.Done()
					select {
					case sem <- struct{}{}:
						defer func() { <-sem }()
					case <-ctx.Done():
						return
					}
					ds, e := m.talos.GetNodeDisks(ctx, node.IP)
					knownCount := 0
					for _, d := range ds {
						if d == nil {
							continue
						}
						for _, p := range d.Partitions {
							if p.MountPath == "" {
								continue
							}
							known := p.Used != "" && e == nil
							if known {
								knownCount++
							}
							resource := node.IP + ":" + p.MountPath
							enabled := false
							if known {
								enabled = m.high(observationKey("runtime.disk.full", resource), float64(p.UsedPercent), 90, 85, active)
							}
							severity := "warning"
							if p.UsedPercent >= 95 {
								severity = "critical"
							}
							add(alertcenter.Observation{RuleID: "runtime.disk.full", ResourceID: resource, Node: node.IP, Component: "storage", Known: known && e == nil, Active: enabled, Severity: severity, Title: "Filesystem nearly full", Details: fmt.Sprintf("%s: %d%% used; trigger 90%% twice, recover below 85%%.", p.MountPath, p.UsedPercent), SuggestedAction: "Inspect disk usage and free space or expand the filesystem."})
						}
					}
					unavailable("storage", node.IP, node.IP, e != nil || knownCount == 0)
				}()
			}
			disks.Wait()
		})
		launch(func() {
			value, err := m.talos.GetEtcdStatus(ctx)
			known := err == nil && value != nil
			unavailable("etcd", "cluster", "", !known)
			healthy := known && value.Healthy
			add(alertcenter.Observation{RuleID: "runtime.etcd.degraded", ResourceID: "cluster", Component: "etcd", Known: known, Active: !healthy, Severity: "critical", Title: "Etcd is degraded", Details: "Etcd health or quorum checks report a problem.", SuggestedAction: "Inspect every control plane; avoid maintenance until quorum is healthy."})
		})
	}
	if m.kubernetes != nil {
		launch(func() {
			_, nodes, err := m.kubernetes.UpgradeInventory(ctx)
			unavailable("kubernetes", "cluster", "", err != nil || len(nodes) == 0)
			if err != nil {
				return
			}
			for _, n := range nodes {
				node := n.Name
				if len(n.Addresses) > 0 {
					node = n.Addresses[0]
				}
				add(alertcenter.Observation{RuleID: "runtime.kubernetes.ready", ResourceID: n.Name, Node: node, Component: "kubelet", Known: true, Active: !n.Ready, Severity: "critical", Title: "Kubernetes node NotReady", Details: n.Name + " Ready condition is false or unknown.", SuggestedAction: "Inspect kubelet, CNI and node pressure."})
				add(alertcenter.Observation{RuleID: "runtime.kubernetes.pressure", ResourceID: n.Name, Node: node, Component: "pressure", Known: true, Active: n.Pressure, Severity: "warning", Title: "Node pressure", Details: n.Name + " reports disk, memory or PID pressure.", SuggestedAction: "Inspect resources and workloads on the node."})
			}
		})
	}
	if m.certificates != nil {
		launch(func() {
			report := m.certificates.Check(ctx)
			for _, c := range report.Certificates {
				severity := c.Status
				if severity != "critical" {
					severity = "warning"
				}
				add(alertcenter.Observation{RuleID: "runtime.certificate", ResourceID: c.ID, Node: c.Node, Component: "certificates", Known: true, Active: c.Status != "healthy", Severity: severity, Title: c.Name, Details: c.Reason, SuggestedAction: c.RenewalGuidance})
			}
		})
	}
	if m.backups != nil {
		launch(func() {
			items, err := m.backups.List(ctx)
			unavailable("backups", "cluster", "", err != nil)
			if err != nil {
				return
			}
			var newest time.Time
			for _, b := range items {
				if b != nil && !b.Partial && b.Timestamp.After(newest) {
					newest = b.Timestamp
				}
			}
			add(alertcenter.Observation{RuleID: "runtime.backup.age", ResourceID: "cluster", Component: "backups", Known: true, Active: newest.IsZero() || time.Since(newest) > 24*time.Hour, Severity: "warning", Title: "No recent complete backup", Details: "No complete restore point newer than 24 hours is listed.", SuggestedAction: "Create a backup and check the schedule and delivery target."})
		})
	}
	wg.Wait()
	// An absent observation breaks consecutive-sample debounce as well.
	m.mu.Lock()
	for key := range m.highCounts {
		if !seen[key] {
			delete(m.highCounts, key)
		}
	}
	m.mu.Unlock()
	if m.jobs != nil {
		for _, job := range m.jobs.List() {
			if job.Status == "interrupted" {
				add(alertcenter.Observation{RuleID: "job.result", ResourceID: job.ID, Component: "jobs", Known: true, Active: !job.Reviewed, Severity: "critical", Title: "Operation interrupted", Details: "Job " + job.ID + " (" + job.Request.Kind + ")", SuggestedAction: "Inspect infrastructure and acknowledge the interrupted job in its journal."})
			}
		}
	}
	deleted := map[string]bool{}
	if m.store != nil {
		if machines, err := operations.ListOwnedMachines(ctx, m.store, m.clusterID); err == nil {
			for _, machine := range machines {
				if machine.Status == "deleted" && machine.Address != "" {
					deleted[machine.Address] = true
				}
			}
			for _, machine := range machines {
				if machine.Status != "deleted" {
					delete(deleted, machine.Address)
				}
			}
		}
	}
	for _, a := range previous {
		if a.State == "active" && strings.HasPrefix(a.RuleID, "runtime.") && !seen[observationKey(a.RuleID, a.ResourceID)] {
			add(alertcenter.Observation{RuleID: a.RuleID, ResourceID: a.ResourceID, Node: a.Node, Component: a.Component, Known: deleted[a.Node], Active: false, Severity: a.Severity, Title: a.Title, Details: a.Details, SuggestedAction: a.SuggestedAction})
		}
	}
	return rows
}
func observeJob(center *alertcenter.Center, job jobs.Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	severity := "warning"
	success := job.Status == "succeeded"
	if success {
		severity = "info"
	}
	if job.Status == "interrupted" {
		severity = "critical"
	}
	_ = center.Observe(ctx, []alertcenter.Observation{{RuleID: "job.result", ResourceID: job.ID, Component: "jobs", Known: true, Active: job.Status == "interrupted" && !job.Reviewed, Event: job.Status != "interrupted", Severity: severity, Title: "Operation " + job.Status, Details: "Job " + job.ID + " (" + job.Request.Kind + ")", SuggestedAction: "Open the job journal; review interrupted operations before retrying."}})
}
