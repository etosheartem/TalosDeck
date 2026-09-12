package clusters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreEncryptionIsolationBackupAndRotation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "data", "clusters.db")
	key := filepath.Join(dir, "keys", "master.key")
	s, err := Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	creds := Credentials{Talosconfig: []byte("TALOS-PRIVATE-KEY-SENTINEL"), Kubeconfig: []byte("KUBE-TOKEN-SENTINEL"), ProviderSecrets: []byte("PROXMOX-SECRET-SENTINEL")}
	a, err := s.Create(ctx, Cluster{Name: "production", Identity: "ca-a", Legacy: true}, creds)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, Cluster{Name: "stage", Identity: "ca-b"}, creds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, Cluster{Name: "alias", Identity: "ca-a"}, creds); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	rev, err := s.AddRevision(ctx, Revision{ClusterID: a.ID, Node: "worker-01", Author: "admin", Config: []byte("CONFIG-SECRET-SENTINEL"), Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetRevision(ctx, b.ID, "worker-01", rev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-cluster revision: %v", err)
	}
	if _, err = s.GetRevision(ctx, a.ID, "worker-02", rev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-node revision: %v", err)
	}
	if err = s.UpdateRevisionStatus(ctx, a.ID, "worker-01", rev.ID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	history, err := s.ListRevisions(ctx, a.ID, "worker-01")
	if err != nil || len(history) != 1 || history[0].Config != nil || history[0].Status != "succeeded" {
		t.Fatalf("history: %+v %v", history, err)
	}
	public, _ := json.Marshal(a)
	if bytes.Contains(public, []byte("ca-a")) || bytes.Contains(public, []byte("legacy")) {
		t.Fatalf("private metadata in API: %s", public)
	}
	backup := filepath.Join(dir, "backup.db")
	if err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{creds.Talosconfig, creds.Kubeconfig, creds.ProviderSecrets, rev.Config} {
		if bytes.Contains(before, secret) {
			t.Fatal("plaintext secret in SQLite")
		}
	}
	newKey := filepath.Join(dir, "keys", "rotated.key")
	if err = s.RotateKey(ctx, newKey); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(db)
	if bytes.Equal(before, after) {
		t.Fatal("rotation did not update ciphertext")
	}
	s.Close()
	for _, keyPath := range []string{key, newKey} {
		restored, e := Open(db, keyPath)
		if e != nil {
			t.Fatal(e)
		}
		got, e := restored.Credentials(ctx, a.ID)
		if e != nil || !bytes.Equal(got.Talosconfig, creds.Talosconfig) {
			t.Fatalf("rotation recovery: %v", e)
		}
		gotCluster, e := restored.Get(ctx, a.ID)
		if e != nil || !gotCluster.Legacy {
			t.Fatalf("legacy flag lost: %v", e)
		}
		gotRev, e := restored.GetRevision(ctx, a.ID, "worker-01", rev.ID)
		if e != nil || !bytes.Equal(gotRev.Config, rev.Config) {
			t.Fatalf("revision recovery: %v", e)
		}
		restored.Close()
	}
	restored, err := Open(backup, newKey)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.Credentials(ctx, a.ID); err != nil {
		t.Fatalf("old backup with rotated keyring: %v", err)
	}
	for _, path := range []string{db, key, newKey, backup} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("permissions %s: %v %v", path, info, e)
		}
	}
}

func TestMissingWrongKeyAndTamperingFailClosed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	key := filepath.Join(dir, "key")
	s, err := Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(ctx, Cluster{Name: "a"}, Credentials{Talosconfig: []byte("a-secret")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, Cluster{Name: "b"}, Credentials{Talosconfig: []byte("b-secret")})
	if err != nil {
		t.Fatal(err)
	}
	// Ciphertext relocation must fail even when both records use the same master key.
	_, err = s.db.Exec(`UPDATE clusters SET credentials=(SELECT credentials FROM clusters WHERE id=?) WHERE id=?`, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Credentials(ctx, b.ID); err == nil {
		t.Fatal("AAD accepted relocated ciphertext")
	}
	s.Close()
	if _, err = Open(db, key); err == nil {
		t.Fatal("startup accepted corrupted credential")
	}
	if _, err = Open(db, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key silently regenerated")
	}
}

func TestRotationFailurePreservesReadableDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	key := filepath.Join(dir, "key")
	s, err := Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Create(ctx, Cluster{Name: "a"}, Credentials{Talosconfig: []byte("secret")})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = s.RotateKey(canceled, filepath.Join(dir, "new-key")); err == nil {
		t.Fatal("canceled rotation succeeded")
	}
	s.Close()
	s, err = Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Credentials(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Backup(ctx, db); err == nil {
		t.Fatal("backup overwrote source database")
	}
}

func TestReadonlyKeyAndCredentialUpdates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	key := filepath.Join(dir, "key")
	s, err := Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Create(ctx, Cluster{Name: "cluster"}, Credentials{Talosconfig: []byte("private")})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err = os.Chmod(key, 0440); err != nil {
		t.Fatal(err)
	}
	s, err = Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, _ := os.Stat(key)
	if info.Mode().Perm() != 0440 {
		t.Fatal("read-only secret mode changed")
	}
	updated := Credentials{Talosconfig: []byte("private"), ProviderSecrets: []byte("new-provider-secret")}
	if err = s.UpdateCredentials(ctx, c.ID, updated); err != nil {
		t.Fatal(err)
	}
	got, err := s.Credentials(ctx, c.ID)
	if err != nil || !bytes.Equal(got.ProviderSecrets, updated.ProviderSecrets) {
		t.Fatalf("update: %v", err)
	}
	if err = s.UpdateCredentials(ctx, "missing", updated); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update: %v", err)
	}
	protected := filepath.Join(dir, "unrelated")
	os.WriteFile(protected, []byte("retain"), 0600)
	if err = s.RotateKey(ctx, protected); err == nil {
		t.Fatal("rotation overwrote existing unrelated file")
	}
	contents, _ := os.ReadFile(protected)
	if string(contents) != "retain" {
		t.Fatal("unrelated file changed")
	}
}

func TestExclusiveLockAndInterruptedRevisionRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	key := filepath.Join(dir, "key")
	s, err := Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Create(ctx, Cluster{Name: "cluster", Identity: "stable-ca"}, Credentials{Talosconfig: []byte("private")})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := s.AddRevision(ctx, Revision{ClusterID: c.ID, Node: "worker", Status: "running", Config: []byte("before-after")})
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Open(db, key); err == nil {
		second.Close()
		t.Fatal("second process opened active database")
	}
	current, err := s.GetRevision(ctx, c.ID, "worker", revision.ID)
	if err != nil || current.Status != "running" {
		t.Fatalf("second open changed running revision: %v", err)
	}
	s.Close()
	s, err = Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err = s.GetRevision(ctx, c.ID, "worker", revision.ID)
	if err != nil || current.Status != "interrupted" || !bytes.Equal(current.Config, revision.Config) {
		t.Fatalf("recovery: %+v %v", current, err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].Identity != "stable-ca" {
		t.Fatalf("hidden identity lost in list: %+v %v", list, err)
	}
}
