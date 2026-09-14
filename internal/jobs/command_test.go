package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"talosdeck/internal/reconcile"
)

func runCommandFixture(t *testing.T, runner Runner, authority reconcile.Authority) (*Manager, Job) {
	t.Helper()
	m, err := Open(t.TempDir(), runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if authority != nil {
		if err = m.SetExecutionAuthority(authority, "command-executor", 1); err != nil {
			t.Fatal(err)
		}
	}
	j, err := m.Submit(Request{Kind: "command-test"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	j, err = m.Get(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m, j
}

func TestCommandIntentPersistedBeforeDispatchAndTargetRedacted(t *testing.T) {
	const sensitiveTarget = "https://user:fixture-secret@private-host/api?token=fixture-token"
	var calls atomic.Int32
	m, j := runCommandFixture(t, func(ctx context.Context, e *Execution, _ Request) error {
		return reconcile.Mutate(ctx, "provider.fixture.create", sensitiveTarget, func() error {
			calls.Add(1)
			raw, err := os.ReadFile(filepath.Join(e.manager.dir, e.id+".json"))
			if err != nil {
				return err
			}
			var saved Job
			if err = json.Unmarshal(raw, &saved); err != nil {
				return err
			}
			if len(saved.Intents) != 1 || saved.Intents[0].Outcome != reconcile.Unknown || saved.Intents[0].Evidence != nil {
				return errors.New("dispatch preceded durable unknown intent")
			}
			if strings.Contains(string(raw), "fixture-secret") || strings.Contains(string(raw), "private-host") || strings.Contains(string(raw), "fixture-token") {
				return errors.New("target leaked into journal")
			}
			return nil
		})
	}, nil)
	if calls.Load() != 1 || j.Status != "succeeded" || len(j.Intents) != 1 || !reconcile.CommandReceipt(j.Intents[0]) {
		t.Fatalf("receipt missing: calls=%d job=%+v", calls.Load(), j)
	}
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(sensitiveTarget)))
	if j.Intents[0].Identity.ResourceID != expected || j.Intents[0].Evidence.State != "acknowledged" {
		t.Fatal("not an opaque command acknowledgement")
	}
	raw, err := os.ReadFile(filepath.Join(m.dir, j.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{sensitiveTarget, "fixture-secret", "fixture-token", "private-host"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("journal leaked %q", secret)
		}
	}
}

func TestCommandAuthorityLossAfterPersistBlocksDispatch(t *testing.T) {
	var calls atomic.Int32
	_, j := runCommandFixture(t, func(ctx context.Context, e *Execution, _ Request) error {
		return reconcile.Mutate(ctx, "talos.reboot", "fixture-node", func() error { calls.Add(1); return nil })
	}, &authorityExpiresAfter{allow: 2})
	if calls.Load() != 0 || j.Status != "interrupted" || len(j.Intents) != 1 || j.Intents[0].Outcome != reconcile.Unknown {
		t.Fatalf("late authority loss admitted call: %+v calls=%d", j, calls.Load())
	}
}

func TestCommandAmbiguityStopsRetryAndFurtherDispatch(t *testing.T) {
	var calls atomic.Int32
	_, j := runCommandFixture(t, func(ctx context.Context, e *Execution, _ Request) error {
		for _, action := range []string{"provider.create", "provider.create", "provider.delete"} {
			err := reconcile.Mutate(ctx, action, "fixture-resource", func() error { calls.Add(1); return errors.New("response lost with credential=fixture-secret") })
			if !errors.Is(err, ErrUncertain) {
				t.Errorf("ambiguous dispatch did not block: %v", err)
			}
		}
		return nil // Even an incorrect caller swallowing errors must remain uncertain.
	}, nil)
	if calls.Load() != 1 || j.Status != "interrupted" || len(j.Intents) != 1 || j.Intents[0].Outcome != reconcile.Unknown {
		t.Fatalf("ambiguous call retried: calls=%d job=%+v", calls.Load(), j)
	}
	if strings.Contains(j.Error, "fixture-secret") {
		t.Fatal("transport error leaked credential")
	}
}

func TestCommandReceiptDoesNotProveResourceConvergence(t *testing.T) {
	_, j := runCommandFixture(t, func(ctx context.Context, e *Execution, _ Request) error {
		if err := e.BeginIntent(ctx, "resource-create", "create", reconcile.Identity{ProviderID: "p", ResourceID: "r", Generation: "g", OwnerID: "c"}); err != nil {
			return err
		}
		return reconcile.Mutate(ctx, "provider.create", "fixture-resource", func() error { return nil })
	}, nil)
	if j.Status != "interrupted" || j.ReconciliationOutcome != reconcile.Unknown || len(j.Intents) != 2 || j.Intents[0].Outcome != reconcile.Unknown || !reconcile.CommandReceipt(j.Intents[1]) {
		t.Fatalf("receipt incorrectly proved infrastructure convergence: %+v", j)
	}
}

func TestCommandExplicitRejectionPermitsLaterRetry(t *testing.T) {
	rejection := errors.New("429 eviction denied by PDB")
	var calls atomic.Int32
	_, j := runCommandFixture(t, func(ctx context.Context, e *Execution, _ Request) error {
		err := reconcile.Mutate(ctx, "k8s.evict", "fixture-pod", func() error { calls.Add(1); return reconcile.Rejected(rejection) })
		if !errors.Is(err, rejection) {
			return fmt.Errorf("rejection lost: %w", err)
		}
		return reconcile.Mutate(ctx, "k8s.evict", "fixture-pod", func() error { calls.Add(1); return nil })
	}, nil)
	if calls.Load() != 2 || j.Status != "succeeded" || len(j.Intents) != 2 || !reconcile.CommandReceipt(j.Intents[0]) || !reconcile.CommandReceipt(j.Intents[1]) || j.Intents[0].Outcome != "failed" {
		t.Fatalf("explicit rejection prevented safe retry: %+v calls=%d", j, calls.Load())
	}
}

func TestStrictAuthorityBlocksRunnerAndManualAdmission(t *testing.T) {
	var calls atomic.Int32
	m, err := Open(t.TempDir(), func(context.Context, *Execution, Request) error { calls.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.RequireExecutionAuthority()
	if release, err := m.ReserveManual(); err == nil {
		release()
		t.Fatal("unfenced manual admitted")
	}
	j, err := m.Submit(Request{Kind: "unfenced"}, "operator")
	if err == nil {
		m.wg.Wait()
		j, _ = m.Get(j.ID)
		if j.Status != "interrupted" {
			t.Fatalf("unfenced job status=%s", j.Status)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unfenced runner dispatched")
	}
}

func TestManualUnknownJournalBlocksLaterJob(t *testing.T) {
	m, err := Open(t.TempDir(), func(context.Context, *Execution, Request) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	release, err := m.ReserveManual()
	if err != nil {
		t.Fatal(err)
	}
	ctx, finish := m.ManualContext(context.Background(), "operator")
	var calls atomic.Int32
	err = reconcile.Mutate(ctx, "provider.delete", "fixture-resource", func() error { calls.Add(1); return errors.New("response lost") })
	if !errors.Is(err, ErrUncertain) {
		t.Errorf("manual ambiguity not surfaced: %v", err)
	}
	if err = finish(err); !errors.Is(err, ErrUncertain) {
		t.Errorf("finish lost uncertainty: %v", err)
	}
	release()
	all := m.List()
	if calls.Load() != 1 || len(all) != 1 || all[0].Status != "interrupted" || all[0].Reviewed || all[0].Intents[0].Outcome != reconcile.Unknown {
		t.Fatalf("manual uncertainty lost: %+v", all)
	}
	if _, err = m.Submit(Request{Kind: "must-block"}, "operator"); !errors.Is(err, ErrBusy) {
		t.Fatalf("unknown manual journal allowed job: %v", err)
	}
}

func TestManualCanceledAfterReceiptDoesNotReturnSuccess(t *testing.T) {
	m, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	release, err := m.ReserveManual()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, finish := m.ManualContext(base, "operator")
	if err = reconcile.Mutate(ctx, "talos.reboot", "fixture-node", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = finish(nil); err == nil {
		t.Fatal("canceled manual request returned success despite interrupted journal")
	}
	all := m.List()
	if len(all) != 1 || all[0].Status != "interrupted" {
		t.Fatalf("canceled manual result: %+v", all)
	}
}
