package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// PreflightMachines is a bounded read-only capacity snapshot. Run it again just
// before allocation because external Proxmox administrators can consume capacity.
func (c *Client) PreflightMachines(ctx context.Context, machines []MachineSpec) error {
	var ram uint64
	disks := map[string]uint64{}
	isos := map[string]bool{}
	for _, m := range machines {
		if err := ValidateMachine(m); err != nil {
			return err
		}
		ram += uint64(m.MemoryMB) << 20
		storage := m.Storage
		if storage == "" {
			storage = c.cfg.DefaultStorage
		}
		disks[storage] += uint64(m.DiskGB) << 30
		iso := m.ISO
		if iso == "" {
			iso = c.cfg.DefaultISO
		}
		isos[iso] = true
	}
	node := url.PathEscape(c.cfg.Node)
	var host struct {
		Data struct {
			Memory struct{ Available, Free uint64 } `json:"memory"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/nodes/"+node+"/status", &host); err != nil {
		return fmt.Errorf("cannot verify host memory capacity")
	}
	available := host.Data.Memory.Available
	if available == 0 {
		available = host.Data.Memory.Free
	}
	if available < ram {
		return fmt.Errorf("insufficient host memory: need %d MiB, available %d MiB", ram>>20, available>>20)
	}
	for storage, required := range disks {
		var response struct {
			Data struct {
				Avail   uint64 `json:"avail"`
				Active  int    `json:"active"`
				Enabled int    `json:"enabled"`
			} `json:"data"`
		}
		if err := c.getJSON(ctx, "/nodes/"+node+"/storage/"+url.PathEscape(storage)+"/status", &response); err != nil {
			return fmt.Errorf("cannot verify storage %s", storage)
		}
		if response.Data.Active != 1 || response.Data.Enabled != 1 {
			return fmt.Errorf("storage %s is not active and enabled", storage)
		}
		if response.Data.Avail < required {
			return fmt.Errorf("insufficient storage %s: need %d GiB, available %d GiB", storage, required>>30, response.Data.Avail>>30)
		}
	}
	for iso := range isos {
		storage, _, ok := strings.Cut(iso, ":")
		if !ok {
			return fmt.Errorf("invalid boot ISO")
		}
		var response struct {
			Data []struct {
				Volid string `json:"volid"`
			} `json:"data"`
		}
		if err := c.getJSON(ctx, "/nodes/"+node+"/storage/"+url.PathEscape(storage)+"/content?content=iso", &response); err != nil {
			return fmt.Errorf("cannot verify boot ISO")
		}
		found := false
		for _, item := range response.Data {
			found = found || item.Volid == iso
		}
		if !found {
			return fmt.Errorf("boot ISO %s does not exist on the provider node", iso)
		}
	}
	return nil
}
