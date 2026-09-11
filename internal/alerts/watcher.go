package alerts

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"talosdeck/internal/talos"
)

// ClusterHealthInspector abstracts cluster inspection for health monitoring.
type ClusterHealthInspector interface {
	ListNodes(ctx context.Context) ([]*talos.NodeOverview, error)
	GetEtcdStatus(ctx context.Context) (*talos.EtcdClusterStatus, error)
}

// NodeStateSnapshot represents the last known state of a node for debouncing.
type NodeStateSnapshot struct {
	IP          string    `json:"ip"`
	Hostname    string    `json:"hostname"`
	Ready       bool      `json:"ready"`
	Role        string    `json:"role"`
	LastSeen    time.Time `json:"lastSeen"`
	LastChanged time.Time `json:"lastChanged"`
	HighCPU     bool      `json:"highCpu"`
}

// WatcherStatus provides runtime statistics for the background health watcher.
type WatcherStatus struct {
	Running          bool      `json:"running"`
	IntervalSeconds  int       `json:"intervalSeconds"`
	LastCheckTime    time.Time `json:"lastCheckTime"`
	MonitoredNodes   int       `json:"monitoredNodes"`
	InitialCheckDone bool      `json:"initialCheckDone"`
}

// Watcher runs background health checks on cluster nodes and etcd, dispatching
// alerts via Telegram on state transitions.
type Watcher struct {
	manager         ClusterHealthInspector
	alerts          *TelegramService
	interval        time.Duration
	mu              sync.RWMutex
	running         bool
	stopChan        chan struct{}
	initialCheck    bool
	lastCheckTime   time.Time
	nodeStates      map[string]NodeStateSnapshot
	lastEtcdHealthy *bool
}

// NewWatcher initializes a new Watcher with the specified check interval.
func NewWatcher(manager ClusterHealthInspector, alertService *TelegramService, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Watcher{
		manager:      manager,
		alerts:       alertService,
		interval:     interval,
		stopChan:     make(chan struct{}),
		initialCheck: true,
		nodeStates:   make(map[string]NodeStateSnapshot),
	}
}

// Start begins periodic health watching in a background goroutine.
func (w *Watcher) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.stopChan = make(chan struct{})
	w.mu.Unlock()

	log.Printf("[AlertWatcher] Background health watcher started (interval: %v)", w.interval)

	go func() {
		// Run initial check right away
		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := w.CheckClusterHealth(checkCtx); err != nil {
			log.Printf("[AlertWatcher] Initial health check encountered error: %v", err)
		}
		cancel()

		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-w.stopChan:
				log.Println("[AlertWatcher] Health watcher stopped")
				return
			case <-ctx.Done():
				log.Println("[AlertWatcher] Context cancelled, stopping health watcher")
				return
			case <-ticker.C:
				cCtx, cCancel := context.WithTimeout(ctx, 20*time.Second)
				if err := w.CheckClusterHealth(cCtx); err != nil {
					log.Printf("[AlertWatcher] Check error: %v", err)
				}
				cCancel()
			}
		}
	}()
}

// Stop gracefully signals the watcher background loop to terminate.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return
	}
	w.running = false
	close(w.stopChan)
}

// CheckClusterHealth checks node status and etcd health, detecting state transitions.
func (w *Watcher) CheckClusterHealth(ctx context.Context) error {
	if w.manager == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().UTC()

	// 1. Inspect Cluster Nodes
	nodes, err := w.manager.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("failed to query node list: %w", err)
	}

	for _, n := range nodes {
		prev, exists := w.nodeStates[n.IP]
		if !exists {
			// First observation of this node
			w.nodeStates[n.IP] = NodeStateSnapshot{
				IP:          n.IP,
				Hostname:    n.Hostname,
				Ready:       n.Ready,
				Role:        n.Role,
				LastSeen:    now,
				LastChanged: now,
				HighCPU:     n.CPUUsage >= 90,
			}

			// If cluster was already initialized and a new node appears in NotReady state
			if !w.initialCheck && !n.Ready && w.alerts != nil && w.alerts.IsEnabled() {
				_ = w.alerts.SendNodeStatusAlert(n.IP, n.Hostname, "NotReady", "New node detected in NotReady state")
			}
			continue
		}

		// State transition detection: Ready -> NotReady or NotReady -> Ready
		if prev.Ready != n.Ready {
			if n.Ready {
				log.Printf("[AlertWatcher] ✅ Node %s (%s) RECOVERED to Ready", n.Hostname, n.IP)
				if w.alerts != nil && w.alerts.IsEnabled() {
					_ = w.alerts.SendNodeStatusAlert(n.IP, n.Hostname, "Ready", "Node communication re-established and healthy")
				}
			} else {
				log.Printf("[AlertWatcher] 🚨 Node %s (%s) transitioned to NOT READY", n.Hostname, n.IP)
				details := "Node is unreachable or reporting degraded core services"
				if n.ServicesSummary != nil {
					details = fmt.Sprintf("Services: etcd=%s, kubelet=%s, containerd=%s, apid=%s",
						n.ServicesSummary.Etcd, n.ServicesSummary.Kubelet,
						n.ServicesSummary.Containerd, n.ServicesSummary.Apid)
				}
				if w.alerts != nil && w.alerts.IsEnabled() {
					_ = w.alerts.SendNodeStatusAlert(n.IP, n.Hostname, "NotReady", details)
				}
			}
			prev.Ready = n.Ready
			prev.LastChanged = now
		}

		// Resource check: CPU spike alert (>90%) with debounce
		if n.CPUUsage >= 90 && !prev.HighCPU {
			prev.HighCPU = true
			log.Printf("[AlertWatcher] ⚠️ Node %s CPU usage exceeded 90%%: %d%%", n.Hostname, n.CPUUsage)
			if w.alerts != nil && w.alerts.IsEnabled() {
				_ = w.alerts.SendResourceAlert(n.IP, n.Hostname, "CPU", n.CPUUsage, "CPU utilization is critically high (>= 90%)")
			}
		} else if n.CPUUsage < 80 && prev.HighCPU {
			prev.HighCPU = false
			log.Printf("[AlertWatcher] ✅ Node %s CPU usage normalized: %d%%", n.Hostname, n.CPUUsage)
			if w.alerts != nil && w.alerts.IsEnabled() {
				_ = w.alerts.SendAlert(LevelRecovered, fmt.Sprintf("CPU Normalized on %s", n.Hostname),
					fmt.Sprintf("Node: %s (%s)\nResource: CPU\nUsage: %d%%\nDetails: CPU utilization has returned to normal range", n.IP, n.Hostname, n.CPUUsage))
			}
		}

		prev.Hostname = n.Hostname
		prev.Role = n.Role
		prev.LastSeen = now
		w.nodeStates[n.IP] = prev
	}

	// 2. Inspect etcd Cluster Health
	etcdStatus, err := w.manager.GetEtcdStatus(ctx)
	if err == nil && etcdStatus != nil {
		if w.lastEtcdHealthy == nil {
			healthy := etcdStatus.Healthy
			w.lastEtcdHealthy = &healthy
			if !w.initialCheck && !healthy && w.alerts != nil && w.alerts.IsEnabled() {
				details := formatEtcdAlertDetails(etcdStatus)
				_ = w.alerts.SendEtcdAlert(false, details)
			}
		} else if *w.lastEtcdHealthy != etcdStatus.Healthy {
			*w.lastEtcdHealthy = etcdStatus.Healthy
			if etcdStatus.Healthy {
				log.Printf("[AlertWatcher] ✅ etcd cluster RECOVERED to healthy state")
				if w.alerts != nil && w.alerts.IsEnabled() {
					_ = w.alerts.SendEtcdAlert(true, fmt.Sprintf("All %d member(s) healthy and operational", len(etcdStatus.Members)))
				}
			} else {
				log.Printf("[AlertWatcher] 🚨 etcd cluster DEGRADED")
				if w.alerts != nil && w.alerts.IsEnabled() {
					details := formatEtcdAlertDetails(etcdStatus)
					_ = w.alerts.SendEtcdAlert(false, details)
				}
			}
		}
	}

	w.initialCheck = false
	w.lastCheckTime = now
	return nil
}

// GetStatus returns the current status and metrics of the watcher.
func (w *Watcher) GetStatus() WatcherStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return WatcherStatus{
		Running:          w.running,
		IntervalSeconds:  int(w.interval.Seconds()),
		LastCheckTime:    w.lastCheckTime,
		MonitoredNodes:   len(w.nodeStates),
		InitialCheckDone: !w.initialCheck,
	}
}

// GetNodeSnapshots returns a copy of current monitored node snapshots.
func (w *Watcher) GetNodeSnapshots() map[string]NodeStateSnapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()

	result := make(map[string]NodeStateSnapshot, len(w.nodeStates))
	for k, v := range w.nodeStates {
		result[k] = v
	}
	return result
}

func formatEtcdAlertDetails(status *talos.EtcdClusterStatus) string {
	if status == nil {
		return "No etcd status information available"
	}

	var parts []string
	if len(status.Alarms) > 0 {
		var alarmStrs []string
		for _, a := range status.Alarms {
			alarmStrs = append(alarmStrs, fmt.Sprintf("%s (member: %s)", a.Alarm, a.MemberID))
		}
		parts = append(parts, fmt.Sprintf("Active Alarms: %s", strings.Join(alarmStrs, ", ")))
	}

	if len(status.Errors) > 0 {
		parts = append(parts, fmt.Sprintf("Errors: %s", strings.Join(status.Errors, "; ")))
	}

	unhealthyMembers := 0
	for _, m := range status.Members {
		if !m.Healthy {
			unhealthyMembers++
		}
	}

	if unhealthyMembers > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d members unhealthy", unhealthyMembers, len(status.Members)))
	} else if len(status.Members) == 0 {
		parts = append(parts, "No active etcd members found")
	}

	if len(parts) == 0 {
		return "Cluster health check failed"
	}
	return strings.Join(parts, "\n")
}
