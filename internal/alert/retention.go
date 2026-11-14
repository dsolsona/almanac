package alert

import (
	"errors"
	"slices"
	"strings"
	"time"
)

var ErrCompaction = errors.New("alert: invalid compaction input")

type RetentionEntry struct {
	ID                       string
	Evaluation               Evaluation
	ScheduledAt, CommittedAt time.Time
	Pinned                   bool
}

// ConditionCompactionSummary describes unchanged outcomes, not numerical values
// at omitted cutoffs. Exact original/transition evidence remains pinned by storage.
type ConditionCompactionSummary struct {
	ID, Threshold               string
	Outcome                     ConditionOutcome
	ShortDuration, LongDuration time.Duration
	ShortQuality, LongQuality   Quality
	ShortReason, LongReason     string
}

type CompactionRun struct {
	EvaluationIDs []string
	Start, End    time.Time
	First, Last   SnapshotIdentity
	Truth         Truth
	Conditions    []ConditionCompactionSummary
}

// PlanCompaction consumes a caller-bounded batch ordered by policy ID, revision,
// then scheduled boundary. It never sorts or omits barriers before planning.
// Storage must supply all intervening entries (including pins), fence their
// identities, and atomically replace selected evaluations with the returned runs.
// Evaluations come from validated canonical decisions; this planner checks their
// structural identities/outcomes, not their numeric calculation or recovery proof.
// Start/End cover scheduled boundaries in [Start, End), at one-minute spacing.
// First/Last preserve actual snapshot provenance; no middle counts are retained.
func PlanCompaction(entries []RetentionEntry, before time.Time) ([]CompactionRun, error) {
	if before.IsZero() {
		return nil, ErrCompaction
	}
	var runs []CompactionRun
	var run CompactionRun
	seen := make(map[string]struct{}, len(entries))
	flush := func() {
		if len(run.EvaluationIDs) >= 2 {
			runs = append(runs, run)
		}
		run = CompactionRun{}
	}
	for index, entry := range entries {
		summary, err := compactionSummary(entry)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return nil, ErrCompaction
		}
		seen[entry.ID] = struct{}{}
		if index > 0 && !compactionOrdered(entries[index-1], entry) {
			return nil, ErrCompaction
		}
		if entry.Pinned || !entry.ScheduledAt.Before(before) || !entry.CommittedAt.Before(before) {
			flush()
			continue
		}
		evaluation := entry.Evaluation
		if len(run.EvaluationIDs) > 0 {
			previous := entries[index-1].Evaluation
			if evaluation.PolicyID != previous.PolicyID || evaluation.Revision != previous.Revision || !entry.ScheduledAt.Equal(run.End) || !evaluation.Snapshot.Cutoff.After(run.Last.Cutoff) || evaluation.Snapshot.SourceGeneration < run.Last.SourceGeneration || !compactionMeaning(run.First, evaluation.Snapshot) || evaluation.Truth != run.Truth || !slices.Equal(summary, run.Conditions) {
				flush()
			}
		}
		if len(run.EvaluationIDs) == 0 {
			run = CompactionRun{Start: entry.ScheduledAt, First: evaluation.Snapshot, Truth: evaluation.Truth, Conditions: summary}
		}
		run.EvaluationIDs = append(run.EvaluationIDs, entry.ID)
		run.End = entry.ScheduledAt.Add(time.Minute)
		run.Last = evaluation.Snapshot
	}
	flush()
	return runs, nil
}

func compactionOrdered(previous, next RetentionEntry) bool {
	a, b := previous.Evaluation, next.Evaluation
	if a.PolicyID != b.PolicyID {
		return a.PolicyID < b.PolicyID
	}
	if a.Revision != b.Revision {
		return a.Revision < b.Revision
	}
	return previous.ScheduledAt.Before(next.ScheduledAt)
}

func compactionMeaning(a, b SnapshotIdentity) bool {
	return a.SLOID == b.SLOID && a.SLORevision == b.SLORevision && a.SLIRevision == b.SLIRevision && a.ExclusionRevision == b.ExclusionRevision && a.CalculationVersion == b.CalculationVersion
}

func compactionSummary(entry RetentionEntry) ([]ConditionCompactionSummary, error) {
	evaluation := entry.Evaluation
	identity := evaluation.Snapshot
	if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(evaluation.PolicyID) == "" || evaluation.Revision <= 0 || strings.TrimSpace(identity.SLOID) == "" || identity.SLORevision <= 0 || identity.SLIRevision <= 0 || identity.SourceGeneration < 0 || identity.ExclusionRevision < 0 || strings.TrimSpace(identity.CalculationVersion) == "" || identity.Cutoff.IsZero() || identity.Cutoff.Nanosecond() != 0 || entry.ScheduledAt.IsZero() || entry.ScheduledAt.Nanosecond() != 0 || entry.ScheduledAt.Unix()%60 != 0 || identity.Cutoff.After(entry.ScheduledAt) || entry.CommittedAt.IsZero() || entry.CommittedAt.Nanosecond()%1000 != 0 || entry.CommittedAt.Before(identity.Cutoff) || len(evaluation.Conditions) == 0 {
		return nil, ErrCompaction
	}
	seen := make(map[string]struct{}, len(evaluation.Conditions))
	summary := make([]ConditionCompactionSummary, 0, len(evaluation.Conditions))
	truth := Clear
	for _, condition := range evaluation.Conditions {
		if strings.TrimSpace(condition.ID) == "" {
			return nil, ErrCompaction
		}
		if _, duplicate := seen[condition.ID]; duplicate {
			return nil, ErrCompaction
		}
		seen[condition.ID] = struct{}{}
		threshold, valid := decimal(condition.Threshold, len(condition.Threshold))
		if !valid || threshold.Sign() <= 0 {
			return nil, ErrCompaction
		}
		for _, window := range []WindowEvidence{condition.Short, condition.Long} {
			duration := window.End.Sub(window.Start)
			if window.Snapshot != identity || !window.End.Equal(identity.Cutoff) || window.Start.IsZero() || duration <= 0 || duration > 30*24*time.Hour || duration%time.Minute != 0 {
				return nil, ErrCompaction
			}
			switch window.Quality {
			case Complete, Missing, Partial, Stale, ZeroTraffic, Warmup, Expired, Excluded, CalculationFailed:
			default:
				return nil, ErrCompaction
			}
		}
		short, long := condition.Short.End.Sub(condition.Short.Start), condition.Long.End.Sub(condition.Long.Start)
		if short > long {
			return nil, ErrCompaction
		}
		complete := condition.Short.Quality == Complete && condition.Long.Quality == Complete
		switch condition.Outcome {
		case Matching:
			if !complete {
				return nil, ErrCompaction
			}
			truth = Breaching
		case NotMatching:
			if !complete {
				return nil, ErrCompaction
			}
		case Unavailable:
			if complete {
				return nil, ErrCompaction
			}
			if truth != Breaching {
				truth = Indeterminate
			}
		default:
			return nil, ErrCompaction
		}
		summary = append(summary, ConditionCompactionSummary{ID: condition.ID, Threshold: condition.Threshold, Outcome: condition.Outcome, ShortDuration: short, LongDuration: long, ShortQuality: condition.Short.Quality, LongQuality: condition.Long.Quality, ShortReason: condition.Short.Reason, LongReason: condition.Long.Reason})
	}
	if evaluation.Truth != truth {
		return nil, ErrCompaction
	}
	return summary, nil
}
