package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/compatibility"
	"talosdeck/internal/imagefactory"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/talos"
)

type ProvisionSpec struct {
	SchematicID       string                `json:"schematicId,omitempty"`
	Architecture      string                `json:"architecture,omitempty"`
	Platform          string                `json:"platform,omitempty"`
	ISOStorage        string                `json:"isoStorage,omitempty"`
	CNI               string                `json:"cni,omitempty"`
	Storage           string                `json:"storage,omitempty"`
	Kind              string                `json:"kind"`
	Name              string                `json:"name"`
	ProviderID        string                `json:"providerId"`
	TalosVersion      string                `json:"talosVersion,omitempty"`
	KubernetesVersion string                `json:"kubernetesVersion,omitempty"`
	InstallerImage    string                `json:"installerImage,omitempty"`
	Endpoint          string                `json:"endpoint,omitempty"`
	Machines          []proxmox.MachineSpec `json:"machines,omitempty"`
	MachineID         string                `json:"machineId,omitempty"`
}
type TemplateReference struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	SpecHash string `json:"specHash"`
	Name     string `json:"name"`
}

type ProvisionPlan struct {
	Template     *TemplateReference    `json:"template,omitempty"`
	ImageProfile *imagefactory.Profile `json:"imageProfile,omitempty"`
	ID           string                `json:"id"`
	Spec         ProvisionSpec         `json:"spec"`
	Status       string                `json:"status"`
	CreatedAt    time.Time             `json:"createdAt"`
	SafetyNotes  []string              `json:"safetyNotes"`
}
type provisionState struct {
	ProvisionPlan
	ClusterID     string         `json:"clusterId"`
	Author        string         `json:"author"`
	Provider      ProviderRecord `json:"provider"`
	MachineIDs    []string       `json:"machineIds"`
	Configs       [][]byte       `json:"configs,omitempty"`
	Talosconfig   []byte         `json:"talosconfig,omitempty"`
	Kubeconfig    []byte         `json:"kubeconfig,omitempty"`
	ImageFilename string         `json:"imageFilename,omitempty"`
	ImageTask     string         `json:"imageTask,omitempty"`
	ImageChecksum string         `json:"imageChecksum,omitempty"`
	ImportStarted bool           `json:"importStarted,omitempty"`
}
type ProvisionService struct {
	Images          FactoryImages
	ClusterID       string
	Store           ProvisionStore
	Talos           *talos.TalosManager
	Kubernetes      *k8s.K8sManager
	RegisterCluster func(context.Context, string, []byte, []byte, string) (string, error)
	ProviderFactory func(ProviderRecord) (proxmox.MachineProvider, error)
	PollInterval    time.Duration
	Audit           func(action, user, status, id string)
}

func (s *ProvisionService) provider(p ProviderRecord) (proxmox.MachineProvider, error) {
	if s.ProviderFactory != nil {
		return s.ProviderFactory(p)
	}
	return proxmox.NewClient(p.Config)
}
func (s *ProvisionService) save(ctx context.Context, p *provisionState, create bool) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if create {
		err = s.Store.CreateSecret(ctx, FleetScope, "provision-plan", p.ID, data)
	} else {
		err = s.Store.PutSecret(ctx, FleetScope, "provision-plan", p.ID, data)
	}
	if err != nil {
		return errors.New("cannot persist encrypted provisioning state")
	}
	return nil
}
func (s *ProvisionService) load(ctx context.Context, id string) (*provisionState, error) {
	data, err := s.Store.GetSecret(ctx, FleetScope, "provision-plan", id)
	var plan provisionState
	if err != nil || json.Unmarshal(data, &plan) != nil || plan.ClusterID != s.ClusterID {
		return nil, errors.New("provisioning plan not found in this scope")
	}
	return &plan, nil
}
func ValidateProvision(spec ProvisionSpec, clusterID string) error {
	if spec.CNI != "" && spec.CNI != "flannel" && spec.CNI != "cilium" {
		return errors.New("CNI must be flannel or cilium")
	}
	if spec.Storage != "" && spec.Storage != "none" && spec.Storage != "local-path" {
		return errors.New("storage must be none or local-path")
	}
	if strings.TrimSpace(spec.Name) == "" || len(spec.Name) > 63 {
		return errors.New("name is required (up to 63 characters)")
	}
	if spec.Kind == "machine-cleanup" {
		if clusterID != FleetScope || spec.MachineID == "" {
			return errors.New("failed-resource cleanup requires the fleet scope and a registered machine")
		}
		return nil
	}
	if spec.Kind == "worker-delete" {
		if clusterID == "" || clusterID == FleetScope || spec.MachineID == "" {
			return errors.New("worker deletion requires a selected cluster and registered machine")
		}
		return nil
	}
	if spec.Kind != "cluster-create" && spec.Kind != "worker-create" {
		return errors.New("unsupported provisioning operation")
	}
	if spec.Kind == "cluster-create" && clusterID != "" && clusterID != FleetScope {
		return errors.New("new clusters must be created from the fleet view")
	}
	if spec.Kind == "worker-create" && (clusterID == "" || clusterID == FleetScope) {
		return errors.New("worker creation requires a selected cluster")
	}
	tv, err := parseVersion(spec.TalosVersion)
	if err != nil {
		return errors.New("stable Talos version is required")
	}
	kv, err := parseVersion(spec.KubernetesVersion)
	if err != nil {
		return errors.New("stable Kubernetes version is required")
	}
	if spec.CNI == "cilium" && (kv.Minor < 33 || kv.Minor > 36) {
		return errors.New("bundled Cilium 1.20.1 supports Kubernetes 1.33–1.36; choose Flannel for this version")
	}
	if tv.Major != 1 || tv.Minor < 13 || tv.Minor > 14 {
		return errors.New("provisioning supports Talos 1.13 and 1.14")
	}
	talosVersion, err := compatibility.ParseTalosVersion(&machine.VersionInfo{Tag: spec.TalosVersion})
	if err != nil {
		return errors.New("invalid Talos version")
	}
	kubernetesVersion, err := compatibility.ParseKubernetesVersion(spec.KubernetesVersion)
	if err != nil {
		return errors.New("invalid Kubernetes version")
	}
	_ = kv
	if err := kubernetesVersion.SupportedWith(talosVersion); err != nil {
		return errors.New("Talos and Kubernetes versions are incompatible")
	}
	if strings.ContainsAny(spec.InstallerImage, " \t\r\n@") || !strings.HasSuffix(spec.InstallerImage, ":v"+strings.TrimPrefix(spec.TalosVersion, "v")) || !strings.Contains(spec.InstallerImage, "/") {
		return errors.New("installer image must be tagged with the selected Talos version")
	}
	if spec.Endpoint != "" {
		endpoint, err := url.Parse(spec.Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" {
			return errors.New("cluster endpoint must be an HTTPS URL without credentials")
		}
	}
	if len(spec.Machines) == 0 || len(spec.Machines) > 100 {
		return errors.New("between 1 and 100 machines are required")
	}
	cp := 0
	names := map[string]bool{}
	addresses := map[string]bool{}
	for _, machine := range spec.Machines {
		if err := proxmox.ValidateMachine(machine); err != nil {
			return err
		}
		if names[machine.Name] {
			return errors.New("machine names must be unique")
		}
		names[machine.Name] = true
		if machine.Role == "controlplane" {
			cp++
		}
		if machine.Address != "" {
			prefix, _ := netip.ParsePrefix(machine.Address)
			address := prefix.Addr().String()
			if addresses[address] {
				return errors.New("static machine addresses must be unique")
			}
			addresses[address] = true
		}
	}
	if spec.Kind == "cluster-create" && cp != 1 && cp != 3 {
		return errors.New("new clusters require one or three control-plane machines")
	}
	if spec.Kind == "worker-create" && cp != 0 {
		return errors.New("only worker machines can be added to an existing cluster")
	}
	return nil
}
func (s *ProvisionService) Plan(ctx context.Context, spec ProvisionSpec, user string) (*ProvisionPlan, error) {
	return s.plan(ctx, spec, user, nil)
}

// PlanFromTemplate accepts provenance only from the server-side template resolver.
// The ordinary provisioning request cannot set or override this reference.
func (s *ProvisionService) PlanFromTemplate(ctx context.Context, spec ProvisionSpec, user string, ref TemplateReference) (*ProvisionPlan, error) {
	if id, err := uuid.Parse(ref.ID); err != nil || id == uuid.Nil || ref.Revision < 1 || !schematicPattern.MatchString(ref.SpecHash) || strings.TrimSpace(ref.Name) == "" || len(ref.Name) > 100 || strings.ContainsAny(ref.Name, "\r\n\x00") {
		return nil, errors.New("invalid template reference")
	}
	if spec.Kind != "cluster-create" {
		return nil, errors.New("templates can only create new clusters")
	}
	return s.plan(ctx, spec, user, &ref)
}
func (s *ProvisionService) plan(ctx context.Context, spec ProvisionSpec, user string, ref *TemplateReference) (*ProvisionPlan, error) {
	// Detach caller-owned slices and strings before network calls or persistence.
	input := struct {
		Spec ProvisionSpec
		Ref  *TemplateReference
		User string
	}{spec, ref, user}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, errors.New("cannot copy provisioning input")
	}
	input.Spec = ProvisionSpec{}
	input.Ref = nil
	input.User = ""
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, errors.New("cannot copy provisioning input")
	}
	spec, ref, user = input.Spec, input.Ref, input.User

	if unavailable(s.Store) {
		return nil, errors.New("provisioning store unavailable")
	}
	profile, err := s.resolveProvisionImage(ctx, &spec)
	if err != nil {
		return nil, err
	}
	if err := ValidateProvision(spec, s.ClusterID); err != nil {
		return nil, err
	}
	if spec.Kind == "worker-delete" {
		record, err := loadOwned(ctx, s.Store, spec.MachineID)
		if err != nil || record.ClusterID != s.ClusterID || record.Role != "worker" || record.Status == "deleted" || spec.Name != record.Name {
			return nil, errors.New("only a registered worker of this cluster can be deleted")
		}
		spec.ProviderID = record.ProviderID
	}
	if spec.Kind == "machine-cleanup" {
		record, err := s.cleanupRecord(ctx, spec)
		if err != nil {
			return nil, err
		}
		spec.ProviderID = record.ProviderID
	}
	provider, err := loadProvider(ctx, s.Store, spec.ProviderID)
	if err != nil {
		return nil, err
	}
	p, err := s.provider(provider)
	if err != nil {
		return nil, errors.New("provider unavailable")
	}
	if spec.Kind == "worker-delete" || spec.Kind == "machine-cleanup" {
		record, _ := loadOwned(ctx, s.Store, spec.MachineID)
		if err := p.VerifyOwned(ctx, record.Machine()); err != nil {
			return nil, err
		}
	}
	if spec.Kind == "worker-create" {
		if s.Talos == nil || s.Kubernetes == nil {
			return nil, errors.New("Talos and Kubernetes clients are required")
		}
		apiVersion, nodes, err := s.Kubernetes.UpgradeInventory(ctx)
		if err != nil {
			return nil, errors.New("Kubernetes inventory unavailable")
		}
		if strings.TrimPrefix(apiVersion, "v") != strings.TrimPrefix(spec.KubernetesVersion, "v") {
			return nil, errors.New("new workers must use the cluster's current Kubernetes version")
		}
		for _, existing := range nodes {
			for _, machine := range spec.Machines {
				if existing.Name == machine.Name {
					return nil, errors.New("machine name already exists in this cluster")
				}
			}
		}
	}
	state := &provisionState{ProvisionPlan: ProvisionPlan{Template: ref, ImageProfile: profile, ID: uuid.NewString(), Spec: spec, Status: "planned", CreatedAt: time.Now().UTC(), SafetyNotes: []string{"Only newly allocated VMs are created. Failures retain ownership records and never replay automatically.", "The boot ISO must include qemu-guest-agent. Initial boot requires DHCP; static addresses are applied in MachineConfig.", "The selected CNI and storage add-on are installed before readiness verification. Manual ISO and installer versions must match."}}, ClusterID: s.ClusterID, Author: user, Provider: provider}
	if profile != nil {
		state.SafetyNotes = append(state.SafetyNotes, "The confirmed Factory schematic is used for both the boot ISO and installer. A unique checksum-verified ISO is retained on the provider for inspection after completion or failure.")
	}
	if spec.Kind != "worker-delete" && spec.Kind != "machine-cleanup" {
		if err := s.imagePreflight(ctx, p, spec); err != nil {
			return nil, err
		}
		// Validate generated Talos configuration before allocating any VM. DHCP
		// addresses are placeholders here; the execution generates and persists
		// the final credentials against guest-agent-confirmed machine addresses.
		preview := *state
		records := make([]proxmox.OwnedMachineRecord, len(spec.Machines))
		for i, machine := range spec.Machines {
			records[i] = proxmox.NewOwnership(spec.ProviderID, s.ClusterID, state.ID, provider.Config.Node, machine, 100+i)
			records[i].Address = fmt.Sprintf("192.0.2.%d", i+1)
		}
		if err := s.generateConfigs(ctx, &preview, records); err != nil {
			return nil, err
		}
	}
	if err := s.save(ctx, state, true); err != nil {
		return nil, err
	}
	return &state.ProvisionPlan, nil
}
func (s *ProvisionService) Request(ctx context.Context, id, confirmedName, user string) (jobs.Request, error) {
	p, err := s.load(ctx, id)
	if err != nil || p.Status != "planned" || p.Author != user || time.Since(p.CreatedAt) > 30*time.Minute {
		return jobs.Request{}, errors.New("provisioning plan unavailable or expired")
	}
	if confirmedName != p.Spec.Name {
		return jobs.Request{}, errors.New("type the planned name to confirm")
	}
	return jobs.Request{Kind: p.Spec.Kind, ProvisionID: id}, nil
}
func (s *ProvisionService) Run(ctx context.Context, e *jobs.Execution, r jobs.Request) (runErr error) {
	plan, err := s.load(ctx, r.ProvisionID)
	if err != nil || plan.Status != "planned" || plan.Spec.Kind != r.Kind || time.Since(plan.CreatedAt) > 30*time.Minute {
		return errors.New("provisioning plan unavailable or already consumed")
	}
	if err := ValidateProvision(plan.Spec, s.ClusterID); err != nil {
		return err
	}
	if err := e.Checkpoint(ctx, "provision-preflight", "Reserving the provisioning plan before creating infrastructure"); err != nil {
		return err
	}
	plan.Status = "running"
	if err := s.save(ctx, plan, false); err != nil {
		return err
	}
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			runErr = jobs.ErrUncertain
		}
		plan.Status = "succeeded"
		if runErr != nil {
			plan.Status = "failed"
		}
		if ctx.Err() != nil || errors.Is(runErr, jobs.ErrUncertain) || panicValue != nil {
			plan.Status = "interrupted"
		}
		finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.save(finalCtx, plan, false); err != nil {
			runErr = jobs.ErrUncertain
		}
		if s.Audit != nil {
			s.Audit(r.Kind, plan.Author, plan.Status, plan.ID)
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	if plan.Template != nil {
		if err := e.Log("template", fmt.Sprintf("Template %s revision %d; spec SHA256 %s", plan.Template.ID, plan.Template.Revision, plan.Template.SpecHash)); err != nil {
			return err
		}
	}
	provider, err := s.provider(plan.Provider)
	if err != nil {
		return errors.New("provider unavailable")
	}
	if r.Kind == "worker-delete" {
		return s.deleteWorker(ctx, e, plan, provider)
	}
	if r.Kind == "machine-cleanup" {
		return s.cleanupMachine(ctx, e, plan, provider)
	}
	if err := s.prepareImage(ctx, e, plan, provider); err != nil {
		return err
	}
	if err := providerPreflight(ctx, provider, plan.Spec.Machines); err != nil {
		return err
	}
	for _, spec := range plan.Spec.Machines {
		if err := e.Checkpoint(ctx, "create-vm", "Allocating VM for "+spec.Name); err != nil {
			return err
		}
		vmid, err := provider.NextID(ctx)
		if err != nil {
			return errors.New("cannot allocate provider VM ID")
		}
		record := proxmox.NewOwnership(plan.Spec.ProviderID, s.ClusterID, plan.ID, plan.Provider.Config.Node, spec, vmid)
		if err := saveOwned(ctx, s.Store, record, true); err != nil {
			return err
		}
		plan.MachineIDs = append(plan.MachineIDs, record.ID)
		if err := s.save(ctx, plan, false); err != nil {
			return err
		}
		task, err := provider.CreateMachine(ctx, spec, record.Machine())
		if err != nil {
			return fmt.Errorf("%w: VM %d creation not confirmed", jobs.ErrUncertain, vmid)
		}
		if err := e.Log("create-task", fmt.Sprintf("VM %d task %s", vmid, task)); err != nil {
			return err
		}
		if err := provider.WaitTask(ctx, task); err != nil {
			return fmt.Errorf("%w: VM %d creation task failed", jobs.ErrUncertain, vmid)
		}
		record.Status = "created"
		if err := saveOwned(ctx, s.Store, record, false); err != nil {
			return err
		}
		if err := e.Checkpoint(ctx, "boot-vm", "Booting "+spec.Name); err != nil {
			return err
		}
		if err := provider.StartOwned(ctx, record.Machine()); err != nil {
			return fmt.Errorf("%w: owned VM could not be started", jobs.ErrUncertain)
		}
		record.Status = "booting"
		if err := saveOwned(ctx, s.Store, record, false); err != nil {
			return err
		}
	}
	records := make([]proxmox.OwnedMachineRecord, 0, len(plan.MachineIDs))
	for _, id := range plan.MachineIDs {
		record, err := loadOwned(ctx, s.Store, id)
		if err != nil {
			return err
		}
		if err := e.Checkpoint(ctx, "discover-address", "Waiting for the guest agent on "+record.Name); err != nil {
			return err
		}
		if err := s.poll(ctx, 10*time.Minute, func(c context.Context) bool {
			address, err := provider.MachineAddress(c, record.Machine())
			if err == nil {
				record.Address = address
			}
			return err == nil
		}); err != nil {
			return fmt.Errorf("guest-agent address discovery failed for %s", record.Name)
		}
		record.Status = "maintenance"
		if err := saveOwned(ctx, s.Store, record, false); err != nil {
			return err
		}
		records = append(records, record)
	}
	if err := e.Checkpoint(ctx, "generate-config", "Generating machine credentials and saving them encrypted before application"); err != nil {
		return err
	}
	if err := s.generateConfigs(ctx, plan, records); err != nil {
		return err
	}
	if err := s.save(ctx, plan, false); err != nil {
		return err
	}
	return s.configureMachines(ctx, e, plan, records)
}
func (s *ProvisionService) poll(ctx context.Context, timeout time.Duration, ready func(context.Context) bool) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	interval := s.PollInterval
	if interval == 0 {
		interval = 3 * time.Second
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ready(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
func (s *ProvisionService) deleteWorker(ctx context.Context, e *jobs.Execution, plan *provisionState, p proxmox.MachineProvider) error {
	if s.Kubernetes == nil || s.Talos == nil {
		return errors.New("Talos identity and Kubernetes drain services are required")
	}
	record, err := loadOwned(ctx, s.Store, plan.Spec.MachineID)
	if err != nil {
		return err
	}
	if record.ClusterID != s.ClusterID || record.Role != "worker" || record.Name != plan.Spec.Name || record.Status == "deleted" {
		return errors.New("registered worker ownership mismatch")
	}
	if err := p.VerifyOwned(ctx, record.Machine()); err != nil {
		return err
	}
	_, nodes, err := s.Kubernetes.UpgradeInventory(ctx)
	if err != nil {
		return errors.New("cannot verify Kubernetes worker identity")
	}
	matched := false
	for _, node := range nodes {
		if node.Name == record.Name {
			for _, address := range node.Addresses {
				if address == record.Address {
					matched = true
				}
			}
		}
	}
	if !matched {
		return errors.New("registered provider worker does not match its Kubernetes node address")
	}
	talosNodes, err := s.Talos.ListNodes(ctx)
	if err != nil {
		return errors.New("cannot verify Talos worker role")
	}
	worker := false
	for _, node := range talosNodes {
		if node != nil && node.IP == record.Address && node.Role == "worker" {
			worker = true
		}
	}
	if !worker {
		return errors.New("registered VM is not currently a verified Talos worker; deletion refused")
	}
	if err := e.Checkpoint(ctx, "drain-worker", "Cordoning and draining registered worker "+record.Name); err != nil {
		return err
	}
	if err := s.Kubernetes.CordonAndDrainNode(ctx, record.Name); err != nil {
		return errors.New("worker drain failed; VM was not deleted")
	}
	if err := e.Checkpoint(ctx, "delete-worker", "Deleting provider-owned worker VM"); err != nil {
		return err
	}
	if err := p.DeleteOwned(ctx, record.Machine()); err != nil {
		return fmt.Errorf("%w: deletion was not confirmed", jobs.ErrUncertain)
	}
	record.Status = "deleted"
	if err := saveOwned(ctx, s.Store, record, false); err != nil {
		return err
	}
	s.Talos.ForgetNode(record.Address, record.Name)
	if err := s.Kubernetes.DeleteProvisionedNode(ctx, record.Name, record.Address); err != nil {
		return err
	}
	return e.Log("complete", "Registered worker VM deleted after a successful drain")
}
