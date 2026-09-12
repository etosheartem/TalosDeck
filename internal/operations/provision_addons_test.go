package operations

import (
	"context"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"talosdeck/internal/proxmox"
	"testing"
)

func TestCiliumConfigDoesNotInstallFlannel(t *testing.T) {
	for _, version := range []string{"1.13.10", "1.14.0"} {
		t.Run(version, func(t *testing.T) {
			s, spec, _ := provisionFixture(t)
			spec.TalosVersion = version
			spec.KubernetesVersion = "1.36.4"
			spec.InstallerImage = "ghcr.io/siderolabs/installer:v" + version
			spec.CNI = "cilium"
			spec.Storage = "local-path"
			if err := ValidateProvision(spec, FleetScope); err != nil {
				t.Fatal(err)
			}
			plan := &provisionState{ProvisionPlan: ProvisionPlan{ID: "addon-test", Spec: spec}}
			records := []proxmox.OwnedMachineRecord{}
			for i, machine := range spec.Machines {
				r := proxmox.NewOwnership(spec.ProviderID, FleetScope, "addon-test", "pve", machine, 160+i)
				r.Address = "10.20.0.10"
				records = append(records, r)
			}
			if err := s.generateConfigs(context.Background(), plan, records); err != nil {
				t.Fatal(err)
			}
			for _, data := range plan.Configs {
				p, err := configloader.NewFromBytes(data)
				if err != nil {
					t.Fatal(err)
				}
				if p.K8sFlannelCNIConfig() != nil {
					t.Fatal("Cilium cluster would also deploy Flannel")
				}
			}
		})
	}
	_, spec, _ := provisionFixture(t)
	spec.CNI = "cilium"
	spec.KubernetesVersion = "1.37.0"
	if err := ValidateProvision(spec, FleetScope); err == nil {
		t.Fatal("unsupported Cilium matrix accepted")
	}
}
