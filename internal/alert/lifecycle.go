package alert

import (
	"errors"
	"strings"
	"time"
)

var ErrTransition = errors.New("alert: invalid or stale lifecycle transition")

type State string

const (
	Firing       State = "firing"
	Acknowledged State = "acknowledged"
	Resolved     State = "resolved"
	Superseded   State = "superseded"
	Disabled     State = "disabled"
)

type Action string

const (
	Unchanged         Action = "unchanged"
	Create            Action = "create"
	AdvanceEvidence   Action = "advance_evidence"
	Resolve           Action = "resolve"
	AcknowledgeAction Action = "acknowledge"
	Terminate         Action = "terminate"
)

type Cause string

const (
	ConditionMatched               Cause = "condition_matched"
	ConditionsCleared              Cause = "conditions_cleared"
	ResolvedAfterMaintenanceChange Cause = "resolved_after_maintenance_change"
	EvidenceUpdated                Cause = "evidence_updated"
	Acknowledgement                Cause = "acknowledged"
	PolicyDisabled                 Cause = "policy_disabled"
	DefinitionSuperseded           Cause = "definition_superseded"
	SLOArchived                    Cause = "slo_archived"
)

type Occurrence struct {
	ID, PolicyID                          string
	Revision                              int64
	State                                 State
	FiringSnapshot, LatestSnapshot        SnapshotIdentity
	LatestBreachingSnapshot               SnapshotIdentity
	OpenedDetectedAt, UpdatedAt, ClosedAt time.Time
	AcknowledgedBy                        string
	AcknowledgedAt                        time.Time
}

type TransitionResult struct {
	Action                  Action
	Next                    *Occurrence
	From                    State
	Cause                   Cause
	Actor, Reason           string
	EffectiveAt, DetectedAt time.Time
}

func active(current Occurrence) bool {
	if current.ID == "" || current.PolicyID == "" || current.Revision <= 0 || current.FiringSnapshot.Cutoff.IsZero() || current.LatestSnapshot.Cutoff.Before(current.FiringSnapshot.Cutoff) || current.OpenedDetectedAt.IsZero() || current.UpdatedAt.Before(current.OpenedDetectedAt) || !current.ClosedAt.IsZero() {
		return false
	}
	switch current.State {
	case Firing:
		return current.AcknowledgedBy == "" && current.AcknowledgedAt.IsZero()
	case Acknowledged:
		return strings.TrimSpace(current.AcknowledgedBy) != "" && !current.AcknowledgedAt.Before(current.OpenedDetectedAt) && !current.AcknowledgedAt.After(current.UpdatedAt)
	default:
		return false
	}
}

// Reduce consumes a validated Evaluate result. It proposes writes; storage must
// still fence the current policy, source, exclusion, claim and occurrence version.
// current is the open occurrence only. Terminal history is never passed for reuse.
func Reduce(current *Occurrence, evaluation Evaluation, detectedAt time.Time) (TransitionResult, error) {
	if evaluation.PolicyID == "" || evaluation.Revision <= 0 || evaluation.Snapshot.Cutoff.IsZero() || detectedAt.IsZero() || detectedAt.Before(evaluation.Snapshot.Cutoff) {
		return TransitionResult{}, ErrTransition
	}
	switch evaluation.Truth {
	case Breaching, Clear, Indeterminate:
	default:
		return TransitionResult{}, ErrTransition
	}
	if current == nil {
		if evaluation.Truth != Breaching {
			return TransitionResult{Action: Unchanged}, nil
		}
		return TransitionResult{Action: Create, Cause: ConditionMatched, EffectiveAt: evaluation.Snapshot.Cutoff, DetectedAt: detectedAt,
			Next: &Occurrence{PolicyID: evaluation.PolicyID, Revision: evaluation.Revision, State: Firing, FiringSnapshot: evaluation.Snapshot, LatestSnapshot: evaluation.Snapshot, LatestBreachingSnapshot: evaluation.Snapshot, OpenedDetectedAt: detectedAt, UpdatedAt: detectedAt}}, nil
	}
	if !active(*current) || current.PolicyID != evaluation.PolicyID || current.Revision != evaluation.Revision || detectedAt.Before(current.UpdatedAt) || !evaluation.Snapshot.Cutoff.After(current.LatestSnapshot.Cutoff) {
		return TransitionResult{}, ErrTransition
	}
	previous, nextSnapshot := current.LatestSnapshot, evaluation.Snapshot
	if previous.SLOID != nextSnapshot.SLOID || previous.SLORevision != nextSnapshot.SLORevision || previous.SLIRevision != nextSnapshot.SLIRevision || previous.CalculationVersion != nextSnapshot.CalculationVersion {
		return TransitionResult{}, ErrTransition
	}
	next := *current
	next.LatestSnapshot = evaluation.Snapshot
	next.UpdatedAt = detectedAt
	if evaluation.Truth == Breaching {
		next.LatestBreachingSnapshot = evaluation.Snapshot
	}
	result := TransitionResult{Action: AdvanceEvidence, Next: &next, From: current.State, Cause: EvidenceUpdated, EffectiveAt: evaluation.Snapshot.Cutoff, DetectedAt: detectedAt}
	if evaluation.Truth == Clear {
		cause := ConditionsCleared
		if current.LatestBreachingSnapshot.ExclusionRevision != evaluation.Snapshot.ExclusionRevision {
			comparison := evaluation.RecoveryComparison
			expected := evaluation.Snapshot
			expected.ExclusionRevision = current.LatestBreachingSnapshot.ExclusionRevision
			if comparison == nil || !sameSnapshot(expected, comparison.Snapshot) {
				return TransitionResult{}, ErrTransition
			}
			switch comparison.Truth {
			case Breaching, Indeterminate:
				cause = ResolvedAfterMaintenanceChange
			case Clear:
			default:
				return TransitionResult{}, ErrTransition
			}
		}
		next.State = Resolved
		next.ClosedAt = evaluation.Snapshot.Cutoff
		result.Action = Resolve
		result.Cause = cause
	}
	return result, nil
}

func Acknowledge(current Occurrence, actor string, at time.Time) (TransitionResult, error) {
	if !active(current) || strings.TrimSpace(actor) == "" || at.IsZero() {
		return TransitionResult{}, ErrTransition
	}
	if current.State == Acknowledged {
		return TransitionResult{Action: Unchanged, Next: new(current)}, nil
	}
	if at.Before(current.UpdatedAt) {
		return TransitionResult{}, ErrTransition
	}
	next := current
	next.State = Acknowledged
	next.AcknowledgedBy = actor
	next.AcknowledgedAt = at
	next.UpdatedAt = at
	return TransitionResult{Action: AcknowledgeAction, Next: &next, From: current.State, Cause: Acknowledgement, Actor: actor, EffectiveAt: at, DetectedAt: at}, nil
}

// ApplyAdministrativeChange has only the named terminal causes. There is no
// manual-close action. Storage owns activation timing, audit and cancelled ranges.
func ApplyAdministrativeChange(current *Occurrence, cause Cause, actor, reason string, effectiveAt, detectedAt time.Time) (TransitionResult, error) {
	var state State
	switch cause {
	case PolicyDisabled, SLOArchived:
		state = Disabled
	case DefinitionSuperseded:
		state = Superseded
	default:
		return TransitionResult{}, ErrTransition
	}
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" || effectiveAt.IsZero() || detectedAt.IsZero() || detectedAt.Before(effectiveAt) {
		return TransitionResult{}, ErrTransition
	}
	if current == nil {
		return TransitionResult{Action: Unchanged}, nil
	}
	if !active(*current) || detectedAt.Before(current.UpdatedAt) || effectiveAt.Before(current.LatestSnapshot.Cutoff) {
		return TransitionResult{}, ErrTransition
	}
	next := *current
	next.State = state
	next.ClosedAt = effectiveAt
	next.UpdatedAt = detectedAt
	return TransitionResult{Action: Terminate, Next: &next, From: current.State, Cause: cause, Actor: actor, Reason: reason, EffectiveAt: effectiveAt, DetectedAt: detectedAt}, nil
}
