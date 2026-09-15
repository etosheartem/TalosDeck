package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
	"talosdeck/internal/reconcile"
)

type State struct {
	SafeMode          bool   `json:"safeMode"`
	AutomationPaused  bool   `json:"automationPaused"`
	ActivationEpoch   uint64 `json:"activationEpoch,omitempty"`
	AuthorityIdentity string `json:"authorityIdentity,omitempty"`
}
type JobReview struct {
	JobID  string `json:"jobId"`
	Reason string `json:"reason"`
}
type ActivationOptions struct {
	DataDir, KeyPath, Actor, FencingEvidence string
	ConfirmOldManagementFenced               bool
	Reviews                                  []JobReview
	Authority                                reconcile.Authority
	InstanceID, AuthorityIdentity            string
	Epoch                                    uint64
	PersistAuthorityEpoch                    func() error
}
type ActivationReceipt struct {
	Version           int         `json:"version"`
	ActivatedAt       time.Time   `json:"activatedAt"`
	SentinelSHA256    string      `json:"sentinelSHA256"`
	Actor             string      `json:"actor"`
	FencingEvidence   string      `json:"fencingEvidence"`
	Reviews           []JobReview `json:"reviews"`
	ReviewDigest      string      `json:"reviewDigest"`
	InstanceID        string      `json:"instanceId"`
	AuthorityIdentity string      `json:"authorityIdentity"`
	Epoch             uint64      `json:"epoch"`
	AutomationPaused  bool        `json:"automationPaused"`
}

func markerDigest(dataDir string) (string, error) {
	path := filepath.Join(dataDir, SafeModeFile)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return "", errors.New("invalid recovery sentinel")
	}
	return digestFile(path)
}
func ReadState(ctx context.Context, s *clusters.Store, dataDir string) (State, error) {
	state := State{SafeMode: true, AutomationPaused: true}
	required, requiredErr := s.GetSecret(ctx, "__fleet__", "recovery", "required")
	if requiredErr != nil && !errors.Is(requiredErr, clusters.ErrNotFound) {
		return state, requiredErr
	}
	digest, err := markerDigest(dataDir)
	if os.IsNotExist(err) {
		if errors.Is(requiredErr, clusters.ErrNotFound) {
			return State{}, nil
		}
		return state, nil // Removing a restored marker never unlocks encrypted state.
	}
	if err != nil {
		return state, err
	}
	if requiredErr != nil || !json.Valid(required) {
		return state, nil
	}
	anchor := sha256.Sum256(required)
	if digest != hex.EncodeToString(anchor[:]) {
		return state, nil
	}
	raw, err := s.GetSecret(ctx, "__fleet__", "recovery", "activation")
	if errors.Is(err, clusters.ErrNotFound) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	var receipt ActivationReceipt
	if json.Unmarshal(raw, &receipt) != nil {
		return state, errors.New("invalid encrypted activation receipt")
	}
	reviewJSON, _ := json.Marshal(receipt.Reviews)
	sum := sha256.Sum256(reviewJSON)
	if receipt.Version != 1 || receipt.SentinelSHA256 != digest || receipt.Epoch == 0 || receipt.AuthorityIdentity == "" || receipt.InstanceID == "" || receipt.Actor == "" || receipt.FencingEvidence == "" || receipt.ReviewDigest != hex.EncodeToString(sum[:]) || !receipt.AutomationPaused {
		return state, nil
	}
	state.SafeMode = false
	state.ActivationEpoch = receipt.Epoch
	state.AuthorityIdentity = receipt.AuthorityIdentity
	if automationResumed(ctx, s, digest, receipt.Epoch) {
		state.AutomationPaused = false
	}
	return state, nil
}

func boundedAttestation(s string, max int) bool {
	return strings.TrimSpace(s) == s && len(s) > 0 && len(s) <= max && !strings.ContainsAny(s, "\x00\r\n\t")
}

type activationJob struct {
	path   string
	job    jobs.Job
	review bool
}

// Activate is offline: the registry and every discovered journal lock stay held
// through the final encrypted receipt commit. It enables manual operations only.
func Activate(ctx context.Context, o ActivationOptions) (ActivationReceipt, error) {
	var receipt ActivationReceipt
	if !o.ConfirmOldManagementFenced || !boundedAttestation(o.Actor, 128) || !boundedAttestation(o.FencingEvidence, 4096) || o.Authority == nil || o.Epoch == 0 || o.InstanceID == "" || o.AuthorityIdentity == "" || o.PersistAuthorityEpoch == nil {
		return receipt, errors.New("explicit actor, fencing attestation and independent execution authority required")
	}
	digest, err := markerDigest(o.DataDir)
	if err != nil {
		return receipt, err
	}
	if _, err = os.Stat(o.KeyPath); err != nil {
		return receipt, errors.New("existing separate encryption key required")
	}
	if _, err = os.Stat(filepath.Join(o.DataDir, "talosdeck.db")); err != nil {
		return receipt, err
	}
	store, err := clusters.Open(filepath.Join(o.DataDir, "talosdeck.db"), o.KeyPath)
	if err != nil {
		return receipt, errors.New("stop TalosDeck before activation; registry must decrypt successfully")
	}
	defer store.Close()
	required, anchorErr := store.GetSecret(ctx, "__fleet__", "recovery", "required")
	anchor := sha256.Sum256(required)
	if anchorErr != nil || !json.Valid(required) || hex.EncodeToString(anchor[:]) != digest {
		return receipt, errors.New("encrypted recovery anchor is missing or differs from sentinel; restore a verified backup")
	}
	if err = o.Authority.Validate(ctx, o.InstanceID, o.Epoch); err != nil {
		return receipt, errors.New("independent execution authority unavailable")
	}
	if err = o.PersistAuthorityEpoch(); err != nil {
		return receipt, err
	}
	current, err := ReadState(ctx, store, o.DataDir)
	if err != nil {
		return receipt, err
	}
	if !current.SafeMode {
		return receipt, errors.New("this recovery has already been activated")
	}
	reviews := map[string]string{}
	for _, r := range o.Reviews {
		if _, e := uuid.Parse(r.JobID); e != nil || !boundedAttestation(r.Reason, 4096) || reviews[r.JobID] != "" {
			return receipt, errors.New("invalid or duplicate job review")
		}
		reviews[r.JobID] = r.Reason
	}
	var locks []*os.File
	defer func() {
		for _, lock := range locks {
			_ = lock.Close()
		}
	}()
	journalDirs := map[string]bool{}
	err = filepath.WalkDir(o.DataDir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("symlinks unsupported during offline activation")
		}
		if !d.IsDir() && d.Name() == ".lock" {
			journalDirs[filepath.Dir(path)] = true
		}
		if d.IsDir() && (d.Name() == "jobs" || d.Name() == "fleet-jobs") {
			journalDirs[path] = true
		}
		return nil
	})
	if err != nil {
		return receipt, err
	}
	dirs := make([]string, 0, len(journalDirs))
	for dir := range journalDirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		f, e := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return receipt, e
		}
		if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			f.Close()
			return receipt, errors.New("stop every job executor before activation")
		}
		locks = append(locks, f)
	}
	var records []activationJob
	seen := map[string]bool{}
	for _, dir := range dirs {
		files, e := filepath.Glob(filepath.Join(dir, "*.json"))
		if e != nil {
			return receipt, e
		}
		for _, path := range files {
			raw, e := os.ReadFile(path)
			if e != nil {
				return receipt, e
			}
			var j jobs.Job
			if len(raw) > 16<<20 || json.Unmarshal(raw, &j) != nil {
				return receipt, errors.New("invalid job journal")
			}
			if _, e = uuid.Parse(j.ID); e != nil || filepath.Base(path) != j.ID+".json" || seen[j.ID] {
				return receipt, errors.New("invalid or duplicate job identity")
			}
			seen[j.ID] = true
			switch j.Status {
			case "queued", "running", "interrupted", "failed", "succeeded", "stopped":
			default:
				return receipt, errors.New("invalid persisted job status")
			}
			needs := j.Status == "running" || j.Status == "queued" || j.Status == "interrupted" && !j.Reviewed || j.ReconciliationOutcome == reconcile.Unknown || j.ReconciliationOutcome == reconcile.RequiresReview || j.WorkflowVersion != 1 || j.PlanVersion != 1 || j.StepSchemaVersion != 1
			for _, i := range j.Intents {
				needs = needs || !i.Compatible() || i.Outcome != "succeeded"
			}
			if needs && reviews[j.ID] == "" {
				return receipt, errors.New("each unresolved or incompatible job requires an explicit review reason")
			}
			records = append(records, activationJob{path: path, job: j, review: reviews[j.ID] != ""})
		}
	}
	for id := range reviews {
		if !seen[id] {
			return receipt, errors.New("review references an unknown job")
		}
	}
	if err = o.Authority.Validate(ctx, o.InstanceID, o.Epoch); err != nil {
		return receipt, errors.New("independent execution authority unavailable")
	}
	now := time.Now().UTC()
	for _, record := range records {
		if !record.review {
			continue
		}
		j := record.job
		j.Reviewed = true
		j.UpdatedAt = now
		if j.Status == "running" || j.Status == "queued" {
			j.Status = "interrupted"
			j.ReconciliationOutcome = reconcile.Unknown
		}
		j.Events = append(j.Events, jobs.Event{Time: now, Step: "recovery-reviewed", Message: "Offline recovery review by " + o.Actor + "; outcome retained, no command resumed"})
		if len(j.Events) > jobs.MaxEvents {
			j.Events = j.Events[len(j.Events)-jobs.MaxEvents:]
		}
		raw, e := json.Marshal(j)
		if e != nil {
			return receipt, e
		}
		if e = atomicActivationWrite(record.path, raw); e != nil {
			return receipt, e
		}
	}
	if err = o.Authority.Validate(ctx, o.InstanceID, o.Epoch); err != nil {
		return receipt, errors.New("execution authority lost before activation commit")
	}
	ordered := append([]JobReview{}, o.Reviews...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].JobID < ordered[j].JobID })
	raw, _ := json.Marshal(ordered)
	sum := sha256.Sum256(raw)
	receipt = ActivationReceipt{Version: 1, ActivatedAt: now, SentinelSHA256: digest, Actor: o.Actor, FencingEvidence: o.FencingEvidence, Reviews: ordered, ReviewDigest: hex.EncodeToString(sum[:]), InstanceID: o.InstanceID, AuthorityIdentity: o.AuthorityIdentity, Epoch: o.Epoch, AutomationPaused: true}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return receipt, err
	}
	if err = store.PutSecret(ctx, "__fleet__", "recovery", "activation", encoded); err != nil {
		return receipt, err
	}
	return receipt, nil
}
func atomicActivationWrite(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".recovery-review-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
