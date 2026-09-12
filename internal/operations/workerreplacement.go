package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/reconcile"
)

const replacementKind = "worker-replacement"
const replacementPlanTTL = 10 * time.Minute

type ReplacementSpec struct {
	MachineID   string        `json:"machineId"`
	Mode        string        `json:"mode"`
	Replacement ProvisionSpec `json:"replacement"`
}
type WorkerReplacementPlan struct {
	WorkflowVersion  int                            `json:"workflowVersion"`
	PreviousPlanID   string                         `json:"previousPlanId,omitempty"`
	ReusedProviderID bool                           `json:"reusedProviderId,omitempty"`
	ID               string                         `json:"id"`
	ClusterID        string                         `json:"clusterId"`
	MachineID        string                         `json:"machineId"`
	Mode             string                         `json:"mode"`
	NodeName         string                         `json:"nodeName"`
	NodeUID          string                         `json:"nodeUID"`
	ReplacementPlan  ProvisionPlan                  `json:"replacementPlan"`
	Impact           k8s.ReplacementImpactReport    `json:"impact"`
	ImpactHash       string                         `json:"impactHash"`
	Status           string                         `json:"status"`
	Step             string                         `json:"step"`
	Revision         int                            `json:"revision"`
	CreatedAt        time.Time                      `json:"createdAt"`
	ExpiresAt        time.Time                      `json:"expiresAt"`
	RequiresReview   bool                           `json:"requiresReview"`
	Error            string                         `json:"error,omitempty"`
	Resume           bool                           `json:"resume"`
	Fence            *proxmox.WorkerFencingEvidence `json:"fence,omitempty"`
	SafetyNotes      []string                       `json:"safetyNotes"`
}
type replacementState struct {
	OriginalImpact k8s.ReplacementImpactReport `json:"originalImpact"`
	SuccessorID    string                      `json:"successorId,omitempty"`
	SuccessorState json.RawMessage             `json:"successorState,omitempty"`
	WorkerReplacementPlan
	Author             string                     `json:"author"`
	Old                proxmox.OwnedMachineRecord `json:"old"`
	Approved           bool                       `json:"approved"`
	AcknowledgeStorage bool                       `json:"acknowledgeStorage"`
	EffectsStarted     bool                       `json:"effectsStarted"`
	DrainStarted       bool                       `json:"drainStarted"`
	DrainComplete      bool                       `json:"drainComplete"`
	FenceStarted       bool                       `json:"fenceStarted"`
	NodeDeleteStarted  bool                       `json:"nodeDeleteStarted"`
	NodeDeleted        bool                       `json:"nodeDeleted"`
	ChildStarted       bool                       `json:"childStarted"`
	ChildComplete      bool                       `json:"childComplete"`
}
type replacementKubernetes interface {
	WorkerReplacementIdentity(context.Context, string, string) (string, bool, error)
	ReplacementImpact(context.Context, string, string) (k8s.ReplacementImpactReport, error)
	CordonAndDrainReplacement(context.Context, string, string, bool) error
	DeleteStaleNode(context.Context, string, string) error
	VerifyStaleNodeAbsent(context.Context, string, string) error
	VerifyReplacementWorkloads(context.Context, k8s.ReplacementImpactReport, string) error
}

func (s *ProvisionService) replacementClient() replacementKubernetes {
	if s.replacementKube != nil {
		return s.replacementKube
	}
	if s.Kubernetes == nil {
		return nil
	}
	return s.Kubernetes
}
func (s *ProvisionService) replacementAvailable() error {
	if s == nil || unavailable(s.Store) || s.ClusterID == "" || s.ClusterID == FleetScope || s.replacementClient() == nil {
		return errors.New("worker replacement requires selected cluster, durable store and Kubernetes client")
	}
	return nil
}
func (s *ProvisionService) saveReplacement(ctx context.Context, p *replacementState, create bool) error {
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	if len(b) > 4<<20 {
		return errors.New("replacement evidence exceeds durable size limit")
	}
	if create {
		e = s.Store.CreateSecret(ctx, s.ClusterID, replacementKind, p.ID, b)
	} else {
		e = s.Store.PutSecret(ctx, s.ClusterID, replacementKind, p.ID, b)
	}
	if e != nil {
		return errors.New("cannot persist worker replacement intent and evidence")
	}
	return nil
}
func (s *ProvisionService) loadReplacement(ctx context.Context, id string) (*replacementState, error) {
	if e := s.replacementAvailable(); e != nil {
		return nil, e
	}
	b, e := s.Store.GetSecret(ctx, s.ClusterID, replacementKind, id)
	var p replacementState
	if e != nil || len(b) > 4<<20 || json.Unmarshal(b, &p) != nil || p.ID != id || p.ClusterID != s.ClusterID || p.Revision < 1 || p.WorkflowVersion != 1 {
		return nil, errors.New("replacement plan unavailable in selected cluster")
	}
	return &p, nil
}
func publicReplacement(p *replacementState) *WorkerReplacementPlan {
	b, _ := json.Marshal(p.WorkerReplacementPlan)
	var out WorkerReplacementPlan
	json.Unmarshal(b, &out)
	return &out
}
func (s *ProvisionService) ListReplacements(ctx context.Context) ([]WorkerReplacementPlan, error) {
	if e := s.replacementAvailable(); e != nil {
		return nil, e
	}
	ids, e := s.Store.ListSecretKeys(ctx, s.ClusterID, replacementKind)
	if e != nil {
		return nil, e
	}
	if len(ids) > 1000 {
		return nil, errors.New("replacement history limit exceeded")
	}
	out := make([]WorkerReplacementPlan, 0, len(ids))
	for _, id := range ids {
		p, e := s.loadReplacement(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, *publicReplacement(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func replacementImpactHash(r k8s.ReplacementImpactReport) string {
	r.ObservedAt = time.Time{}
	r.Volumes = append([]k8s.ReplacementVolumeImpact{}, r.Volumes...)
	for i := range r.Volumes {
		r.Volumes[i].Attachments = append([]k8s.ReplacementAttachment{}, r.Volumes[i].Attachments...)
		sort.Slice(r.Volumes[i].Attachments, func(a, b int) bool { return r.Volumes[i].Attachments[a].Name < r.Volumes[i].Attachments[b].Name })
	}
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *ProvisionService) replacementProvider(ctx context.Context, p *replacementState) (proxmox.MachineProvider, proxmox.WorkerFencingProvider, error) {
	cfg, e := loadProvider(ctx, s.Store, p.Old.ProviderID)
	if e != nil {
		return nil, nil, e
	}
	provider, e := s.provider(cfg)
	if e != nil {
		return nil, nil, errors.New("replacement provider unavailable")
	}
	fencer, ok := provider.(proxmox.WorkerFencingProvider)
	if !ok {
		return nil, nil, errors.New("provider does not support verified durable worker fencing")
	}
	return provider, fencer, nil
}
func validReplacementSpec(spec ReplacementSpec, old proxmox.OwnedMachineRecord) error {
	if spec.Mode != "reachable" && spec.Mode != "dead" {
		return errors.New("replacement mode must be reachable or dead")
	}
	r := spec.Replacement
	if r.Kind != "worker-create" || len(r.Machines) != 1 || r.Machines[0].Role != "worker" || r.ProviderID != old.ProviderID || r.MachineID != "" {
		return errors.New("replacement must create exactly one worker with the same provider")
	}
	if r.Machines[0].Name == old.Name {
		return errors.New("replacement requires a new node name; old identity cannot be reused")
	}
	if address, e := netip.ParsePrefix(r.Machines[0].Address); e == nil && address.Addr().String() == old.Address {
		return errors.New("replacement requires a new address")
	}
	return nil
}
func (s *ProvisionService) PlanReplacement(ctx context.Context, spec ReplacementSpec, user string) (*WorkerReplacementPlan, error) {
	if e := s.replacementAvailable(); e != nil {
		return nil, e
	}
	s.replacementMu.Lock()
	defer s.replacementMu.Unlock()
	old, e := loadOwned(ctx, s.Store, spec.MachineID)
	if e != nil || old.ClusterID != s.ClusterID || old.Role != "worker" || old.Status != "ready" || old.Token == "" || old.Address == "" {
		return nil, errors.New("only an identity-confirmed owned ready worker can be replaced")
	}
	if e = validReplacementSpec(spec, old); e != nil {
		return nil, e
	}
	previous, e := s.ListReplacements(ctx)
	if e != nil {
		return nil, e
	}
	for _, prior := range previous {
		if prior.MachineID != old.ID || prior.Status == "succeeded" || prior.Status == "superseded" {
			continue
		}
		existing, loadErr := s.loadReplacement(ctx, prior.ID)
		if loadErr != nil {
			return nil, loadErr
		}
		child, childErr := s.load(ctx, existing.ReplacementPlan.ID)
		neverStarted := !existing.EffectsStarted && !existing.ChildStarted && !existing.FenceStarted && !existing.DrainStarted && !existing.NodeDeleteStarted && !s.replacementActive[existing.ID]
		canSupersede := existing.Author == user && neverStarted && childErr == nil && child.Status == "planned" && len(child.MachineIDs) == 0 && ((existing.Status == "planned" && (!existing.Approved || time.Now().After(existing.ExpiresAt))) || existing.Status == "requires_review")
		if canSupersede {
			existing.Status = "superseded"
			if e = s.saveReplacement(ctx, existing, false); e != nil {
				return nil, e
			}
			continue
		}
		return nil, errors.New("worker already has an approved, active or unresolved replacement plan")
	}
	uid, ready, e := s.replacementClient().WorkerReplacementIdentity(ctx, old.Name, old.Address)
	if e != nil {
		return nil, e
	}
	if spec.Mode == "reachable" && !ready || spec.Mode == "dead" && ready {
		return nil, errors.New("replacement mode does not match observed Kubernetes readiness")
	}
	impact, e := s.replacementClient().ReplacementImpact(ctx, old.Name, uid)
	if e != nil {
		return nil, e
	}
	if impact.Unknown {
		return nil, errors.New("storage impact is unknown; replacement blocked")
	}
	p := &replacementState{WorkerReplacementPlan: WorkerReplacementPlan{WorkflowVersion: 1, ID: uuid.NewString(), ClusterID: s.ClusterID, MachineID: old.ID, Mode: spec.Mode, NodeName: old.Name, NodeUID: uid, Impact: impact, ImpactHash: replacementImpactHash(impact), Status: "planned", Step: "preflight", Revision: 1, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(replacementPlanTTL), SafetyNotes: []string{"Old VM is permanently destroyed for fencing; stopping or unreachable is insufficient.", "Drain does not migrate or restore local/PVC data. Review every affected workload and volume.", "Replacement uses a new VM identity, node name and address; incomplete operations never replay blindly."}}, Old: old, OriginalImpact: impact, Author: strings.Clone(user)}
	provider, _, e := s.replacementProvider(ctx, p)
	if e != nil {
		return nil, e
	}
	if e = provider.VerifyOwned(ctx, old.Machine()); e != nil {
		return nil, errors.New("provider ownership or reachability cannot be verified")
	}
	plan := s.Plan
	if s.replacementChildPlan != nil {
		plan = s.replacementChildPlan
	}
	child, e := plan(ctx, spec.Replacement, user)
	if e != nil {
		return nil, e
	}
	p.ReplacementPlan = *child
	if e = s.saveReplacement(ctx, p, true); e != nil {
		return nil, e
	}
	return publicReplacement(p), nil
}
func (s *ProvisionService) ApproveReplacement(ctx context.Context, id, name, hash, user string, ack bool) (jobs.Request, error) {
	if e := s.replacementAvailable(); e != nil {
		return jobs.Request{}, e
	}
	s.replacementMu.Lock()
	defer s.replacementMu.Unlock()
	p, e := s.loadReplacement(ctx, id)
	if e != nil {
		return jobs.Request{}, e
	}
	if p.Status != "planned" || p.Author != user || time.Now().After(p.ExpiresAt) || name != p.NodeName || hash != p.ImpactHash {
		return jobs.Request{}, errors.New("replacement plan expired or confirmation/impact does not match")
	}
	if p.Impact.Unknown {
		return jobs.Request{}, errors.New("unknown storage impact blocks replacement")
	}
	if !ack {
		return jobs.Request{}, errors.New("explicit storage impact acknowledgement is required")
	}
	p.Approved = true
	p.AcknowledgeStorage = ack
	if e = s.saveReplacement(ctx, p, false); e != nil {
		return jobs.Request{}, e
	}
	return jobs.Request{Kind: "worker-replace", ProvisionID: p.ID, DedupeKey: fmt.Sprintf("replacement:%s:%d", p.ID, p.Revision)}, nil
}

func (s *ProvisionService) replacementStep(ctx context.Context, e *jobs.Execution, p *replacementState, step, message string) error {
	p.Step = step
	if err := s.saveReplacement(ctx, p, false); err != nil {
		return err
	}
	return e.Checkpoint(ctx, "replace-"+step, message)
}
func (s *ProvisionService) replacementObserve(ctx context.Context, p *replacementState, exact bool) error {
	uid, _, err := s.replacementClient().WorkerReplacementIdentity(ctx, p.NodeName, p.Old.Address)
	if err != nil || uid != p.NodeUID {
		return errors.New("old worker identity cannot be reverified")
	}
	impact, err := s.replacementClient().ReplacementImpact(ctx, p.NodeName, p.NodeUID)
	if err != nil || impact.Unknown {
		return errors.New("current storage impact is unknown")
	}
	if exact {
		if replacementImpactHash(impact) != p.ImpactHash {
			return errors.New("storage impact changed since review; create a fresh reviewed plan")
		}
	} else if !replacementImpactSubset(impact, p.Impact) {
		return errors.New("new workloads or storage risks appeared after review")
	}
	return nil
}
func replacementImpactSubset(current, reviewed k8s.ReplacementImpactReport) bool {
	if current.Unknown || current.NodeUID != reviewed.NodeUID {
		return false
	}
	normalize := func(v k8s.ReplacementVolumeImpact) (string, string) {
		v.Attachments = append([]k8s.ReplacementAttachment{}, v.Attachments...)
		key := v.Namespace + "/" + v.Pod + "/" + v.Volume
		if v.PVUID != "" {
			key = "pv:" + v.PVUID + ":pvc:" + v.PVCUID
			v.Pod = ""
			v.Volume = ""
			v.Kind = "persistent-volume"
			v.Reasons = nil
		}
		for i := range v.Attachments {
			v.Attachments[i].Attached = false
			v.Attachments[i].Deleting = false
		}
		sort.Slice(v.Attachments, func(i, j int) bool { return v.Attachments[i].UID < v.Attachments[j].UID })
		b, _ := json.Marshal(v)
		return key, string(b)
	}
	known := map[string]string{}
	for _, v := range reviewed.Volumes {
		key, risk := normalize(v)
		known[key] = risk
	}
	for _, v := range current.Volumes {
		key, risk := normalize(v)
		if known[key] != risk {
			return false
		}
	}
	pods := map[string]string{}
	for _, p := range reviewed.Pods {
		p.Phase = ""
		b, _ := json.Marshal(p)
		pods[p.Namespace+"/"+p.Name] = string(b)
	}
	for _, p := range current.Pods {
		p.Phase = ""
		b, _ := json.Marshal(p)
		if prior, ok := pods[p.Namespace+"/"+p.Name]; !ok || prior != string(b) {
			return false
		}
	}
	return true
}

func (s *ProvisionService) replacementRevalidateFence(ctx context.Context, p *replacementState, fencer proxmox.WorkerFencingProvider) error {
	if p.Fence == nil {
		return errors.New("no durable fencing evidence is available")
	}
	prior := *p.Fence
	evidence, err := fencer.RevalidateWorkerFence(ctx, p.Old.Machine(), prior)
	if err != nil && p.NodeDeleted && p.ChildStarted && prior.Outcome == "fenced" && prior.DeletionTaskID != "" && prior.MachineID == p.Old.ID {
		if child, records, checkErr := s.replacementChildRecords(ctx, p); checkErr == nil && records[0].VMID == p.Old.VMID && records[0].ID != p.Old.ID && records[0].Token != p.Old.Token && child.ID == p.ReplacementPlan.ID {
			if checkErr = s.replacementClient().VerifyStaleNodeAbsent(ctx, p.NodeName, p.NodeUID); checkErr == nil {
				p.ReusedProviderID = true
				return s.saveReplacement(ctx, p, false)
			}
		}
	}
	p.Fence = &evidence
	if saveErr := s.saveReplacement(ctx, p, false); saveErr != nil {
		return saveErr
	}
	if err != nil || !evidence.Fresh(time.Now().UTC()) {
		return errors.New("fresh provider fencing cannot be proved; destructive continuation blocked")
	}
	return nil
}
func (s *ProvisionService) replacementFence(ctx context.Context, e *jobs.Execution, p *replacementState, fencer proxmox.WorkerFencingProvider) error {
	if p.FenceStarted {
		return s.replacementRevalidateFence(ctx, p, fencer)
	}
	if err := s.replacementObserve(ctx, p, false); err != nil {
		return err
	}
	p.EffectsStarted = true
	p.FenceStarted = true
	if err := s.replacementStep(ctx, e, p, "fencing", "Permanently fencing the old provider-owned worker VM"); err != nil {
		return err
	}
	if err := beginMachineIntent(ctx, e, "delete", p.Old); err != nil {
		return err
	}
	evidence, operationErr := fencer.DestroyOwnedForFence(ctx, p.Old.Machine())
	p.Fence = &evidence
	// A task ID returned before a timeout is valuable evidence. Persist even if
	// caller cancellation occurred, without converting an unknown result to success.
	final, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.saveReplacement(final, p, false); err != nil {
		return jobs.ErrUncertain
	}
	if operationErr != nil || !evidence.Fresh(time.Now().UTC()) {
		return fmt.Errorf("%w: old worker fencing is not confirmed", jobs.ErrUncertain)
	}
	if err := proveMachineIntent(ctx, e, "delete", p.Old); err != nil {
		return err
	}
	return nil
}
func (s *ProvisionService) replacementDeleteNode(ctx context.Context, e *jobs.Execution, p *replacementState, fencer proxmox.WorkerFencingProvider) error {
	if p.NodeDeleted {
		return s.replacementClient().VerifyStaleNodeAbsent(ctx, p.NodeName, p.NodeUID)
	}
	if p.NodeDeleteStarted {
		if err := s.replacementClient().VerifyStaleNodeAbsent(ctx, p.NodeName, p.NodeUID); err != nil {
			return errors.New("previous Kubernetes Node deletion is ambiguous; no blind retry")
		}
		p.NodeDeleted = true
		return s.saveReplacement(ctx, p, false)
	}
	if err := s.replacementObserve(ctx, p, false); err != nil {
		return err
	}
	if err := s.replacementRevalidateFence(ctx, p, fencer); err != nil {
		return err
	}
	p.NodeDeleteStarted = true
	if err := s.replacementStep(ctx, e, p, "remove-stale-node", "Deleting only the pinned stale Kubernetes Node UID after fresh provider fencing"); err != nil {
		return err
	}
	if !p.Fence.Fresh(time.Now().UTC()) {
		return errors.New("fencing evidence expired before Kubernetes deletion")
	}
	deletionCtx := reconcile.WithMutationGuard(ctx, func(c context.Context) error {
		if err := e.ValidateExecution(c); err != nil {
			return err
		}
		if p.Fence == nil || !p.Fence.Fresh(time.Now().UTC()) {
			return fmt.Errorf("%w: fencing evidence expired at Kubernetes deletion", jobs.ErrUncertain)
		}
		return nil
	})
	if err := s.replacementClient().DeleteStaleNode(deletionCtx, p.NodeName, p.NodeUID); err != nil {
		return fmt.Errorf("%w: Kubernetes Node deletion result not confirmed", jobs.ErrUncertain)
	}
	if err := s.poll(ctx, 2*time.Minute, func(c context.Context) bool {
		return s.replacementClient().VerifyStaleNodeAbsent(c, p.NodeName, p.NodeUID) == nil
	}); err != nil {
		return fmt.Errorf("%w: stale Kubernetes identity absence not proved", jobs.ErrUncertain)
	}
	p.NodeDeleted = true
	return s.saveReplacement(ctx, p, false)
}
func (s *ProvisionService) replacementCreate(ctx context.Context, e *jobs.Execution, p *replacementState) error {
	if p.ChildComplete {
		return s.replacementVerifyChild(ctx, e, p)
	}
	if p.ChildStarted {
		return errors.New("replacement VM creation/configuration already started; observe through resume-plan instead of retry")
	}
	child, err := s.load(ctx, p.ReplacementPlan.ID)
	if err != nil || child.Status != "planned" || child.Spec.Kind != "worker-create" || len(child.Spec.Machines) != 1 {
		return errors.New("pinned replacement provisioning plan unavailable")
	}
	p.ChildStarted = true
	p.EffectsStarted = true
	if err = s.replacementStep(ctx, e, p, "create-replacement", "Creating the single replacement worker from its immutable reviewed plan"); err != nil {
		return err
	}
	run := s.Run
	if s.replacementChildRun != nil {
		run = s.replacementChildRun
	}
	if err = run(ctx, e, jobs.Request{Kind: "worker-create", ProvisionID: child.ID}); err != nil {
		return err
	}
	p.ChildComplete = true
	if child, err = s.load(ctx, child.ID); err == nil {
		p.ReplacementPlan = child.ProvisionPlan
	}
	return s.saveReplacement(ctx, p, false)
}
func (s *ProvisionService) replacementChildRecords(ctx context.Context, p *replacementState) (*provisionState, []proxmox.OwnedMachineRecord, error) {
	child, err := s.load(ctx, p.ReplacementPlan.ID)
	if err != nil || child.Spec.Kind != "worker-create" || len(child.MachineIDs) != 1 || len(child.Spec.Machines) != 1 {
		return nil, nil, errors.New("replacement child does not have exactly one durable machine identity")
	}
	record, err := loadOwned(ctx, s.Store, child.MachineIDs[0])
	if err != nil || record.ClusterID != s.ClusterID || record.PlanID != child.ID || record.Role != "worker" || record.Name == p.NodeName || record.Address == p.Old.Address || record.Address == "" || record.Token == "" {
		return nil, nil, errors.New("replacement machine identity cannot be proved")
	}
	if record.Status != "config-applied" && record.Status != "ready" {
		return nil, nil, errors.New("resume only supports an already configured or ready replacement; no create or config replay")
	}
	provider, err := s.provider(child.Provider)
	if err != nil || provider.VerifyOwned(ctx, record.Machine()) != nil {
		return nil, nil, errors.New("replacement provider ownership unavailable")
	}
	return child, []proxmox.OwnedMachineRecord{record}, nil
}
func (s *ProvisionService) replacementVerifyChild(ctx context.Context, e *jobs.Execution, p *replacementState) error {
	child, records, err := s.replacementChildRecords(ctx, p)
	if err != nil {
		return err
	}
	verify := s.verifyConfiguredMachines
	if s.replacementChildVerify != nil {
		verify = s.replacementChildVerify
	}
	if err = s.replacementStep(ctx, e, p, "verify-replacement", "Observing existing replacement Talos/Kubernetes readiness; no create or configuration replay"); err != nil {
		return err
	}
	if err = verify(ctx, e, child, records); err != nil {
		return err
	}
	child.Status = "succeeded"
	if err = s.save(ctx, child, false); err != nil {
		return err
	}
	p.ChildComplete = true
	p.ReplacementPlan = child.ProvisionPlan
	return s.saveReplacement(ctx, p, false)
}
func (s *ProvisionService) RunReplacement(ctx context.Context, e *jobs.Execution, r jobs.Request) (runErr error) {
	ctx = reconcile.WithMutationGuard(ctx, e.ValidateExecution)
	if err := s.replacementAvailable(); err != nil {
		return err
	}
	s.replacementMu.Lock()
	p, err := s.loadReplacement(ctx, r.ProvisionID)
	if err != nil || p.Status != "planned" || !p.Approved || time.Now().After(p.ExpiresAt) || r.Kind != "worker-replace" || r.DedupeKey != fmt.Sprintf("replacement:%s:%d", p.ID, p.Revision) {
		s.replacementMu.Unlock()
		return errors.New("replacement plan unavailable, expired or not approved")
	}
	if s.replacementActive == nil {
		s.replacementActive = map[string]bool{}
	}
	if s.replacementActive[p.ID] {
		s.replacementMu.Unlock()
		return errors.New("replacement already executing")
	}
	s.replacementActive[p.ID] = true
	p.Status = "running"
	err = s.saveReplacement(ctx, p, false)
	s.replacementMu.Unlock()
	defer func() { s.replacementMu.Lock(); delete(s.replacementActive, p.ID); s.replacementMu.Unlock() }()
	if err != nil {
		return err
	}
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			runErr = jobs.ErrUncertain
		}
		p.Status = "succeeded"
		p.RequiresReview = false
		p.Error = ""
		if runErr != nil {
			p.Status = "requires_review"
			p.RequiresReview = true
			p.Error = "Execution stopped; inspect persisted identity and evidence before an explicitly approved resume"
			if p.EffectsStarted {
				runErr = fmt.Errorf("%w: replacement outcome requires review", jobs.ErrUncertain)
			}
		}
		final, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.saveReplacement(final, p, false) != nil {
			runErr = jobs.ErrUncertain
		}
		if s.Audit != nil {
			s.Audit("worker-replace", p.Author, p.Status, p.ID)
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	if err := validateReplacementOriginalImpact(p); err != nil {
		return err
	}
	if !p.Resume {
		child, checkErr := s.load(ctx, p.ReplacementPlan.ID)
		if checkErr != nil || child.Status != "planned" || len(child.MachineIDs) != 0 || child.Spec.Kind != "worker-create" || time.Since(child.CreatedAt) > 30*time.Minute {
			return errors.New("replacement child plan was already consumed; no old-worker mutation allowed")
		}
		expected, _ := json.Marshal(p.ReplacementPlan.Spec)
		actual, _ := json.Marshal(child.Spec)
		if string(expected) != string(actual) {
			return errors.New("pinned replacement child specification changed")
		}
	}
	provider, fencer, err := s.replacementProvider(ctx, p)
	if err != nil {
		return err
	}
	// The private ownership snapshot must still match the registry. Status may
	// change to deleted after fencing, but immutable identity cannot change.
	current, err := loadOwned(ctx, s.Store, p.Old.ID)
	if err != nil || machineIntentIdentity(current) != machineIntentIdentity(p.Old) || current.Token != p.Old.Token || current.Name != p.NodeName || current.Address != p.Old.Address || current.Role != "worker" {
		return errors.New("original worker ownership registry changed")
	}
	if !p.FenceStarted {
		if err = provider.VerifyOwned(ctx, p.Old.Machine()); err != nil {
			return errors.New("old worker provider ownership unavailable")
		}
	}
	if !p.Resume {
		uid, ready, err := s.replacementClient().WorkerReplacementIdentity(ctx, p.NodeName, p.Old.Address)
		if err != nil || uid != p.NodeUID || p.Mode == "reachable" && !ready || p.Mode == "dead" && ready {
			return errors.New("worker identity/readiness changed since planning")
		}
		if err = s.replacementObserve(ctx, p, true); err != nil {
			return err
		}
		if p.Mode == "reachable" {
			p.EffectsStarted = true
			p.DrainStarted = true
			if err = s.replacementStep(ctx, e, p, "drain", "Cordoning and draining the reviewed worker; local data is not migrated"); err != nil {
				return err
			}
			if err = s.replacementClient().CordonAndDrainReplacement(ctx, p.NodeName, p.NodeUID, p.AcknowledgeStorage); err != nil {
				return err
			}
			p.DrainComplete = true
			if err = s.saveReplacement(ctx, p, false); err != nil {
				return err
			}
		}
	} else {
		if p.FenceStarted {
			if err = s.replacementRevalidateFence(ctx, p, fencer); err != nil {
				return err
			}
		}
		if p.ChildStarted {
			if err = s.replacementVerifyChild(ctx, e, p); err != nil {
				return err
			}
		} else {
			return errors.New("resume requires an already configured replacement; initial mutation stages cannot be replayed")
		}
	}
	if p.Mode == "dead" {
		if err = s.replacementFence(ctx, e, p, fencer); err != nil {
			return err
		}
		if err = s.replacementDeleteNode(ctx, e, p, fencer); err != nil {
			return err
		}
	}
	if !p.ChildComplete {
		if err = s.replacementCreate(ctx, e, p); err != nil {
			return err
		}
	}
	if p.Mode == "reachable" {
		if !p.DrainComplete {
			return errors.New("completed drain evidence required")
		}
		if err = s.replacementFence(ctx, e, p, fencer); err != nil {
			return err
		}
		if err = s.replacementDeleteNode(ctx, e, p, fencer); err != nil {
			return err
		}
	}
	if err = s.replacementRevalidateFence(ctx, p, fencer); err != nil {
		return err
	}
	if err = s.replacementClient().VerifyStaleNodeAbsent(ctx, p.NodeName, p.NodeUID); err != nil {
		return err
	}
	if err = s.replacementVerifyChild(ctx, e, p); err != nil {
		return err
	}
	if err = s.replacementStep(ctx, e, p, "verify-workloads", "Verifying the affected workload controllers recovered on the replacement infrastructure"); err != nil {
		return err
	}
	if len(p.ReplacementPlan.Spec.Machines) != 1 {
		return errors.New("replacement child identity missing")
	}
	if err = s.poll(ctx, 5*time.Minute, func(c context.Context) bool {
		return s.replacementClient().VerifyReplacementWorkloads(c, p.OriginalImpact, p.ReplacementPlan.Spec.Machines[0].Name) == nil
	}); err != nil {
		return errors.New("affected workloads have not been proved recovered; review required")
	}
	p.Old.Status = "deleted"
	if err = saveOwned(ctx, s.Store, p.Old, false); err != nil {
		return err
	}
	if s.Talos != nil {
		s.Talos.ForgetNode(p.Old.Address, p.Old.Name)
	}
	p.Step = "complete"
	return e.Log("replacement-complete", "Old worker VM fenced and stale UID absent; replacement is Ready with verified Talos/Kubernetes identity")
}

// Historical workload identities are evidence, not inventory to rediscover after
// drain. Older plans without this evidence remain readable but cannot execute.
func validateReplacementOriginalImpact(p *replacementState) error {
	if p.OriginalImpact.NodeUID == "" || p.OriginalImpact.NodeUID != p.NodeUID || p.OriginalImpact.Unknown {
		return errors.New("original workload evidence is missing or incompatible; operator review required")
	}
	for _, pod := range p.OriginalImpact.Pods {
		if pod.ControllerUID == "" || pod.ControllerName == "" || pod.Namespace == "" {
			return errors.New("original workload has no pinned controller identity; operator review required")
		}
		switch pod.ControllerKind {
		case "ReplicaSet", "Deployment", "StatefulSet", "DaemonSet":
		default:
			return errors.New("original workload controller cannot be verified automatically; operator review required")
		}
	}
	return nil
}

func (s *ProvisionService) PlanReplacementResume(ctx context.Context, id, user string) (*WorkerReplacementPlan, error) {
	if err := s.replacementAvailable(); err != nil {
		return nil, err
	}
	s.replacementMu.Lock()
	defer s.replacementMu.Unlock()
	if s.replacementActive[id] {
		return nil, errors.New("active replacement cannot be replanned")
	}
	p, err := s.loadReplacement(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := validateReplacementOriginalImpact(p); err != nil {
		return nil, err
	}
	if p.Status == "superseded" && p.SuccessorID != "" {
		if next, getErr := s.loadReplacement(ctx, p.SuccessorID); getErr == nil {
			if next.Status != "planned" || next.Author != user {
				return nil, errors.New("successor plan already consumed or belongs to another operator")
			}
			return publicReplacement(next), nil
		}
		var next replacementState
		if json.Unmarshal(p.SuccessorState, &next) != nil || next.ID != p.SuccessorID || next.PreviousPlanID != p.ID || next.Author != user || next.Status != "planned" {
			return nil, errors.New("pending successor plan cannot be reconstructed safely")
		}
		if err = s.saveReplacement(ctx, &next, true); err != nil {
			return nil, err
		}
		return publicReplacement(&next), nil
	}
	if p.Status != "requires_review" && p.Status != "running" {
		return nil, errors.New("only interrupted replacement can be reviewed for resume")
	}
	if !p.ChildStarted {
		return nil, errors.New("no configured child to adopt; initial create/fence stages require manual recovery and cannot be replayed")
	}
	if _, _, err = s.replacementChildRecords(ctx, p); err != nil {
		return nil, err
	}
	_, fencer, err := s.replacementProvider(ctx, p)
	if err != nil {
		return nil, err
	}
	if p.FenceStarted {
		if err = s.replacementRevalidateFence(ctx, p, fencer); err != nil {
			return nil, err
		}
	}
	if p.NodeDeleteStarted {
		if err = s.replacementClient().VerifyStaleNodeAbsent(ctx, p.NodeName, p.NodeUID); err != nil {
			return nil, err
		}
		p.NodeDeleted = true
	}
	if !p.NodeDeleted {
		impact, err := s.replacementClient().ReplacementImpact(ctx, p.NodeName, p.NodeUID)
		if err != nil || impact.Unknown {
			return nil, errors.New("storage impact cannot be re-observed for resume")
		}
		p.Impact = impact
		p.ImpactHash = replacementImpactHash(impact)
	}
	originalID := p.ID
	original, err := s.loadReplacement(ctx, originalID)
	if err != nil {
		return nil, err
	}
	p.ID = uuid.NewString()
	p.PreviousPlanID = originalID
	p.CreatedAt = time.Now().UTC()
	p.SuccessorID = ""
	p.SuccessorState = nil
	p.Status = "planned"
	p.Step = "resume-review"
	p.Resume = true
	p.RequiresReview = true
	p.Error = ""
	p.Author = strings.Clone(user)
	p.Approved = false
	p.AcknowledgeStorage = false
	p.Revision++
	p.ExpiresAt = time.Now().UTC().Add(replacementPlanTTL)
	// Persist the complete successor intent before publishing it. A crash or
	// storage error between these writes can reconstruct the same plan ID.
	successorJSON, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	original.SuccessorID = p.ID
	original.SuccessorState = successorJSON
	original.Status = "superseded"
	original.RequiresReview = true
	if err = s.saveReplacement(ctx, original, false); err != nil {
		return nil, err
	}
	if err = s.saveReplacement(ctx, p, true); err != nil {
		return nil, err
	}
	return publicReplacement(p), nil
}
