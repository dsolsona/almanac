package storage

import (
	"errors"
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
)

type retainedSloTile struct {
	Attainment []api.SloOperationalSample `json:"attainment"`
	Budget     []api.SloOperationalSample `json:"budget"`
	Burn1h     []api.SloOperationalSample `json:"burn_1h"`
	Burn6h     []api.SloOperationalSample `json:"burn_6h"`
	Compliance *api.SloDailyComplianceDay `json:"compliance,omitempty"`
}

func retainedOperationalResult(value api.SloOperationalValue) slo.WindowResult {
	q, p, w := value.Quality, value.Performance, value.Window
	result := slo.WindowResult{End: value.EvaluationAt, RequestedStart: w.RequestedStart, EffectiveStart: w.EffectiveStart,
		EligibleStart: w.EligibleStart, RetentionStart: w.RetentionStart, State: string(q.State), Freshness: string(q.Freshness),
		Performance: string(p.State), Provisional: p.Provisional, Expected: q.ExpectedIntervals, Observed: q.ObservedIntervals,
		KnownEmpty: q.KnownEmptyIntervals, Missing: q.MissingIntervals, Expired: q.ExpiredIntervals, ZeroTraffic: q.ZeroTrafficIntervals,
		Coverage: q.Coverage, FirstObservation: q.FirstObservationAt, LastObservation: q.LastObservationAt, LatestReceipt: q.LatestReceiptAt,
		Good: p.Good, Total: p.Total, Bad: p.Bad, Attainment: p.Attainment, Allowed: p.AllowedBudget, Remaining: p.RemainingBudget,
		Consumed: p.ConsumedFraction, RemainingFraction: p.RemainingFraction, Burn: p.Burn}
	if p.ObservedState != nil {
		result.ObservedPerformance = string(*p.ObservedState)
	}
	if p.Reason != nil {
		result.Reason = *p.Reason
	}
	return result
}

func retainedWindowSample(at time.Time, quality api.SloOperationalQuality, performance api.SloEvaluationPerformance, revision string) api.SloOperationalSample {
	value := slo.WindowResult{End: at, State: string(quality.State), Freshness: string(quality.Freshness),
		Observed: int64(quality.ObservedIntervals), KnownEmpty: int64(quality.KnownEmptyIntervals), Missing: int64(quality.MissingIntervals),
		Expired: int64(quality.ExpiredIntervals), Expected: int64(quality.ExpectedIntervals), ZeroTraffic: int64(quality.ZeroTrafficIntervals),
		Coverage: quality.Coverage, FirstObservation: quality.FirstObservationAt, LastObservation: quality.LastObservationAt,
		LatestReceipt: quality.LatestReceiptAt, Good: performance.Good, Total: performance.Total, Attainment: performance.Attainment,
		Allowed: performance.AllowedBudget, Remaining: performance.RemainingBudget, RemainingFraction: performance.RemainingFraction,
		Burn: performance.Burn}
	if quality.Reason != nil {
		value.Reason = *quality.Reason
	}
	return operationalSample(value, at, at, revision)
}

func compactRetainedPoints(points []retainedSloPoint, start, end time.Time, target, revision string) (retainedSloTile, error) {
	var tile retainedSloTile
	attainment, budget, burn1h, burn6h := make([]api.SloOperationalSample, 0, len(points)), make([]api.SloOperationalSample, 0, len(points)), make([]api.SloOperationalSample, 0, len(points)), make([]api.SloOperationalSample, 0, len(points))
	for _, point := range points {
		at := point.Current.EvaluationAt
		if !at.Equal(point.Attainment.EvaluationAt) || !at.Equal(point.Burn1h.End) || !at.Equal(point.Burn6h.End) {
			return tile, slo.ErrOperationalFacts
		}
		attainment = append(attainment, point.Attainment)
		budget = append(budget, retainedWindowSample(at, point.Current.Quality, point.Current.Performance, revision))
		burn1h = append(burn1h, retainedWindowSample(at, point.Burn1h.Quality, point.Burn1h.Performance, revision))
		burn6h = append(burn6h, retainedWindowSample(at, point.Burn6h.Quality, point.Burn6h.Performance, revision))
	}
	return compactRetainedTile(retainedSloTile{Attainment: attainment, Budget: budget, Burn1h: burn1h, Burn6h: burn6h}, start, end, target)
}

func compactRetainedTile(tile retainedSloTile, start, end time.Time, target string) (retainedSloTile, error) {
	var err error
	tile.Attainment, err = reduceRetainedLane(tile.Attainment, start, end, target, "attainment")
	if err != nil {
		return tile, err
	}
	tile.Budget, err = reduceRetainedLane(tile.Budget, start, end, target, "budget")
	if err != nil {
		return tile, err
	}
	tile.Burn1h, err = reduceRetainedLane(tile.Burn1h, start, end, target, "burn")
	if err != nil {
		return tile, err
	}
	tile.Burn6h, err = reduceRetainedLane(tile.Burn6h, start, end, target, "burn")
	return tile, err
}

// At a fixed target, attainment and remaining budget increase together, while
// burn decreases. Their exact extrema need only the unrounded attainment ratio.
func retainedExtremumKey(sample api.SloOperationalSample, metric string) (*big.Rat, error) {
	if sample.Good == nil && sample.Total == nil {
		return nil, nil
	}
	if sample.Good == nil || sample.Total == nil {
		return nil, slo.ErrOperationalFacts
	}
	value, err := slo.ExactAttainment(*sample.Good, *sample.Total)
	if errors.Is(err, slo.ErrNoTraffic) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch metric {
	case "attainment", "budget":
		return value, nil
	case "burn":
		return value.Neg(value), nil
	default:
		return nil, slo.ErrOperationalFacts
	}
}

// Keep exact extrema and both sides of quality changes; page boundaries never
// participate in selecting the points retained by a stable tile.
func reduceRetainedLane(samples []api.SloOperationalSample, start, end time.Time, target, metric string) ([]api.SloOperationalSample, error) {
	if !start.Before(end) {
		return nil, ErrOperationalRange
	}
	if len(samples) == 0 {
		return []api.SloOperationalSample{}, nil
	}
	if _, err := slo.CalculateOccurrences("0", "1", target, false); err != nil {
		return nil, err
	}
	selected := map[int]bool{0: true, len(samples) - 1: true}
	minimum, maximum := -1, -1
	var low, high *big.Rat
	for index, sample := range samples {
		if sample.EvaluationAt.Before(start) || sample.EvaluationAt.After(end) || !sample.State.Valid() || !sample.Quality.State.Valid() ||
			index > 0 && !samples[index-1].EvaluationAt.Before(sample.EvaluationAt) {
			return nil, slo.ErrOperationalFacts
		}
		if index > 0 {
			previous := samples[index-1]
			if previous.State != sample.State || previous.Quality.State != sample.Quality.State || previous.Quality.Freshness != sample.Quality.Freshness {
				selected[index-1], selected[index] = true, true
			}
		}
		value, err := retainedExtremumKey(sample, metric)
		if err != nil {
			return nil, err
		}
		if value != nil {
			if low == nil || value.Cmp(low) < 0 {
				minimum, low = index, value
			}
			if high == nil || value.Cmp(high) > 0 {
				maximum, high = index, value
			}
		}
	}
	if minimum >= 0 {
		selected[minimum], selected[maximum] = true, true
	}
	if len(selected) > sloOperationalSeriesLimit {
		return nil, ErrOperationalRangeDense
	}
	result := make([]api.SloOperationalSample, 0, len(selected))
	for index, sample := range samples {
		if !selected[index] {
			continue
		}
		sample.BucketStart, sample.BucketEnd = start, end
		sample.RetainedExtrema = []api.SloOperationalSampleRetainedExtrema{}
		for _, mark := range []struct {
			index int
			name  api.SloOperationalSampleRetainedExtrema
		}{{0, "first"}, {len(samples) - 1, "last"}, {minimum, "minimum"}, {maximum, "maximum"}} {
			if mark.index == index {
				sample.RetainedExtrema = append(sample.RetainedExtrema, mark.name)
			}
		}
		result = append(result, sample)
	}
	return result, nil
}
