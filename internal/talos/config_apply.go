package talos

import (
	"context"
	"fmt"

	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
)

// ApplyNodeConfig uses the authenticated connection and exactly one selected
// node. Response bodies can contain configuration values and are never logged.
func (m *TalosManager) ApplyNodeConfig(ctx context.Context, node string, data []byte, mode string, dryRun bool) error {
	// REBOOT was removed from Talos 1.14. Stage then explicitly reboot, which
	// preserves the requested semantics on both supported Talos generations.
	modes := map[string]machine.ApplyConfigurationRequest_Mode{"auto": machine.ApplyConfigurationRequest_AUTO, "reboot": machine.ApplyConfigurationRequest_STAGED, "staged": machine.ApplyConfigurationRequest_STAGED}
	selected, ok := modes[mode]
	if !ok {
		return fmt.Errorf("unsupported configuration mode")
	}
	c := m.GetClient()
	if c == nil {
		return fmt.Errorf("Talos client unavailable")
	}
	response, err := c.ApplyConfiguration(client.WithNode(ctx, node), &machine.ApplyConfigurationRequest{Data: data, Mode: selected, DryRun: dryRun})
	if err != nil {
		return fmt.Errorf("Talos rejected configuration request; inspect node diagnostics")
	}
	if response == nil || len(response.Messages) != 1 {
		return fmt.Errorf("Talos returned an unexpected configuration response")
	}
	for _, message := range response.Messages {
		if message == nil {
			return fmt.Errorf("Talos returned an empty configuration response")
		}
		if message.Metadata != nil && message.Metadata.Error != "" {
			return fmt.Errorf("Talos node rejected configuration request")
		}
	}
	if mode == "reboot" && !dryRun {
		if err := c.Reboot(client.WithNode(ctx, node)); err != nil {
			return fmt.Errorf("configuration staged but reboot outcome is uncertain; inspect node before retrying")
		}
	}
	return nil
}
