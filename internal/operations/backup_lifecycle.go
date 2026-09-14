package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"talosdeck/internal/backup"
	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
	"talosdeck/internal/reconcile"
)

type BackupTarget struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Endpoint   string `json:"endpoint,omitempty"`
	Bucket     string `json:"bucket,omitempty"`
	Region     string `json:"region,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	AccessKey  string `json:"accessKey,omitempty"`
	SecretKey  string `json:"secretKey,omitempty"`
	Configured bool   `json:"configured"`
}
type BackupSchedule struct {
	Enabled       bool      `json:"enabled"`
	IntervalHours int       `json:"intervalHours"`
	Retention     int       `json:"retention"`
	TargetID      string    `json:"targetId"`
	NextRun       time.Time `json:"nextRun,omitempty"`
}
type RemoteBackup struct {
	Info     backup.BackupInfo `json:"info"`
	TargetID string            `json:"targetId"`
	Object   string            `json:"object"`
}
type BackupService struct {
	ClusterID  string
	Store      ProvisionStore
	Manager    *backup.BackupManager
	Operations *Service
	mu         sync.Mutex
}

func (s *BackupService) put(ctx context.Context, kind, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Store.PutSecret(ctx, s.ClusterID, kind, key, b)
}
func (s *BackupService) get(ctx context.Context, kind, key string, v any) error {
	b, err := s.Store.GetSecret(ctx, s.ClusterID, kind, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func (s *BackupService) target(ctx context.Context, id string) (BackupTarget, error) {
	if id == "" || id == "local" {
		return BackupTarget{ID: "local", Name: "Local", Type: "local", Configured: true}, nil
	}
	var t BackupTarget
	err := s.get(ctx, "backup-target", id, &t)
	return t, err
}
func (s *BackupService) Targets(ctx context.Context) ([]BackupTarget, error) {
	ids, err := s.Store.ListSecretKeys(ctx, s.ClusterID, "backup-target")
	if err != nil {
		return nil, err
	}
	local, _ := s.target(ctx, "local")
	out := []BackupTarget{local}
	for _, id := range ids {
		t, e := s.target(ctx, id)
		if e != nil {
			return nil, e
		}
		t.Configured = t.AccessKey != "" && t.SecretKey != ""
		t.AccessKey = ""
		t.SecretKey = ""
		out = append(out, t)
	}
	return out, nil
}
func backupS3(t BackupTarget) (*minio.Client, error) {
	u, err := url.Parse(t.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("S3 endpoint must be an HTTP(S) origin")
	}
	if t.Bucket == "" || strings.ContainsAny(t.Bucket, "/\\") || t.AccessKey == "" || t.SecretKey == "" {
		return nil, errors.New("bucket and credentials are required")
	}
	transport, err := minio.DefaultTransport(u.Scheme == "https")
	if err != nil {
		return nil, err
	}
	return minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(t.AccessKey, t.SecretKey, ""), Secure: u.Scheme == "https", Region: t.Region, MaxRetries: 1, Transport: backupMutationTransport{transport}})
}

// SaveTarget reserves the same cluster journal used by backups/restores before
// reading historical references. This also protects first-use targets whose
// initial upload has not yet published remote-backup metadata.
func (s *BackupService) SaveTarget(ctx context.Context, t BackupTarget, manager *jobs.Manager) (BackupTarget, error) {
	if manager == nil {
		return t, errors.New("backup job manager unavailable")
	}
	release, err := manager.ReserveManual()
	if err != nil {
		return t, err
	}
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "local" || t.Type != "s3" || strings.TrimSpace(t.Name) == "" {
		return t, errors.New("a named S3 target is required")
	}
	if t.ID == "" {
		t.ID = uuid.NewString()
	} else if _, err := uuid.Parse(t.ID); err != nil {
		return t, errors.New("invalid target ID")
	}
	old, err := s.target(ctx, t.ID)
	if err != nil && !errors.Is(err, clusters.ErrNotFound) {
		return t, err
	}
	if err == nil { // Changing the location would strand historical restore points.
		ids, e := s.Store.ListSecretKeys(ctx, s.ClusterID, "remote-backup")
		if e != nil {
			return t, e
		}
		for _, id := range ids {
			var r RemoteBackup
			if e = s.get(ctx, "remote-backup", id, &r); e != nil {
				return t, e
			}
			if r.TargetID == t.ID && (old.Endpoint != t.Endpoint || old.Bucket != t.Bucket || old.Prefix != t.Prefix) {
				return t, errors.New("create a new target to change a location containing backups")
			}
		}
		if t.AccessKey == "" {
			t.AccessKey = old.AccessKey
		}
		if t.SecretKey == "" {
			t.SecretKey = old.SecretKey
		}
	}
	client, err := backupS3(t)
	if err != nil {
		return t, err
	}
	exists, err := client.BucketExists(ctx, t.Bucket)
	if err != nil || !exists {
		return t, errors.New("S3 bucket is not accessible; verify endpoint, trust and credentials")
	}
	if err = s.put(ctx, "backup-target", t.ID, t); err != nil {
		return t, err
	}
	t.AccessKey = ""
	t.SecretKey = ""
	t.Configured = true
	return t, nil
}
func (s *BackupService) Schedule(ctx context.Context) (BackupSchedule, error) {
	q := BackupSchedule{IntervalHours: 6, Retention: 20, TargetID: "local"}
	err := s.get(ctx, "backup-schedule", "schedule", &q)
	if errors.Is(err, clusters.ErrNotFound) {
		err = nil
	}
	return q, err
}
func (s *BackupService) SaveSchedule(ctx context.Context, q BackupSchedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.IntervalHours < 1 || q.IntervalHours > 8760 || q.Retention < 1 || q.Retention > 1000 {
		return errors.New("interval must be 1–8760 hours and retention 1–1000")
	}
	if _, err := s.target(ctx, q.TargetID); err != nil {
		return errors.New("backup target unavailable")
	}
	q.NextRun = time.Now().UTC().Add(time.Duration(q.IntervalHours) * time.Hour)
	return s.put(ctx, "backup-schedule", "schedule", q)
}

// Scheduler submits a durable job before advancing its due time. A crash between
// the journal and registry writes is deduplicated using the persisted due time.
func (s *BackupService) Scheduler(ctx context.Context, jm *jobs.Manager) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.scheduleDue(ctx, jm, time.Now())
		}
	}
}

func (s *BackupService) scheduleDue(ctx context.Context, jm *jobs.Manager, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, err := s.Schedule(ctx)
	if err != nil || !q.Enabled || q.NextRun.IsZero() || now.Before(q.NextRun) {
		return err
	}
	if _, err = jm.Submit(jobs.Request{Kind: "backup-create", BackupType: "full", TargetID: q.TargetID, DedupeKey: "backup:" + q.NextRun.UTC().Format(time.RFC3339Nano)}, "scheduler"); err != nil {
		return err
	}
	q.NextRun = now.UTC().Add(time.Duration(q.IntervalHours) * time.Hour)
	return s.put(ctx, "backup-schedule", "schedule", q)
}
func (s *BackupService) List(ctx context.Context) ([]*backup.BackupInfo, error) {
	local, err := s.Manager.ListBackups()
	if err != nil {
		return nil, err
	}
	if local == nil {
		local = []*backup.BackupInfo{}
	}
	ids, err := s.Store.ListSecretKeys(ctx, s.ClusterID, "remote-backup")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, b := range local {
		seen[b.ID] = true
	}
	for _, id := range ids {
		var r RemoteBackup
		if err = s.get(ctx, "remote-backup", id, &r); err != nil {
			return nil, err
		}
		if !seen[id] {
			b := r.Info
			local = append(local, &b)
		}
	}
	sort.Slice(local, func(i, j int) bool { return local[i].Timestamp.After(local[j].Timestamp) })
	return local, nil
}
func (s *BackupService) Run(ctx context.Context, e *jobs.Execution, r jobs.Request) error {
	if r.Kind == "backup-restore" {
		return s.runRestore(ctx, e, r)
	}
	t, err := s.target(ctx, r.TargetID)
	if err != nil {
		return errors.New("backup target unavailable")
	}
	if err = e.Checkpoint(ctx, "snapshot", "Creating cluster backup"); err != nil {
		return err
	}
	var info *backup.BackupInfo
	if r.BackupType == "etcd" {
		info, err = s.Manager.CreateEtcdSnapshot(ctx, r.Node)
	} else if r.BackupType == "full" || r.BackupType == "" {
		info, err = s.Manager.CreateFullClusterBackup(ctx)
	} else {
		return errors.New("invalid backup type")
	}
	if err != nil {
		return err
	}
	ok, _, err := s.Manager.VerifyBackup(info.ID)
	if err != nil || !ok {
		return errors.New("backup integrity verification failed")
	}
	inventory, invErr := s.restoreInventory(ctx)
	if invErr == nil {
		if err = s.put(ctx, "backup-inventory", info.ID, inventory); err != nil {
			return err
		}
	}
	if err = e.Log("verified", "Verified backup "+info.ID); err != nil {
		return err
	}
	if t.Type == "s3" {
		if err = e.Checkpoint(ctx, "upload", "Uploading verified backup to S3"); err != nil {
			return err
		}
		client, err := backupS3(t)
		if err != nil {
			return err
		}
		_, file, err := s.Manager.GetBackup(info.ID)
		if err != nil {
			return err
		}
		object := path.Join(strings.Trim(t.Prefix, "/"), s.ClusterID, info.Filename)
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = client.PutObject(ctx, t.Bucket, object, f, info.Size, minio.PutObjectOptions{ContentType: "application/octet-stream", UserMetadata: map[string]string{"sha256": info.Checksum}})
		if err != nil {
			return fmt.Errorf("S3 upload uncertain; verified local backup remains available: %w", err)
		}
		stat, err := client.StatObject(ctx, t.Bucket, object, minio.StatObjectOptions{})
		if err != nil || stat.Size != info.Size {
			return errors.New("S3 upload verification failed")
		}
		remote, err := client.GetObject(ctx, t.Bucket, object, minio.GetObjectOptions{})
		if err != nil {
			return errors.New("S3 read-back verification failed")
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(remote, info.Size+1))
		remote.Close()
		if readErr != nil || n != info.Size || hex.EncodeToString(hash.Sum(nil)) != info.Checksum {
			return errors.New("S3 read-back checksum mismatch; local backup retained")
		}
		if err = s.put(ctx, "remote-backup", info.ID, RemoteBackup{Info: *info, TargetID: t.ID, Object: object}); err != nil {
			return err
		}
	}
	if info.Partial {
		return errors.New("backup retained, but some machine configurations were unavailable; archive is partial and retention was not applied")
	}
	if err = e.Checkpoint(ctx, "retention", "Applying backup retention"); err != nil {
		return err
	}
	q, err := s.Schedule(ctx)
	if err != nil {
		return err
	}
	return s.prune(ctx, q.Retention)
}
func (s *BackupService) Delete(ctx context.Context, id string) error {
	var r RemoteBackup
	err := s.get(ctx, "remote-backup", id, &r)
	if err == nil {
		t, e := s.target(ctx, r.TargetID)
		if e != nil {
			return e
		}
		c, e := backupS3(t)
		if e != nil {
			return e
		}
		if e = c.RemoveObject(ctx, t.Bucket, r.Object, minio.RemoveObjectOptions{}); e != nil {
			return fmt.Errorf("remote backup deletion uncertain: %w", e)
		}
		if e = s.Store.DeleteSecret(ctx, s.ClusterID, "remote-backup", id); e != nil {
			return e
		}
	} else if !errors.Is(err, clusters.ErrNotFound) {
		return err
	}
	if _, _, e := s.Manager.GetBackup(id); e == nil {
		return s.Manager.DeleteBackupContext(ctx, id)
	} else if err == nil {
		return nil
	} else {
		return e
	}
}
func (s *BackupService) prune(ctx context.Context, limit int) error {
	if limit < 1 {
		return fmt.Errorf("invalid retention")
	}
	// A failed upload leaves a useful local copy, but must never evict a
	// successful remote restore point. Apply independent quotas per location.
	ids, err := s.Store.ListSecretKeys(ctx, s.ClusterID, "remote-backup")
	if err != nil {
		return err
	}
	byTarget := map[string][]RemoteBackup{}
	for _, id := range ids {
		var r RemoteBackup
		if err = s.get(ctx, "remote-backup", id, &r); err != nil {
			return err
		}
		if !r.Info.Partial {
			byTarget[r.TargetID] = append(byTarget[r.TargetID], r)
		}
	}
	for targetID, records := range byTarget {
		sort.Slice(records, func(i, j int) bool { return records[i].Info.Timestamp.After(records[j].Info.Timestamp) })
		if len(records) <= limit {
			continue
		}
		t, err := s.target(ctx, targetID)
		if err != nil {
			return err
		}
		client, err := backupS3(t)
		if err != nil {
			return err
		}
		for _, r := range records[limit:] {
			if err = client.RemoveObject(ctx, t.Bucket, r.Object, minio.RemoveObjectOptions{}); err != nil {
				return fmt.Errorf("remote retention uncertain; local backups retained: %w", err)
			}
			if err = s.Store.DeleteSecret(ctx, s.ClusterID, "remote-backup", r.Info.ID); err != nil {
				return err
			}
		}
	}
	local, err := s.Manager.ListBackups()
	if err != nil {
		return err
	}
	sort.Slice(local, func(i, j int) bool { return local[i].Timestamp.After(local[j].Timestamp) })
	complete := 0
	for _, info := range local {
		if info.Partial {
			continue // Partial archives neither evict nor replace complete copies.
		}
		complete++
		if complete <= limit {
			continue
		}
		if err = s.Manager.DeleteBackupContext(ctx, info.ID); err != nil {
			return err
		}
	}
	return nil
}

// Each multipart request is admitted separately; read-only retries do not grant mutation rights.
type backupMutationTransport struct{ http.RoundTripper }

func (t backupMutationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		return t.RoundTripper.RoundTrip(req)
	}
	var response *http.Response
	err := reconcile.Mutate(req.Context(), "backup.s3-"+strings.ToLower(req.Method), req.URL.String(), func() error {
		var err error
		response, err = t.RoundTripper.RoundTrip(req)
		if err == nil && response != nil && response.StatusCode >= 400 {
			return fmt.Errorf("S3 mutation rejected with status %d", response.StatusCode)
		}
		return err
	})
	if err != nil && response != nil {
		response.Body.Close()
		response = nil
	}
	return response, err
}
