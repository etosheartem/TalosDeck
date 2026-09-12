package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
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

func TestCopiedRotatedKeyringsExcluded(t *testing.T) {
	o, s := fixture(t)
	s.Close()
	key, e := os.ReadFile(o.KeyPath)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(o.DataDir, "old-keyring.json"), key, 0600); e != nil {
		t.Fatal(e)
	}
	k, _ := readKeys(o.KeyPath)
	k.Active = "rotated"
	k.Keys[k.Active] = bytes.Repeat([]byte{3}, 32)
	key, _ = json.Marshal(k)
	os.Mkdir(filepath.Join(o.DataDir, "secrets"), 0700)
	os.WriteFile(filepath.Join(o.DataDir, "secrets", "new-keyring"), key, 0600)
	m, e := Create(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range m.Entries {
		if entry.Path == "old-keyring.json" || entry.Path == "secrets/new-keyring" {
			t.Fatal("copied keyring entered archive")
		}
	}
}
func TestRejectOutputViaSymlinkIntoData(t *testing.T) {
	o, s := fixture(t)
	s.Close()
	alias := filepath.Join(filepath.Dir(o.DataDir), "alias-data")
	if e := os.Symlink(o.DataDir, alias); e != nil {
		t.Fatal(e)
	}
	o.ArchivePath = filepath.Join(alias, "backup")
	if _, e := Create(context.Background(), o); e == nil {
		t.Fatal("accepted backup inside data through alias")
	}
}
func TestSchemaMigrationAndMismatch(t *testing.T) {
	ctx := context.Background()
	o, s := fixture(t)
	s.Close()
	db, e := sql.Open("sqlite", filepath.Join(o.DataDir, "talosdeck.db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("PRAGMA user_version=1"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	m, e := Create(ctx, o)
	if e != nil {
		t.Fatal(e)
	}
	if m.SchemaVersion != 1 {
		t.Fatal("lost source schema version")
	}
	dest := filepath.Join(filepath.Dir(o.DataDir), "restored-schema")
	if _, e = Restore(ctx, o.ArchivePath, dest, o.KeyPath); e != nil {
		t.Fatal(e)
	}
	if v, e := databaseSchema(ctx, dest); e != nil || v != 2 {
		t.Fatalf("migration failed: %d %v", v, e)
	}
	// An authenticated archive may still have inconsistent metadata due to a
	// producer bug. Reject it instead of claiming a different schema was tested.
	raw, e := os.Open(o.ArchivePath)
	if e != nil {
		t.Fatal(e)
	}
	reader, e := archiveReader(raw, o.KeyPath)
	if e != nil {
		t.Fatal(e)
	}
	tr := tar.NewReader(reader)
	bad := filepath.Join(filepath.Dir(o.DataDir), "mismatched.tdr")
	out, e := os.Create(bad)
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := archiveWriter(out, o.KeyPath)
	if e != nil {
		t.Fatal(e)
	}
	tw := tar.NewWriter(sealed)
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		payload, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		if header.Name == "manifest.json" {
			var manifest Manifest
			json.Unmarshal(payload, &manifest)
			manifest.SchemaVersion = 2
			payload, _ = json.Marshal(manifest)
			header.Size = int64(len(payload))
		}
		if e = tw.WriteHeader(header); e != nil {
			t.Fatal(e)
		}
		if _, e = tw.Write(payload); e != nil {
			t.Fatal(e)
		}
	}
	tw.Close()
	sealed.Close()
	out.Close()
	raw.Close()
	if _, e = Drill(ctx, bad, o.KeyPath); e == nil {
		t.Fatal("schema mismatch accepted")
	}
}

func TestRestoreMarkerBoundToEncryptedDatabase(t *testing.T) {
	ctx := context.Background()
	o, s := fixture(t)
	s.Close()
	if _, e := Create(ctx, o); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(filepath.Dir(o.DataDir), "restored-marker")
	if _, e := Restore(ctx, o.ArchivePath, dest, o.KeyPath); e != nil {
		t.Fatal(e)
	}
	marker, e := os.ReadFile(filepath.Join(dest, SafeModeFile))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(dest, SafeModeFile)); e != nil {
		t.Fatal(e)
	}
	s, e = clusters.Open(filepath.Join(dest, "talosdeck.db"), o.KeyPath)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	durable, e := s.GetSecret(ctx, "__fleet__", "recovery", "required")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(durable, marker) {
		t.Fatal("durable restore marker missing or changed after file removal")
	}
}
