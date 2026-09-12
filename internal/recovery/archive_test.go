package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"talosdeck/internal/clusters"
	"testing"
)

func fixture(t *testing.T) (Options, *clusters.Store) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	key := filepath.Join(root, "separate.key")
	s, e := clusters.Open(filepath.Join(data, "talosdeck.db"), key)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(data, "credential-artifact"), []byte("super-secret-credential"), 0600); e != nil {
		t.Fatal(e)
	}
	return Options{DataDir: data, KeyPath: key, ArchivePath: filepath.Join(root, "backup.tdk"), ApplicationVersion: "test"}, s
}
func TestRoundTripOfflineEncryptedAndRotation(t *testing.T) {
	ctx := context.Background()
	o, s := fixture(t)
	if _, e := Create(ctx, o); e == nil {
		t.Fatal("accepted running instance")
	}
	s.Close()
	before, _ := os.ReadFile(filepath.Join(o.DataDir, "talosdeck.db"))
	m, e := Create(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(o.DataDir, "talosdeck.db"))
	if !bytes.Equal(before, after) {
		t.Fatal("source database changed")
	}
	raw, _ := os.ReadFile(o.ArchivePath)
	if bytes.Contains(raw, []byte("super-secret-credential")) {
		t.Fatal("archive exposes credentials")
	}
	k, e := readKeys(o.KeyPath)
	if e != nil {
		t.Fatal(e)
	}
	k.Keys["new-key"] = bytes.Repeat([]byte{42}, 32)
	k.Active = "new-key"
	b, _ := json.Marshal(k)
	os.WriteFile(o.KeyPath, b, 0600)
	drill, e := Drill(ctx, o.ArchivePath, o.KeyPath)
	if e != nil {
		t.Fatal(e)
	}
	if !drill.CreatedAt.Equal(m.CreatedAt) {
		t.Fatal("drill changed backup age")
	}
	dest := filepath.Join(filepath.Dir(o.DataDir), "restored")
	if _, e = Restore(ctx, o.ArchivePath, dest, o.KeyPath); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dest, SafeModeFile)); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dest, "master.key")); !os.IsNotExist(e) {
		t.Fatal("key restored alongside database")
	}
	got, _ := os.ReadFile(filepath.Join(dest, "credential-artifact"))
	if string(got) != "super-secret-credential" {
		t.Fatal("durable data lost")
	}
	if _, e = Restore(ctx, o.ArchivePath, dest, o.KeyPath); e == nil {
		t.Fatal("overwrote existing destination")
	}
}
func TestCorruptionWrongKeyAndTruncation(t *testing.T) {
	ctx := context.Background()
	o, s := fixture(t)
	s.Close()
	if _, e := Create(ctx, o); e != nil {
		t.Fatal(e)
	}
	other, otherStore := fixture(t)
	otherStore.Close()
	if _, e := Drill(ctx, o.ArchivePath, other.KeyPath); e == nil {
		t.Fatal("wrong key accepted")
	}
	raw, _ := os.ReadFile(o.ArchivePath)
	for _, kind := range []string{"truncate", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			bad := append([]byte{}, raw...)
			if kind == "truncate" {
				bad = bad[:len(bad)-20]
			} else {
				bad[len(bad)/2] ^= 1
			}
			p := filepath.Join(t.TempDir(), "bad")
			os.WriteFile(p, bad, 0600)
			if _, e := Drill(ctx, p, o.KeyPath); e == nil {
				t.Fatal("damaged archive accepted")
			}
		})
	}
}
func TestRejectSymlinkAndExcludeKeyAlias(t *testing.T) {
	ctx := context.Background()
	o, s := fixture(t)
	s.Close()
	os.Link(o.KeyPath, filepath.Join(o.DataDir, "alias"))
	if _, e := Create(ctx, o); e != nil {
		t.Fatal(e)
	}
	m, e := Drill(ctx, o.ArchivePath, o.KeyPath)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range m.Entries {
		if f.Path == "alias" {
			t.Fatal("key alias archived")
		}
	}
	os.Remove(o.ArchivePath)
	os.Symlink(o.KeyPath, filepath.Join(o.DataDir, "link"))
	if _, e = Create(ctx, o); e == nil {
		t.Fatal("symlink accepted")
	}
}
