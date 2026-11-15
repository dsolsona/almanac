// Package alert contains deterministic alert decisions. Storage owns scheduling,
// canonical calculation, snapshot fencing and atomic persistence; none run here.
package alert

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

var (
	ErrPolicy   = errors.New("alert: invalid policy")
	ErrEvidence = errors.New("alert: inconsistent evaluation evidence")
)

type Condition struct {
	ID          string
	Short, Long time.Duration
	Threshold   string
}

type PolicyRevision struct {
	ID         string
	Revision   int64
	Conditions []Condition
}

type Objective struct {
	Target                                                    string
	Cadence                                                   time.Duration
	DefinitionValidFrom, MeasurementValidFrom, RetentionStart time.Time
}

// Limits are mandatory caller admission limits, not public product defaults.
// Aggregate count strings can be larger than an individual observation's count.
type Limits struct{ MaxConditions, MaxDecimalBytes int }

type SnapshotIdentity struct {
	SLOID                                                         string
	SLORevision, SLIRevision, SourceGeneration, ExclusionRevision int64
	CalculationVersion                                            string
	Cutoff                                                        time.Time
}

type Quality string

const (
	Complete          Quality = "complete"
	Missing           Quality = "missing"
	Partial           Quality = "partial"
	Stale             Quality = "stale"
	ZeroTraffic       Quality = "zero_traffic"
	Warmup            Quality = "warmup"
	Expired           Quality = "expired"
	Excluded          Quality = "excluded"
	CalculationFailed Quality = "calculation_failed"
)

// WindowEvidence uses requested full bounds. Quality comes from canonical
// coverage/freshness calculation; pending work is not a valid quality value.
type WindowEvidence struct {
	Snapshot            SnapshotIdentity
	Start, End          time.Time
	Quality             Quality
	Good, Total, Reason string
}

type Input struct {
	Policy           PolicyRevision
	Objective        Objective
	Snapshot         SnapshotIdentity
	Windows          []WindowEvidence
	RecoveryBaseline *RecoveryInput
}

// RecoveryInput evaluates the same present facts under the last breaching
// exclusion snapshot. A changed revision alone cannot prove recovery causality.
type RecoveryInput struct {
	ExclusionRevision int64
	Windows           []WindowEvidence
}

type RecoveryComparison struct {
	Snapshot   SnapshotIdentity
	Truth      Truth
	Conditions []ConditionResult
}

type Truth string

const (
	Breaching     Truth = "breaching"
	Clear         Truth = "clear"
	Indeterminate Truth = "indeterminate"
)

type ConditionOutcome string

const (
	Matching    ConditionOutcome = "matching"
	NotMatching ConditionOutcome = "not_matching"
	Unavailable ConditionOutcome = "unavailable"
)

type ConditionResult struct {
	ID, Threshold string
	Outcome       ConditionOutcome
	Short, Long   WindowEvidence
}

type Evaluation struct {
	PolicyID           string
	Revision           int64
	Snapshot           SnapshotIdentity
	Truth              Truth
	Conditions         []ConditionResult
	RecoveryComparison *RecoveryComparison
}

func decimal(text string, limit int) (*big.Rat, bool) {
	if text == "" || len(text) > limit {
		return nil, false
	}
	whole, fraction, point := strings.Cut(text, ".")
	if whole == "" || point && fraction == "" {
		return nil, false
	}
	for _, part := range []string{whole, fraction} {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return nil, false
			}
		}
	}
	// The digit/point check excludes fraction, exponent and base-prefix syntax.
	value, ok := new(big.Rat).SetString(text)
	return value, ok
}

func Validate(policy PolicyRevision, objective Objective, limits Limits) error {
	invalid := func(reason string) error { return fmt.Errorf("%w: %s", ErrPolicy, reason) }
	if limits.MaxConditions <= 0 || limits.MaxDecimalBytes <= 0 || len(policy.Conditions) == 0 || len(policy.Conditions) > limits.MaxConditions {
		return invalid("condition admission")
	}
	if policy.ID == "" || policy.Revision <= 0 {
		return invalid("revision identity")
	}
	target, ok := decimal(objective.Target, limits.MaxDecimalBytes)
	if !ok || target.Sign() < 0 || target.Cmp(big.NewRat(1, 1)) >= 0 {
		return invalid("target")
	}
	if objective.Cadence <= 0 || objective.Cadence%time.Second != 0 {
		return invalid("cadence")
	}
	if objective.DefinitionValidFrom.IsZero() || objective.MeasurementValidFrom.IsZero() || objective.RetentionStart.IsZero() {
		return invalid("validity bounds")
	}
	allowance := new(big.Rat).Sub(big.NewRat(1, 1), target)
	seen := make(map[string]bool, len(policy.Conditions))
	for _, condition := range policy.Conditions {
		if strings.TrimSpace(condition.ID) == "" || seen[condition.ID] {
			return invalid("condition identity")
		}
		seen[condition.ID] = true
		if condition.Short <= 0 || condition.Long < condition.Short || condition.Long > 30*24*time.Hour {
			return invalid("window bounds")
		}
		for _, duration := range []time.Duration{condition.Short, condition.Long} {
			if duration%time.Minute != 0 || duration%objective.Cadence != 0 {
				return invalid("window cadence")
			}
		}
		threshold, ok := decimal(condition.Threshold, limits.MaxDecimalBytes)
		if !ok || threshold.Sign() <= 0 || new(big.Rat).Mul(threshold, allowance).Cmp(big.NewRat(1, 1)) > 0 {
			return invalid("unreachable or invalid threshold")
		}
	}
	return nil
}

func sameSnapshot(a, b SnapshotIdentity) bool {
	return a.SLOID == b.SLOID && a.SLORevision == b.SLORevision && a.SLIRevision == b.SLIRevision && a.SourceGeneration == b.SourceGeneration && a.ExclusionRevision == b.ExclusionRevision && a.CalculationVersion == b.CalculationVersion && a.Cutoff.Equal(b.Cutoff)
}

func window(input Input, evidence WindowEvidence, limits Limits) (WindowEvidence, error) {
	invalid := func() (WindowEvidence, error) { return WindowEvidence{}, ErrEvidence }
	if !sameSnapshot(input.Snapshot, evidence.Snapshot) || !evidence.End.Equal(input.Snapshot.Cutoff) || !evidence.Start.Before(evidence.End) {
		return invalid()
	}
	duration := evidence.End.Sub(evidence.Start)
	if duration > 30*24*time.Hour || duration%time.Minute != 0 || duration%input.Objective.Cadence != 0 {
		return invalid()
	}
	switch evidence.Quality {
	case Complete, Missing, Partial, Stale, ZeroTraffic, Warmup, Expired, Excluded, CalculationFailed:
	default:
		return invalid()
	}
	if evidence.Good != "" || evidence.Total != "" || evidence.Quality == Complete {
		good, goodOK := decimal(evidence.Good, limits.MaxDecimalBytes)
		total, totalOK := decimal(evidence.Total, limits.MaxDecimalBytes)
		if !goodOK || !totalOK || good.Cmp(total) > 0 {
			return invalid()
		}
		if evidence.Quality == Complete && total.Sign() == 0 {
			evidence.Quality = ZeroTraffic
			evidence.Reason = "zero_traffic"
		}
	}
	// Current-status windows may be shortened by canonical validity/retention.
	// Alert evidence requires the entire requested population to be eligible.
	if evidence.Start.Before(input.Objective.DefinitionValidFrom) || evidence.Start.Before(input.Objective.MeasurementValidFrom) {
		evidence.Quality = Warmup
		evidence.Reason = "full_window_warmup"
	}
	if evidence.Start.Before(input.Objective.RetentionStart) {
		evidence.Quality = Expired
		evidence.Reason = "full_window_not_retained"
	}
	return evidence, nil
}

func Evaluate(input Input, limits Limits) (Evaluation, error) {
	if baseline := input.RecoveryBaseline; baseline != nil {
		if baseline.ExclusionRevision < 0 || baseline.ExclusionRevision == input.Snapshot.ExclusionRevision {
			return Evaluation{}, ErrEvidence
		}
		input.RecoveryBaseline = nil
		current, err := Evaluate(input, limits)
		if err != nil {
			return Evaluation{}, err
		}
		if current.Truth != Clear {
			return Evaluation{}, ErrEvidence
		}
		input.Snapshot.ExclusionRevision = baseline.ExclusionRevision
		input.Windows = baseline.Windows
		previous, err := Evaluate(input, limits)
		if err != nil {
			return Evaluation{}, err
		}
		current.RecoveryComparison = &RecoveryComparison{Snapshot: previous.Snapshot, Truth: previous.Truth, Conditions: previous.Conditions}
		return current, nil
	}
	if err := Validate(input.Policy, input.Objective, limits); err != nil {
		return Evaluation{}, err
	}
	id := input.Snapshot
	if id.SLOID == "" || id.SLORevision <= 0 || id.SLIRevision <= 0 || id.SourceGeneration < 0 || id.ExclusionRevision < 0 || strings.TrimSpace(id.CalculationVersion) == "" || id.Cutoff.IsZero() || id.Cutoff.Nanosecond() != 0 || id.Cutoff.Unix()%int64(input.Objective.Cadence/time.Second) != 0 {
		return Evaluation{}, ErrEvidence
	}
	windows := make(map[time.Duration]WindowEvidence, len(input.Windows))
	for _, raw := range input.Windows {
		evidence, err := window(input, raw, limits)
		if err != nil {
			return Evaluation{}, err
		}
		duration := evidence.End.Sub(evidence.Start)
		if _, exists := windows[duration]; exists {
			return Evaluation{}, ErrEvidence
		}
		windows[duration] = evidence
	}
	target, _ := decimal(input.Objective.Target, limits.MaxDecimalBytes)
	allowance := new(big.Rat).Sub(big.NewRat(1, 1), target)
	result := Evaluation{PolicyID: input.Policy.ID, Revision: input.Policy.Revision, Snapshot: input.Snapshot, Truth: Clear, Conditions: make([]ConditionResult, 0, len(input.Policy.Conditions))}
	for _, condition := range input.Policy.Conditions {
		short, shortOK := windows[condition.Short]
		long, longOK := windows[condition.Long]
		if !shortOK || !longOK {
			return Evaluation{}, ErrEvidence
		}
		outcome := Unavailable
		if short.Quality == Complete && long.Quality == Complete {
			threshold, _ := decimal(condition.Threshold, limits.MaxDecimalBytes)
			matches := func(evidence WindowEvidence) bool {
				good, _ := decimal(evidence.Good, limits.MaxDecimalBytes)
				total, _ := decimal(evidence.Total, limits.MaxDecimalBytes)
				bad := new(big.Rat).Sub(total, good)
				right := new(big.Rat).Mul(threshold, total)
				right.Mul(right, allowance)
				return bad.Cmp(right) >= 0
			}
			outcome = NotMatching
			if matches(short) && matches(long) {
				outcome = Matching
			}
		}
		result.Conditions = append(result.Conditions, ConditionResult{ID: condition.ID, Threshold: condition.Threshold, Outcome: outcome, Short: short, Long: long})
		if outcome == Matching {
			result.Truth = Breaching
		} else if outcome == Unavailable && result.Truth != Breaching {
			result.Truth = Indeterminate
		}
	}
	return result, nil
}
