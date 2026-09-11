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

func TestPathTraversalProtection(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "talosdeck-traversal-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	bm, err := NewBackupManager(tempDir, nil)
	if err != nil {
		t.Fatalf("failed to init backup manager: %v", err)
	}

	traversalPayloads := []string{
		"..",
		"../",
		"../../",
		"..\\",
		"../data",
		"../../etc/passwd",
		"a/../../b",
		"/etc/shadow",
		".",
		"",
	}

	for _, p := range traversalPayloads {
		_, _, err := bm.GetBackup(p)
		if err == nil {
			t.Errorf("expected error for GetBackup(%q), got nil", p)
		}

		err = bm.DeleteBackup(p)
		if err == nil {
			t.Errorf("expected error for DeleteBackup(%q), got nil", p)
		}
	}
}

func TestSecurePermissionsAndIntegrity(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "talosdeck-perm-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	bm, err := NewBackupManager(tempDir, nil)
	if err != nil {
		t.Fatalf("failed to init backup manager: %v", err)
	}

	// Verify directory permissions
	dirStat, err := os.Stat(tempDir)
	if err != nil {
		t.Fatalf("failed to stat tempDir: %v", err)
	}
	// Perm should not be world-readable/executable
	if dirStat.Mode().Perm()&0007 != 0 {
		t.Errorf("expected directory to not be world-accessible, got perm: %v", dirStat.Mode().Perm())
	}

	// Create test backup
	snapName := "etcd-10.42.0.110-test.snapshot"
	snapData := []byte("integrity and security test content")
	snapPath := filepath.Join(tempDir, snapName)
	if err := os.WriteFile(snapPath, snapData, 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	hasher := sha256.New()
	hasher.Write(snapData)
	checksum := hex.EncodeToString(hasher.Sum(nil))

	_ = os.WriteFile(snapPath+".sha256", []byte(checksum+"  "+snapName+"\n"), 0600)
	_ = os.WriteFile(snapPath+".json", []byte(`{"id":"`+snapName+`","filename":"`+snapName+`","checksum":"`+checksum+`"}`), 0600)

	// Test VerifyBackup
	valid, computed, err := bm.VerifyBackup(snapName)
	if err != nil || !valid {
		t.Fatalf("expected valid backup, got valid=%v, computed=%s, err=%v", valid, computed, err)
	}

	// Corrupt file and verify detection
	_ = os.WriteFile(snapPath, []byte("corrupted payload"), 0600)
	valid, _, _ = bm.VerifyBackup(snapName)
	if valid {
		t.Errorf("expected corrupt backup to fail verification")
	}
}

func TestDeleteBackup_SafetyAndOrphanCleanup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "talosdeck-delete-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	bm, err := NewBackupManager(tempDir, nil)
	if err != nil {
		t.Fatalf("failed to init backup manager: %v", err)
	}

	// 1. Refuse directory deletion (BKP-13)
	subDir := filepath.Join(tempDir, "mysubdir")
	_ = os.Mkdir(subDir, 0700)
	if err := bm.DeleteBackup("mysubdir"); err == nil {
		t.Errorf("expected error when trying to delete directory, got nil")
	}

	// 2. Orphan sidecar cleanup (BKP-12)
	sidecarBase := filepath.Join(tempDir, "orphan-backup")
	_ = os.WriteFile(sidecarBase+".sha256", []byte("12345"), 0600)
	_ = os.WriteFile(sidecarBase+".json", []byte(`{"id":"orphan-backup"}`), 0600)

	if err := bm.DeleteBackup("orphan-backup"); err != nil {
		t.Fatalf("expected DeleteBackup to clean up orphan sidecars, got: %v", err)
	}

	if _, err := os.Stat(sidecarBase + ".sha256"); !os.IsNotExist(err) {
		t.Errorf("expected orphan sha256 to be removed")
	}
	if _, err := os.Stat(sidecarBase + ".json"); !os.IsNotExist(err) {
		t.Errorf("expected orphan json to be removed")
	}
}

func TestConcurrencyGuard(t *testing.T) {
	bm, err := NewBackupManager("", nil)
	if err != nil {
		t.Fatalf("failed to init backup manager: %v", err)
	}

	// Simulate in-flight backup
	bm.isBackingUp.Store(true)

	_, err = bm.CreateEtcdSnapshot(context.Background(), "10.42.0.110")
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("expected 'already in progress' error, got: %v", err)
	}

	_, err = bm.CreateFullClusterBackup(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("expected 'already in progress' error, got: %v", err)
	}

	bm.isBackingUp.Store(false)
}

func TestBackupPublicationIsAtomic(t *testing.T) {
	tempDir := t.TempDir()
	bm, err := NewBackupManager(tempDir, nil)
	if err != nil {
		t.Fatalf("failed to init backup manager: %v", err)
	}

	finalPath := filepath.Join(tempDir, "etcd-test.snapshot")
	file, tempPath, err := createBackupTempFile(finalPath)
	if err != nil {
		t.Fatalf("failed to create temporary backup: %v", err)
	}
	if _, err := file.Write([]byte("complete snapshot")); err != nil {
		t.Fatalf("failed to write temporary backup: %v", err)
	}

	list, err := bm.ListBackups()
	if err != nil {
		t.Fatalf("failed to list backups: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("temporary backup became visible before publication: %v", list)
	}
	if err := file.Sync(); err != nil {
		t.Fatalf("failed to sync temporary backup: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("failed to close temporary backup: %v", err)
	}

	info := &BackupInfo{ID: "etcd-test.snapshot", Filename: "etcd-test.snapshot", Type: BackupTypeEtcd}
	if err := publishBackup(tempPath, finalPath, "checksum", info); err != nil {
		t.Fatalf("failed to publish backup: %v", err)
	}
	list, err = bm.ListBackups()
	if err != nil || len(list) != 1 {
		t.Fatalf("expected one published backup, got %d, err=%v", len(list), err)
	}
	data, err := os.ReadFile(finalPath)
	if err != nil || string(data) != "complete snapshot" {
		t.Fatalf("published backup is incomplete: %q, err=%v", data, err)
	}
}
