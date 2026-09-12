package talos

import (
	"context"
	"fmt"
	"strings"

	"github.com/siderolabs/talos/pkg/machinery/client"
)

// CheckUpgradeDisk requires a reserve for image pulls on /var. This is a
// conservative admission threshold, not an estimate of a particular image size.
func (m *TalosManager) CheckUpgradeDisk(ctx context.Context, node string) error {
	c := m.GetClient()
	if c == nil {
		return fmt.Errorf("Talos client unavailable")
	}
	response, err := c.Mounts(client.WithNode(ctx, node))
	if err != nil || response == nil {
		return fmt.Errorf("cannot verify upgrade disk reserve on %s", node)
	}
	found := false
	for _, message := range response.Messages {
		if message == nil || (message.Metadata != nil && message.Metadata.Error != "") {
			return fmt.Errorf("cannot verify upgrade disk reserve on %s", node)
		}
		for _, stat := range message.Stats {
			if stat == nil || strings.TrimRight(stat.MountedOn, "/") != "/var" {
				continue
			}
			found = true
			if err := validateUpgradeReserve(stat.Size, stat.Available); err != nil {
				return fmt.Errorf("%s: %w", node, err)
			}
		}
	}
	if !found {
		return fmt.Errorf("/var filesystem usage unavailable on %s; upgrade disk reserve cannot be verified", node)
	}
	return nil
}
func validateUpgradeReserve(size, available uint64) error {
	if size == 0 || available > size {
		return fmt.Errorf("invalid /var filesystem usage")
	}
	if available < 1024*1024*1024 || available < size/10 {
		return fmt.Errorf("upgrade requires at least 1 GiB and 10%% free space on /var")
	}
	return nil
}
