package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"talosdeck/internal/reconcile"
	"testing"
)

func TestBackupDeleteRejectsExpiredExecutionBeforeArtifactRemoval(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewBackupManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := "etcd-test.snapshot"
	if err = os.WriteFile(filepath.Join(dir, name), []byte("backup"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := reconcile.WithMutationRecorder(context.Background(), func(context.Context, string, string, func() error) error { return reconcile.ErrAuthority })
	if err = manager.DeleteBackupContext(ctx, name); !errors.Is(err, reconcile.ErrAuthority) {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatal("artifact changed before admission", err)
	}
}
