package slo

import (
	"fmt"
	"slices"
	"time"
)

func validateCoverageInput(input WindowInput) error {
	if len(input.Exclusions) != 0 || len(input.Observations) != 0 {
		return fmt.Errorf("%w: compiled coverage cannot be mixed with a raw mask or observations", ErrOperationalFacts)
	}
	if input.Cadence < time.Microsecond || input.Cadence%time.Microsecond != 0 || input.Duration <= 0 || input.Duration%input.Cadence != 0 || !alignedExclusionInstant(input.At, input.Cadence) || !coverageTime(input.At.Add(-input.Duration)) {
		return ErrInvalidCalculation
	}
	for _, at := range []time.Time{input.ValidFrom, input.SliValidFrom, input.RetentionStart} {
		if !at.IsZero() && !coverageTime(at) {
			return ErrInvalidCalculation
		}
	}
	return nil
}

// RequiredCoverageAnchors returns the sorted distinct boundaries needed to
// certify one compiled-mask window. It shares validity/retention rounding with
// the evaluator, owns its output, and does not certify totals or mask identity.
// LatestEligibleEnd is the global endpoint and can precede the requested span.
func RequiredCoverageAnchors(input WindowInput, totals WindowTotals, latest time.Time) ([]time.Time, error) {
	if err := validateCoverageInput(input); err != nil {
		return nil, err
	}
	invalid := func() ([]time.Time, error) {
		return nil, fmt.Errorf("%w: invalid compiled coverage anchors", ErrOperationalFacts)
	}
	if !alignedExclusionInstant(latest, input.Cadence) || latest.After(input.At) || totals.Observed < 0 {
		return invalid()
	}
	_, eligible, retention := windowBounds(input, input.At)
	previous := latest.Add(-input.Cadence)
	if !alignedExclusionInstant(eligible, input.Cadence) || !alignedExclusionInstant(retention, input.Cadence) || !alignedExclusionInstant(previous, input.Cadence) {
		return invalid()
	}
	anchors := []time.Time{eligible, retention, input.At, previous, latest}
	if totals.Observed > 0 {
		if totals.FirstObservation == nil || totals.LastObservation == nil {
			return invalid()
		}
		first, last := *totals.FirstObservation, *totals.LastObservation
		if !alignedExclusionInstant(first, input.Cadence) || !alignedExclusionInstant(last, input.Cadence) || first.Before(retention) || last.After(input.At) || !first.Before(last) {
			return invalid()
		}
		anchors = append(anchors, first, first.Add(input.Cadence), last.Add(-input.Cadence), last)
	} else if totals.FirstObservation != nil || totals.LastObservation != nil {
		return invalid()
	}
	slices.SortFunc(anchors, func(a, b time.Time) int { return a.Compare(b) })
	return slices.CompactFunc(anchors, func(a, b time.Time) bool { return a.Equal(b) }), nil
}
