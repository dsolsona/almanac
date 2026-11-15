package slo

import (
	"context"
	"fmt"
	"math"
	"time"
)

// ExclusionCoveragePoint is the exact number of excluded cadence intervals
// before At in one immutable, SLO-scoped mask. All points share a prefix origin;
// its absolute rank is irrelevant because only differences are consumed.
type ExclusionCoveragePoint struct {
	At             time.Time
	ExcludedBefore uint64
}

// WindowCoverage is a bounded certificate supplied by the same snapshot as the
// totals. Points contain exactly the sorted, distinct required boundaries: the
// effective eligible/retention starts, evaluation end, LatestEligibleEnd and its
// previous cadence, and (when observed>0) both bounds of the first/last observed
// intervals. There are at most nine points. The storage caller authenticates the
// mask identity; this package validates internal coverage/count consistency.
// LatestEligibleEnd is global at input.At, not clipped to the requested window.
type WindowCoverage struct {
	LatestEligibleEnd time.Time
	Points            []ExclusionCoveragePoint
}

type windowCoverageCounts struct {
	excluded, retainedExcluded int64
	latestEligibleEnd          time.Time
}

// EvaluateOperationalCoveredTotals uses the canonical quality and arithmetic
// path with indexed mask statistics instead of a full exclusion fragment list.
// It is not a raw-observation or mixed raw-mask/compiled-summary entry point.
func EvaluateOperationalCoveredTotals(ctx context.Context, input WindowInput, totals WindowTotals, coverage WindowCoverage) (WindowResult, error) {
	result, err := EvaluateOperationalCoveredTotalsSummary(ctx, input, totals, coverage)
	if err != nil {
		return WindowResult{}, err
	}
	return result.WithValues(), nil
}

// EvaluateOperationalCoveredTotalsSummary defers decimal formatting while owning
// the resulting evidence. No caller-owned counts, timestamps or points are changed.
func EvaluateOperationalCoveredTotalsSummary(ctx context.Context, input WindowInput, totals WindowTotals, coverage WindowCoverage) (WindowResult, error) {
	if err := ctx.Err(); err != nil {
		return WindowResult{}, err
	}
	if err := validateCoverageInput(input); err != nil {
		return WindowResult{}, err
	}
	cloneTime := func(at *time.Time) *time.Time {
		if at == nil {
			return nil
		}
		return new(*at)
	}
	totals.FirstObservation, totals.LastObservation, totals.LatestReceipt = cloneTime(totals.FirstObservation), cloneTime(totals.LastObservation), cloneTime(totals.LatestReceipt)
	return evaluateOperationalTotalsSummary(ctx, input, totals, &coverage)
}

func coverageTime(at time.Time) bool {
	return !at.IsZero() && at.Nanosecond()%1000 == 0 && time.UnixMicro(at.UnixMicro()).Equal(at)
}

func validateWindowCoverage(input WindowInput, totals WindowTotals, coverage WindowCoverage) (windowCoverageCounts, error) {
	invalid := func() (windowCoverageCounts, error) {
		return windowCoverageCounts{}, fmt.Errorf("%w: inconsistent compiled exclusion coverage", ErrOperationalFacts)
	}
	anchors, err := RequiredCoverageAnchors(input, totals, coverage.LatestEligibleEnd)
	if err != nil {
		return invalid()
	}
	_, eligible, retention := windowBounds(input, input.At)
	previous := coverage.LatestEligibleEnd.Add(-input.Cadence)
	if len(anchors) != len(coverage.Points) {
		return invalid()
	}
	// Ordered representable instants can span more than a signed Duration. Use
	// unsigned microsecond differences, never saturated Time.Sub, for rank bounds.
	distance := func(a, b time.Time) uint64 {
		return (uint64(b.UnixMicro()) - uint64(a.UnixMicro())) / uint64(input.Cadence.Microseconds())
	}
	for i, point := range coverage.Points {
		if !alignedExclusionInstant(point.At, input.Cadence) || !point.At.Equal(anchors[i]) {
			return invalid()
		}
		if i > 0 {
			previous := coverage.Points[i-1]
			if point.ExcludedBefore < previous.ExcludedBefore || point.ExcludedBefore-previous.ExcludedBefore > distance(previous.At, point.At) {
				return invalid()
			}
		}
	}
	rank := func(at time.Time) uint64 {
		for _, p := range coverage.Points {
			if p.At.Equal(at) {
				return p.ExcludedBefore
			}
		}
		panic("slo: missing validated coverage boundary")
	}
	count := func(a, b time.Time) uint64 { return rank(b) - rank(a) }
	if count(previous, coverage.LatestEligibleEnd) != 0 || count(coverage.LatestEligibleEnd, input.At) != distance(coverage.LatestEligibleEnd, input.At) {
		return invalid()
	}
	if totals.Observed > 0 {
		first, last := *totals.FirstObservation, *totals.LastObservation
		if count(first, first.Add(input.Cadence)) != 0 || count(last.Add(-input.Cadence), last) != 0 || uint64(totals.Observed) > distance(first, last)-count(first, last) || totals.Observed == 1 && !last.Equal(first.Add(input.Cadence)) {
			return invalid()
		}
	}
	excluded, retained := count(eligible, input.At), count(retention, input.At)
	if excluded > math.MaxInt64 || retained > excluded {
		return invalid()
	}
	return windowCoverageCounts{excluded: int64(excluded), retainedExcluded: int64(retained), latestEligibleEnd: coverage.LatestEligibleEnd}, nil
}
