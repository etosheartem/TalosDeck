package api

import (
	"context"
	"os"
	"path/filepath"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
	"talosdeck/internal/recovery"
	"testing"
)

type permittedRecoveryAuthority struct{}

func (permittedRecoveryAuthority) Validate(context.Context, string, uint64) error { return nil }
func TestActivatedFleetStillRequiresFreshBoundAuthorityAndPausesAutomation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	key := filepath.Join(root, "key")
	store, err := clusters.Open(filepath.Join(dir, "talosdeck.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte(`{"requiresReview":true,"restoredAt":"test"}`)
	if err = store.PutSecret(ctx, "__fleet__", "recovery", "required", marker); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err = os.WriteFile(filepath.Join(dir, recovery.SafeModeFile), marker, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = recovery.Activate(ctx, recovery.ActivationOptions{DataDir: dir, KeyPath: key, Actor: "operator", FencingEvidence: "Old management VM fenced and reviewed", ConfirmOldManagementFenced: true, Authority: permittedRecoveryAuthority{}, InstanceID: "offline-review", Epoch: 42, AuthorityIdentity: "authority", PersistAuthorityEpoch: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	store, err = clusters.Open(filepath.Join(dir, "talosdeck.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := FleetOptions{Store: store, DataDir: dir, Auth: auth.NewAuthManager("password-password", "long-secret-at-least-thirty-two-bytes")}
	if f, err := OpenFleet(opts); err == nil {
		f.Close()
		t.Fatal("activated data started without external authority")
	}
	opts.ExecutionAuthority = permittedRecoveryAuthority{}
	opts.ManagementInstanceID = "new"
	opts.ExecutionEpoch = 42
	opts.ManagementAuthorityIdentity = "authority"
	if f, err := OpenFleet(opts); err == nil {
		f.Close()
		t.Fatal("activation epoch reused")
	}
	opts.ExecutionEpoch = 43
	opts.ManagementAuthorityIdentity = "wrong-authority"
	if f, err := OpenFleet(opts); err == nil {
		f.Close()
		t.Fatal("different authority accepted")
	}
	opts.ManagementAuthorityIdentity = "authority"
	f, err := OpenFleet(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.recoverySafeMode || !f.automationPaused {
		t.Fatal("manual mode or persistent automation pause incorrect")
	}
}
