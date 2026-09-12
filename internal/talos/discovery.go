package talos

import (
	"context"
	"log"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
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
			// A cold mTLS connection can take over six seconds even when warm
			// resource reads take milliseconds. Keep discovery inside the HTTP
			// request's 12s budget while leaving time for the warm status reads.
			discoveryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			if specs, ok := discoverMembers(discoveryCtx, c, append(m.GetEndpoints(), configured...)); ok {
				aliases := resolveMemberAliases(discoveryCtx, configured, specs)
				m.discoveredNodes = canonicalMemberInventory(configured, specs, aliases)
				m.discoveryAt = time.Now()
			}
			cancel()
			if time.Since(m.discoveryAt) >= 30*time.Second {
				log.Printf("[Warning] Talos member discovery unavailable; using configured and last discovered nodes")
				m.discoveryAt = time.Now()
			}
		}
	}
	if len(m.discoveredNodes) > 0 {
		return append([]string(nil), m.discoveredNodes...)
	}
	return canonicalMemberInventory(configured, nil, nil)
}

// Try at most four targets at once: one unreachable configured node must not
// consume the entire discovery deadline and hide the healthy members.
func discoverMembers(ctx context.Context, c *client.Client, targets []string) ([]cluster.MemberSpec, bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan string, len(targets))
	seen := map[string]bool{}
	for _, target := range targets {
		if target != "" && !seen[target] {
			queue <- target
			seen[target] = true
		}
	}
	close(queue)
	result := make(chan []cluster.MemberSpec, 1)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(seen)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for target := range queue {
				if ctx.Err() != nil {
					return
				}
				members, err := safe.StateListAll[*cluster.Member](client.WithNode(ctx, target), c.COSI)
				if err != nil {
					continue
				}
				specs := []cluster.MemberSpec{}
				for member := range members.All() {
					if member != nil {
						specs = append(specs, *member.TypedSpec())
					}
				}
				if len(specs) == 0 {
					continue
				}
				select {
				case result <- specs:
					cancel()
				default:
				}
				return
			}
		}()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case specs := <-result:
		return specs, true
	case <-done:
		select {
		case specs := <-result:
			return specs, true
		default:
			return nil, false
		}
	case <-ctx.Done():
		select {
		case specs := <-result:
			return specs, true
		default:
			return nil, false
		}
	}
}

func normalizedHostname(name string) string { return strings.TrimSuffix(strings.ToLower(name), ".") }

func resolveMemberAliases(ctx context.Context, configured []string, members []cluster.MemberSpec) map[string][]netip.Addr {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	names := map[string]bool{}
	for _, member := range members {
		names[normalizedHostname(member.Hostname)] = true
	}
	resolved := map[string][]netip.Addr{}
	for _, target := range configured {
		if _, err := netip.ParseAddr(target); err == nil || names[normalizedHostname(target)] {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		if addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", target); err == nil {
			resolved[target] = addresses
		}
	}
	return resolved
}

// Return a complete canonical inventory, replacing hostname aliases with member
// IPs. Keep unmatched configured targets so an unreachable machine remains
// visible; dual-stack addresses and DNS aliases must not create extra nodes.
func canonicalMemberInventory(configured []string, members []cluster.MemberSpec, resolved map[string][]netip.Addr) []string {
	preferred := map[netip.Addr]bool{}
	for _, target := range configured {
		if ip, err := netip.ParseAddr(target); err == nil {
			preferred[ip.Unmap()] = true
		}
		for _, ip := range resolved[target] {
			preferred[ip.Unmap()] = true
		}
	}
	set := map[string]bool{}
	memberIPs := map[netip.Addr]bool{}
	hostnames := map[string]bool{}
	for _, member := range members {
		var selected netip.Addr
		for _, raw := range member.Addresses {
			address := raw.Unmap()
			if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() {
				continue
			}
			if !selected.IsValid() || (preferred[address] && !preferred[selected]) || (preferred[address] == preferred[selected] && address.Is4() && !selected.Is4()) {
				selected = address
			}
		}
		if selected.IsValid() {
			set[selected.String()] = true
			hostnames[normalizedHostname(member.Hostname)] = true
			for _, ip := range member.Addresses {
				memberIPs[ip.Unmap()] = true
			}
		}
	}
	for _, target := range configured {
		if ip, err := netip.ParseAddr(target); err == nil {
			if !memberIPs[ip.Unmap()] {
				set[ip.Unmap().String()] = true
			}
			continue
		}
		if hostnames[normalizedHostname(target)] {
			continue
		}
		matched := false
		for _, ip := range resolved[target] {
			if memberIPs[ip.Unmap()] {
				matched = true
				break
			}
		}
		if !matched && target != "" {
			set[target] = true
		}
	}
	result := make([]string, 0, len(set))
	for target := range set {
		result = append(result, target)
	}
	sort.Strings(result)
	return result
}
