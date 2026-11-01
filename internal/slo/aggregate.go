package slo

import (
	"context"
)

// EvaluateOperationalTotals shares the canonical window quality and arithmetic
// rules. Callers aggregate only whole eligible intervals and retain exact counts.
func EvaluateOperationalTotals(ctx context.Context, input WindowInput, totals WindowTotals) (WindowResult, error) {
	if len(input.Observations) != 0 || totals.Good == nil || totals.Total == nil ||
		totals.Good.Sign() < 0 || totals.Total.Sign() < 0 || totals.Good.Cmp(totals.Total) > 0 ||
		totals.Observed < 0 || totals.KnownEmpty < 0 || totals.ZeroTraffic < 0 || totals.ZeroTraffic > totals.Observed {
		return WindowResult{}, ErrOperationalFacts
	}
	if totals.Observed == 0 && (totals.Total.Sign() != 0 || totals.Good.Sign() != 0 || totals.FirstObservation != nil || totals.LastObservation != nil) {
		return WindowResult{}, ErrOperationalFacts
	}
	if totals.Observed > 0 && (totals.FirstObservation == nil || totals.LastObservation == nil || totals.LatestReceipt == nil) {
		return WindowResult{}, ErrOperationalFacts
	}
	if totals.Total.Sign() == 0 && totals.ZeroTraffic != totals.Observed {
		return WindowResult{}, ErrOperationalFacts
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
	if totals.FirstObservation != nil && (totals.FirstObservation.Before(result.RetentionStart) ||
		totals.LastObservation.After(input.At) || !totals.LastObservation.After(*totals.FirstObservation)) {
		return WindowResult{}, ErrOperationalFacts
	}
	return result, nil
}
