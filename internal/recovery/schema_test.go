package recovery

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOlderSchemaMigratesOnlyRestoredCopy(t *testing.T) {
	o, s := fixture(t)
	s.Close()
	source := filepath.Join(o.DataDir, "talosdeck.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TABLE private_records; DROP TABLE settings; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	m, err := Create(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 1 {
		t.Fatal(m.SchemaVersion)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if _, err = Restore(context.Background(), o.ArchivePath, destination, o.KeyPath); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{source: 1, filepath.Join(destination, "talosdeck.db"): 2} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		var got int
		err = db.QueryRow("PRAGMA user_version").Scan(&got)
		db.Close()
		if err != nil || got != want {
			t.Fatal(path, got, want, err)
		}
	}
}

func TestFutureSchemaCannotProduceClaimedRecoverableArchive(t *testing.T) {
	o, s := fixture(t)
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(o.DataDir, "talosdeck.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = Create(context.Background(), o); err == nil {
		t.Fatal("accepted future schema")
	}
}
