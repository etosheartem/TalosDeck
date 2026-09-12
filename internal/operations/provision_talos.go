package operations

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/config"
	coreconfig "github.com/siderolabs/talos/pkg/machinery/config/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/container"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	machinetype "github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/config/types/meta"
	"github.com/siderolabs/talos/pkg/machinery/config/types/network"
	"github.com/siderolabs/talos/pkg/machinery/config/types/v1alpha1"
	"github.com/siderolabs/talos/pkg/machinery/resources/runtime"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/proxmox"
)

func (s *ProvisionService) generateConfigs(ctx context.Context, plan *provisionState, records []proxmox.OwnedMachineRecord) error {
	contract, err := config.ParseContractFromVersion(plan.Spec.TalosVersion)
	if err != nil {
		return errors.New("invalid Talos version contract")
	}
	var bundle *secrets.Bundle
	var original config.Provider
	endpoint := plan.Spec.Endpoint
	if plan.Spec.Kind == "worker-create" {
		if s.Talos == nil {
			return errors.New("existing Talos cluster unavailable")
		}
		nodes, err := s.Talos.ListNodes(ctx)
		if err != nil {
			return errors.New("cannot discover existing control plane")
		}
		for _, node := range nodes {
			if node == nil || node.Role != "controlplane" || !node.Ready {
				continue
			}
			data, readErr := s.Talos.GetNodeConfig(ctx, node.IP)
			if readErr != nil {
				continue
			}
			original, err = configloader.NewFromBytes(data)
			if err == nil {
				break
			}
		}
		if original == nil {
			return errors.New("cannot read a control-plane machine configuration")
		}
		if original.K8sClusterConfig() == nil || original.K8sClusterConfig().ClusterEndpoint() == nil {
			return errors.New("existing cluster endpoint unavailable")
		}
		endpoint = original.K8sClusterConfig().ClusterEndpoint().String()
		bundle, err = secrets.NewBundleFromConfig(secrets.NewClock(), original)
	} else {
		bundle, err = secrets.NewBundle(secrets.NewClock(), contract)
		if endpoint == "" {
			for i, record := range records {
				if record.Role == "controlplane" {
					endpoint = "https://" + machineFinalAddress(plan.Spec.Machines[i], record) + ":6443"
					break
				}
			}
		}
	}
	if err != nil {
		return errors.New("cannot generate cluster machine credentials")
	}
	if endpoint == "" {
		return errors.New("cluster API endpoint unavailable")
	}
	plan.Spec.Endpoint = endpoint
	cpAddresses := []string{}
	for i, record := range records {
		if record.Role == "controlplane" {
			cpAddresses = append(cpAddresses, machineFinalAddress(plan.Spec.Machines[i], record))
		}
	}
	plan.Configs = make([][]byte, len(records))
	for i, record := range records {
		spec := plan.Spec.Machines[i]
		networkConfig := provisionNetwork(spec, record.MAC)
		if contract.HostDNSMultidocConfig() {
			networkConfig.NameServers = nil
		}
		input, err := generate.NewInput(plan.Spec.Name, endpoint, strings.TrimPrefix(plan.Spec.KubernetesVersion, "v"), generate.WithSecretsBundle(bundle), generate.WithVersionContract(contract), generate.WithInstallDisk("/dev/sda"), generate.WithInstallImage(plan.Spec.InstallerImage), generate.WithNetworkOptions(v1alpha1.WithNetworkConfig(networkConfig)))
		if err != nil {
			return errors.New("cannot generate machine configuration")
		}
		if original != nil {
			input.ClusterName = original.K8sClusterConfig().ClusterName()
			network := original.K8sNetworkConfig()
			if network == nil {
				return errors.New("existing Kubernetes network configuration unavailable")
			}
			input.PodNet = nil
			for _, prefix := range network.PodCIDRs() {
				input.PodNet = append(input.PodNet, prefix.String())
			}
			input.ServiceNet = nil
			for _, prefix := range network.ServiceCIDRs() {
				input.ServiceNet = append(input.ServiceNet, prefix.String())
			}
			if err := generate.WithDNSDomain(network.DNSDomain())(&input.Options); err != nil {
				return errors.New("cannot preserve cluster DNS domain")
			}
		}
		role := machinetype.TypeWorker
		if spec.Role == "controlplane" {
			role = machinetype.TypeControlPlane
		}
		provider, err := input.Config(role)
		if err != nil {
			return errors.New("cannot generate node role configuration")
		}
		customCNI := plan.Spec.CNI == "cilium" || (original != nil && original.K8sFlannelCNIConfig() == nil)
		if customCNI && !contract.MultidocKubernetesConfigSupported() {
			provider, err = provider.PatchV1Alpha1(func(c *v1alpha1.Config) error {
				if c.ClusterConfig != nil && c.ClusterConfig.ClusterNetwork != nil {
					c.ClusterConfig.ClusterNetwork.CNI = &v1alpha1.CNIConfig{CNIName: "none"}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		documents := []coreconfig.Document{}
		for _, document := range provider.Documents() {
			if customCNI && document.Kind() == "KubeFlannelCNIConfig" {
				continue
			}
			if resolver, ok := document.(*network.ResolverConfigV1Alpha1); ok && len(spec.Nameservers) > 0 {
				resolver = resolver.DeepCopy()
				resolver.ResolverNameservers = nil
				for _, address := range spec.Nameservers {
					resolver.ResolverNameservers = append(resolver.ResolverNameservers, network.NameserverConfig{Address: meta.Addr{Addr: netip.MustParseAddr(address)}})
				}
				document = resolver
			}
			if document.Kind() != network.HostnameKind {
				documents = append(documents, document)
			}
		}
		hostname := network.NewHostnameConfigV1Alpha1()
		hostname.ConfigHostname = spec.Name
		documents = append(documents, hostname)
		provider, err = container.New(documents...)
		if err != nil {
			return errors.New("cannot configure machine hostname")
		}
		data, err := provider.Bytes()
		if err != nil {
			return errors.New("cannot encode node configuration")
		}
		plan.Configs[i] = data
		if err := validateConfig(data); err != nil {
			return err
		}
		if plan.Spec.Kind == "cluster-create" && len(plan.Talosconfig) == 0 {
			cfg, err := input.Talosconfig()
			if err != nil {
				return errors.New("cannot generate Talos client credentials")
			}
			cfg.Contexts[cfg.Context].Endpoints = cpAddresses
			cfg.Contexts[cfg.Context].Nodes = cpAddresses
			plan.Talosconfig, err = cfg.Bytes()
			if err != nil {
				return errors.New("cannot encode Talos credentials")
			}
		}
	}
	return nil
}
func machineFinalAddress(spec proxmox.MachineSpec, record proxmox.OwnedMachineRecord) string {
	if spec.NetworkMode == "static" {
		if prefix, err := netip.ParsePrefix(spec.Address); err == nil {
			return prefix.Addr().String()
		}
	}
	return record.Address
}
func provisionNetwork(spec proxmox.MachineSpec, mac string) *v1alpha1.NetworkConfig {
	dhcp := spec.NetworkMode == "dhcp"
	device := &v1alpha1.Device{DeviceSelector: &v1alpha1.NetworkDeviceSelector{NetworkDeviceHardwareAddress: mac}, DeviceDHCP: &dhcp}
	if !dhcp {
		device.DeviceAddresses = []string{spec.Address}
		device.DeviceRoutes = []*v1alpha1.Route{{RouteNetwork: "0.0.0.0/0", RouteGateway: spec.Gateway}}
	}
	return &v1alpha1.NetworkConfig{NetworkInterfaces: []*v1alpha1.Device{device}, NameServers: spec.Nameservers}
}

func (s *ProvisionService) configureMachines(ctx context.Context, e *jobs.Execution, plan *provisionState, records []proxmox.OwnedMachineRecord) error {
	for i, record := range records {
		if err := e.Checkpoint(ctx, "apply-config", "Applying generated configuration to owned VM "+record.Name); err != nil {
			return err
		}
		// The address was obtained from the registered VM's guest agent and MAC.
		// Insecure TLS is allowed only for this new machine's maintenance bootstrap;
		// all subsequent communication verifies the generated cluster CA.
		bootstrap, err := client.New(ctx, client.WithEndpoints(record.Address), client.WithTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}))
		if err != nil {
			return errors.New("cannot connect to owned machine maintenance API")
		}
		readyErr := s.poll(ctx, 5*time.Minute, func(c context.Context) bool {
			checkCtx, cancel := context.WithTimeout(c, 8*time.Second)
			defer cancel()
			_, err := bootstrap.Version(checkCtx)
			return err == nil
		})
		if readyErr != nil {
			bootstrap.Close()
			return errors.New("owned machine maintenance API unavailable")
		}
		response, applyErr := bootstrap.ApplyConfiguration(ctx, &machine.ApplyConfigurationRequest{Data: plan.Configs[i], Mode: machine.ApplyConfigurationRequest_AUTO})
		bootstrap.Close()
		if applyErr != nil || response == nil || len(response.Messages) != 1 {
			return fmt.Errorf("%w: machine configuration application not confirmed", jobs.ErrUncertain)
		}
		for _, message := range response.Messages {
			if message == nil || (message.Metadata != nil && message.Metadata.Error != "") {
				return jobs.ErrUncertain
			}
		}
		record.Address = machineFinalAddress(plan.Spec.Machines[i], record)
		record.Status = "config-applied"
		if err := saveOwned(ctx, s.Store, record, false); err != nil {
			return err
		}
		records[i] = record
	}
	var talosClient *client.Client
	var kube *k8s.K8sManager
	if plan.Spec.Kind == "cluster-create" {
		cfg, err := clientconfig.FromBytes(plan.Talosconfig)
		if err != nil {
			return errors.New("encrypted Talos credentials invalid")
		}
		talosClient, err = client.New(ctx, client.WithConfig(cfg))
		if err != nil {
			return errors.New("cannot initialize new cluster client")
		}
		defer talosClient.Close()
		cp := ""
		for _, record := range records {
			if record.Role == "controlplane" {
				cp = record.Address
				break
			}
		}
		if err := e.Checkpoint(ctx, "wait-talos", "Waiting for authenticated control-plane API"); err != nil {
			return err
		}
		if err := s.poll(ctx, 15*time.Minute, func(c context.Context) bool {
			check, cancel := context.WithTimeout(c, 8*time.Second)
			defer cancel()
			nodeCtx := client.WithNode(check, cp)
			status, err := safe.StateGet[*runtime.MachineStatus](nodeCtx, talosClient.COSI, resource.NewMetadata(runtime.NamespaceName, runtime.MachineStatusType, runtime.MachineStatusID, resource.VersionUndefined))
			if err != nil {
				return false
			}
			services, err := talosClient.ServiceList(nodeCtx)
			return err == nil && provisionBootstrapReady(status.TypedSpec().Stage, services)
		}); err != nil {
			return errors.New("authenticated control-plane API did not become ready")
		}
		if err := e.Checkpoint(ctx, "bootstrap-etcd", "Bootstrapping the new cluster exactly once"); err != nil {
			return err
		}
		if err := talosClient.Bootstrap(client.WithNode(ctx, cp), &machine.BootstrapRequest{}); err != nil {
			return fmt.Errorf("%w: bootstrap outcome not confirmed", jobs.ErrUncertain)
		}
		if err := e.Checkpoint(ctx, "wait-kubernetes", "Waiting for Kubernetes client credentials"); err != nil {
			return err
		}
		if err := s.poll(ctx, 15*time.Minute, func(c context.Context) bool {
			check, cancel := context.WithTimeout(c, 8*time.Second)
			defer cancel()
			data, err := talosClient.Kubeconfig(client.WithNode(check, cp))
			if err != nil {
				return false
			}
			manager, err := k8s.NewK8sManagerFromBytes(data)
			if err != nil {
				return false
			}
			kube = manager
			plan.Kubeconfig = data
			return true
		}); err != nil {
			return errors.New("new Kubernetes credentials did not become available")
		}
		if err := s.save(ctx, plan, false); err != nil {
			return err
		}
		for _, addon := range []string{plan.Spec.CNI, plan.Spec.Storage} {
			if addon == "" || addon == "flannel" || addon == "none" {
				continue
			}
			if err := e.Checkpoint(ctx, "install-addon", "Installing bundled "+addon); err != nil {
				return err
			}
			if err := s.poll(ctx, 5*time.Minute, func(c context.Context) bool {
				check, cancel := context.WithTimeout(c, 30*time.Second)
				defer cancel()
				return kube.InstallAddon(check, addon) == nil
			}); err != nil {
				return fmt.Errorf("addon %s could not be applied; inspect the retained cluster", addon)
			}
			if err := s.poll(ctx, 15*time.Minute, func(c context.Context) bool {
				check, cancel := context.WithTimeout(c, 10*time.Second)
				defer cancel()
				return kube.AddonReady(check, addon)
			}); err != nil {
				return fmt.Errorf("addon %s did not become Ready", addon)
			}
		}
	} else {
		if s.Talos == nil || s.Kubernetes == nil {
			return errors.New("existing cluster clients unavailable")
		}
		talosClient = s.Talos.GetClient()
		kube = s.Kubernetes
	}
	if err := e.Checkpoint(ctx, "verify-ready", "Waiting for every created machine to join Kubernetes Ready"); err != nil {
		return err
	}
	if err := s.poll(ctx, 20*time.Minute, func(c context.Context) bool {
		api, nodes, err := kube.UpgradeInventory(c)
		if err != nil || strings.TrimPrefix(api, "v") != strings.TrimPrefix(plan.Spec.KubernetesVersion, "v") {
			return false
		}
		for _, record := range records {
			found := false
			for _, node := range nodes {
				if node.Name == record.Name && node.Ready && !node.Unschedulable && !node.Pressure && strings.TrimPrefix(node.Version, "v") == strings.TrimPrefix(plan.Spec.KubernetesVersion, "v") {
					for _, address := range node.Addresses {
						if address == record.Address {
							found = true
						}
					}
				}
			}
			if !found {
				return false
			}
			check, cancel := context.WithTimeout(c, 8*time.Second)
			response, err := talosClient.Version(client.WithNode(check, record.Address))
			cancel()
			if err != nil || response == nil || len(response.Messages) != 1 || strings.TrimPrefix(response.Messages[0].GetVersion().GetTag(), "v") != strings.TrimPrefix(plan.Spec.TalosVersion, "v") {
				return false
			}
		}
		return true
	}); err != nil {
		return errors.New("created machines did not reach the expected Talos/Kubernetes versions and Ready state")
	}
	clusterID := s.ClusterID
	if plan.Spec.Kind == "cluster-create" {
		if s.RegisterCluster == nil {
			return errors.New("cluster registry callback unavailable")
		}
		if err := e.Checkpoint(ctx, "register-cluster", "Registering the verified new cluster"); err != nil {
			return err
		}
		var err error
		// Persist before registry import: even a lost response must never make an
		// attached control plane eligible for failed-resource cleanup.
		plan.ImportStarted = true
		if err := s.save(ctx, plan, false); err != nil {
			return err
		}
		clusterID, err = s.RegisterCluster(ctx, plan.Spec.Name, plan.Talosconfig, plan.Kubeconfig, plan.Spec.ProviderID)
		if err != nil {
			return errors.New("new cluster is ready but registry import failed; credentials and ownership remain in encrypted plan")
		}
	}
	for _, record := range records {
		record.ClusterID = clusterID
		record.Status = "ready"
		if err := saveOwned(ctx, s.Store, record, false); err != nil {
			return err
		}
	}
	return e.Log("complete", "Created machines are Ready; cluster credentials and VM ownership are stored encrypted")
}
