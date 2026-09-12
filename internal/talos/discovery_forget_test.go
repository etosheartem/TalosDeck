package talos

import (
	"context"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/cosi-project/runtime/pkg/state/impl/namespaced"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/cluster"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestForgetOwnedNodeSuppressesStaleMembershipAndKeepsUnavailableInventory(t *testing.T) {
	ctx := context.Background()
	store := state.WrapCore(namespaced.NewState(func(ns resource.Namespace) state.CoreState { return inmem.NewState(ns) }))
	for _, fixture := range []struct {
		name string
		ips  []string
	}{{"cp", []string{"10.0.0.10"}}, {"deleted-worker", []string{"10.0.0.11", "fd00::11"}}, {"unavailable-worker", []string{"10.0.0.12"}}} {
		member := cluster.NewMember(cluster.NamespaceName, fixture.name)
		member.TypedSpec().Hostname = fixture.name
		for _, ip := range fixture.ips {
			member.TypedSpec().Addresses = append(member.TypedSpec().Addresses, netip.MustParseAddr(ip))
		}
		if err := store.Create(ctx, member); err != nil {
			t.Fatal(err)
		}
	}
	mgr := &TalosManager{client: &client.Client{COSI: store}, nodes: []string{"10.0.0.10", "fd00::11", "deleted-worker", "10.0.0.99"}, cpuSamples: map[string]cpuSnapshot{"fd00::11": {}, "10.0.0.12": {}}}
	mgr.clusterNodes(ctx)
	mgr.ForgetNode("::ffff:10.0.0.11", "DELETED-WORKER.")
	want := []string{"10.0.0.10", "10.0.0.12", "10.0.0.99"}
	// Authenticated COSI still includes deleted worker: suppress its entire dual-stack member.
	if got := mgr.clusterNodes(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("stale COSI inventory: %v", got)
	}
	if got := mgr.GetConfiguredNodes(); !reflect.DeepEqual(got, []string{"10.0.0.10", "10.0.0.99"}) {
		t.Fatalf("configured targets: %v", got)
	}
	if _, exists := mgr.cpuSamples["fd00::11"]; exists {
		t.Fatal("removed node CPU sample remains")
	}
	if _, exists := mgr.cpuSamples["10.0.0.12"]; !exists {
		t.Fatal("unrelated node CPU sample erased")
	}
	mgr.client = &client.Client{COSI: unavailableDiscovery{store}}
	mgr.discoveryAt = time.Time{}
	if got := mgr.clusterNodes(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("outage lost unrelated inventory: %v", got)
	}
	// Explicit registry tombstone replay after a process restart suppresses a configured stale target.
	restarted := &TalosManager{client: &client.Client{COSI: store}, nodes: []string{"10.0.0.10", "10.0.0.11", "fd00::11", "deleted-worker", "10.0.0.99"}}
	restarted.ForgetNode("10.0.0.11", "deleted-worker")
	if got := restarted.clusterNodes(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("restart restored deleted target: %v", got)
	}
}

func TestForgetNodeAllowsNewIdentityAndExplicitReadmission(t *testing.T) {
	mgr := &TalosManager{nodes: []string{"10.0.0.10", "10.0.0.11"}}
	mgr.ForgetNode("10.0.0.11", "old-worker")
	members := []cluster.MemberSpec{{Hostname: "old-worker", Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.11")}}, {Hostname: "replacement-worker", Addresses: []netip.Addr{netip.MustParseAddr("10.0.0.11")}}}
	retained := mgr.retainedMembers(members)
	if len(retained) != 1 || retained[0].Hostname != "replacement-worker" {
		t.Fatalf("replacement was hidden: %+v", retained)
	}
	mgr.RememberNode("::ffff:10.0.0.11", "old-worker")
	if len(mgr.retainedMembers(members)) != 2 {
		t.Fatal("explicit readmission did not clear tombstone")
	}
	if got := mgr.GetConfiguredNodes(); !reflect.DeepEqual(got, []string{"10.0.0.10", "10.0.0.11"}) {
		t.Fatalf("readmission targets: %v", got)
	}
	mgr.RememberNode("10.0.0.11")
	if len(mgr.GetConfiguredNodes()) != 2 {
		t.Fatal("readmission duplicated target")
	}
}

func TestForgetNodeConcurrentInventoryReads(t *testing.T) {
	mgr := &TalosManager{nodes: []string{"10.0.0.10", "10.0.0.11"}}
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 30; j++ {
				mgr.ForgetNode("10.0.0.11", "worker")
				mgr.GetConfiguredNodes()
				mgr.clusterNodes(context.Background())
				mgr.RememberNode("10.0.0.11", "worker")
			}
		}()
	}
	workers.Wait()
}
