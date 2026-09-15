package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"talosdeck/internal/clusters"
)

// ResumeReceipt is the explicit operator decision to let schedules, collectors and
// background deliveries start again after a restore has been activated and its
// interrupted work reviewed. It authorizes nothing by itself: the decision is bound
// to this restore's sentinel and activation epoch, so an older copy replayed into a
// new restore does not resume anything.
type ResumeReceipt struct {
	Version         int       `json:"version"`
	ResumedAt       time.Time `json:"resumedAt"`
	SentinelSHA256  string    `json:"sentinelSHA256"`
	ActivationEpoch uint64    `json:"activationEpoch"`
	Actor           string    `json:"actor"`
	Reason          string    `json:"reason"`
}

// ResumeAutomation records the decision. It never starts anything in the running
// process: automation is wired at startup, so the decision takes effect on the next
// start and the caller must say so.
func ResumeAutomation(ctx context.Context, s *clusters.Store, dataDir, actor, reason string) (ResumeReceipt, error) {
	var receipt ResumeReceipt
	if !boundedAttestation(actor, 128) || !boundedAttestation(reason, 4096) {
		return receipt, errors.New("explicit actor and reason required")
	}
	state, err := ReadState(ctx, s, dataDir)
	if err != nil {
		return receipt, err
	}
	if state.SafeMode {
		return receipt, errors.New("activate the restored management plane and review interrupted work first")
	}
	if !state.AutomationPaused {
		return receipt, errors.New("automation is not paused")
	}
	digest, err := markerDigest(dataDir)
	if err != nil {
		return receipt, err
	}
	receipt = ResumeReceipt{Version: 1, ResumedAt: time.Now().UTC(), SentinelSHA256: digest, ActivationEpoch: state.ActivationEpoch, Actor: actor, Reason: reason}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return receipt, err
	}
	if err = s.PutSecret(ctx, "__fleet__", "recovery", "automation", encoded); err != nil {
		return receipt, err
	}
	return receipt, nil
}

// automationResumed reports whether a resume decision matches this exact restore.
func automationResumed(ctx context.Context, s *clusters.Store, sentinel string, epoch uint64) bool {
	raw, err := s.GetSecret(ctx, "__fleet__", "recovery", "automation")
	if err != nil {
		return false
	}
	var receipt ResumeReceipt
	if json.Unmarshal(raw, &receipt) != nil {
		return false
	}
	return receipt.Version == 1 && receipt.SentinelSHA256 == sentinel && receipt.ActivationEpoch == epoch && epoch != 0 &&
		boundedAttestation(receipt.Actor, 128) && boundedAttestation(receipt.Reason, 4096) && !receipt.ResumedAt.IsZero()
}
