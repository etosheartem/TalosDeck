package operations

import (
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/resources/runtime"
	"testing"
)

func TestBootstrapWaitsForInstalledControlPlane(t *testing.T) {
	response := &machine.ServiceListResponse{Messages: []*machine.ServiceList{{Services: []*machine.ServiceInfo{
		{Id: "etcd", State: "Preparing"}, {Id: "cri", State: "Running", Health: &machine.ServiceHealth{Healthy: true}}, {Id: "kubelet", State: "Running", Health: &machine.ServiceHealth{Healthy: true}},
	}}}}
	if !provisionBootstrapReady(runtime.MachineStageBooting, response) {
		t.Fatal("installed control plane waiting for bootstrap must proceed")
	}
	for _, stage := range []runtime.MachineStage{runtime.MachineStageInstalling, runtime.MachineStageMaintenance, runtime.MachineStageRebooting} {
		if provisionBootstrapReady(stage, response) {
			t.Fatalf("unsafe bootstrap during %v", stage)
		}
	}
	response.Messages[0].Services = response.Messages[0].Services[1:]
	if provisionBootstrapReady(runtime.MachineStageBooting, response) {
		t.Fatal("installer API without etcd must not pass")
	}
}
