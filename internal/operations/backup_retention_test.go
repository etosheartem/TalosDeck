package operations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talosdeck/internal/backup"
)

func TestRetentionKeepsRemoteQuotaDespiteFailedLocalUploadAndPartialArchive(t *testing.T) {
	s := backupFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	deleted := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected S3 operation %s", r.Method)
			w.WriteHeader(500)
			return
		}
		mu.Lock()
		deleted[r.URL.Path] = true
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	target := BackupTarget{ID: "target", Type: "s3", Endpoint: server.URL, Region: "us-east-1", Bucket: "backups", AccessKey: "fixture", SecretKey: "fixture-secret"}
	if err := s.put(ctx, "backup-target", target.ID, target); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, id := range []string{"oldest", "previous", "failed-upload", "newest", "partial"} {
		info := backup.BackupInfo{ID: id, Filename: id + ".snapshot", Timestamp: now.Add(time.Duration(i) * time.Second), Partial: id == "partial"}
		file := filepath.Join(s.Manager.GetStorageDir(), info.Filename)
		if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(info)
		if err := os.WriteFile(file+".json", data, 0600); err != nil {
			t.Fatal(err)
		}
		if id != "failed-upload" {
			if err := s.put(ctx, "remote-backup", id, RemoteBackup{Info: info, TargetID: target.ID, Object: info.Filename}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.prune(ctx, 2); err != nil {
		t.Fatal(err)
	}
	local, err := s.Manager.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, b := range local {
		got[b.ID] = true
	}
	if len(got) != 3 || !got["failed-upload"] || !got["newest"] || !got["partial"] {
		t.Fatalf("local quota: %v", got)
	}
	keys, err := s.Store.ListSecretKeys(ctx, s.ClusterID, "remote-backup")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(keys, ",")
	if len(keys) != 3 || !strings.Contains(joined, "previous") || !strings.Contains(joined, "newest") || !strings.Contains(joined, "partial") {
		t.Fatalf("remote quota lost successful copies: %v", keys)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 1 || !deleted["/backups/oldest.snapshot"] {
		t.Fatalf("unsafe remote deletion: %v", deleted)
	}
}

func TestRemoteRetentionFailureDoesNotPruneLocalCopies(t *testing.T) {
	s := backupFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	ctx := context.Background()
	target := BackupTarget{ID: "target", Type: "s3", Endpoint: server.URL, Region: "us-east-1", Bucket: "backups", AccessKey: "fixture", SecretKey: "fixture-secret"}
	if err := s.put(ctx, "backup-target", target.ID, target); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"old", "new"} {
		info := backup.BackupInfo{ID: id, Filename: id + ".snapshot", Timestamp: time.Now().Add(time.Duration(i) * time.Minute)}
		file := filepath.Join(s.Manager.GetStorageDir(), info.Filename)
		data, _ := json.Marshal(info)
		if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file+".json", data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.put(ctx, "remote-backup", id, RemoteBackup{Info: info, TargetID: target.ID, Object: info.Filename}); err != nil {
			t.Fatal(err)
		}
	}
	limited, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := s.prune(limited, 1); err == nil {
		t.Fatal("unavailable remote retention succeeded")
	}
	local, err := s.Manager.ListBackups()
	if err != nil || len(local) != 2 {
		t.Fatalf("local copies lost after remote failure: %v %v", local, err)
	}
	remote, err := s.Store.ListSecretKeys(ctx, s.ClusterID, "remote-backup")
	if err != nil || len(remote) != 2 {
		t.Fatalf("remote records lost after failed delete: %v %v", remote, err)
	}
}
