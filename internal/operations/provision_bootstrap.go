package operations

import (
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/resources/runtime"
)

// An authenticated API appears during installation, before the mandatory reboot.
// Bootstrap must wait for the installed system's control-plane services. Etcd
// itself is expected to be Preparing until this first bootstrap request.
func provisionBootstrapReady(stage runtime.MachineStage, response *machine.ServiceListResponse) bool {
	if stage != runtime.MachineStageBooting && stage != runtime.MachineStageRunning {
		return false
	}
	ready := map[string]bool{}
	for _, message := range response.GetMessages() {
		if message.GetMetadata().GetError() != "" {
			return false
		}
		for _, service := range message.GetServices() {
			if service.GetId() == "etcd" {
				ready["etcd"] = service.GetState() == "Preparing" || service.GetState() == "Running"
			}
			if service.GetId() == "cri" || service.GetId() == "kubelet" {
				ready[service.GetId()] = service.GetHealth().GetHealthy()
			}
		}
	}
	return ready["etcd"] && ready["cri"] && ready["kubelet"]
}
