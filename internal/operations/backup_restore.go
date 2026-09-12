package operations

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"talosdeck/internal/backup"
	"talosdeck/internal/jobs"
)

type RestoreNode struct {
	IP        string `json:"ip"`
	Version   string `json:"version"`
	Partition string `json:"partition"`
}
type RestorePlan struct {
	ID        string        `json:"id"`
	BackupID  string        `json:"backupId"`
	Warnings  []string      `json:"warnings"`
	Steps     []string      `json:"steps"`
	Nodes     []string      `json:"nodes"`
	Inventory []RestoreNode `json:"inventory"`
	CreatedAt time.Time     `json:"createdAt"`
	Checksum  string        `json:"checksum"`
	User      string        `json:"user"`
	Started   bool          `json:"started"`
}
type restoreInspector interface {
	RestorePartition(context.Context, string) (string, error)
	EtcdPreparing(context.Context, string) bool
}

// Talos etcd snapshots append SHA-256 after the page-aligned database. Validate
// that hash before any reset, not only the outer archive checksum.
func verifyEtcdSnapshot(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() <= sha256.Size || st.Size()%512 != sha256.Size {
		return errors.New("snapshot lacks the required etcd integrity hash")
	}
	h := sha256.New()
	if _, err = io.CopyN(h, f, st.Size()-sha256.Size); err != nil {
		return err
	}
	want := make([]byte, sha256.Size)
	if _, err = io.ReadFull(f, want); err != nil {
		return err
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return errors.New("etcd snapshot hash mismatch")
	}
	return nil
}

// Materialize verifies the entire object before returning a private temporary
// file. Callers must run cleanup; remote ETags are not treated as checksums.
func (s *BackupService) Materialize(ctx context.Context, id string) (*backup.BackupInfo, string, func(), error) {
	info, file, err := s.Manager.GetBackup(id)
	if err == nil {
		ok, _, e := s.Manager.VerifyBackup(id)
		if e != nil || !ok {
			return nil, "", nil, errors.New("local backup checksum mismatch")
		}
		return info, file, func() {}, nil
	}
	var r RemoteBackup
	if err = s.get(ctx, "remote-backup", id, &r); err != nil {
		return nil, "", nil, errors.New("backup unavailable")
	}
	t, err := s.target(ctx, r.TargetID)
	if err != nil {
		return nil, "", nil, err
	}
	c, err := backupS3(t)
	if err != nil {
		return nil, "", nil, err
	}
	obj, err := c.GetObject(ctx, t.Bucket, r.Object, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", nil, errors.New("S3 download failed")
	}
	defer obj.Close()
	f, err := os.CreateTemp(s.Manager.GetStorageDir(), ".restore-*")
	if err != nil {
		return nil, "", nil, err
	}
	cleanup := func() { os.Remove(f.Name()) }
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(obj, r.Info.Size+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != r.Info.Size || hex.EncodeToString(h.Sum(nil)) != r.Info.Checksum {
		cleanup()
		return nil, "", nil, errors.New("downloaded backup integrity verification failed")
	}
	return &r.Info, f.Name(), cleanup, nil
}

// snapshotFile extracts one regular snapshot into a generated filename. Archive
// paths, links and credentials are never extracted to the filesystem.
func snapshotFile(info *backup.BackupInfo, file string) (string, func(), *backup.FullBackupMetadata, error) {
	if info.Type == backup.BackupTypeEtcd {
		return file, func() {}, nil, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return "", nil, nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", nil, nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out *os.File
	var meta backup.FullBackupMetadata
	foundMeta := false
	cleanup := func() {
		if out != nil {
			out.Close()
			os.Remove(out.Name())
		}
	}
	for {
		hdr, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			cleanup()
			return "", nil, nil, e
		}
		if hdr.Name == "metadata.json" {
			if hdr.Size > 1024*1024 {
				cleanup()
				return "", nil, nil, errors.New("oversized backup metadata")
			}
			if e = json.NewDecoder(tr).Decode(&meta); e != nil {
				cleanup()
				return "", nil, nil, e
			}
			foundMeta = true
		}
		if strings.HasPrefix(hdr.Name, "etcd/") && strings.HasSuffix(hdr.Name, ".snapshot") {
			if out != nil || hdr.Typeflag != tar.TypeReg || hdr.Size < 1 || hdr.Size > 64<<30 {
				cleanup()
				return "", nil, nil, errors.New("invalid snapshot entry")
			}
			out, e = os.CreateTemp("", "talosdeck-restore-*.snapshot")
			if e != nil {
				return "", nil, nil, e
			}
			if _, e = io.Copy(out, tr); e != nil {
				cleanup()
				return "", nil, nil, e
			}
			if e = out.Sync(); e != nil {
				cleanup()
				return "", nil, nil, e
			}
			out.Close()
		}
	}
	if out == nil || !foundMeta {
		cleanup()
		return "", nil, nil, errors.New("backup archive lacks snapshot or metadata")
	}
	return out.Name(), cleanup, &meta, nil
}
func (s *BackupService) restoreInventory(ctx context.Context) ([]RestoreNode, error) {
	inspector, ok := s.Operations.Talos.(restoreInspector)
	if !ok {
		return nil, errors.New("restore inspector unavailable")
	}
	nodes, err := s.Operations.Talos.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := []RestoreNode{}
	for _, n := range nodes {
		if n.Role != "controlplane" {
			continue
		}
		label, e := inspector.RestorePartition(ctx, n.IP)
		if e != nil {
			return nil, e
		}
		out = append(out, RestoreNode{IP: n.IP, Version: n.Version, Partition: label})
	}
	if len(out) == 0 {
		return nil, errors.New("no reachable control planes")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out, nil
}
func (s *BackupService) PlanRestore(ctx context.Context, id, user string) (*RestorePlan, error) {
	info, file, cleanup, err := s.Materialize(ctx, id)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	snapshot, cleanSnapshot, meta, err := snapshotFile(info, file)
	if err != nil {
		return nil, err
	}
	defer cleanSnapshot()
	if err = verifyEtcdSnapshot(snapshot); err != nil {
		return nil, err
	}
	inventory, err := s.restoreInventory(ctx)
	if err != nil {
		return nil, err
	}
	if meta != nil {
		for _, node := range inventory {
			found := false
			for _, old := range meta.Nodes {
				if old.IP == node.IP && old.Role == "controlplane" && strings.TrimPrefix(old.Version, "v") == strings.TrimPrefix(node.Version, "v") {
					found = true
				}
			}
			if !found {
				return nil, errors.New("restore requires the original control plane inventory and Talos versions from this backup")
			}
		}
	} else {
		var old []RestoreNode
		if err = s.get(ctx, "backup-inventory", id, &old); err != nil || !reflect.DeepEqual(old, inventory) {
			return nil, errors.New("snapshot has no matching version and inventory record; use the documented manual recovery procedure")
		}
	}
	p := &RestorePlan{ID: uuid.NewString(), BackupID: id, Inventory: inventory, Checksum: info.Checksum, User: user, CreatedAt: time.Now().UTC(), Nodes: []string{}, Steps: []string{"Verify backup and original control planes", "Reset only the partition containing etcd on every control plane", "Wait for every etcd service to prepare", "Bootstrap one control plane from the verified snapshot", "Wait for etcd quorum and Kubernetes readiness"}, Warnings: []string{"The Kubernetes API will be unavailable. All etcd state after this backup will be lost.", "Application volumes are not restored. EPHEMERAL reset also removes local data on affected control planes.", "Keep an external copy of the backup, original machine configs, talosconfig and encryption key before continuing."}}
	for _, n := range inventory {
		p.Nodes = append(p.Nodes, n.IP)
	}
	if info.Partial {
		p.Warnings = append(p.Warnings, "This archive is partial: some machine configurations could not be captured. Verify independent copies before recovery.")
	}
	if err = s.put(ctx, "restore-plan", p.ID, p); err != nil {
		return nil, err
	}
	return p, nil
}
func (s *BackupService) CheckRestorePlan(ctx context.Context, id, user string) error {
	var p RestorePlan
	if err := s.get(ctx, "restore-plan", id, &p); err != nil {
		return errors.New("restore plan unavailable")
	}
	if p.Started || p.User != user || time.Since(p.CreatedAt) > 30*time.Minute {
		return errors.New("restore plan expired, already started or belongs to another user")
	}
	return nil
}
func (s *BackupService) runRestore(ctx context.Context, e *jobs.Execution, r jobs.Request) (result error) {
	var p RestorePlan
	if err := s.get(ctx, "restore-plan", r.RestorePlanID, &p); err != nil {
		return err
	}
	if p.Started || time.Since(p.CreatedAt) > 30*time.Minute {
		return errors.New("restore plan expired or already started")
	}
	if err := e.Checkpoint(ctx, "verify", "Verifying backup and control plane storage layout"); err != nil {
		return err
	}
	info, file, cleanup, err := s.Materialize(ctx, p.BackupID)
	if err != nil {
		return err
	}
	defer cleanup()
	if info.Checksum != p.Checksum {
		return errors.New("backup changed since restore preview")
	}
	snap, cleanSnapshot, _, err := snapshotFile(info, file)
	if err != nil {
		return err
	}
	defer cleanSnapshot()
	if err = verifyEtcdSnapshot(snap); err != nil {
		return err
	}
	inventory, err := s.restoreInventory(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(inventory, p.Inventory) {
		return errors.New("control plane inventory changed since restore preview")
	}
	if err = s.Operations.CLI.Check(ctx); err != nil {
		return err
	}
	p.Started = true
	if err = s.put(ctx, "restore-plan", p.ID, p); err != nil {
		return err
	}
	mutated := false
	defer func() {
		if result != nil && mutated {
			result = fmt.Errorf("%w: restore interrupted; review every control plane before continuing: %v", jobs.ErrUncertain, result)
		}
	}()
	inspector := s.Operations.Talos.(restoreInspector)
	// Once reset starts, complete recovery as one critical step. Stop is honored
	// before the first mutation, never halfway through wiping the quorum.
	if err = e.Checkpoint(ctx, "reset", "Resetting the selected etcd partitions; recovery must finish before stopping"); err != nil {
		return err
	}
	for _, n := range inventory {
		if n.Partition != "ETCD" && n.Partition != "EPHEMERAL" {
			return errors.New("unsafe partition")
		}
		mutated = true
		args := append(s.Operations.args(n.IP), "--endpoints", n.IP, "reset", "--graceful=false", "--reboot", "--system-labels-to-wipe="+n.Partition, "--wait=false")
		if err = s.Operations.CLI.Run(ctx, args, func(line string) error { return e.Log("reset", line) }); err != nil {
			return err
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err = e.Log("prepare", "Waiting for all etcd services to enter Preparing"); err != nil {
		return err
	}
	for {
		ready := true
		for _, n := range inventory {
			if !inspector.EtcdPreparing(waitCtx, n.IP) {
				ready = false
			}
		}
		if ready {
			break
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	args := append(s.Operations.args(inventory[0].IP), "--endpoints", inventory[0].IP, "bootstrap", "--recover-from", snap)
	if err = s.Operations.CLI.Run(ctx, args, func(line string) error { return e.Log("recover", line) }); err != nil {
		return err
	}
	if err = e.Log("health", "Waiting for restored etcd quorum and Kubernetes nodes"); err != nil {
		return err
	}
	for {
		etcd, ee := s.Operations.Talos.GetEtcdStatus(waitCtx)
		_, nodes, ke := s.Operations.Kubernetes.UpgradeInventory(waitCtx)
		ready := ee == nil && etcd != nil && etcd.Healthy && ke == nil && len(nodes) >= len(inventory)
		for _, n := range nodes {
			if !n.Ready {
				ready = false
			}
		}
		if ready {
			return e.Log("complete", "Etcd restored and Kubernetes nodes are Ready; verify application data separately")
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
