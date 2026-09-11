package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"

	"talosdeck/internal/talos"
)

// BackupType indicates the kind of backup.
type BackupType string

const (
	BackupTypeEtcd BackupType = "etcd"
	BackupTypeFull BackupType = "full"
)

// NodeMeta holds metadata about a node at backup time.
type NodeMeta struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Role     string `json:"role"`
	Version  string `json:"version,omitempty"`
	Ready    bool   `json:"ready"`
}

// FullBackupMetadata describes the contents and environment of a full cluster backup.
type FullBackupMetadata struct {
	Timestamp         time.Time  `json:"timestamp"`
	ClusterName       string     `json:"clusterName"`
	Endpoint          string     `json:"endpoint,omitempty"`
	TalosVersion      string     `json:"talosVersion,omitempty"`
	KubernetesVersion string     `json:"kubernetesVersion,omitempty"`
	Nodes             []NodeMeta `json:"nodes"`
	BackupType        string     `json:"backupType"`
	ArchiveFormat     string     `json:"archiveFormat"`
}

// BackupInfo describes an existing backup artifact.
type BackupInfo struct {
	ID          string     `json:"id"`
	Filename    string     `json:"filename"`
	Size        int64      `json:"size"`
	HumanSize   string     `json:"humanSize"`
	Timestamp   time.Time  `json:"timestamp"`
	Type        BackupType `json:"type"`
	Checksum    string     `json:"checksum"` // SHA256 hex string
	ClusterName string     `json:"clusterName,omitempty"`
	Node        string     `json:"node,omitempty"`
	NodeCount   int        `json:"nodeCount,omitempty"`
	Description string     `json:"description,omitempty"`
}

// BackupManager handles creation, listing, deletion, and downloading of cluster backups.
type BackupManager struct {
	storageDir   string
	talosManager *talos.TalosManager
	mu           sync.RWMutex
	isBackingUp  atomic.Bool
	maxBackups   int
}

// sanitizeFilename strips or replaces directory separators and invalid characters.
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	res := strings.Trim(b.String(), "._-")
	if res == "" {
		return "unnamed"
	}
	return res
}

// checkDiskSpace verifies that storageDir has at least minBytes available disk space (BKP-07).
func checkDiskSpace(dir string, minBytes uint64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		// If statfs fails on specialized virtual mounts, do not block execution
		return nil
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	if freeBytes < minBytes {
		return fmt.Errorf("insufficient disk space in %s: %d MB available, %d MB required",
			dir, freeBytes/(1024*1024), minBytes/(1024*1024))
	}
	return nil
}

// NewBackupManager creates and initializes a BackupManager with strict 0700 directory permissions (BKP-05, BKP-06).
func NewBackupManager(storageDir string, talosManager *talos.TalosManager) (*BackupManager, error) {
	if storageDir == "" {
		storageDir = "./data/backups"
	}

	// BKP-06: Create directory with secure 0700 permissions
	if err := os.MkdirAll(storageDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create backup storage directory %s: %w", storageDir, err)
	}

	bm := &BackupManager{
		storageDir:   storageDir,
		talosManager: talosManager,
		maxBackups:   20, // Keep last 20 backups by default (BKP-07)
	}

	// BKP-08: Clean up orphan temporary files (.temp-etcd-*) from previous crashed runs
	bm.cleanStaleTempFiles()

	return bm, nil
}

// cleanStaleTempFiles cleans up any leftover .temp-etcd-* files.
func (m *BackupManager) cleanStaleTempFiles() {
	entries, err := os.ReadDir(m.storageDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".temp-etcd-") {
			_ = os.Remove(filepath.Join(m.storageDir, entry.Name()))
		}
	}
}

// SetMaxBackups configures the retention quota limit.
func (m *BackupManager) SetMaxBackups(limit int) {
	if limit > 0 {
		m.maxBackups = limit
	}
}

// GetStorageDir returns the active backup storage directory.
func (m *BackupManager) GetStorageDir() string {
	return m.storageDir
}

// rotateBackups removes oldest backups if total exceeds limit (BKP-07).
func (m *BackupManager) rotateBackups(limit int) {
	if limit <= 0 {
		return
	}
	backups, err := m.ListBackups()
	if err != nil || len(backups) <= limit {
		return
	}
	// backups are sorted newest first, delete from index limit onwards
	for i := limit; i < len(backups); i++ {
		_ = m.DeleteBackup(backups[i].ID)
	}
}

// CreateEtcdSnapshot triggers an etcd snapshot via the Talos SDK and saves a .snapshot file.
// BKP-04: Non-blocking atomic concurrency guard prevents UI deadlocks during streaming.
// BKP-05: Secure 0600 file permissions.
// BKP-03: IP address validation prevents path injection.
func (m *BackupManager) CreateEtcdSnapshot(ctx context.Context, controlPlaneIP string) (*BackupInfo, error) {
	// BKP-04 / BKP-05: Prevent overlapping backups and deadlock
	if !m.isBackingUp.CompareAndSwap(false, true) {
		return nil, errors.New("backup operation is already in progress")
	}
	defer m.isBackingUp.Store(false)

	if m.talosManager == nil {
		return nil, errors.New("talos cluster manager is not initialized")
	}

	if err := os.MkdirAll(m.storageDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to ensure backup directory: %w", err)
	}

	// BKP-07: Verify disk space (at least 200MB free)
	if err := checkDiskSpace(m.storageDir, 200*1024*1024); err != nil {
		return nil, err
	}

	// 1. Resolve target control plane IP if not specified and validate IP format (BKP-03)
	targetIP := strings.TrimSpace(controlPlaneIP)
	if targetIP != "" {
		if net.ParseIP(targetIP) == nil {
			return nil, fmt.Errorf("invalid control plane IP address: %q", targetIP)
		}
	} else {
		nodes, err := m.talosManager.ListNodes(ctx)
		if err == nil {
			for _, n := range nodes {
				if n.Role == "controlplane" && n.Ready {
					targetIP = n.IP
					break
				}
			}
		}
		if targetIP == "" {
			endpoints := m.talosManager.GetEndpoints()
			if len(endpoints) > 0 {
				targetIP = endpoints[0]
			} else {
				targetIP = "10.42.0.110"
			}
		}
	}

	now := time.Now().UTC()
	timestampStr := now.Format("20060102-150405")
	filename := fmt.Sprintf("etcd-%s-%s.snapshot", sanitizeFilename(targetIP), timestampStr)
	targetPath := filepath.Join(m.storageDir, filename)

	// 2. Call Talos SDK EtcdSnapshot
	nodeCtx := client.WithNode(ctx, targetIP)
	reader, err := m.talosManager.GetClient().EtcdSnapshot(nodeCtx, &machine.EtcdSnapshotRequest{})
	if err != nil {
		return nil, fmt.Errorf("talos etcd snapshot failed on %s: %w", targetIP, err)
	}
	defer reader.Close()

	// 3. Write snapshot to disk with 0600 permissions (BKP-05) while calculating SHA256
	outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot file %s: %w", targetPath, err)
	}

	hasher := sha256.New()
	multiWriter := io.MultiWriter(outFile, hasher)

	written, copyErr := io.Copy(multiWriter, reader)
	closeErr := outFile.Close() // BKP-09: check Close error

	if copyErr != nil {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("failed to stream etcd snapshot to file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("failed to flush snapshot file to disk: %w", closeErr)
	}

	checksum := hex.EncodeToString(hasher.Sum(nil))

	// 4. Save sidecar .sha256 file with 0600 permissions (BKP-05, BKP-09)
	shaPath := targetPath + ".sha256"
	if err := os.WriteFile(shaPath, []byte(fmt.Sprintf("%s  %s\n", checksum, filename)), 0600); err != nil {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("failed to write sha256 sidecar file: %w", err)
	}

	clusterName := m.talosManager.GetConfig().Context
	if clusterName == "" {
		clusterName = "talos-cluster"
	}

	info := &BackupInfo{
		ID:          filename,
		Filename:    filename,
		Size:        written,
		HumanSize:   formatSize(written),
		Timestamp:   now,
		Type:        BackupTypeEtcd,
		Checksum:    checksum,
		ClusterName: clusterName,
		Node:        targetIP,
		Description: fmt.Sprintf("Etcd database snapshot from control plane %s", targetIP),
	}

	// 5. Save sidecar .json metadata with 0600 permissions (BKP-05, BKP-09)
	metaPath := targetPath + ".json"
	metaBytes, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		_ = os.Remove(targetPath)
		_ = os.Remove(shaPath)
		return nil, fmt.Errorf("failed to serialize backup metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, metaBytes, 0600); err != nil {
		_ = os.Remove(targetPath)
		_ = os.Remove(shaPath)
		return nil, fmt.Errorf("failed to write metadata sidecar file: %w", err)
	}

	// Rotate backups according to retention policy
	m.rotateBackups(m.maxBackups)

	return info, nil
}

// CreateFullClusterBackup creates a disaster recovery .tar.gz archive.
// BKP-04: Runs streaming outside of global locks.
// BKP-05: Strict 0600 file permissions and 0700 dir permissions.
// BKP-02: Sanitizes tar internal paths against TarSlip / ZipSlip.
func (m *BackupManager) CreateFullClusterBackup(ctx context.Context) (*BackupInfo, error) {
	// BKP-04 / BKP-05: Non-blocking atomic guard
	if !m.isBackingUp.CompareAndSwap(false, true) {
		return nil, errors.New("backup operation is already in progress")
	}
	defer m.isBackingUp.Store(false)

	if m.talosManager == nil {
		return nil, errors.New("talos cluster manager is not initialized")
	}

	if err := os.MkdirAll(m.storageDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to ensure backup directory: %w", err)
	}

	// BKP-07: Verify disk space (at least 500MB free)
	if err := checkDiskSpace(m.storageDir, 500*1024*1024); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	timestampStr := now.Format("20060102-150405")

	// 1. Gather cluster info
	clusterInfo, _ := m.talosManager.GetClusterInfo(ctx)
	clusterName := "talos-cluster"
	talosVersion := "unknown"
	k8sVersion := "unknown"
	endpoint := ""
	if clusterInfo != nil {
		if clusterInfo.Name != "" {
			clusterName = clusterInfo.Name
		}
		talosVersion = clusterInfo.TalosVersion
		k8sVersion = clusterInfo.KubernetesVersion
		endpoint = clusterInfo.Endpoint
	}

	// 2. Query nodes
	nodes, err := m.talosManager.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query cluster nodes for backup: %w", err)
	}

	var nodeMetas []NodeMeta
	var cpIP string
	for _, n := range nodes {
		nodeMetas = append(nodeMetas, NodeMeta{
			IP:       n.IP,
			Hostname: n.Hostname,
			Role:     n.Role,
			Version:  n.Version,
			Ready:    n.Ready,
		})
		if n.Role == "controlplane" && n.Ready && cpIP == "" {
			cpIP = n.IP
		}
	}
	if cpIP == "" {
		endpoints := m.talosManager.GetEndpoints()
		if len(endpoints) > 0 {
			cpIP = endpoints[0]
		} else {
			cpIP = "10.42.0.110"
		}
	}

	// 3. Retrieve active talosconfig
	talosconfigBytes, err := m.talosManager.GetRawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve active talosconfig: %w", err)
	}

	// 4. Retrieve MachineConfigs for each node, sanitizing keys (BKP-02)
	nodeConfigs := make(map[string][]byte)
	for _, n := range nodes {
		safeHost := sanitizeFilename(n.Hostname)
		safeIP := sanitizeFilename(n.IP)
		configFileName := fmt.Sprintf("%s-%s.yaml", safeHost, safeIP)

		cfgBytes, cfgErr := m.talosManager.GetNodeConfig(ctx, n.IP)
		if cfgErr != nil {
			nodeConfigs[configFileName] = []byte(fmt.Sprintf("# Failed to retrieve live config for %s (%s): %v\n", n.Hostname, n.IP, cfgErr))
		} else {
			nodeConfigs[configFileName] = cfgBytes
		}
	}

	// 5. Download etcd snapshot to a temporary file (BKP-08: clean temp files)
	tempSnapshotPath := filepath.Join(m.storageDir, fmt.Sprintf(".temp-etcd-%s-%d.snapshot", sanitizeFilename(cpIP), time.Now().UnixNano()))
	defer os.Remove(tempSnapshotPath)

	nodeCtx := client.WithNode(ctx, cpIP)
	snapshotReader, err := m.talosManager.GetClient().EtcdSnapshot(nodeCtx, &machine.EtcdSnapshotRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to stream etcd snapshot from %s: %w", cpIP, err)
	}

	tempFile, err := os.OpenFile(tempSnapshotPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		snapshotReader.Close()
		return nil, fmt.Errorf("failed to create temporary snapshot file: %w", err)
	}

	_, copyErr := io.Copy(tempFile, snapshotReader)
	snapshotReader.Close()
	closeTempErr := tempFile.Close()

	if copyErr != nil {
		return nil, fmt.Errorf("failed writing etcd snapshot to temp storage: %w", copyErr)
	}
	if closeTempErr != nil {
		return nil, fmt.Errorf("failed closing temp snapshot file: %w", closeTempErr)
	}

	tempStat, err := os.Stat(tempSnapshotPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat temporary snapshot: %w", err)
	}
	snapshotSize := tempStat.Size()

	// 6. Assemble .tar.gz archive
	safeClusterName := sanitizeFilename(clusterName)
	filename := fmt.Sprintf("cluster-backup-%s-%s.tar.gz", safeClusterName, timestampStr)
	archivePath := filepath.Join(m.storageDir, filename)

	// BKP-05: Create archive with 0600 permissions
	outFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create backup archive file %s: %w", archivePath, err)
	}

	hasher := sha256.New()
	archiveWriter := io.MultiWriter(outFile, hasher)
	gzWriter := gzip.NewWriter(archiveWriter)
	tarWriter := tar.NewWriter(gzWriter)

	// Helper to add in-memory bytes to tar (BKP-02: ensure paths are safe and clean)
	addFileToTar := func(name string, data []byte) error {
		cleanName := filepath.Clean(name)
		if strings.HasPrefix(cleanName, "/") || strings.HasPrefix(cleanName, "..") {
			return fmt.Errorf("illegal tar entry path: %s", name)
		}
		hdr := &tar.Header{
			Name:    cleanName,
			Mode:    0600,
			Size:    int64(len(data)),
			ModTime: now,
		}
		if err := tarWriter.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tarWriter.Write(data)
		return err
	}

	// Add metadata.json
	metaObj := FullBackupMetadata{
		Timestamp:         now,
		ClusterName:       clusterName,
		Endpoint:          endpoint,
		TalosVersion:      talosVersion,
		KubernetesVersion: k8sVersion,
		Nodes:             nodeMetas,
		BackupType:        "full",
		ArchiveFormat:     "tar.gz",
	}
	metaBytes, _ := json.MarshalIndent(metaObj, "", "  ")
	if err := addFileToTar("metadata.json", metaBytes); err != nil {
		outFile.Close()
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to write metadata.json to archive: %w", err)
	}

	// Add talosconfig
	if err := addFileToTar("talosconfig", talosconfigBytes); err != nil {
		outFile.Close()
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to write talosconfig to archive: %w", err)
	}

	// Add node machine configs
	for cfgName, data := range nodeConfigs {
		tarPath := fmt.Sprintf("machine-configs/%s", cfgName)
		if err := addFileToTar(tarPath, data); err != nil {
			outFile.Close()
			_ = os.Remove(archivePath)
			return nil, fmt.Errorf("failed to write machine config %s to archive: %w", cfgName, err)
		}
	}

	// Add etcd snapshot from temp file
	etcdEntryName := fmt.Sprintf("etcd/snapshot-%s.snapshot", sanitizeFilename(cpIP))
	etcdHdr := &tar.Header{
		Name:    etcdEntryName,
		Mode:    0600,
		Size:    snapshotSize,
		ModTime: now,
	}
	if err := tarWriter.WriteHeader(etcdHdr); err != nil {
		outFile.Close()
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to write etcd snapshot header to archive: %w", err)
	}

	tempFileReader, err := os.Open(tempSnapshotPath)
	if err != nil {
		outFile.Close()
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to open temp snapshot for archiving: %w", err)
	}
	_, copyTarErr := io.Copy(tarWriter, tempFileReader)
	tempFileReader.Close()
	if copyTarErr != nil {
		outFile.Close()
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to copy etcd snapshot into archive: %w", copyTarErr)
	}

	// Flush and close archive
	if err := tarWriter.Close(); err != nil {
		outFile.Close()
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to close tar writer: %w", err)
	}
	if err := gzWriter.Close(); err != nil {
		outFile.Close()
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}
	// BKP-09: check Close error
	if err := outFile.Close(); err != nil {
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to close archive file: %w", err)
	}

	checksum := hex.EncodeToString(hasher.Sum(nil))

	// Get final archive file size
	finalStat, err := os.Stat(archivePath)
	var finalSize int64
	if err == nil {
		finalSize = finalStat.Size()
	}

	// BKP-05, BKP-09: Save sidecar .sha256 with 0600 and check error
	shaPath := archivePath + ".sha256"
	if err := os.WriteFile(shaPath, []byte(fmt.Sprintf("%s  %s\n", checksum, filename)), 0600); err != nil {
		_ = os.Remove(archivePath)
		return nil, fmt.Errorf("failed to write sha256 sidecar file: %w", err)
	}

	info := &BackupInfo{
		ID:          filename,
		Filename:    filename,
		Size:        finalSize,
		HumanSize:   formatSize(finalSize),
		Timestamp:   now,
		Type:        BackupTypeFull,
		Checksum:    checksum,
		ClusterName: clusterName,
		NodeCount:   len(nodes),
		Description: fmt.Sprintf("Full disaster recovery archive: %d nodes, talosconfig, machine configs, etcd snapshot", len(nodes)),
	}

	// BKP-05, BKP-09: Save sidecar .json metadata with 0600 and check error
	infoBytes, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		_ = os.Remove(archivePath)
		_ = os.Remove(shaPath)
		return nil, fmt.Errorf("failed to serialize backup metadata: %w", err)
	}
	if err := os.WriteFile(archivePath+".json", infoBytes, 0600); err != nil {
		_ = os.Remove(archivePath)
		_ = os.Remove(shaPath)
		return nil, fmt.Errorf("failed to write metadata sidecar file: %w", err)
	}

	// Rotate backups according to retention policy
	m.rotateBackups(m.maxBackups)

	return info, nil
}

// ListBackups scans the storage directory and returns a sorted list of existing backups (newest first).
func (m *BackupManager) ListBackups() ([]*BackupInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := os.MkdirAll(m.storageDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to access backup directory: %w", err)
	}

	entries, err := os.ReadDir(m.storageDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	var backups []*BackupInfo

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Filter out temporary files and sidecar files (.sha256, .json)
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".sha256") || strings.HasSuffix(name, ".json") {
			continue
		}

		// Check supported backup extensions
		isSnapshot := strings.HasSuffix(name, ".snapshot")
		isArchive := strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tgz")
		if !isSnapshot && !isArchive {
			continue
		}

		fullPath := filepath.Join(m.storageDir, name)
		fileInfo, err := entry.Info()
		if err != nil {
			continue
		}

		// Try loading companion .json
		var item BackupInfo
		metaPath := fullPath + ".json"
		if data, err := os.ReadFile(metaPath); err == nil {
			if json.Unmarshal(data, &item) == nil && item.Filename != "" {
				// Refresh file size
				item.Size = fileInfo.Size()
				item.HumanSize = formatSize(fileInfo.Size())
				if item.Timestamp.IsZero() {
					item.Timestamp = fileInfo.ModTime().UTC()
				}
				backups = append(backups, &item)
				continue
			}
		}

		// Fallback: construct from file and sidecars
		bType := BackupTypeFull
		if isSnapshot || strings.Contains(name, "etcd") {
			bType = BackupTypeEtcd
		}

		checksum := ""
		shaPath := fullPath + ".sha256"
		if shaBytes, err := os.ReadFile(shaPath); err == nil {
			fields := strings.Fields(string(shaBytes))
			if len(fields) > 0 {
				checksum = fields[0]
			}
		}

		backups = append(backups, &BackupInfo{
			ID:          name,
			Filename:    name,
			Size:        fileInfo.Size(),
			HumanSize:   formatSize(fileInfo.Size()),
			Timestamp:   fileInfo.ModTime().UTC(),
			Type:        bType,
			Checksum:    checksum,
			Description: fmt.Sprintf("Cluster backup file (%s)", bType),
		})
	}

	// Sort descending by timestamp
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	return backups, nil
}

// GetBackup returns the BackupInfo and the verified absolute path of a backup by ID or filename.
// BKP-01: Full protection against path traversal (cleanID == "..", absolute paths, escaping).
func (m *BackupManager) GetBackup(id string) (*BackupInfo, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cleanID := filepath.Base(filepath.Clean(id))
	if cleanID == "" || cleanID == "." || cleanID == ".." || cleanID == "/" || cleanID == "\\" ||
		strings.Contains(cleanID, "/") || strings.Contains(cleanID, "\\") {
		return nil, "", errors.New("invalid backup ID: path traversal attempt detected")
	}

	targetPath := filepath.Join(m.storageDir, cleanID)

	// Lexical isolation check (BKP-01)
	cleanStorage := filepath.Clean(m.storageDir)
	cleanTarget := filepath.Clean(targetPath)
	if !strings.HasPrefix(cleanTarget, cleanStorage+string(filepath.Separator)) && cleanTarget != cleanStorage {
		return nil, "", errors.New("invalid backup ID: path traversal attempt detected")
	}

	// If file doesn't directly exist, try searching by candidates
	if _, err := os.Stat(targetPath); err != nil {
		candidates := []string{
			targetPath + ".snapshot",
			targetPath + ".tar.gz",
			targetPath + ".zip",
		}
		found := false
		for _, cand := range candidates {
			if _, err := os.Stat(cand); err == nil {
				targetPath = cand
				cleanID = filepath.Base(cand)
				found = true
				break
			}
		}
		if !found {
			return nil, "", fmt.Errorf("backup %s not found: %w", cleanID, os.ErrNotExist)
		}
	}

	fileInfo, err := os.Stat(targetPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to stat backup file: %w", err)
	}

	// Load meta if available
	var item BackupInfo
	metaPath := targetPath + ".json"
	if data, err := os.ReadFile(metaPath); err == nil {
		if json.Unmarshal(data, &item) == nil && item.Filename != "" {
			item.Size = fileInfo.Size()
			item.HumanSize = formatSize(fileInfo.Size())
			return &item, targetPath, nil
		}
	}

	bType := BackupTypeFull
	if strings.HasSuffix(cleanID, ".snapshot") || strings.Contains(cleanID, "etcd") {
		bType = BackupTypeEtcd
	}

	checksum := ""
	if shaBytes, err := os.ReadFile(targetPath + ".sha256"); err == nil {
		fields := strings.Fields(string(shaBytes))
		if len(fields) > 0 {
			checksum = fields[0]
		}
	}

	return &BackupInfo{
		ID:        cleanID,
		Filename:  cleanID,
		Size:      fileInfo.Size(),
		HumanSize: formatSize(fileInfo.Size()),
		Timestamp: fileInfo.ModTime().UTC(),
		Type:      bType,
		Checksum:  checksum,
	}, targetPath, nil
}

// VerifyBackup validates the integrity of a backup artifact by recalculating its SHA256 checksum (BKP-10).
func (m *BackupManager) VerifyBackup(id string) (bool, string, error) {
	info, filePath, err := m.GetBackup(id)
	if err != nil {
		return false, "", err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return false, "", fmt.Errorf("failed to open backup file for verification: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return false, "", fmt.Errorf("failed to calculate checksum: %w", err)
	}

	computed := hex.EncodeToString(hasher.Sum(nil))
	valid := computed == info.Checksum && info.Checksum != ""
	return valid, computed, nil
}

// DeleteBackup removes a backup file and its sidecars (.sha256, .json) by ID.
// BKP-01: Path traversal protection.
// BKP-06 / BKP-11: Deletes main file before sidecars to prevent orphan corrupt archives.
// BKP-12: Cleans up orphan sidecars even if main file is missing.
// BKP-13: Refuses to delete directories.
func (m *BackupManager) DeleteBackup(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanID := filepath.Base(filepath.Clean(id))
	if cleanID == "" || cleanID == "." || cleanID == ".." || cleanID == "/" || cleanID == "\\" ||
		strings.Contains(cleanID, "/") || strings.Contains(cleanID, "\\") {
		return errors.New("invalid backup ID: path traversal attempt detected")
	}

	targetPath := filepath.Join(m.storageDir, cleanID)

	cleanStorage := filepath.Clean(m.storageDir)
	cleanTarget := filepath.Clean(targetPath)
	if !strings.HasPrefix(cleanTarget, cleanStorage+string(filepath.Separator)) && cleanTarget != cleanStorage {
		return errors.New("invalid backup ID: path traversal attempt detected")
	}

	// Check if exact file exists, or check common extensions
	if _, err := os.Stat(targetPath); err != nil {
		candidates := []string{
			targetPath + ".snapshot",
			targetPath + ".tar.gz",
			targetPath + ".zip",
		}
		found := false
		for _, cand := range candidates {
			if _, err := os.Stat(cand); err == nil {
				targetPath = cand
				found = true
				break
			}
		}
		// BKP-12: Check if sidecars exist even if main file is gone
		if !found {
			hasSidecars := false
			if _, err := os.Stat(targetPath + ".sha256"); err == nil {
				hasSidecars = true
			}
			if _, err := os.Stat(targetPath + ".json"); err == nil {
				hasSidecars = true
			}
			if !hasSidecars {
				return fmt.Errorf("backup %s not found: %w", cleanID, os.ErrNotExist)
			}
		}
	}

	// BKP-13: Reject if target is a directory
	if info, err := os.Stat(targetPath); err == nil {
		if info.IsDir() {
			return errors.New("target is a directory, not a backup file")
		}
	}

	// BKP-11: First remove the main backup file
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove backup file %s: %w", targetPath, err)
	}

	// Only clean sidecars after main file is successfully removed (or was already missing)
	_ = os.Remove(targetPath + ".sha256")
	_ = os.Remove(targetPath + ".json")

	return nil
}

// formatSize produces human-readable disk sizes.
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
