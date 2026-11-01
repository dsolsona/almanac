package storage

import (
	"context"
	"encoding/json/v2"
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

// Raw expiry must not erase the known prefix of a retained historical window.
// Only newly committed canonical intervals are added to its sufficient totals.
func preserveRetainedPrefixes(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, start, end, now time.Time, points []retainedSloPoint) error {
	stored, err := q.ReadRetainedTilePoints(ctx, dbgen.ReadRetainedTilePointsParams{SloID: row.ID,
		DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
		CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(start), EvaluationAt_2: timestamp(end)})
	if err != nil {
		return err
	}
	if len(stored) > len(points) {
		return slo.ErrOperationalFacts
	}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	cutoff := ceilSliBoundary(now.Add(-sli.ObservationRetention), cadence)
	type repair struct {
		point, lane int
		input       slo.WindowInput
		quality     api.SloOperationalQuality
		performance api.SloEvaluationPerformance
		window      api.SloEvaluationWindow
	}
	var repairs []repair
	params := dbgen.ReadRetainedLateTotalsParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision}
	bytes := 0
	for _, saved := range stored {
		bytes += len(saved.Payload)
		if bytes > 20<<20 {
			return ErrOperationalResource
		}
		var old retainedSloPoint
		if err := json.Unmarshal(saved.Payload, &old); err != nil || !old.Current.EvaluationAt.Equal(saved.EvaluationAt.Time) {
			return ErrDerivedFailed
		}
		index := int(saved.EvaluationAt.Time.Sub(start)/cadence) - 1
		if index < 0 || index >= len(points) || !points[index].Current.EvaluationAt.Equal(saved.EvaluationAt.Time) {
			return slo.ErrOperationalFacts
		}
		for lane, duration := range []time.Duration{time.Duration(row.RollingWindowSeconds) * time.Second, time.Hour, 6 * time.Hour} {
			if duration%cadence != 0 {
				continue
			}
			quality, performance := old.Current.Quality, old.Current.Performance
			if lane == 1 {
				quality, performance = old.Burn1h.Quality, old.Burn1h.Performance
			} else if lane == 2 {
				quality, performance = old.Burn6h.Quality, old.Burn6h.Performance
			}
			if quality.ExpectedIntervals < 0 || quality.ExpectedIntervals > int64(duration/cadence) ||
				quality.ExpiredIntervals < 0 || quality.ExpiredIntervals > quality.ExpectedIntervals {
				return slo.ErrOperationalFacts
			}
			lower := old.Current.EvaluationAt.Add(-time.Duration(quality.ExpectedIntervals-quality.ExpiredIntervals) * cadence)
			if !lower.Before(cutoff) {
				continue
			}
			input := slo.WindowInput{At: old.Current.EvaluationAt, Now: now, Duration: duration, Cadence: cadence,
				ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(), RetentionStart: lower,
				Target: sli.NormalizeDecimal(row.Target), FreshnessGrace: cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))}
			repairs = append(repairs, repair{index, lane, input, quality, performance, old.Current.Window})
			params.Starts, params.Ends = append(params.Starts, timestamp(lower)), append(params.Ends, saved.EvaluationAt)
			params.Generations = append(params.Generations, saved.SourceGeneration)
		}
	}
	if len(repairs) == 0 {
		return nil
	}
	deltas, err := q.ReadRetainedLateTotals(ctx, params)
	if err != nil {
		return err
	}
	if len(deltas) != len(repairs) {
		return slo.ErrOperationalFacts
	}
	for index, delta := range deltas {
		if delta.Ordinal != int32(index+1) {
			return slo.ErrOperationalFacts
		}
		work := repairs[index]
		value, err := mergeRetainedWindow(ctx, work.input, work.quality, work.performance, delta)
		if err != nil {
			return err
		}
		point := &points[work.point]
		switch work.lane {
		case 0:
			point.Current = operationalValue(value)
			point.Current.Window = work.window
		case 1:
			point.Burn1h = burnValue(value, 3600)
		case 2:
			point.Burn6h = burnValue(value, 21600)
		}
	}
	return nil
}

func mergeRetainedWindow(ctx context.Context, input slo.WindowInput, quality api.SloOperationalQuality, performance api.SloEvaluationPerformance, delta dbgen.ReadRetainedLateTotalsRow) (slo.WindowResult, error) {
	if quality.MissingIntervals < 0 || delta.Observed < 0 || delta.KnownEmpty < 0 || delta.ZeroTraffic < 0 ||
		delta.Observed > quality.MissingIntervals || delta.KnownEmpty > quality.MissingIntervals-delta.Observed {
		return slo.WindowResult{}, slo.ErrOperationalFacts
	}
	good, total := new(big.Rat), new(big.Rat)
	for _, part := range []struct {
		old *string
		new string
		sum *big.Rat
	}{{performance.Good, delta.Good, good}, {performance.Total, delta.Total, total}} {
		if part.old == nil && quality.ObservedIntervals != 0 {
			return slo.WindowResult{}, slo.ErrOperationalFacts
		}
		if part.old != nil {
			if _, ok := part.sum.SetString(*part.old); !ok || part.sum.Sign() < 0 {
				return slo.WindowResult{}, slo.ErrOperationalFacts
			}
		}
		added, ok := new(big.Rat).SetString(part.new)
		if !ok || added.Sign() < 0 {
			return slo.WindowResult{}, slo.ErrOperationalFacts
		}
		part.sum.Add(part.sum, added)
	}
	first, last, receipt := quality.FirstObservationAt, quality.LastObservationAt, quality.LatestReceiptAt
	if delta.FirstObservation.Valid && (first == nil || delta.FirstObservation.Time.Before(*first)) {
		first = new(delta.FirstObservation.Time.UTC())
	}
	if delta.LastObservation.Valid && (last == nil || delta.LastObservation.Time.After(*last)) {
		last = new(delta.LastObservation.Time.UTC())
	}
	if delta.LatestReceipt.Valid && (receipt == nil || delta.LatestReceipt.Time.After(*receipt)) {
		receipt = new(delta.LatestReceipt.Time.UTC())
	}
	value, err := slo.EvaluateOperationalTotals(ctx, input, slo.WindowTotals{Good: good, Total: total,
		Observed: quality.ObservedIntervals + delta.Observed, KnownEmpty: quality.KnownEmptyIntervals + delta.KnownEmpty,
		ZeroTraffic: quality.ZeroTrafficIntervals + delta.ZeroTraffic, FirstObservation: first, LastObservation: last, LatestReceipt: receipt})
	if err == nil && (value.Expected != quality.ExpectedIntervals || value.Expired != quality.ExpiredIntervals) {
		return slo.WindowResult{}, slo.ErrOperationalFacts
	}
	return value, err
}
