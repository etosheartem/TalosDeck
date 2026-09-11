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
	checkMu         sync.Mutex
	wg              sync.WaitGroup
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
	stopChan := w.stopChan
	w.wg.Add(1)
	w.mu.Unlock()

	log.Printf("[AlertWatcher] Background health watcher started (interval: %v)", w.interval)

	go func(stopCh chan struct{}) {
		defer w.wg.Done()
		defer func() {
			w.mu.Lock()
			w.running = false
			w.mu.Unlock()
		}()

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
			case <-stopCh:
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
	}(stopChan)
}

// Stop gracefully signals the watcher background loop to terminate and waits for it (ALT-04, ALT-08).
func (w *Watcher) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	close(w.stopChan)
	w.mu.Unlock()

	w.wg.Wait()
}

// CheckClusterHealth checks node status and etcd health, detecting state transitions.
// Network calls and alert dispatches are performed outside of state lock (ALT-02).
// Alert sending failures preserve transition state for retry (ALT-01).
func (w *Watcher) CheckClusterHealth(ctx context.Context) error {
	if w.manager == nil {
		return nil
	}

	w.checkMu.Lock()
	defer w.checkMu.Unlock()

	// 1. Inspect Cluster Nodes (network I/O without holding w.mu - ALT-02)
	nodes, err := w.manager.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("failed to query node list: %w", err)
	}

	// 2. Inspect etcd Cluster Health (network I/O without holding w.mu - ALT-02)
	etcdStatus, _ := w.manager.GetEtcdStatus(ctx)

	now := time.Now().UTC()

	type alertTask struct {
		desc      string
		sendFn    func() error
		onSuccess func()
	}
	var alertsToSend []alertTask

	w.mu.Lock()
	isInitial := w.initialCheck

	for _, n := range nodes {
		prev, exists := w.nodeStates[n.IP]
		nodeIP := n.IP
		hostname := n.Hostname
		ready := n.Ready
		role := n.Role
		cpuUsage := n.CPUUsage
		servicesSummary := n.ServicesSummary

		if !exists {
			snapshot := NodeStateSnapshot{
				IP:          nodeIP,
				Hostname:    hostname,
				Ready:       ready,
				Role:        role,
				LastSeen:    now,
				LastChanged: now,
				HighCPU:     cpuUsage >= 90,
			}

			if !isInitial && !ready && w.alerts != nil && w.alerts.IsEnabled() {
				alertsToSend = append(alertsToSend, alertTask{
					desc: fmt.Sprintf("new NotReady node %s", hostname),
					sendFn: func() error {
						return w.alerts.SendNodeStatusAlert(nodeIP, hostname, "NotReady", "New node detected in NotReady state")
					},
					onSuccess: func() {
						w.mu.Lock()
						defer w.mu.Unlock()
						w.nodeStates[nodeIP] = snapshot
					},
				})
			} else {
				w.nodeStates[nodeIP] = snapshot
			}
			continue
		}

		if prev.Ready != ready {
			targetReady := ready
			if targetReady {
				alertsToSend = append(alertsToSend, alertTask{
					desc: fmt.Sprintf("Node %s RECOVERED to Ready", hostname),
					sendFn: func() error {
						if w.alerts != nil && w.alerts.IsEnabled() {
							return w.alerts.SendNodeStatusAlert(nodeIP, hostname, "Ready", "Node communication re-established and healthy")
						}
						return nil
					},
					onSuccess: func() {
						w.mu.Lock()
						defer w.mu.Unlock()
						if s, ok := w.nodeStates[nodeIP]; ok {
							s.Ready = true
							s.LastChanged = time.Now().UTC()
							w.nodeStates[nodeIP] = s
						}
					},
				})
			} else {
				details := "Node is unreachable or reporting degraded core services"
				if servicesSummary != nil {
					details = fmt.Sprintf("Services: etcd=%s, kubelet=%s, containerd=%s, apid=%s",
						servicesSummary.Etcd, servicesSummary.Kubelet,
						servicesSummary.Containerd, servicesSummary.Apid)
				}
				alertsToSend = append(alertsToSend, alertTask{
					desc: fmt.Sprintf("Node %s NOT READY", hostname),
					sendFn: func() error {
						if w.alerts != nil && w.alerts.IsEnabled() {
							return w.alerts.SendNodeStatusAlert(nodeIP, hostname, "NotReady", details)
						}
						return nil
					},
					onSuccess: func() {
						w.mu.Lock()
						defer w.mu.Unlock()
						if s, ok := w.nodeStates[nodeIP]; ok {
							s.Ready = false
							s.LastChanged = time.Now().UTC()
							w.nodeStates[nodeIP] = s
						}
					},
				})
			}
		}

		// CPU check
		if cpuUsage >= 90 && !prev.HighCPU {
			alertsToSend = append(alertsToSend, alertTask{
				desc: fmt.Sprintf("Node %s CPU spike (%d%%)", hostname, cpuUsage),
				sendFn: func() error {
					if w.alerts != nil && w.alerts.IsEnabled() {
						return w.alerts.SendResourceAlert(nodeIP, hostname, "CPU", cpuUsage, "CPU utilization is critically high (>= 90%)")
					}
					return nil
				},
				onSuccess: func() {
					w.mu.Lock()
					defer w.mu.Unlock()
					if s, ok := w.nodeStates[nodeIP]; ok {
						s.HighCPU = true
						w.nodeStates[nodeIP] = s
					}
				},
			})
		} else if cpuUsage < 80 && prev.HighCPU {
			alertsToSend = append(alertsToSend, alertTask{
				desc: fmt.Sprintf("Node %s CPU normalized (%d%%)", hostname, cpuUsage),
				sendFn: func() error {
					if w.alerts != nil && w.alerts.IsEnabled() {
						return w.alerts.SendAlert(LevelRecovered, fmt.Sprintf("CPU Normalized on %s", hostname),
							fmt.Sprintf("Node: %s (%s)\nResource: CPU\nUsage: %d%%\nDetails: CPU utilization has returned to normal range", nodeIP, hostname, cpuUsage))
					}
					return nil
				},
				onSuccess: func() {
					w.mu.Lock()
					defer w.mu.Unlock()
					if s, ok := w.nodeStates[nodeIP]; ok {
						s.HighCPU = false
						w.nodeStates[nodeIP] = s
					}
				},
			})
		}

		prev.Hostname = hostname
		prev.Role = role
		prev.LastSeen = now
		w.nodeStates[nodeIP] = prev
	}

	if etcdStatus != nil {
		if w.lastEtcdHealthy == nil {
			healthy := etcdStatus.Healthy
			if !isInitial && !healthy && w.alerts != nil && w.alerts.IsEnabled() {
				details := formatEtcdAlertDetails(etcdStatus)
				alertsToSend = append(alertsToSend, alertTask{
					desc: "etcd initial degraded",
					sendFn: func() error {
						return w.alerts.SendEtcdAlert(false, details)
					},
					onSuccess: func() {
						w.mu.Lock()
						defer w.mu.Unlock()
						h := false
						w.lastEtcdHealthy = &h
					},
				})
			} else {
				w.lastEtcdHealthy = &healthy
			}
		} else if *w.lastEtcdHealthy != etcdStatus.Healthy {
			healthy := etcdStatus.Healthy
			if healthy {
				alertsToSend = append(alertsToSend, alertTask{
					desc: "etcd RECOVERED",
					sendFn: func() error {
						if w.alerts != nil && w.alerts.IsEnabled() {
							return w.alerts.SendEtcdAlert(true, fmt.Sprintf("All %d member(s) healthy and operational", len(etcdStatus.Members)))
						}
						return nil
					},
					onSuccess: func() {
						w.mu.Lock()
						defer w.mu.Unlock()
						h := true
						w.lastEtcdHealthy = &h
					},
				})
			} else {
				details := formatEtcdAlertDetails(etcdStatus)
				alertsToSend = append(alertsToSend, alertTask{
					desc: "etcd DEGRADED",
					sendFn: func() error {
						if w.alerts != nil && w.alerts.IsEnabled() {
							return w.alerts.SendEtcdAlert(false, details)
						}
						return nil
					},
					onSuccess: func() {
						w.mu.Lock()
						defer w.mu.Unlock()
						h := false
						w.lastEtcdHealthy = &h
					},
				})
			}
		}
	}

	w.initialCheck = false
	w.lastCheckTime = now
	w.mu.Unlock()

	// Dispatch alerts OUTSIDE w.mu (ALT-02)
	// If dispatch fails, state transition is NOT marked, so it retries next tick (ALT-01)
	for _, task := range alertsToSend {
		if err := task.sendFn(); err != nil {
			log.Printf("[AlertWatcher] Failed to send alert (%s): %v. State not updated, will retry on next tick.", task.desc, err)
		} else {
			task.onSuccess()
		}
	}

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

// GetActiveAlertsCount returns the number of active alerts in the cluster (unhealthy nodes, high CPU, degraded etcd).
func (w *Watcher) GetActiveAlertsCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()

	count := 0
	for _, n := range w.nodeStates {
		if !n.Ready {
			count++
		}
		if n.HighCPU {
			count++
		}
	}
	if w.lastEtcdHealthy != nil && !*w.lastEtcdHealthy {
		count++
	}
	return count
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
