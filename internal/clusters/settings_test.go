package clusters

import (
	"bytes"
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretNamespacesRotateAndBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, key := filepath.Join(dir, "db"), filepath.Join(dir, "key")
	s, err := Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := uuid.NewString()
	other := uuid.NewString()
	secret := []byte("provider-secret-sentinel")
	if err = s.CreateSecret(ctx, scope, "provider", "default", secret); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateSecret(ctx, scope, "provider", "default", []byte("overwrite")); !errors.Is(err, ErrDuplicate) {
		t.Fatal("reservation overwritten")
	}
	for _, parts := range [][3]string{{other, "provider", "default"}, {scope, "backup", "default"}, {scope, "provider", "other"}} {
		if _, err = s.GetSecret(ctx, parts[0], parts[1], parts[2]); !errors.Is(err, ErrNotFound) {
			t.Fatal("cross-namespace lookup")
		}
	}
	if err = s.PutPrivateState(ctx, "__fleet__", "plan", secret); err != nil {
		t.Fatal(err)
	}
	if err = s.PutSetting(ctx, scope, "schedule", "backup", []byte(`{"hours":6}`)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(db)
	if bytes.Contains(before, secret) {
		t.Fatal("plaintext secret in DB")
	}
	backup := filepath.Join(dir, "backup")
	if err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	rotated := filepath.Join(dir, "rotated-key")
	if err = s.RotateKey(ctx, rotated); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSecret(ctx, scope, "provider", "default")
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("rotated secret: %v", err)
	}
	restored, err := Open(backup, rotated)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err = restored.GetPrivateState(ctx, "__fleet__", "plan")
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("backup recovery: %v", err)
	}
	keys, err := s.ListSecretKeys(ctx, scope, "provider")
	if err != nil || len(keys) != 1 || keys[0] != "default" {
		t.Fatal("secret key listing leaked/missed namespace")
	}
	if err = s.DeleteSecret(ctx, scope, "provider", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetSecret(ctx, scope, "provider", "default"); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted secret present")
	}
}
func TestSchemaOneMigrationPreservesClusters(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, key := filepath.Join(dir, "db"), filepath.Join(dir, "key")
	s, err := Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Create(ctx, Cluster{Name: "legacy"}, Credentials{Talosconfig: []byte("secret")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP TABLE private_records; DROP TABLE settings; DELETE FROM schema_migrations WHERE version=2; PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
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
	if err = s.PutSecret(ctx, "__fleet__", "auth", "state", []byte("new-state")); err != nil {
		t.Fatal(err)
	}
}
