package slo

import (
	"context"
	"fmt"
)

// EvaluateOperationalTotals shares the canonical window quality and arithmetic
// rules. Callers aggregate only whole retained eligible intervals outside the
// SLO-scoped mask; Excluded carries separate statistics from inside that mask.
// Both populations must come from the same source and exclusion revision.
func EvaluateOperationalTotals(ctx context.Context, input WindowInput, totals WindowTotals) (WindowResult, error) {
	result, err := EvaluateOperationalTotalsSummary(ctx, input, totals)
	if err != nil {
		return WindowResult{}, err
	}
	return result.WithValues(), nil
}

// EvaluateOperationalTotalsSummary retains exact arithmetic and quality while
// deferring public decimal formatting until WithValues is called on selected results.
func EvaluateOperationalTotalsSummary(ctx context.Context, input WindowInput, totals WindowTotals) (WindowResult, error) {
	switch {
	case len(input.Observations) != 0 || totals.Good == nil || totals.Total == nil:
		return WindowResult{}, fmt.Errorf("%w: totals and observations both supplied or counts missing", ErrOperationalFacts)
	case totals.Good.Sign() < 0 || totals.Total.Sign() < 0 || totals.Good.Cmp(totals.Total) > 0:
		return WindowResult{}, fmt.Errorf("%w: good is negative or exceeds total", ErrOperationalFacts)
	case totals.Observed < 0 || totals.KnownEmpty < 0 || totals.ZeroTraffic < 0 || totals.ZeroTraffic > totals.Observed:
		return WindowResult{}, fmt.Errorf("%w: interval counts are negative or zero traffic exceeds observed", ErrOperationalFacts)
	case totals.Observed == 0 && (totals.Total.Sign() != 0 || totals.Good.Sign() != 0 || totals.FirstObservation != nil || totals.LastObservation != nil):
		return WindowResult{}, fmt.Errorf("%w: counts or observation times without observed intervals", ErrOperationalFacts)
	case totals.Observed > 0 && (totals.FirstObservation == nil || totals.LastObservation == nil || totals.LatestReceipt == nil):
		return WindowResult{}, fmt.Errorf("%w: observed intervals without observation or receipt times", ErrOperationalFacts)
	case totals.Total.Sign() == 0 && totals.ZeroTraffic != totals.Observed:
		return WindowResult{}, fmt.Errorf("%w: zero total but not every observed interval is zero traffic", ErrOperationalFacts)
	}
	sweep, err := NewWindowSweepContext(ctx, input)
	if err != nil {
		return WindowResult{}, err
	}
	excluded := totals.Excluded
	switch {
	case excluded.Observed < 0 || excluded.KnownEmpty < 0:
		return WindowResult{}, fmt.Errorf("%w: negative excluded interval counts", ErrOperationalFacts)
	case (excluded.Good == nil) != (excluded.Total == nil):
		return WindowResult{}, fmt.Errorf("%w: excluded event counts must be paired", ErrOperationalFacts)
	case excluded.Observed > 0 && excluded.Good == nil:
		return WindowResult{}, fmt.Errorf("%w: excluded observed intervals lack exact counts", ErrOperationalFacts)
	}
	if excluded.Good != nil {
		if excluded.Good.Sign() < 0 || excluded.Total.Sign() < 0 || excluded.Good.Cmp(excluded.Total) > 0 || excluded.Observed == 0 && excluded.Total.Sign() != 0 {
			return WindowResult{}, fmt.Errorf("%w: inconsistent excluded event counts", ErrOperationalFacts)
		}
		sweep.excludedGood.Set(excluded.Good)
		sweep.excludedTotal.Set(excluded.Total)
	}
	sweep.excludedObserved, sweep.excludedEmpty = excluded.Observed, excluded.KnownEmpty
	sweep.good.Set(totals.Good)
	sweep.total.Set(totals.Total)
	sweep.observedCount, sweep.emptyCount, sweep.zeroTraffic = totals.Observed, totals.KnownEmpty, totals.ZeroTraffic
	sweep.totals = &totals
	result, err := sweep.AtSummaryContext(ctx, input.At)
	if err != nil {
		return WindowResult{}, err
	}
	if totals.FirstObservation != nil {
		switch {
		case totals.FirstObservation.Before(result.RetentionStart):
			return WindowResult{}, fmt.Errorf("%w: first observation precedes retention start", ErrOperationalFacts)
		case totals.LastObservation.After(input.At):
			return WindowResult{}, fmt.Errorf("%w: last observation follows the window end", ErrOperationalFacts)
		case !totals.LastObservation.After(*totals.FirstObservation):
			return WindowResult{}, fmt.Errorf("%w: last observation does not follow the first", ErrOperationalFacts)
		}
		if len(input.Exclusions) > 0 {
			first, last := *totals.FirstObservation, *totals.LastObservation
			if !alignedExclusionInstant(first, input.Cadence) || !alignedExclusionInstant(last, input.Cadence) ||
				sweep.exclusions.contains(first) || sweep.exclusions.contains(last.Add(-input.Cadence)) ||
				totals.Observed > int64(last.Sub(first)/input.Cadence)-sweep.exclusions.count(first, last) ||
				totals.Observed == 1 && !last.Equal(first.Add(input.Cadence)) {
				return WindowResult{}, fmt.Errorf("%w: observation metadata disagrees with eligible masked intervals", ErrOperationalFacts)
			}
		}
	}
	return result, nil
}
