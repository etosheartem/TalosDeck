package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talosdeck/internal/talos"
)

func TestBackupManagerBasicOperations(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "talosdeck-backup-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	bm, err := NewBackupManager(tempDir, nil)
	if err != nil {
		t.Fatalf("failed to init backup manager: %v", err)
	}

	// 1. Initially empty
	list, err := bm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 backups, got %d", len(list))
	}

	// 2. Add dummy snapshot file and sidecars
	snapName := "etcd-10.42.0.110-20260911-120000.snapshot"
	snapData := []byte("dummy etcd snapshot content 1234567890")
	snapPath := filepath.Join(tempDir, snapName)
	if err := os.WriteFile(snapPath, snapData, 0644); err != nil {
		t.Fatalf("failed to write dummy snapshot: %v", err)
	}

	hasher := sha256.New()
	hasher.Write(snapData)
	checksum := hex.EncodeToString(hasher.Sum(nil))

	_ = os.WriteFile(snapPath+".sha256", []byte(checksum+"  "+snapName+"\n"), 0644)
	meta := BackupInfo{
		ID:          snapName,
		Filename:    snapName,
		Size:        int64(len(snapData)),
		HumanSize:   formatSize(int64(len(snapData))),
		Timestamp:   time.Now().UTC(),
		Type:        BackupTypeEtcd,
		Checksum:    checksum,
		ClusterName: "test-cluster",
		Node:        "10.42.0.110",
	}
	metaBytes, _ := json.Marshal(meta)
	_ = os.WriteFile(snapPath+".json", metaBytes, 0644)

	// 3. List should return 1 backup with correct fields
	list, err = bm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(list))
	}
	if list[0].Filename != snapName {
		t.Errorf("expected filename %s, got %s", snapName, list[0].Filename)
	}
	if list[0].Type != BackupTypeEtcd {
		t.Errorf("expected type %s, got %s", BackupTypeEtcd, list[0].Type)
	}
	if list[0].Checksum != checksum {
		t.Errorf("expected checksum %s, got %s", checksum, list[0].Checksum)
	}

	// 4. GetBackup
	bInfo, bPath, err := bm.GetBackup(snapName)
	if err != nil {
		t.Fatalf("GetBackup failed: %v", err)
	}
	if bPath != snapPath {
		t.Errorf("expected path %s, got %s", snapPath, bPath)
	}
	if bInfo.ID != snapName {
		t.Errorf("expected ID %s, got %s", snapName, bInfo.ID)
	}

	// 5. DeleteBackup
	if err := bm.DeleteBackup(snapName); err != nil {
		t.Fatalf("DeleteBackup failed: %v", err)
	}

	// Verify main file and sidecars deleted
	if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
		t.Errorf("expected snap file to be deleted")
	}
	if _, err := os.Stat(snapPath + ".sha256"); !os.IsNotExist(err) {
		t.Errorf("expected sha256 sidecar to be deleted")
	}
	if _, err := os.Stat(snapPath + ".json"); !os.IsNotExist(err) {
		t.Errorf("expected json sidecar to be deleted")
	}

	// Verify list is empty again
	list, _ = bm.ListBackups()
	if len(list) != 0 {
		t.Errorf("expected 0 backups after delete, got %d", len(list))
	}
}

func TestLiveClusterBackups(t *testing.T) {
	configPath := "/home/artem/laba-kuber/cluster-config/talosconfig"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Skip("talosconfig not found, skipping live cluster test")
	}

	mgr, err := talos.NewTalosManager(configPath, "10.42.0.110", "10.42.0.111", "10.42.0.112")
	if err != nil {
		t.Skipf("failed to connect to Talos cluster: %v", err)
	}
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	tempDir, err := os.MkdirTemp("", "talosdeck-live-backup-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	bm, err := NewBackupManager(tempDir, mgr)
	if err != nil {
		t.Fatalf("failed to init backup manager: %v", err)
	}

	// Test 1: Etcd snapshot
	t.Log("Testing live etcd snapshot...")
	etcdInfo, err := bm.CreateEtcdSnapshot(ctx, "10.42.0.110")
	if err != nil {
		t.Fatalf("CreateEtcdSnapshot failed: %v", err)
	}
	if etcdInfo.Size <= 0 {
		t.Errorf("expected snapshot size > 0, got %d", etcdInfo.Size)
	}
	if etcdInfo.Checksum == "" {
		t.Errorf("expected non-empty checksum")
	}
	t.Logf("Snapshot created: %s, size: %s, sha256: %s", etcdInfo.Filename, etcdInfo.HumanSize, etcdInfo.Checksum)

	// Test 2: Full cluster backup
	t.Log("Testing live full cluster disaster recovery backup archive...")
	fullInfo, err := bm.CreateFullClusterBackup(ctx)
	if err != nil {
		t.Fatalf("CreateFullClusterBackup failed: %v", err)
	}
	if fullInfo.Size <= 0 {
		t.Errorf("expected archive size > 0, got %d", fullInfo.Size)
	}
	t.Logf("Full backup created: %s, size: %s, sha256: %s", fullInfo.Filename, fullInfo.HumanSize, fullInfo.Checksum)

	// Inspect contents of the generated .tar.gz archive
	archivePath := filepath.Join(tempDir, fullInfo.Filename)
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("failed to open archive: %v", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gz.Close()

	tarR := tar.NewReader(gz)
	foundMetadata := false
	foundTalosconfig := false
	foundMachineConfigs := false
	foundEtcdSnapshot := false

	for {
		hdr, err := tarR.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}

		t.Logf("Archive entry: %s (%d bytes)", hdr.Name, hdr.Size)
		if hdr.Name == "metadata.json" {
			foundMetadata = true
			var meta FullBackupMetadata
			data, _ := io.ReadAll(tarR)
			if err := json.Unmarshal(data, &meta); err != nil {
				t.Errorf("metadata.json is not valid json: %v", err)
			}
			if len(meta.Nodes) == 0 {
				t.Errorf("expected nodes in metadata, got 0")
			}
		} else if hdr.Name == "talosconfig" {
			foundTalosconfig = true
		} else if strings.HasPrefix(hdr.Name, "machine-configs/") {
			foundMachineConfigs = true
		} else if strings.HasPrefix(hdr.Name, "etcd/") {
			foundEtcdSnapshot = true
		}
	}

	if !foundMetadata {
		t.Errorf("metadata.json missing in archive")
	}
	if !foundTalosconfig {
		t.Errorf("talosconfig missing in archive")
	}
	if !foundMachineConfigs {
		t.Errorf("machine-configs/ missing in archive")
	}
	if !foundEtcdSnapshot {
		t.Errorf("etcd/ snapshot missing in archive")
	}

	// Test 3: List backups shows both
	list, err := bm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 backups, got %d", len(list))
	}
}
