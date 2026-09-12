package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"talosdeck/internal/clusters"
)

func persistentFixture(t *testing.T) (*AuthManager, *clusters.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	db, key := filepath.Join(dir, "auth.db"), filepath.Join(dir, "master.key")
	store, err := clusters.Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	manager, err := NewPersistentAuthManager(store, "bootstrap-password-sentinel", "")
	if err != nil {
		t.Fatal(err)
	}
	return manager, store, db, key
}
func TestPersistentUsersRevocationAndRestart(t *testing.T) {
	manager, store, db, key := persistentFixture(t)
	adminToken, admin, err := manager.Login("admin", "bootstrap-password-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	operator, err := manager.CreateUser("operator", "operator-password-sentinel", "operator")
	if err != nil {
		t.Fatal(err)
	}
	token, user, err := manager.Login("operator", "operator-password-sentinel")
	if err != nil || user.ID != operator.ID {
		t.Fatalf("operator login: %v", err)
	}
	if _, err = manager.GenerateToken("operator", "admin"); err == nil {
		t.Fatal("role escalation through token issuer")
	}
	role := "viewer"
	if _, err = manager.UpdateUser(operator.ID, &role, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ValidateToken(token); err == nil {
		t.Fatal("old elevated token survived role downgrade")
	}
	token, _, err = manager.Login("operator", "operator-password-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.RevokeToken(token); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if _, err = manager.UpdateUser(admin.ID, nil, &disabled); !errors.Is(err, ErrConflict) {
		t.Fatalf("last admin disabled: %v", err)
	}
	raw, err := store.GetSecret(context.Background(), "__fleet__", "auth", "state")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("bootstrap-password-sentinel")) || bytes.Contains(raw, []byte("operator-password-sentinel")) {
		t.Fatal("plaintext password in decrypted user storage")
	}
	disk, _ := os.ReadFile(db)
	if bytes.Contains(disk, []byte("passwordHash")) {
		t.Fatal("user secret state not encrypted")
	}
	store.Close()
	restored, err := clusters.Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	manager, err = NewPersistentAuthManager(restored, "different-env-password", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ValidateToken(token); err == nil {
		t.Fatal("revoked token resurrected after restart")
	}
	if _, err = manager.ValidateToken(adminToken); err != nil {
		t.Fatalf("persisted signing key lost: %v", err)
	}
	if _, _, err = manager.Login("admin", "different-env-password"); err == nil {
		t.Fatal("restart reset administrator password from environment")
	}
	if err = manager.SetPassword(operator.ID, "replacement-password-sentinel"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = manager.Login("operator", "operator-password-sentinel"); err == nil {
		t.Fatal("password reset retained old credential")
	}
	token, _, err = manager.Login("operator", "replacement-password-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.UpdateUser(operator.ID, nil, &disabled); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ValidateToken(token); err == nil {
		t.Fatal("disabled user session remained valid")
	}
}

type failingSecretStore struct {
	SecretStore
	fail bool
}

func (s *failingSecretStore) PutSecret(ctx context.Context, scope, kind, key string, data []byte) error {
	if s.fail {
		return errors.New("disk unavailable")
	}
	return s.SecretStore.PutSecret(ctx, scope, kind, key, data)
}
func TestUserMutationStorageFailureDoesNotClaimSuccess(t *testing.T) {
	_, store, _, _ := persistentFixture(t)
	wrapped := &failingSecretStore{SecretStore: store}
	manager, err := NewPersistentAuthManager(wrapped, "", "")
	if err != nil {
		t.Fatal(err)
	}
	token, user, err := manager.Login("admin", "bootstrap-password-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	wrapped.fail = true
	if err = manager.RevokeToken(token); err == nil {
		t.Fatal("failed revocation reported success")
	}
	if err = manager.SetPassword(user.ID, "replacement-password"); err == nil {
		t.Fatal("failed password change reported success")
	}
	if _, err = manager.ValidateToken(token); err != nil {
		t.Fatal("uncommitted authentication state was published")
	}
}
func TestPermissionMatrixIncludesScopedRoutesAndJobKinds(t *testing.T) {
	cases := []struct {
		role, method, path string
		allowed            bool
	}{
		{"viewer", "GET", "/api/clusters", true}, {"viewer", "GET", "/api/clusters/id/nodes", true}, {"viewer", "GET", "/api/clusters/id/ws/nodes/10.0.0.1/logs", true}, {"viewer", "GET", "/api/clusters/id/backups", true}, {"viewer", "GET", "/api/clusters/id/backups/id/download", false},
		{"viewer", "POST", "/api/clusters/id/nodes/ip/reboot", false}, {"operator", "POST", "/api/clusters/id/nodes/ip/reboot", true}, {"operator", "POST", "/api/clusters/id/nodes/ip/shutdown", false}, {"operator", "POST", "/api/clusters/id/config/apply", false}, {"operator", "GET", "/api/auth/users", false}, {"viewer", "GET", "/api/providers", false}, {"viewer", "GET", "/api/unknown-future-sensitive-route", false}, {"admin", "DELETE", "/api/clusters/id", true},
	}
	for _, c := range cases {
		if got := Can(c.role, c.method, c.path); got != c.allowed {
			t.Errorf("%s %s %s=%t want%t", c.role, c.method, c.path, got, c.allowed)
		}
	}
	if !CanJob("operator", "talos-upgrade") || CanJob("operator", "config-apply") || CanJob("operator", "restore-etcd") || CanJob("viewer", "rolling-reboot") {
		t.Fatal("job kind privilege boundary failed")
	}
}
