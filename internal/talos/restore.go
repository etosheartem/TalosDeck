package talos

import (
	"context"
	"errors"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/block"
	"path"
	"strings"
)

// RestorePartition resolves the backing partition rather than assuming etcd
// always lives in EPHEMERAL. STATE and user volumes are never reset.
func (m *TalosManager) RestorePartition(ctx context.Context, node string) (string, error) {
	c := m.GetClient()
	if c == nil {
		return "", errors.New("Talos unavailable")
	}
	volumes, err := safe.StateListAll[*block.VolumeStatus](client.WithNode(ctx, node), c.COSI)
	if err != nil {
		return "", errors.New("cannot inspect etcd storage layout")
	}
	specs := map[string]block.VolumeStatusSpec{}
	for v := range volumes.All() {
		if v == nil || v.TypedSpec() == nil {
			continue
		}
		specs[string(v.Metadata().ID())] = *v.TypedSpec()
	}
	return restorePartitionFromVolumes(specs)
}

func restorePartitionFromVolumes(specs map[string]block.VolumeStatusSpec) (string, error) {
	if _, present := specs["ETCD"]; !present {
		mount, backing, err := resolveRestoreMount(specs, "EPHEMERAL", map[string]bool{})
		if err == nil && mount == "/var" && backing == "EPHEMERAL" {
			return "EPHEMERAL", nil
		}
		return "", errors.New("cannot identify the partition containing etcd; automatic restore refused")
	}
	mount, backing, err := resolveRestoreMount(specs, "ETCD", map[string]bool{})
	if err != nil || mount != "/var/lib/etcd" || (backing != "ETCD" && backing != "EPHEMERAL") {
		return "", errors.New("unsupported ETCD backing volume; automatic restore refused")
	}
	if backing == "EPHEMERAL" {
		parent, root, e := resolveRestoreMount(specs, "EPHEMERAL", map[string]bool{})
		if e != nil || parent != "/var" || root != "EPHEMERAL" {
			return "", errors.New("unrecognized EPHEMERAL backing partition")
		}
	}
	return backing, nil
}

// MountSpec.ParentID, not the block-device ParentID, describes Talos 1.14's
// directory chain ETCD -> /var/lib -> EPHEMERAL. Follow only known directories
// and partitions, rejecting aliases, cycles, traversal and alternate bind paths.
func resolveRestoreMount(specs map[string]block.VolumeStatusSpec, id string, visited map[string]bool) (string, string, error) {
	spec, ok := specs[id]
	if !ok || visited[id] || len(visited) >= 32 || spec.MountSpec.BindTarget != nil {
		return "", "", errors.New("unsupported volume mount chain")
	}
	if spec.Type != block.VolumeTypeDirectory && spec.Type != block.VolumeTypePartition {
		return "", "", errors.New("unsupported volume backing type")
	}
	visited[id] = true
	target := strings.TrimRight(spec.MountSpec.TargetPath, "/")
	if target == "" {
		return "", "", errors.New("empty volume mount")
	}
	for _, component := range strings.Split(target, "/") {
		if component == ".." {
			return "", "", errors.New("volume path traversal")
		}
	}
	parentID := spec.MountSpec.ParentID
	// Some older directory resources identify their backing parent directly.
	if parentID == "" && spec.Type == block.VolumeTypeDirectory {
		parentID = spec.ParentID
	}
	root := id
	if parentID != "" {
		parent, backing, err := resolveRestoreMount(specs, parentID, visited)
		if err != nil {
			return "", "", err
		}
		if path.IsAbs(target) {
			if !strings.HasPrefix(path.Clean(target), parent+"/") {
				return "", "", errors.New("mount escapes backing parent")
			}
		} else {
			target = path.Join(parent, target)
		}
		if spec.Type == block.VolumeTypeDirectory {
			root = backing
		}
	} else if spec.Type == block.VolumeTypeDirectory || !path.IsAbs(target) {
		return "", "", errors.New("unidentified volume parent")
	}
	if spec.Type == block.VolumeTypePartition && spec.Location == "" {
		return "", "", errors.New("partition location unavailable")
	}
	return path.Clean(target), root, nil
}
func (m *TalosManager) EtcdPreparing(ctx context.Context, node string) bool {
	services, err := m.ListServices(ctx, node)
	if err != nil {
		return false
	}
	for _, s := range services {
		if s.ID == "etcd" {
			return strings.EqualFold(s.State, "Preparing")
		}
	}
	return false
}
