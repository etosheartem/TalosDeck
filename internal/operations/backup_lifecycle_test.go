package operations

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	bolt "go.etcd.io/bbolt"
	"talosdeck/internal/backup"
	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
)

func backupFixture(t *testing.T) *BackupService {
	t.Helper()
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "registry.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cluster, err := store.Create(context.Background(), clusters.Cluster{Name: "fixture", Identity: "backup-fixture"}, clusters.Credentials{Talosconfig: []byte("test-credential")})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := backup.NewBackupManager(filepath.Join(dir, "backups"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return &BackupService{ClusterID: cluster.ID, Store: store, Manager: manager}
}
func TestBackupScheduleDurabilityAndIsolation(t *testing.T) {
	s := backupFixture(t)
	ctx := context.Background()
	q := BackupSchedule{Enabled: true, IntervalHours: 6, Retention: 3, TargetID: "local"}
	if err := s.SaveSchedule(ctx, q); err != nil {
		t.Fatal(err)
	}
	copyService := &BackupService{ClusterID: s.ClusterID, Store: s.Store}
	got, err := copyService.Schedule(ctx)
	if err != nil || !got.Enabled || got.Retention != 3 || got.NextRun.IsZero() {
		t.Fatalf("schedule not persisted: %+v %v", got, err)
	}
	q.IntervalHours = 0
	if err = s.SaveSchedule(ctx, q); err == nil {
		t.Fatal("invalid interval accepted")
	}
	copyService.ClusterID = "00000000-0000-4000-8000-000000000001"
	other, err := copyService.Schedule(ctx)
	if err != nil || other.Enabled {
		t.Fatal("cross-cluster schedule")
	}
}
func TestScheduledJobDeduplicatesAcrossJournalReopen(t *testing.T) {
	dir := t.TempDir()
	runner := func(context.Context, *jobs.Execution, jobs.Request) error { return nil }
	jm, err := jobs.OpenCluster(dir, "fixture", runner)
	if err != nil {
		t.Fatal(err)
	}
	request := jobs.Request{Kind: "backup-create", DedupeKey: "backup:due-time"}
	one, err := jm.Submit(request, "scheduler")
	if err != nil {
		t.Fatal(err)
	}
	jm.Close()
	jm, err = jobs.OpenCluster(dir, "fixture", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()
	two, err := jm.Submit(request, "scheduler")
	if err != nil || one.ID != two.ID {
		t.Fatalf("duplicate scheduled backup: %s %s %v", one.ID, two.ID, err)
	}
}
func TestSnapshotHashCheckedBeforeReset(t *testing.T) {
	file := filepath.Join(t.TempDir(), "snapshot")
	db := bytes.Repeat([]byte{0x42}, 4096)
	hash := sha256.Sum256(db)
	data := append(db, hash[:]...)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	// Correct hash on random bytes is not a recoverable etcd database.
	if err := verifyEtcdSnapshot(file); err == nil {
		t.Fatal("hashed garbage accepted")
	}
	data[20] ^= 0xff
	os.WriteFile(file, data, 0600)
	if err := verifyEtcdSnapshot(file); err == nil {
		t.Fatal("corrupt etcd accepted")
	}
	os.WriteFile(file, db, 0600)
	if err := verifyEtcdSnapshot(file); err == nil {
		t.Fatal("unhashed snapshot accepted")
	}
	validFile := filepath.Join(t.TempDir(), "valid.snapshot")
	database, err := bolt.Open(validFile, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Update(func(tx *bolt.Tx) error { _, e := tx.CreateBucket([]byte("key")); return e }); err != nil {
		t.Fatal(err)
	}
	database.Close()
	validData, err := os.ReadFile(validFile)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(validData)
	os.WriteFile(validFile, append(validData, sum[:]...), 0600)
	if err = verifyEtcdSnapshot(validFile); err != nil {
		t.Fatal(err)
	}
}

func TestLiveEtcdSnapshotStructure(t *testing.T) {
	file := os.Getenv("TALOSDECK_TEST_SNAPSHOT")
	if file == "" {
		t.Skip("set an isolated downloaded etcd snapshot")
	}
	if err := verifyEtcdSnapshot(file); err != nil {
		t.Fatal(err)
	}
}
func TestRestoreArchiveDoesNotExtractSecretsOrPaths(t *testing.T) {
	file := filepath.Join(t.TempDir(), "archive.tar.gz")
	f, _ := os.Create(file)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range map[string][]byte{"../../escape": []byte("secret"), "talosconfig": []byte("private-key-value"), "metadata.json": []byte(`{"nodes":[]}`), "etcd/snapshot-cp.snapshot": []byte("snapshot")} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))})
		tw.Write(body)
	}
	tw.Close()
	gz.Close()
	f.Close()
	snap, cleanup, _, err := snapshotFile(&backup.BackupInfo{Type: backup.BackupTypeFull}, file)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, _ := os.ReadFile(snap)
	if string(data) != "snapshot" {
		t.Fatal("incorrect extracted data")
	}
	st, _ := os.Stat(snap)
	if st.Mode().Perm() != 0600 {
		t.Fatal("unsafe snapshot permissions")
	}
}

// Opt-in integration against an actual disposable MinIO server. The bucket is
// created solely for this test and removed afterward; no production credentials.
func TestMinIORemoteBackupIntegrityAndRedaction(t *testing.T) {
	endpoint := os.Getenv("TALOSDECK_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("set disposable MinIO endpoint and credentials")
	}
	s := backupFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	target := BackupTarget{Name: "test-minio", Type: "s3", Endpoint: endpoint, Bucket: "talosdeck-test-" + strings.ReplaceAll(s.ClusterID, "-", ""), Region: "us-east-1", AccessKey: os.Getenv("TALOSDECK_TEST_MINIO_ACCESS_KEY"), SecretKey: os.Getenv("TALOSDECK_TEST_MINIO_SECRET_KEY")}
	client, err := backupS3(target)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.MakeBucket(ctx, target.Bucket, minio.MakeBucketOptions{Region: target.Region}); err != nil {
		t.Fatal(err)
	}
	defer client.RemoveBucket(context.Background(), target.Bucket)
	jm, err := jobs.OpenCluster(t.TempDir(), s.ClusterID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()
	saved, err := s.SaveTarget(ctx, target, jm)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SecretKey != "" || saved.AccessKey != "" || !saved.Configured {
		t.Fatal("credentials exposed")
	}
	targets, _ := s.Targets(ctx)
	encoded, _ := json.Marshal(targets)
	if bytes.Contains(encoded, []byte(target.SecretKey)) {
		t.Fatal("secret returned in target list")
	}
	payload := []byte("verified-external-backup")
	hash := sha256.Sum256(payload)
	info := backup.BackupInfo{ID: "external.snapshot", Filename: "external.snapshot", Size: int64(len(payload)), Checksum: hex.EncodeToString(hash[:]), Type: backup.BackupTypeEtcd}
	object := s.ClusterID + "/" + info.Filename
	defer client.RemoveObject(context.Background(), target.Bucket, object, minio.RemoveObjectOptions{})
	_, err = client.PutObject(ctx, target.Bucket, object, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.put(ctx, "remote-backup", info.ID, RemoteBackup{Info: info, TargetID: saved.ID, Object: object}); err != nil {
		t.Fatal(err)
	}
	_, file, cleanup, err := s.Materialize(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(file)
	cleanup()
	if !bytes.Equal(body, payload) {
		t.Fatal("download mismatch")
	}
	changed := saved
	changed.Endpoint = "http://127.0.0.1:1"
	if _, err = s.SaveTarget(ctx, changed, jm); err == nil {
		t.Fatal("historical target location allowed to change")
	}
	_, err = client.PutObject(ctx, target.Bucket, object, strings.NewReader("tampered"), 8, minio.PutObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, cleanup, err = s.Materialize(ctx, info.ID); err == nil {
		cleanup()
		t.Fatal("corrupt remote copy accepted")
	}
	if err = s.Delete(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatal("remote catalog not deleted")
	}
}
