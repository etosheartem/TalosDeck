package talos

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/cosi-project/runtime/pkg/state/impl/namespaced"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/cluster"
)

type unavailableDiscovery struct{ state.State }

func (s unavailableDiscovery) List(context.Context, resource.Kind, ...state.ListOption) (resource.List, error) {
	return resource.List{}, errors.New("endpoint unavailable")
}

func TestClusterNodesDiscoversWorkersAndRetainsInventoryOnFailure(t *testing.T) {
	ctx := context.Background()
	store := state.WrapCore(namespaced.NewState(func(ns resource.Namespace) state.CoreState { return inmem.NewState(ns) }))
	for i, ip := range []string{"10.0.0.10", "10.0.0.11", "10.0.0.12"} {
		member := cluster.NewMember(cluster.NamespaceName, ip)
		member.TypedSpec().Addresses = []netip.Addr{netip.MustParseAddr(ip)}
		member.TypedSpec().Hostname = []string{"master", "worker-1", "worker-2"}[i]
		if err := store.Create(ctx, member); err != nil {
			t.Fatal(err)
		}
	}
	mgr := &TalosManager{client: &client.Client{COSI: store}, nodes: []string{"10.0.0.10"}}
	want := []string{"10.0.0.10", "10.0.0.11", "10.0.0.12"}
	if got := mgr.clusterNodes(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Failed refresh must not make workers disappear from the dashboard.
	mgr.client.COSI = unavailableDiscovery{store}
	mgr.discoveryAt = time.Time{}
	if got := mgr.clusterNodes(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed discovery lost nodes: %v", got)
	}
	// A subsequent successful inventory removes a deleted discovered worker.
	mgr.client.COSI = store
	if err := store.Destroy(ctx, cluster.NewMember(cluster.NamespaceName, "10.0.0.12").Metadata()); err != nil {
		t.Fatal(err)
	}
	mgr.discoveryAt = time.Time{}
	if got := mgr.clusterNodes(ctx); !reflect.DeepEqual(got, want[:2]) {
		t.Fatalf("deleted member still present: %v", got)
	}
}

func TestMemberAddressesDoesNotDuplicateDualStackOrConfiguredNodes(t *testing.T) {
	addr := func(values ...string) []netip.Addr {
		result := []netip.Addr{}
		for _, v := range values {
			result = append(result, netip.MustParseAddr(v))
		}
		return result
	}
	members := []cluster.MemberSpec{
		{Hostname: "master", Addresses: addr("fd00::10", "10.0.0.10")},
		{Hostname: "worker", Addresses: addr("fe80::1", "fd00::11", "10.0.0.11")},
		{Hostname: "invalid", Addresses: addr("127.0.0.1", "::", "224.0.0.1")},
		{Hostname: "ipv6", Addresses: addr("fd00::12")},
	}
	for _, configured := range [][]string{{"10.0.0.10"}, {"master"}, {"fd00::10"}} {
		got := memberAddresses(configured, members)
		if want := []string{"10.0.0.11", "fd00::12"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("configured %v: got %v", configured, got)
		}
	}
}
