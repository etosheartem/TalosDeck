package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"talosdeck/internal/reconcile"
)

// Simulate upgrading the binary after an older process persisted a running job.
// The only supported legacy migration is quarantine, never executable replay.
func TestPersistedWorkflowUpgradeQuarantinesWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		workflow, plan, step int
	}{
		{"legacy-unversioned", 0, 0, 0}, {"future-workflow", 2, 1, 1},
		{"future-plan", 1, 2, 1}, {"future-step", 1, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			now := time.Now().UTC()
			identity := reconcile.Identity{ProviderID: "provider", ResourceID: "117", Generation: "stable-uuid", OwnerID: "cluster"}
			evidence := reconcile.Observation{State: "unknown", Identity: identity, ObservedAt: now}
			original := Job{ID: "f43db3f9-94c5-4308-8a7e-1ac376ea2570", Status: "running", WorkflowVersion: tc.workflow, PlanVersion: tc.plan, StepSchemaVersion: tc.step,
				Request: Request{Kind: "provision"}, CreatedAt: now, UpdatedAt: now,
				Intents: []reconcile.Intent{{ID: "create", Action: "create", WorkflowVersion: 1, PlanVersion: 1, StepSchemaVersion: 1, Identity: identity, CreatedAt: now, ExecutorEpoch: 42, Outcome: reconcile.Unknown, Evidence: &evidence}}}
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, original.ID+".json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			runner := func(context.Context, *Execution, Request) error { calls.Add(1); return nil }
			m, err := OpenCluster(dir, "cluster", runner)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			got, _ := m.Get(original.ID)
			if got.Status != "interrupted" || got.ReconciliationOutcome != reconcile.RequiresReview || got.Reviewed {
				t.Fatalf("not quarantined: %+v", got)
			}
			if !reflect.DeepEqual(got.Intents, original.Intents) || !reflect.DeepEqual(got.Request, original.Request) {
				t.Fatal("migration altered original intent, evidence or request")
			}
			if err = m.Acknowledge(original.ID, "operator"); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				result, err := m.ObserveIntent(context.Background(), original.ID, "create", reconcile.Observation{State: "exists", Identity: identity, ObservedAt: time.Now().UTC()})
				if err != nil || result != reconcile.RequiresReview {
					t.Fatalf("incompatible workflow proved success after review: %q %v", result, err)
				}
			}
			if err = m.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenCluster(dir, "cluster", runner)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			got, _ = reopened.Get(original.ID)
			if calls.Load() != 0 || got.Status != "interrupted" || got.ReconciliationOutcome != reconcile.RequiresReview {
				t.Fatalf("restart replayed or released old workflow: calls=%d job=%+v", calls.Load(), got)
			}
			if got.WorkflowVersion != tc.workflow || got.PlanVersion != tc.plan || got.StepSchemaVersion != tc.step {
				t.Fatal("review silently promoted incompatible versions")
			}
		})
	}
}
