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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
}

// NewBackupManager creates and initializes a BackupManager.
func NewBackupManager(storageDir string, talosManager *talos.TalosManager) (*BackupManager, error) {
	if storageDir == "" {
		storageDir = "./data/backups"
	}

	if err := os.MkdirAll(storageDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backup storage directory %s: %w", storageDir, err)
	}

	return &BackupManager{
		storageDir:   storageDir,
		talosManager: talosManager,
	}, nil
}

// GetStorageDir returns the active backup storage directory.
func (m *BackupManager) GetStorageDir() string {
	return m.storageDir
}

// CreateEtcdSnapshot triggers an etcd snapshot via the Talos SDK and saves a .snapshot file with timestamp and SHA256 checksum.
func (m *BackupManager) CreateEtcdSnapshot(ctx context.Context, controlPlaneIP string) (*BackupInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.talosManager == nil {
		return nil, errors.New("talos cluster manager is not initialized")
	}

	if err := os.MkdirAll(m.storageDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to ensure backup directory: %w", err)
	}

	// 1. Resolve target control plane IP if not specified
	targetIP := controlPlaneIP
	if targetIP == "" {
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
	filename := fmt.Sprintf("etcd-%s-%s.snapshot", targetIP, timestampStr)
	targetPath := filepath.Join(m.storageDir, filename)

	// 2. Call Talos SDK EtcdSnapshot
	nodeCtx := client.WithNode(ctx, targetIP)
	reader, err := m.talosManager.GetClient().EtcdSnapshot(nodeCtx, &machine.EtcdSnapshotRequest{})
	if err != nil {
		return nil, fmt.Errorf("talos etcd snapshot failed on %s: %w", targetIP, err)
	}
	defer reader.Close()

	// 3. Write snapshot to disk while calculating SHA256
	outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot file %s: %w", targetPath, err)
	}

	hasher := sha256.New()
	multiWriter := io.MultiWriter(outFile, hasher)

	written, copyErr := io.Copy(multiWriter, reader)
	_ = outFile.Close()

	if copyErr != nil {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("failed to stream etcd snapshot to file: %w", copyErr)
	}

	checksum := hex.EncodeToString(hasher.Sum(nil))

	// 4. Save sidecar .sha256 file
	shaPath := targetPath + ".sha256"
	_ = os.WriteFile(shaPath, []byte(fmt.Sprintf("%s  %s\n", checksum, filename)), 0644)

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

	// 5. Save sidecar .json metadata
	metaPath := targetPath + ".json"
	if metaBytes, err := json.MarshalIndent(info, "", "  "); err == nil {
		_ = os.WriteFile(metaPath, metaBytes, 0644)
	}

	return info, nil
}

// CreateFullClusterBackup creates a disaster recovery .tar.gz archive containing:
// - Active talosconfig
// - Node MachineConfigs (retrieved via Talos client)
// - Etcd snapshot
// - Metadata JSON (timestamp, cluster name, node list, Talos version, k8s version).
func (m *BackupManager) CreateFullClusterBackup(ctx context.Context) (*BackupInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.talosManager == nil {
		return nil, errors.New("talos cluster manager is not initialized")
	}

	if err := os.MkdirAll(m.storageDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to ensure backup directory: %w", err)
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

	// 4. Retrieve MachineConfigs for each node
	nodeConfigs := make(map[string][]byte)
	for _, n := range nodes {
		cfgBytes, cfgErr := m.talosManager.GetNodeConfig(ctx, n.IP)
		if cfgErr != nil {
			nodeConfigs[fmt.Sprintf("%s-%s.yaml", n.Hostname, n.IP)] = []byte(fmt.Sprintf("# Failed to retrieve live config for %s (%s): %v\n", n.Hostname, n.IP, cfgErr))
		} else {
			nodeConfigs[fmt.Sprintf("%s-%s.yaml", n.Hostname, n.IP)] = cfgBytes
		}
	}

	// 5. Download etcd snapshot to a temporary file
	tempSnapshotPath := filepath.Join(m.storageDir, fmt.Sprintf(".temp-etcd-%s-%d.snapshot", cpIP, time.Now().UnixNano()))
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
	_ = tempFile.Close()

	if copyErr != nil {
		return nil, fmt.Errorf("failed writing etcd snapshot to temp storage: %w", copyErr)
	}

	tempStat, err := os.Stat(tempSnapshotPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat temporary snapshot: %w", err)
	}
	snapshotSize := tempStat.Size()

	// 6. Assemble .tar.gz archive
	safeClusterName := strings.ReplaceAll(clusterName, " ", "_")
	filename := fmt.Sprintf("cluster-backup-%s-%s.tar.gz", safeClusterName, timestampStr)
	archivePath := filepath.Join(m.storageDir, filename)

	outFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to create backup archive file %s: %w", archivePath, err)
	}

	hasher := sha256.New()
	archiveWriter := io.MultiWriter(outFile, hasher)
	gzWriter := gzip.NewWriter(archiveWriter)
	tarWriter := tar.NewWriter(gzWriter)

	// Helper to add in-memory bytes to tar
	addFileToTar := func(name string, data []byte) error {
		hdr := &tar.Header{
			Name:    name,
			Mode:    0644,
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
		if err := addFileToTar(fmt.Sprintf("machine-configs/%s", cfgName), data); err != nil {
			outFile.Close()
			_ = os.Remove(archivePath)
			return nil, fmt.Errorf("failed to write machine config %s to archive: %w", cfgName, err)
		}
	}

	// Add etcd snapshot from temp file
	etcdHdr := &tar.Header{
		Name:    fmt.Sprintf("etcd/snapshot-%s.snapshot", cpIP),
		Mode:    0644,
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

	// Save sidecar .sha256
	_ = os.WriteFile(archivePath+".sha256", []byte(fmt.Sprintf("%s  %s\n", checksum, filename)), 0644)

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

	// Save sidecar .json metadata
	if infoBytes, err := json.MarshalIndent(info, "", "  "); err == nil {
		_ = os.WriteFile(archivePath+".json", infoBytes, 0644)
	}

	return info, nil
}

// ListBackups scans the storage directory and returns a sorted list of existing backups (newest first).
func (m *BackupManager) ListBackups() ([]*BackupInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if err := os.MkdirAll(m.storageDir, 0755); err != nil {
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
func (m *BackupManager) GetBackup(id string) (*BackupInfo, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cleanID := filepath.Base(id)
	if cleanID == "" || cleanID == "." || cleanID == "/" {
		return nil, "", errors.New("invalid backup ID")
	}

	targetPath := filepath.Join(m.storageDir, cleanID)

	// If file doesn't directly exist, try searching by prefix or extension
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

// DeleteBackup removes a backup file and any sidecars (.sha256, .json) by ID.
func (m *BackupManager) DeleteBackup(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanID := filepath.Base(id)
	if cleanID == "" || cleanID == "." || cleanID == "/" {
		return errors.New("invalid backup ID")
	}

	targetPath := filepath.Join(m.storageDir, cleanID)

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
		if !found {
			return fmt.Errorf("backup %s not found: %w", cleanID, os.ErrNotExist)
		}
	}

	// Delete main file and sidecars
	_ = os.Remove(targetPath + ".sha256")
	_ = os.Remove(targetPath + ".json")

	if err := os.Remove(targetPath); err != nil {
		return fmt.Errorf("failed to remove backup file %s: %w", targetPath, err)
	}

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
