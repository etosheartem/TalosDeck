package operations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/reconcile"
)

type replacementTestKube struct {
	ready, deleted, unknown bool
	calls                   *[]string
}

func (k *replacementTestKube) WorkerReplacementIdentity(context.Context, string, string) (string, bool, error) {
	if k.deleted {
		return "", false, errors.New("node absent")
	}
	return "pinned-node-uid", k.ready, nil
}
func (k *replacementTestKube) ReplacementImpact(context.Context, string, string) (k8s.ReplacementImpactReport, error) {
	if k.deleted {
		return k8s.ReplacementImpactReport{}, errors.New("node absent")
	}
	return k8s.ReplacementImpactReport{NodeName: "old-worker", NodeUID: "pinned-node-uid", ObservedAt: time.Now().UTC(), Unknown: k.unknown, Pods: []k8s.ReplacementPodImpact{}, Volumes: []k8s.ReplacementVolumeImpact{}, Issues: []string{}}, nil
}
func (k *replacementTestKube) CordonAndDrainReplacement(ctx context.Context, _ string, uid string, ack bool) error {
	if uid != "pinned-node-uid" || !ack {
		return errors.New("unreviewed drain")
	}
	if e := reconcile.CheckRequiredMutation(ctx); e != nil {
		return e
	}
	*k.calls = append(*k.calls, "drain")
	return nil
}
func (k *replacementTestKube) DeleteStaleNode(ctx context.Context, _ string, uid string) error {
	if uid != "pinned-node-uid" {
		return errors.New("wrong identity")
	}
	if e := reconcile.CheckRequiredMutation(ctx); e != nil {
		return e
	}
	*k.calls = append(*k.calls, "delete-node")
	k.deleted = true
	return nil
}
func (k *replacementTestKube) VerifyReplacementWorkloads(context.Context, k8s.ReplacementImpactReport, string) error {
	return nil
}
func (k *replacementTestKube) VerifyStaleNodeAbsent(context.Context, string, string) error {
	if !k.deleted {
		return errors.New("node exists")
	}
	return nil
}

type replacementTestProvider struct {
	failCreateProvider
	calls   *[]string
	outage  bool
	deleted bool
	oldID   string
}

func (p *replacementTestProvider) VerifyOwned(_ context.Context, m proxmox.OwnedMachine) error {
	if p.outage {
		return errors.New("provider unavailable")
	}
	if p.deleted && m.ID == p.oldID {
		return errors.New("old VM absent")
	}
	return nil
}
func (p *replacementTestProvider) DestroyOwnedForFence(ctx context.Context, m proxmox.OwnedMachine) (proxmox.WorkerFencingEvidence, error) {
	if e := reconcile.CheckRequiredMutation(ctx); e != nil {
		return proxmox.WorkerFencingEvidence{}, e
	}
	*p.calls = append(*p.calls, "fence")
	if p.outage {
		return proxmox.WorkerFencingEvidence{DeletionTaskID: "known-task"}, proxmox.ErrFencingUnknown
	}
	p.deleted = true
	return proxmox.WorkerFencingEvidence{Version: 1, Mode: "destroy-owned", Outcome: "fenced", MachineID: m.ID, ProviderID: m.ProviderID, Generation: m.ID, ProviderResourceID: "pve/150", DeletionTaskID: "known-task", ResourceConfirmedAbsent: true, HAConfirmedAbsent: true, VerifiedAt: time.Now().UTC()}, nil
}
func (p *replacementTestProvider) RevalidateWorkerFence(_ context.Context, _ proxmox.OwnedMachine, e proxmox.WorkerFencingEvidence) (proxmox.WorkerFencingEvidence, error) {
	if p.outage || !p.deleted {
		return e, proxmox.ErrFencingUnknown
	}
	e.VerifiedAt = time.Now().UTC()
	return e, nil
}

type replacementTestAuthority struct{}

func (replacementTestAuthority) Validate(context.Context, string, uint64) error { return nil }
func replacementFixture(t *testing.T, mode string) (*ProvisionService, *replacementTestKube, *replacementTestProvider, ReplacementSpec, *[]string) {
	t.Helper()
	s, spec, _ := provisionFixture(t)
	s.ClusterID = uuid.NewString()
	s.PollInterval = time.Millisecond
	calls := []string{}
	kube := &replacementTestKube{ready: mode == "reachable", calls: &calls}
	provider := &replacementTestProvider{calls: &calls}
	s.replacementKube = kube
	s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return provider, nil }
	old := proxmox.NewOwnership(spec.ProviderID, s.ClusterID, "old-plan", "pve", proxmox.MachineSpec{Name: "old-worker", Role: "worker"}, 150)
	old.Address = "10.0.0.10"
	old.Status = "ready"
	provider.oldID = old.ID
	if e := saveOwned(context.Background(), s.Store, old, true); e != nil {
		t.Fatal(e)
	}
	spec.Kind = "worker-create"
	spec.Name = "new-worker"
	spec.Machines = []proxmox.MachineSpec{{Name: "new-worker", Role: "worker", NetworkMode: "dhcp", Cores: 2, MemoryMB: 2048, DiskGB: 20}}
	s.replacementChildPlan = func(ctx context.Context, spec ProvisionSpec, user string) (*ProvisionPlan, error) {
		cfg, e := loadProvider(ctx, s.Store, spec.ProviderID)
		if e != nil {
			return nil, e
		}
		child := &provisionState{ProvisionPlan: ProvisionPlan{ID: uuid.NewString(), Spec: spec, Status: "planned", CreatedAt: time.Now().UTC()}, Author: user, ClusterID: s.ClusterID, Provider: cfg}
		if e = s.save(ctx, child, true); e != nil {
			return nil, e
		}
		return &child.ProvisionPlan, nil
	}
	s.replacementChildRun = func(ctx context.Context, _ *jobs.Execution, r jobs.Request) error {
		calls = append(calls, "create-child")
		child, e := s.load(ctx, r.ProvisionID)
		if e != nil {
			return e
		}
		record := proxmox.NewOwnership(child.Spec.ProviderID, s.ClusterID, child.ID, "pve", child.Spec.Machines[0], 151)
		record.Address = "10.0.0.11"
		record.Status = "config-applied"
		if e = saveOwned(ctx, s.Store, record, true); e != nil {
			return e
		}
		child.MachineIDs = []string{record.ID}
		child.Status = "succeeded"
		return s.save(ctx, child, false)
	}
	s.replacementChildVerify = func(_ context.Context, _ *jobs.Execution, _ *provisionState, _ []proxmox.OwnedMachineRecord) error {
		calls = append(calls, "verify-child")
		return nil
	}
	return s, kube, provider, ReplacementSpec{MachineID: old.ID, Mode: mode, Replacement: spec}, &calls
}
func replacementManager(t *testing.T, s *ProvisionService) *jobs.Manager {
	t.Helper()
	m, e := jobs.OpenCluster(t.TempDir(), s.ClusterID, s.RunReplacement)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.SetExecutionAuthority(replacementTestAuthority{}, "test-instance", 1); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	return m
}
func replacementRun(t *testing.T, m *jobs.Manager, r jobs.Request) jobs.Job {
	t.Helper()
	j, e := m.Submit(r, "admin")
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, e = m.Get(j.ID)
		if e != nil {
			t.Fatal(e)
		}
		if j.Status != "queued" && j.Status != "running" {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("replacement did not finish")
	return j
}
func TestReplacementWorkflowOrdersAndProof(t *testing.T) {
	for _, mode := range []string{"reachable", "dead"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, spec, calls := replacementFixture(t, mode)
			plan, e := s.PlanReplacement(context.Background(), spec, "admin")
			if e != nil {
				t.Fatal(e)
			}
			request, e := s.ApproveReplacement(context.Background(), plan.ID, plan.NodeName, plan.ImpactHash, "admin", true)
			if e != nil {
				t.Fatal(e)
			}
			j := replacementRun(t, replacementManager(t, s), request)
			if j.Status != "succeeded" {
				t.Fatalf("%+v", j)
			}
			want := []string{"fence", "delete-node", "create-child", "verify-child"}
			if mode == "reachable" {
				want = []string{"drain", "create-child", "fence", "delete-node", "verify-child"}
			}
			if !reflect.DeepEqual(*calls, want) {
				t.Fatalf("order %v want%v", *calls, want)
			}
			stored, e := s.loadReplacement(context.Background(), plan.ID)
			if e != nil || stored.Status != "succeeded" || !stored.NodeDeleted || !stored.ChildComplete {
				t.Fatalf("missing durable proof %+v %v", stored, e)
			}
			raw, _ := json.Marshal(publicReplacement(stored))
			if strings.Contains(string(raw), stored.Old.Token) || strings.Contains(string(raw), "private-provider-value") {
				t.Fatal("private credentials exposed in public plan")
			}
		})
	}
}
func TestReplacementFailureBeforeEffectsCanExplicitlyReplan(t *testing.T) {
	s, _, provider, spec, calls := replacementFixture(t, "dead")
	plan, e := s.PlanReplacement(context.Background(), spec, "admin")
	if e != nil {
		t.Fatal(e)
	}
	request, e := s.ApproveReplacement(context.Background(), plan.ID, plan.NodeName, plan.ImpactHash, "admin", true)
	if e != nil {
		t.Fatal(e)
	}
	provider.outage = true
	j := replacementRun(t, replacementManager(t, s), request)
	if j.Status == "succeeded" || len(*calls) != 0 {
		t.Fatal("provider outage allowed mutation")
	}
	provider.outage = false
	next, e := s.PlanReplacement(context.Background(), spec, "admin")
	if e != nil || next.ID == plan.ID {
		t.Fatalf("explicit no-effect replan failed %v", e)
	}
}
func TestReplacementUnknownImpactAndStaleApprovalBlock(t *testing.T) {
	s, k, _, spec, _ := replacementFixture(t, "dead")
	k.unknown = true
	if _, e := s.PlanReplacement(context.Background(), spec, "admin"); e == nil {
		t.Fatal("unknown storage allowed")
	}
	k.unknown = false
	p, e := s.PlanReplacement(context.Background(), spec, "admin")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApproveReplacement(context.Background(), p.ID, p.NodeName, p.ImpactHash, "admin", false); e == nil {
		t.Fatal("missing destructive acknowledgement accepted")
	}
	if _, e = s.ApproveReplacement(context.Background(), p.ID, p.NodeName, "stale", "admin", true); e == nil {
		t.Fatal("stale impact accepted")
	}
	p2, e := s.PlanReplacement(context.Background(), spec, "admin")
	if e != nil || p2.ID == p.ID {
		t.Fatal("unapproved plan cannot be superseded")
	}
	if _, e = s.ApproveReplacement(context.Background(), p.ID, p.NodeName, p.ImpactHash, "admin", true); e == nil {
		t.Fatal("superseded approval accepted")
	}
}
func TestReplacementResumeObservesConfiguredChildWithoutDuplicate(t *testing.T) {
	s, _, _, spec, calls := replacementFixture(t, "reachable")
	create := s.replacementChildRun
	s.replacementChildRun = func(ctx context.Context, e *jobs.Execution, r jobs.Request) error {
		if err := create(ctx, e, r); err != nil {
			return err
		}
		return jobs.ErrUncertain
	}
	p, e := s.PlanReplacement(context.Background(), spec, "admin")
	if e != nil {
		t.Fatal(e)
	}
	r, _ := s.ApproveReplacement(context.Background(), p.ID, p.NodeName, p.ImpactHash, "admin", true)
	m := replacementManager(t, s)
	j := replacementRun(t, m, r)
	if j.Status != "interrupted" {
		t.Fatalf("crash not unknown %+v", j)
	}
	// Reopen encrypted state and explicitly acknowledge the old job in the SAME
	// journal before approving a new immutable resume plan.
	next, e := s.PlanReplacementResume(context.Background(), p.ID, "admin")
	if e != nil {
		t.Fatal(e)
	}
	if next.ID == p.ID || next.PreviousPlanID != p.ID {
		t.Fatal("resume reused original approval identity")
	}
	if e = m.Acknowledge(j.ID, "admin"); e != nil {
		t.Fatal(e)
	}
	r, e = s.ApproveReplacement(context.Background(), next.ID, next.NodeName, next.ImpactHash, "admin", true)
	if e != nil {
		t.Fatal(e)
	}
	result := replacementRun(t, m, r)
	if result.Status != "succeeded" {
		t.Fatalf("resume %+v", result)
	}
	creates := 0
	for _, call := range *calls {
		if call == "create-child" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatal("resume duplicated child")
	}
	if _, e = s.ApproveReplacement(context.Background(), p.ID, p.NodeName, p.ImpactHash, "admin", true); e == nil {
		t.Fatal("old approval authorized resume")
	}
}

func TestReplacementExpiredApprovedPlanSupersedesWithoutExecutingQueuedOldRequest(t *testing.T) {
	s, _, _, spec, calls := replacementFixture(t, "dead")
	ctx := context.Background()
	p, e := s.PlanReplacement(ctx, spec, "admin")
	if e != nil {
		t.Fatal(e)
	}
	oldRequest, e := s.ApproveReplacement(ctx, p.ID, p.NodeName, p.ImpactHash, "admin", true)
	if e != nil {
		t.Fatal(e)
	}
	stored, _ := s.loadReplacement(ctx, p.ID)
	stored.ExpiresAt = time.Now().Add(-time.Second)
	if e = s.saveReplacement(ctx, stored, false); e != nil {
		t.Fatal(e)
	}
	next, e := s.PlanReplacement(ctx, spec, "admin")
	if e != nil || next.ID == p.ID {
		t.Fatalf("expired unstarted approval cannot be replaced: %v", e)
	}
	j := replacementRun(t, replacementManager(t, s), oldRequest)
	if j.Status == "succeeded" || len(*calls) != 0 {
		t.Fatal("queued superseded request mutated infrastructure")
	}
}
func TestReplacementImpactSubsetKeepsImmutableAttachmentEvidence(t *testing.T) {
	v := k8s.ReplacementVolumeImpact{Namespace: "prod", Pod: "db-0", Volume: "data", PVUID: "pv-uid", PVCUID: "pvc-uid", State: "requires_review", Kind: "pvc", Attachments: []k8s.ReplacementAttachment{{Name: "attach", UID: "attach-uid", Attached: true, Deleting: true}}}
	original := k8s.ReplacementImpactReport{NodeUID: "node", Volumes: []k8s.ReplacementVolumeImpact{v}}
	currentJSON, _ := json.Marshal(original)
	var current k8s.ReplacementImpactReport
	json.Unmarshal(currentJSON, &current)
	current.Volumes[0].Pod = ""
	current.Volumes[0].Volume = "unmounted:pv"
	current.Volumes[0].Kind = "unmounted-pv"
	current.Volumes[0].Attachments[0].Attached = false
	current.Volumes[0].Attachments[0].Deleting = false
	if !replacementImpactSubset(current, original) {
		t.Fatal("same PV after drain treated as unreviewed data")
	}
	if !original.Volumes[0].Attachments[0].Attached || !original.Volumes[0].Attachments[0].Deleting {
		t.Fatal("normalization changed reviewed fencing evidence")
	}
	current.Volumes[0].PVCUID = "new-claim"
	if replacementImpactSubset(current, original) {
		t.Fatal("rebound PVC accepted as reviewed")
	}
}

type replacementFailCreateStore struct {
	ProvisionStore
	fail bool
}

func (s *replacementFailCreateStore) CreateSecret(ctx context.Context, scope, kind, id string, b []byte) error {
	if s.fail && kind == replacementKind {
		s.fail = false
		return errors.New("simulated successor publication failure")
	}
	return s.ProvisionStore.CreateSecret(ctx, scope, kind, id, b)
}
func TestReplacementResumeRecoversPendingSuccessorWithoutChangingIdentity(t *testing.T) {
	s, _, _, spec, _ := replacementFixture(t, "reachable")
	create := s.replacementChildRun
	s.replacementChildRun = func(ctx context.Context, e *jobs.Execution, r jobs.Request) error {
		if err := create(ctx, e, r); err != nil {
			return err
		}
		return jobs.ErrUncertain
	}
	ctx := context.Background()
	p, e := s.PlanReplacement(ctx, spec, "admin")
	if e != nil {
		t.Fatal(e)
	}
	request, _ := s.ApproveReplacement(ctx, p.ID, p.NodeName, p.ImpactHash, "admin", true)
	j := replacementRun(t, replacementManager(t, s), request)
	if j.Status != "interrupted" {
		t.Fatal(j)
	}
	s.Store = &replacementFailCreateStore{ProvisionStore: s.Store, fail: true}
	if _, e = s.PlanReplacementResume(ctx, p.ID, "admin"); e == nil {
		t.Fatal("expected publication failure")
	}
	original, e := s.loadReplacement(ctx, p.ID)
	if e != nil || original.SuccessorID == "" || len(original.SuccessorState) == 0 {
		t.Fatal("successor intent was lost")
	}
	next, e := s.PlanReplacementResume(ctx, p.ID, "admin")
	if e != nil || next.ID != original.SuccessorID {
		t.Fatalf("retry did not reconstruct same successor: %v", e)
	}
}

func TestReplacementOriginalEvidenceRequired(t *testing.T) {
	valid := replacementState{WorkerReplacementPlan: WorkerReplacementPlan{NodeUID: "old-node"}, OriginalImpact: k8s.ReplacementImpactReport{NodeUID: "old-node", Pods: []k8s.ReplacementPodImpact{{Namespace: "system", ControllerName: "network", ControllerKind: "DaemonSet", ControllerUID: "controller-uid"}}}}
	if err := validateReplacementOriginalImpact(&valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*replacementState)
	}{
		{"missing historical inventory", func(p *replacementState) { p.OriginalImpact = k8s.ReplacementImpactReport{} }},
		{"different node", func(p *replacementState) { p.OriginalImpact.NodeUID = "different" }},
		{"unknown inventory", func(p *replacementState) { p.OriginalImpact.Unknown = true }},
		{"missing controller identity", func(p *replacementState) { p.OriginalImpact.Pods[0].ControllerUID = "" }},
		{"unsupported job", func(p *replacementState) { p.OriginalImpact.Pods[0].ControllerKind = "Job" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p replacementState
			raw, _ := json.Marshal(valid)
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatal(err)
			}
			tc.change(&p)
			if err := validateReplacementOriginalImpact(&p); err == nil {
				t.Fatal("unsafe historical evidence accepted")
			}
		})
	}
}
