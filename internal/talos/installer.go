package talos

import (
	"context"
	"fmt"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/client"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/resources/config"
)

// GetInstallerImage returns only the configured installer, never the machine's
// credentials. Keeping its repository/schematic preserves system extensions.
func (m *TalosManager) GetInstallerImage(ctx context.Context, node string) (string, error) {
	c := m.GetClient()
	if c == nil {
		return "", fmt.Errorf("Talos client unavailable")
	}
	mc, err := safe.StateGet[*talosconfig.MachineConfig](client.WithNode(ctx, node), c.COSI, resource.NewMetadata("config", talosconfig.MachineConfigType, talosconfig.ActiveID, resource.VersionUndefined))
	if err != nil {
		return "", err
	}
	if mc == nil || mc.Provider() == nil || mc.Provider().Machine() == nil || mc.Provider().Machine().Install() == nil {
		return "", fmt.Errorf("installer image unavailable on %s", node)
	}
	return mc.Provider().Machine().Install().Image(), nil
}
