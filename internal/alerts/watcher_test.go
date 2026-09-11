package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"talosdeck/internal/talos"
)

type mockClusterInspector struct {
	mu         sync.Mutex
	nodes      []*talos.NodeOverview
	etcdStatus *talos.EtcdClusterStatus
}

func (m *mockClusterInspector) ListNodes(ctx context.Context) ([]*talos.NodeOverview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*talos.NodeOverview, len(m.nodes))
	for i, n := range m.nodes {
		nodeCopy := *n
		copied[i] = &nodeCopy
	}
	return copied, nil
}

func (m *mockClusterInspector) GetEtcdStatus(ctx context.Context) (*talos.EtcdClusterStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.etcdStatus, nil
}

func (m *mockClusterInspector) setNodeReady(ip string, ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.nodes {
		if n.IP == ip {
			n.Ready = ready
			return
		}
	}
}

func (m *mockClusterInspector) setNodeCPU(ip string, cpu int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.nodes {
		if n.IP == ip {
			n.CPUUsage = cpu
			return
		}
	}
}

func (m *mockClusterInspector) addNode(node *talos.NodeOverview) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = append(m.nodes, node)
}

func (m *mockClusterInspector) setEtcdStatus(status *talos.EtcdClusterStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.etcdStatus = status
}

func (m *mockClusterInspector) setEtcdHealthy(healthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.etcdStatus.Healthy = healthy
}

func TestWatcher_StateTransitionsAndDeduplication(t *testing.T) {
	var alertCount int32
	var lastAlertText string
	var alertMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&alertCount, 1)
		body, _ := io.ReadAll(r.Body)
		var payload telegramPayload
		_ = json.Unmarshal(body, &payload)

		alertMu.Lock()
		lastAlertText = payload.Text
		alertMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	alertSvc := NewTelegramService("bot:token", "-100123", true)
	alertSvc.SetAPIBaseURL(server.URL)

	mockCluster := &mockClusterInspector{
		nodes: []*talos.NodeOverview{
			{
				IP:       "10.42.0.110",
				Hostname: "talos-cp-1",
				Ready:    true,
				Role:     "controlplane",
				CPUUsage: 20,
			},
			{
				IP:       "10.42.0.111",
				Hostname: "talos-worker-1",
				Ready:    true,
				Role:     "worker",
				CPUUsage: 35,
			},
		},
		etcdStatus: &talos.EtcdClusterStatus{
			Healthy: true,
			Members: []talos.EtcdMemberInfo{
				{ID: "1", Hostname: "talos-cp-1", Healthy: true},
			},
		},
	}

	watcher := NewWatcher(mockCluster, alertSvc, 100*time.Millisecond)

	ctx := context.Background()

	// 1. Initial run: records state, should NOT alert because nodes are initially healthy
	err := watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 0 {
		t.Errorf("expected 0 alerts on initial healthy scan, got %d", alertCount)
	}

	// 2. Second run without state change (deduplication / debounce verification)
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("second check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 0 {
		t.Errorf("expected 0 alerts when state is unchanged, got %d", alertCount)
	}

	// 3. Node switches Ready -> NotReady (failure transition)
	mockCluster.setNodeReady("10.42.0.111", false)
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Fatalf("expected 1 alert for Ready -> NotReady, got %d", alertCount)
	}
	alertMu.Lock()
	if !strings.Contains(lastAlertText, "CRITICAL") || !strings.Contains(lastAlertText, "talos-worker-1") {
		t.Errorf("expected critical alert for talos-worker-1, got: %s", lastAlertText)
	}
	alertMu.Unlock()

	// 4. Third run with node still NotReady -> should NOT alert again (deduplication!)
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 1 {
		t.Fatalf("expected alert count to stay 1, got %d (alert fatigue failure)", alertCount)
	}

	// 5. Node switches NotReady -> Ready (recovery transition)
	mockCluster.setNodeReady("10.42.0.111", true)
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 2 {
		t.Fatalf("expected 2 alerts after recovery, got %d", alertCount)
	}
	alertMu.Lock()
	if !strings.Contains(lastAlertText, "RECOVERED") || !strings.Contains(lastAlertText, "Ready") {
		t.Errorf("expected recovery alert, got: %s", lastAlertText)
	}
	alertMu.Unlock()

	// 6. CPU spike to 95% -> should alert once
	mockCluster.setNodeCPU("10.42.0.111", 95)
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 3 {
		t.Fatalf("expected 3 alerts after CPU spike, got %d", alertCount)
	}
	alertMu.Lock()
	if !strings.Contains(lastAlertText, "High CPU") {
		t.Errorf("expected High CPU alert, got: %s", lastAlertText)
	}
	alertMu.Unlock()

	// 7. CPU stays high -> no second alert
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 3 {
		t.Fatalf("expected alert count to stay 3, got %d", alertCount)
	}

	// 8. CPU drops to 50% -> triggers CPU normalized alert
	mockCluster.setNodeCPU("10.42.0.111", 50)
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 4 {
		t.Fatalf("expected 4 alerts after CPU normalization, got %d", alertCount)
	}

	// 9. etcd degrades -> triggers etcd alert
	mockCluster.setEtcdHealthy(false)
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 5 {
		t.Fatalf("expected 5 alerts after etcd degraded, got %d", alertCount)
	}

	// 10. etcd recovers -> triggers etcd recovered alert
	mockCluster.setEtcdHealthy(true)
	err = watcher.CheckClusterHealth(ctx)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if atomic.LoadInt32(&alertCount) != 6 {
		t.Fatalf("expected 6 alerts after etcd recovery, got %d", alertCount)
	}
}

func TestWatcher_StartAndStop(t *testing.T) {
	mockCluster := &mockClusterInspector{
		nodes: []*talos.NodeOverview{},
	}
	alertSvc := NewTelegramService("", "", false)

	watcher := NewWatcher(mockCluster, alertSvc, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher.Start(ctx)

	status := watcher.GetStatus()
	if !status.Running {
		t.Error("expected watcher to be running")
	}

	time.Sleep(120 * time.Millisecond)

	watcher.Stop()

	status = watcher.GetStatus()
	if status.Running {
		t.Error("expected watcher to be stopped")
	}
}

func TestWatcher_NoAlertSuppressionOnFailure(t *testing.T) {
	var shouldFail atomic.Bool
	shouldFail.Store(true)
	var alertAttempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&alertAttempts, 1)
		if shouldFail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok": false, "error": "server error"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	alertSvc := NewTelegramService("bot:token", "-100123", true)
	alertSvc.SetAPIBaseURL(server.URL)

	mockCluster := &mockClusterInspector{
		nodes: []*talos.NodeOverview{
			{IP: "10.42.0.111", Hostname: "talos-worker-1", Ready: true, Role: "worker"},
		},
	}
	watcher := NewWatcher(mockCluster, alertSvc, 100*time.Millisecond)
	ctx := context.Background()

	// Initial check (healthy)
	_ = watcher.CheckClusterHealth(ctx)

	// Node transitions to NotReady
	mockCluster.setNodeReady("10.42.0.111", false)

	// Tick 1: Telegram fails (returns 500)
	_ = watcher.CheckClusterHealth(ctx)
	if atomic.LoadInt32(&alertAttempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", alertAttempts)
	}

	// State should NOT be considered Ready=false yet because alert failed (ALT-01)
	snaps := watcher.GetNodeSnapshots()
	if snaps["10.42.0.111"].Ready == false {
		t.Errorf("expected node Ready in snapshot to still be true until alert delivery succeeds")
	}

	// Tick 2: Telegram is now back online (returns 200)
	shouldFail.Store(false)
	_ = watcher.CheckClusterHealth(ctx)
	if atomic.LoadInt32(&alertAttempts) != 2 {
		t.Fatalf("expected second attempt (retry on failure), got %d", alertAttempts)
	}

	// Now state is successfully updated
	snaps = watcher.GetNodeSnapshots()
	if snaps["10.42.0.111"].Ready != false {
		t.Errorf("expected node Ready in snapshot to be updated to false after delivery")
	}

	// Tick 3: No new alert should be sent (deduplication)
	_ = watcher.CheckClusterHealth(ctx)
	if atomic.LoadInt32(&alertAttempts) != 2 {
		t.Fatalf("expected alert attempts to stay 2, got %d", alertAttempts)
	}
}

func TestWatcher_FirstObservedFailuresRetryUntilDelivered(t *testing.T) {
	var shouldFail atomic.Bool
	shouldFail.Store(true)
	var alertAttempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&alertAttempts, 1)
		if shouldFail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok": false}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	alertSvc := NewTelegramService("bot:token", "-100123", true)
	alertSvc.SetAPIBaseURL(server.URL)
	mockCluster := &mockClusterInspector{nodes: []*talos.NodeOverview{}}
	watcher := NewWatcher(mockCluster, alertSvc, time.Second)

	// Finish the initial scan without either resource being present.
	if err := watcher.CheckClusterHealth(context.Background()); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	mockCluster.addNode(&talos.NodeOverview{IP: "10.42.0.120", Hostname: "new-worker", Role: "worker", Ready: false})

	// A first-seen NotReady node must remain uncommitted after a failed send.
	_ = watcher.CheckClusterHealth(context.Background())
	if _, exists := watcher.GetNodeSnapshots()["10.42.0.120"]; exists {
		t.Fatal("first-seen NotReady node was committed before alert delivery")
	}
	_ = watcher.CheckClusterHealth(context.Background())
	if got := atomic.LoadInt32(&alertAttempts); got != 2 {
		t.Fatalf("expected node alert retry, got %d attempts", got)
	}

	shouldFail.Store(false)
	_ = watcher.CheckClusterHealth(context.Background())
	if _, exists := watcher.GetNodeSnapshots()["10.42.0.120"]; !exists {
		t.Fatal("node state was not committed after successful delivery")
	}

	// A degraded etcd state first observed later follows the same retry rule.
	mockCluster.setEtcdStatus(&talos.EtcdClusterStatus{Healthy: false})
	shouldFail.Store(true)
	_ = watcher.CheckClusterHealth(context.Background())
	if watcher.lastEtcdHealthy != nil {
		t.Fatal("first degraded etcd state was committed before alert delivery")
	}
	_ = watcher.CheckClusterHealth(context.Background())
	if got := atomic.LoadInt32(&alertAttempts); got != 5 {
		t.Fatalf("expected etcd alert retry, got %d total attempts", got)
	}

	shouldFail.Store(false)
	_ = watcher.CheckClusterHealth(context.Background())
	if watcher.lastEtcdHealthy == nil || *watcher.lastEtcdHealthy {
		t.Fatal("etcd state was not committed after successful delivery")
	}
}

func TestWatcher_RestartAfterContextCancel(t *testing.T) {
	// ALT-03: context cancel must reset w.running to false so Start() can be called again
	mockCluster := &mockClusterInspector{nodes: []*talos.NodeOverview{}}
	alertSvc := NewTelegramService("", "", false)
	watcher := NewWatcher(mockCluster, alertSvc, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	watcher.Start(ctx)

	time.Sleep(30 * time.Millisecond)
	cancel() // Cancel context

	// Wait for goroutine to exit
	time.Sleep(50 * time.Millisecond)

	status := watcher.GetStatus()
	if status.Running {
		t.Errorf("expected watcher running=false after context cancel, got %v", status.Running)
	}

	// Start again with new context
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	watcher.Start(ctx2)

	status = watcher.GetStatus()
	if !status.Running {
		t.Errorf("expected watcher running=true after restart")
	}
	watcher.Stop()
}
