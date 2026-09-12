package talos

import (
	"context"
	"errors"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/block"
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
	ephemeral := false
	for v := range volumes.All() {
		if v == nil || v.TypedSpec() == nil {
			continue
		}
		spec := v.TypedSpec()
		id := string(v.Metadata().ID())
		if id == "ETCD" && spec.Type == block.VolumeTypePartition && spec.Location != "" && strings.TrimRight(spec.MountSpec.TargetPath, "/") == "/var/lib/etcd" {
			return "ETCD", nil
		}
		if id == "ETCD" && spec.Type == block.VolumeTypePartition {
			return "", errors.New("unrecognized ETCD partition mount")
		}
		if id == "EPHEMERAL" && spec.Type == block.VolumeTypePartition && spec.Location != "" && strings.TrimRight(spec.MountSpec.TargetPath, "/") == "/var" {
			ephemeral = true
		}
	}
	if ephemeral {
		return "EPHEMERAL", nil
	}
	return "", errors.New("cannot identify the partition containing etcd; automatic restore refused")
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
