package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"talosdeck/internal/reconcile"
)

type epochAuthority struct{ epoch atomic.Uint64 }

func (a *epochAuthority) Validate(_ context.Context, _ string, epoch uint64) error {
	if a.epoch.Load() != epoch {
		return reconcile.ErrAuthority
	}
	return nil
}

func TestC8AmbiguousCreateDeleteNeverReplayed(t *testing.T) {
	for _, action := range []string{"create", "delete"} {
		t.Run(action, func(t *testing.T) {
			identity := reconcile.Identity{ProviderID: "provider", ResourceID: "117", Generation: "generation-uuid", OwnerID: "cluster-uuid"}
			var calls atomic.Int32
			m, err := Open(t.TempDir(), func(ctx context.Context, e *Execution, _ Request) error {
				if err := e.BeginIntent(ctx, "operation", action, identity); err != nil {
					return err
				}
				calls.Add(1) // Provider committed the action, response disappeared.
				if err := e.BeginIntent(ctx, "operation", action, identity); !errors.Is(err, ErrUncertain) {
					t.Errorf("replay authorized: %v", err)
				}
				return ErrUncertain
			})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			j, err := m.Submit(Request{Kind: "acceptance"}, "tester")
			if err != nil {
				t.Fatal(err)
			}
			m.wg.Wait()
			j, _ = m.Get(j.ID)
			if j.Status != "interrupted" || len(j.Intents) != 1 || calls.Load() != 1 {
				t.Fatal(j, calls.Load())
			}
			state := "exists"
			if action == "delete" {
				state = "absent"
			}
			result, err := m.ObserveIntent(context.Background(), j.ID, "operation", reconcile.Observation{State: state, Identity: identity, ObservedAt: time.Now().UTC()})
			if err != nil || result != "succeeded" {
				t.Fatal(result, err)
			}
			j, _ = m.Get(j.ID)
			if j.Status != "interrupted" || j.Reviewed {
				t.Fatal("observation resumed job")
			}
			if _, err = m.Submit(Request{Kind: "another"}, "tester"); !errors.Is(err, ErrBusy) {
				t.Fatal("review gate cleared", err)
			}
		})
	}
}

func TestC9OldExecutorRejectedBeforeRunnerAndNextStep(t *testing.T) {
	a := &epochAuthority{}
	a.epoch.Store(43)
	var called atomic.Bool
	m, err := Open(t.TempDir(), func(context.Context, *Execution, Request) error { called.Store(true); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.SetExecutionAuthority(a, "old-instance", 42); err != nil {
		t.Fatal(err)
	}
	j, err := m.Submit(Request{Kind: "test"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	j, _ = m.Get(j.ID)
	if called.Load() || j.Status != "interrupted" {
		t.Fatal("stale executor ran", j)
	}
	if _, err = m.ReserveManual(); err == nil {
		t.Fatal("manual mutation permitted")
	}
}

func TestC9EpochChangesBetweenCheckpoints(t *testing.T) {
	a := &epochAuthority{}
	a.epoch.Store(42)
	var secondMutation atomic.Bool
	m, err := Open(t.TempDir(), func(ctx context.Context, e *Execution, _ Request) error {
		if err := e.Checkpoint(ctx, "first", "authorized"); err != nil {
			return err
		}
		a.epoch.Store(43)
		if err := e.Checkpoint(ctx, "second", "must block"); err != nil {
			return err
		}
		secondMutation.Store(true)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.SetExecutionAuthority(a, "old", 42); err != nil {
		t.Fatal(err)
	}
	j, err := m.Submit(Request{Kind: "test"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	j, _ = m.Get(j.ID)
	if secondMutation.Load() || j.Status != "interrupted" {
		t.Fatal("superseded executor continued", j)
	}
}
