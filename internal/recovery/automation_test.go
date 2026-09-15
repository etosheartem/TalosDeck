package recovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
)

func activatedStore(t *testing.T) (*clusters.Store, ActivationOptions) {
	t.Helper()
	o, path := activationFixture(t)
	var j jobs.Job
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &j)
	o.Reviews = []JobReview{{JobID: j.ID, Reason: "Reviewed; no command resumed"}}
	if _, err := Activate(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	store, err := clusters.Open(filepath.Join(o.DataDir, "talosdeck.db"), o.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, o
}

func TestResumeAutomationRequiresReviewedActivationAndExplicitReason(t *testing.T) {
	ctx := context.Background()
	o, s := fixture(t)
	// A normal installation has no paused automation to resume.
	if _, err := ResumeAutomation(ctx, s, o.DataDir, "admin", "routine"); err == nil {
		t.Fatal("resume accepted outside recovery")
	}
	s.Close()

	store, opts := activatedStore(t)
	if _, err := ResumeAutomation(ctx, store, opts.DataDir, "", "reviewed"); err == nil {
		t.Fatal("anonymous resume accepted")
	}
	if _, err := ResumeAutomation(ctx, store, opts.DataDir, "admin", ""); err == nil {
		t.Fatal("resume accepted without a reason")
	}
	state, err := ReadState(ctx, store, opts.DataDir)
	if err != nil || state.SafeMode || !state.AutomationPaused {
		t.Fatal("activation did not leave automation paused", state, err)
	}
	if _, err = ResumeAutomation(ctx, store, opts.DataDir, "admin", "Interrupted work reviewed; schedules may run again"); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(ctx, store, opts.DataDir)
	if err != nil || state.SafeMode || state.AutomationPaused {
		t.Fatal("recorded decision did not resume automation on the next read", state, err)
	}
	if _, err = ResumeAutomation(ctx, store, opts.DataDir, "admin", "again"); err == nil {
		t.Fatal("resume accepted twice")
	}
}

func TestResumeDecisionDoesNotCarryIntoAnotherRestore(t *testing.T) {
	ctx := context.Background()
	store, opts := activatedStore(t)
	if _, err := ResumeAutomation(ctx, store, opts.DataDir, "admin", "Reviewed"); err != nil {
		t.Fatal(err)
	}
	// A later restore writes a new sentinel; the old decision must not apply to it.
	marker := []byte(`{"requiresReview":true,"restoredAt":"second-restore"}`)
	if err := os.WriteFile(filepath.Join(opts.DataDir, SafeModeFile), marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSecret(ctx, "__fleet__", "recovery", "required", marker); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(ctx, store, opts.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.SafeMode || !state.AutomationPaused {
		t.Fatal("a stale resume decision unlocked a new restore", state)
	}
}
