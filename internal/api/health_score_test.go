package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
	"talosdeck/internal/health"
)

type healthCollectFunc func(context.Context) health.Snapshot

func (f healthCollectFunc) Collect(ctx context.Context) health.Snapshot { return f(ctx) }
func testHealthSnapshot(id string) health.Snapshot {
	now := time.Now().UTC()
	return health.Snapshot{ID: uuid.NewString(), ClusterID: id, CheckedAt: now, Checks: []health.Check{{ID: "node-1", Category: "nodes", State: "critical", Reason: "node-not-ready", Title: "Node not ready", ObservedAt: now}}}
}
func TestHealthCacheSingleFlightDetachedAndDurable(t *testing.T) {
	ctx := context.Background()
	id := uuid.NewString()
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	expected := testHealthSnapshot(id)
	source := healthCollectFunc(func(context.Context) health.Snapshot {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return expected
	})
	h := newHealthMonitor(ctx, id, source, store)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.Refresh(ctx) }()
	<-started
	// Pure cached reads do not wait for network or report an invented score.
	if r := health.Evaluate(h.Snapshot(ctx), time.Now()); r.Score != nil {
		t.Fatal("empty snapshot produced score")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	h.Refresh(canceled)
	if calls.Load() != 1 {
		t.Fatal("parallel request duplicated collection")
	}
	close(release)
	wg.Wait()
	copy := h.Snapshot(ctx)
	copy.Checks[0].Title = "caller mutation"
	if h.Snapshot(ctx).Checks[0].Title != expected.Checks[0].Title {
		t.Fatal("cached snapshot aliased")
	}
	reopened := newHealthMonitor(ctx, id, nil, store)
	if reopened.HealthError() != nil || reopened.Snapshot(ctx).ID != expected.ID {
		t.Fatal("snapshot lost on restart")
	}
	foreign := newHealthMonitor(ctx, uuid.NewString(), nil, store)
	if foreign.Snapshot(ctx).ID != "" {
		t.Fatal("cross-cluster snapshot leak")
	}
}
func TestHealthCanceledRefreshPreservesSnapshotAndPanicReleasesWaiters(t *testing.T) {
	ctx := context.Background()
	id := uuid.NewString()
	expected := testHealthSnapshot(id)
	h := newHealthMonitor(ctx, id, healthCollectFunc(func(context.Context) health.Snapshot { return expected }), nil)
	h.Refresh(ctx)
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	h.Refresh(stopped)
	if h.Snapshot(ctx).ID != expected.ID {
		t.Fatal("canceled collection replaced previous snapshot")
	}
	h.collector = healthCollectFunc(func(context.Context) health.Snapshot { panic("private collector error") })
	h.Refresh(ctx)
	if h.HealthError() == nil || h.inFlight != nil {
		t.Fatal("panic left cache refresh locked")
	}
	h.collector = healthCollectFunc(func(context.Context) health.Snapshot { return expected })
	h.Refresh(ctx)
	if h.HealthError() != nil {
		t.Fatal("collection did not recover")
	}
}
func TestHealthScoreAPIShowsPartialCriticalForEveryReadRole(t *testing.T) {
	ctx := context.Background()
	id := uuid.NewString()
	snapshot := testHealthSnapshot(id)
	h := newHealthMonitor(ctx, id, healthCollectFunc(func(context.Context) health.Snapshot { return snapshot }), nil)
	h.Refresh(ctx)
	am := auth.NewAuthManager("test-password", "test-jwt-key")
	app := fiber.New()
	RegisterHealthScoreRoutes(app.Group("/api/clusters/"+id), h, am)
	for _, role := range []string{"viewer", "operator", "admin"} {
		token, _ := am.GenerateToken("fixture", role)
		req := httptest.NewRequest("GET", "/api/clusters/"+id+"/health-score", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var report health.Report
		if res.StatusCode != 200 || json.Unmarshal(raw, &report) != nil || report.Score != nil || report.Status != "critical" || report.SnapshotID != snapshot.ID || report.Coverage >= 100 {
			t.Fatalf("partial critical score not preserved for %s: %s", role, raw)
		}
		if !strings.Contains(res.Header.Get("Cache-Control"), "no-store") {
			t.Fatal("health data cacheable")
		}
	}
}

type failingHealthStore struct {
	healthStore
	fail bool
}

func (s *failingHealthStore) PutSecret(ctx context.Context, scope, kind, key string, raw []byte) error {
	if s.fail {
		return errors.New("private-storage-error-marker")
	}
	return s.healthStore.PutSecret(ctx, scope, kind, key, raw)
}
func TestHealthPersistenceFailureDoesNotPublishUnsavedSnapshot(t *testing.T) {
	ctx := context.Background()
	id := uuid.NewString()
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wrapped := &failingHealthStore{healthStore: store}
	value := testHealthSnapshot(id)
	h := newHealthMonitor(ctx, id, healthCollectFunc(func(context.Context) health.Snapshot { return value }), wrapped)
	h.Refresh(ctx)
	saved := h.Snapshot(ctx).ID
	wrapped.fail = true
	value.ID = uuid.NewString()
	h.Refresh(ctx)
	if h.HealthError() == nil || strings.Contains(h.HealthError().Error(), "private-storage-error-marker") || h.Snapshot(ctx).ID != saved {
		t.Fatal("unsaved snapshot published or storage detail disclosed")
	}
	wrapped.fail = false
	h.Refresh(ctx)
	if h.HealthError() != nil || h.Snapshot(ctx).ID != value.ID {
		t.Fatal("snapshot did not recover after storage restored")
	}
}
