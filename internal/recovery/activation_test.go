package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
	"testing"
	"time"
)

type activationAuthority struct{ deny bool }

func (a activationAuthority) Validate(context.Context, string, uint64) error {
	if a.deny {
		return errors.New("denied")
	}
	return nil
}
func activationFixture(t *testing.T) (ActivationOptions, string) {
	t.Helper()
	o, s := fixture(t)
	if err := s.PutSecret(context.Background(), "__fleet__", "recovery", "required", []byte(`{"requiresReview":true,"restoredAt":"first"}`)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.WriteFile(filepath.Join(o.DataDir, SafeModeFile), []byte(`{"requiresReview":true,"restoredAt":"first"}`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(o.DataDir, "fleet-jobs")
	os.MkdirAll(dir, 0700)
	j := jobs.Job{ID: uuid.NewString(), ClusterID: "__fleet__", Status: "running", CreatedAt: time.Now(), UpdatedAt: time.Now(), WorkflowVersion: 1, PlanVersion: 1, StepSchemaVersion: 1, ReconciliationOutcome: "UNKNOWN"}
	raw, _ := json.Marshal(j)
	path := filepath.Join(dir, j.ID+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return ActivationOptions{DataDir: o.DataDir, KeyPath: o.KeyPath, Actor: "operator", FencingEvidence: "Old management machine permanently fenced by operator", ConfirmOldManagementFenced: true, Authority: activationAuthority{}, InstanceID: "new-instance", Epoch: 42, AuthorityIdentity: "independent-host-namespace", PersistAuthorityEpoch: func() error { return nil }}, path
}
func TestActivationRequiresExplicitReviewKeepsUnknownAndPausesAutomation(t *testing.T) {
	o, path := activationFixture(t)
	ctx := context.Background()
	if _, err := Activate(ctx, o); err == nil {
		t.Fatal("unreviewed running job activated")
	}
	var j jobs.Job
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &j)
	o.Reviews = []JobReview{{JobID: j.ID, Reason: "Provider checked; ambiguous result remains under manual review"}}
	receipt, err := Activate(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.AutomationPaused || receipt.ReviewDigest == "" {
		t.Fatal(receipt)
	}
	raw, _ = os.ReadFile(path)
	json.Unmarshal(raw, &j)
	if j.Status != "interrupted" || j.ReconciliationOutcome != "UNKNOWN" || !j.Reviewed {
		t.Fatal("activation resumed or relabelled job", j)
	}
	store, err := clusters.Open(filepath.Join(o.DataDir, "talosdeck.db"), o.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := ReadState(ctx, store, o.DataDir)
	if err != nil || state.SafeMode || !state.AutomationPaused || state.ActivationEpoch != 42 {
		t.Fatal(state, err)
	}
	if err = os.Remove(filepath.Join(o.DataDir, SafeModeFile)); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(ctx, store, o.DataDir)
	if err != nil || !state.SafeMode {
		t.Fatal("deleting marker unlocked restored DB", state, err)
	}
	if err = os.WriteFile(filepath.Join(o.DataDir, SafeModeFile), []byte(`{"requiresReview":true,"restoredAt":"new-restore"}`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(ctx, store, o.DataDir)
	if err != nil || !state.SafeMode {
		t.Fatal("receipt survived another restore", state, err)
	}
}
func TestActivationRejectsLiveServerJournalAndLostAuthority(t *testing.T) {
	o, _ := activationFixture(t)
	ctx := context.Background()
	store, err := clusters.Open(filepath.Join(o.DataDir, "talosdeck.db"), o.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Activate(ctx, o); err == nil {
		t.Fatal("live registry accepted")
	}
	store.Close()
	manager, err := jobs.OpenCluster(filepath.Join(o.DataDir, "fleet-jobs"), "__fleet__", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Activate(ctx, o); err == nil {
		t.Fatal("live job executor accepted")
	}
	manager.Close()
	o.Authority = activationAuthority{deny: true}
	if _, err = Activate(ctx, o); err == nil {
		t.Fatal("lost authority accepted")
	}
}
