package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
	"talosdeck/internal/health"
)

type HealthSnapshotProvider interface {
	Snapshot(context.Context) health.Snapshot
}
type healthCollector interface {
	Collect(context.Context) health.Snapshot
}
type healthStore interface {
	GetSecret(context.Context, string, string, string) ([]byte, error)
	PutSecret(context.Context, string, string, string, []byte) error
}
type healthMonitor struct {
	mu         sync.Mutex
	clusterID  string
	collector  healthCollector
	store      healthStore
	latest     health.Snapshot
	inFlight   chan struct{}
	storageErr error
}

func newHealthMonitor(ctx context.Context, clusterID string, collector healthCollector, store healthStore) *healthMonitor {
	h := &healthMonitor{clusterID: clusterID, collector: collector, store: store, latest: health.Snapshot{ClusterID: clusterID, Checks: []health.Check{}}}
	if store == nil {
		return h
	}
	raw, err := store.GetSecret(ctx, clusterID, "health", "latest")
	if errors.Is(err, clusters.ErrNotFound) {
		return h
	}
	if err != nil {
		h.storageErr = errors.New("health snapshot storage unavailable")
		return h
	}
	var saved health.Snapshot
	if len(raw) > 4<<20 || json.Unmarshal(raw, &saved) != nil || saved.ClusterID != clusterID || saved.ID == "" || saved.CheckedAt.IsZero() {
		h.storageErr = errors.New("saved health snapshot is invalid")
		return h
	}
	h.latest = saved
	return h
}
func copyHealth(value health.Snapshot) health.Snapshot {
	raw, _ := json.Marshal(value)
	var out health.Snapshot
	_ = json.Unmarshal(raw, &out)
	return out
}

// Snapshot is a pure cached read. Opening the dashboard never creates a job or
// repeats infrastructure requests; background and manual diagnostics refresh it.
func (h *healthMonitor) Snapshot(context.Context) health.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return copyHealth(h.latest)
}
func (h *healthMonitor) HealthError() error { h.mu.Lock(); defer h.mu.Unlock(); return h.storageErr }
func (h *healthMonitor) Refresh(ctx context.Context) (out health.Snapshot) {
	if ctx.Err() != nil {
		return h.Snapshot(ctx)
	}
	h.mu.Lock()
	if done := h.inFlight; done != nil {
		h.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		return h.Snapshot(ctx)
	}
	if h.collector == nil {
		h.mu.Unlock()
		return h.Snapshot(ctx)
	}
	done := make(chan struct{})
	h.inFlight = done
	h.mu.Unlock()
	defer func() {
		recovered := recover()
		h.mu.Lock()
		if recovered != nil {
			h.storageErr = errors.New("health collection interrupted")
			out = copyHealth(h.latest)
		}
		h.inFlight = nil
		close(done)
		h.mu.Unlock()
	}()
	check, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	value := h.collector.Collect(check)
	cancel()
	// A stopped diagnostic job or shutdown must not replace a valid snapshot with
	// cancellation artifacts. An internal collection timeout may retain partial data.
	if ctx.Err() != nil {
		return h.Snapshot(ctx)
	}
	if value.ClusterID != h.clusterID || value.ID == "" || value.CheckedAt.IsZero() {
		h.mu.Lock()
		h.storageErr = errors.New("health collector returned invalid scope")
		h.mu.Unlock()
		return h.Snapshot(ctx)
	}
	raw, err := json.Marshal(value)
	if err == nil && len(raw) > 4<<20 {
		err = errors.New("health snapshot exceeds limit")
	}
	if err == nil && h.store != nil {
		save, stop := context.WithTimeout(ctx, 5*time.Second)
		err = h.store.PutSecret(save, h.clusterID, "health", "latest", raw)
		stop()
	}
	h.mu.Lock()
	if err != nil {
		h.storageErr = errors.New("health snapshot could not be saved")
	} else {
		h.storageErr = nil
		h.latest = copyHealth(value)
	}
	h.mu.Unlock()
	return h.Snapshot(ctx)
}
func (h *healthMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		h.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func RegisterHealthScoreRoutes(router fiber.Router, source HealthSnapshotProvider, am *auth.AuthManager) {
	router.Get("/health-score", auth.RequireAuth(am), func(c *fiber.Ctx) error {
		if source == nil {
			return fiber.NewError(503, "Health assessment unavailable")
		}
		if monitored, ok := source.(interface{ HealthError() error }); ok && monitored.HealthError() != nil {
			return fiber.NewError(503, "Health snapshot storage unavailable")
		}
		snapshot := source.Snapshot(c.UserContext())
		c.Set("Cache-Control", "no-store")
		return c.JSON(health.Evaluate(snapshot, time.Now().UTC()))
	})
}
