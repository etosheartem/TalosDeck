package talos

import (
	"context"
	"log"
	"net/netip"
	"sort"
	"time"

	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/cluster"
)

// clusterNodes discovers the inventory through the authenticated Talos connection,
// not through a possibly unrelated Kubernetes context. Keep configured targets and
// the last successful inventory when discovery is temporarily unavailable.
func (m *TalosManager) clusterNodes(ctx context.Context) []string {
	m.discoveryMu.Lock()
	defer m.discoveryMu.Unlock()
	configured := m.GetConfiguredNodes()
	if time.Since(m.discoveryAt) >= 30*time.Second {
		c := m.GetClient()
		if c != nil && c.COSI != nil {
			discoveryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			// COSI requires a single node target. Try configured targets until one
			// answers; use one bounded deadline for the entire discovery attempt.
			for _, target := range configured {
				members, err := safe.StateListAll[*cluster.Member](client.WithNode(discoveryCtx, target), c.COSI)
				if err != nil {
					if discoveryCtx.Err() != nil {
						break
					}
					continue
				}
				specs := make([]cluster.MemberSpec, 0)
				for member := range members.All() {
					if member != nil {
						specs = append(specs, *member.TypedSpec())
					}
				}
				m.discoveredNodes = memberAddresses(configured, specs)
				m.discoveryAt = time.Now()
				break
			}
			cancel()
			if time.Since(m.discoveryAt) >= 30*time.Second {
				log.Printf("[Warning] Talos member discovery unavailable; using configured and last discovered nodes")
				m.discoveryAt = time.Now()
			}
		}
	}
	set := make(map[string]bool)
	for _, address := range append(configured, m.discoveredNodes...) {
		set[address] = true
	}
	result := make([]string, 0, len(set))
	for address := range set {
		result = append(result, address)
	}
	sort.Strings(result)
	return result
}

// Pick one routable address per machine; dual-stack members are not two nodes.
// Prefer an explicitly configured address/hostname and then IPv4 for lab networks.
func memberAddresses(configured []string, members []cluster.MemberSpec) []string {
	known := make(map[string]bool)
	for _, address := range configured {
		known[address] = true
	}
	result := make([]string, 0, len(members))
	for _, member := range members {
		if known[member.Hostname] {
			continue
		}
		var selected netip.Addr
		alreadyConfigured := false
		for _, raw := range member.Addresses {
			address := raw.Unmap()
			if known[address.String()] {
				alreadyConfigured = true
				break
			}
			if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() {
				continue
			}
			if !selected.IsValid() || (address.Is4() && !selected.Is4()) {
				selected = address
			}
		}
		if !alreadyConfigured && selected.IsValid() && !known[selected.String()] {
			result = append(result, selected.String())
			known[selected.String()] = true
		}
	}
	sort.Strings(result)
	return result
}
