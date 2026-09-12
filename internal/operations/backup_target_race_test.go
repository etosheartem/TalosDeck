package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/backup"
	"talosdeck/internal/jobs"
)

func TestFirstBackupReservesTargetUntilRestoreLocationPublished(t *testing.T) {
	s := backupFixture(t)
	ctx := context.Background()
	payload := []byte("first verified remote backup")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/first.snapshot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"fixture"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	target := BackupTarget{ID: uuid.NewString(), Name: "first-use", Type: "s3", Endpoint: server.URL, Bucket: "bucket", Region: "us-east-1", AccessKey: "fixture", SecretKey: "fixture-secret"}
	if err := s.put(ctx, "backup-target", target.ID, target); err != nil {
		t.Fatal(err)
	}
	captured := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(proceed) }) }
	hash := sha256.Sum256(payload)
	info := backup.BackupInfo{ID: "first.snapshot", Filename: "first.snapshot", Type: backup.BackupTypeEtcd, Size: int64(len(payload)), Checksum: hex.EncodeToString(hash[:])}
	manager, err := jobs.OpenCluster(t.TempDir(), s.ClusterID, func(c context.Context, _ *jobs.Execution, _ jobs.Request) error {
		snapshot, err := s.target(c, target.ID)
		if err != nil {
			return err
		}
		close(captured)
		select {
		case <-proceed:
		case <-c.Done():
			return c.Err()
		}
		return s.put(c, "remote-backup", info.ID, RemoteBackup{Info: info, TargetID: snapshot.ID, Object: "first.snapshot"})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { unblock(); manager.Close() }()
	job, err := manager.Submit(jobs.Request{Kind: "backup-create", TargetID: target.ID}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-captured:
	case <-time.After(time.Second):
		t.Fatal("job did not capture target")
	}
	ids, err := s.Store.ListSecretKeys(ctx, s.ClusterID, "remote-backup")
	if err != nil || len(ids) != 0 {
		t.Fatal("test must pause before first remote metadata exists")
	}
	changed := target
	changed.Endpoint = "http://127.0.0.1:1"
	changed.Bucket = "other"
	if _, err = s.SaveTarget(ctx, changed, manager); !errors.Is(err, jobs.ErrBusy) {
		t.Fatalf("in-flight target change accepted: %v", err)
	}
	unchanged, err := s.target(ctx, target.ID)
	if err != nil || unchanged.Endpoint != target.Endpoint || unchanged.Bucket != target.Bucket {
		t.Fatal("first upload location changed")
	}
	unblock()
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, e := manager.Get(job.ID)
		if e != nil {
			t.Fatal(e)
		}
		if result.Status == "succeeded" {
			break
		}
		if result.Status != "queued" && result.Status != "running" {
			t.Fatalf("unexpected terminal status %s", result.Status)
		}
		if time.Now().After(deadline) {
			t.Fatal("backup did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = s.SaveTarget(ctx, changed, manager); err == nil {
		t.Fatal("published history allowed location change")
	}
	_, file, cleanup, err := s.Materialize(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, err := os.ReadFile(file)
	if err != nil || string(got) != string(payload) {
		t.Fatal("published restore point no longer resolves to original location")
	}
}
