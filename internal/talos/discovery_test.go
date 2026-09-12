package talos

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/cosi-project/runtime/pkg/state/impl/namespaced"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/cluster"
	"google.golang.org/grpc/metadata"
)

// Opt-in, read-only check against a separately supplied Talos endpoint. No
// credentials are printed and no cluster resources are changed.
func TestLiveMemberDiscovery(t *testing.T) {
	path := os.Getenv("TALOSDECK_TEST_TALOSCONFIG")
	if path == "" {
		t.Skip("set TALOSDECK_TEST_TALOSCONFIG for read-only integration")
	}
	mgr, err := NewTalosManager(path)
	if err != nil {
		t.Fatal("cannot create Talos client")
	}
	defer mgr.Close()
	targets := mgr.GetConfiguredNodes()
	if len(targets) == 0 {
		t.Fatal("no configured nodes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	started := time.Now()
	nodes := mgr.clusterNodes(ctx)
	t.Logf("cold manager discovery: %d nodes in %s", len(nodes), time.Since(started))
	started = time.Now()
	members, err := safe.StateListAll[*cluster.Member](client.WithNode(ctx, targets[0]), mgr.GetClient().COSI)
	if err != nil {
		t.Fatalf("member read failed after %s: %v", time.Since(started), err)
	}
	count := 0
	for range members.All() {
		count++
	}
	t.Logf("SDK member read: %d members in %s", count, time.Since(started))
	if count == 0 {
		t.Fatal("no members returned")
	}
	if len(nodes) < count {
		t.Fatalf("discovery returned %d of %d members", len(nodes), count)
	}
}

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
	for _, test := range []struct{ configured, want []string }{
		{[]string{"10.0.0.10"}, []string{"10.0.0.10", "10.0.0.11", "fd00::12"}},
		{[]string{"master"}, []string{"10.0.0.10", "10.0.0.11", "fd00::12"}},
		{[]string{"MASTER."}, []string{"10.0.0.10", "10.0.0.11", "fd00::12"}},
		{[]string{"fd00::10"}, []string{"10.0.0.11", "fd00::10", "fd00::12"}},
	} {
		got := canonicalMemberInventory(test.configured, members, nil)
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("configured %v: got %v", test.configured, got)
		}
	}
}

func TestConfiguredDNSAliasesBecomeCanonicalMemberIPs(t *testing.T) {
	members := []cluster.MemberSpec{{Hostname: "cp", Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.10"), netip.MustParseAddr("fd00::10")}}}
	configured := []string{"cp.example.test", "10.0.0.10", "cp", "missing.example.test"}
	resolved := map[string][]netip.Addr{"cp.example.test": {netip.MustParseAddr("10.0.0.10")}}
	want := []string{"10.0.0.10", "missing.example.test"}
	if got := canonicalMemberInventory(configured, members, resolved); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

type slowDiscovery struct {
	state.State
	active  atomic.Int32
	healthy string
	delay   time.Duration
}

func (s *slowDiscovery) List(ctx context.Context, kind resource.Kind, opts ...state.ListOption) (resource.List, error) {
	s.active.Add(1)
	defer s.active.Add(-1)
	md, _ := metadata.FromOutgoingContext(ctx)
	if s.healthy != "" && (len(md.Get("node")) == 0 || md.Get("node")[0] != s.healthy) {
		<-ctx.Done()
		return resource.List{}, ctx.Err()
	}
	select {
	case <-ctx.Done():
		return resource.List{}, ctx.Err()
	case <-time.After(s.delay):
	}
	return s.State.List(ctx, kind, opts...)
}

func TestDiscoveryAllowsColdConnectionAndBypassesDeadTarget(t *testing.T) {
	store := state.WrapCore(namespaced.NewState(func(ns resource.Namespace) state.CoreState { return inmem.NewState(ns) }))
	member := cluster.NewMember(cluster.NamespaceName, "cp")
	member.TypedSpec().Hostname = "cp"
	member.TypedSpec().Addresses = []netip.Addr{netip.MustParseAddr("10.0.0.10")}
	if err := store.Create(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	slow := &slowDiscovery{State: store, healthy: "cp", delay: 3100 * time.Millisecond}
	mgr := &TalosManager{client: &client.Client{COSI: slow}, nodes: []string{"dead", "cp"}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	got := mgr.clusterNodes(ctx)
	if want := []string{"10.0.0.10", "dead"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	deadline := time.Now().Add(time.Second)
	for slow.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if slow.active.Load() != 0 {
		t.Fatal("discovery did not cancel losing target reads")
	}
}

func TestDiscoveryHonorsCallerDeadline(t *testing.T) {
	slow := &slowDiscovery{healthy: "not-configured"}
	mgr := &TalosManager{client: &client.Client{COSI: slow}, nodes: []string{"10.0.0.10"}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	got := mgr.clusterNodes(ctx)
	if time.Since(started) > time.Second || len(got) != 1 {
		t.Fatalf("unbounded discovery: %v", got)
	}
}
