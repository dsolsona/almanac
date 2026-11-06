package slo

import (
	"context"
	"fmt"
)

// EvaluateOperationalTotals shares the canonical window quality and arithmetic
// rules. Callers aggregate only whole eligible intervals and retain exact counts.
func EvaluateOperationalTotals(ctx context.Context, input WindowInput, totals WindowTotals) (WindowResult, error) {
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
	sweep.good.Set(totals.Good)
	sweep.total.Set(totals.Total)
	sweep.observedCount, sweep.emptyCount, sweep.zeroTraffic = totals.Observed, totals.KnownEmpty, totals.ZeroTraffic
	sweep.totals = &totals
	result, err := sweep.AtContext(ctx, input.At)
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
	}
	return result, nil
}
