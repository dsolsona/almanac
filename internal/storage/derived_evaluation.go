package storage

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrDerivedPending = errors.New("storage: derived data preparation pending")
var ErrDerivedSourceFailed = errors.Join(ErrDerivedFailed, errors.New("storage: source aggregate preparation exhausted"))

// Whole-hour sums are safe only when both boundaries also lie on the source
// cadence. The common grid leaves at most 7,200 raw intervals in both edges,
// even for a cadence that does not divide an hour or exceeds an hour.
func derivedAggregateBounds(start, end time.Time, cadence time.Duration) (time.Time, time.Time) {
	a, b := time.Hour, cadence
	for b != 0 {
		a, b = b, a%b
	}
	grid := time.Hour / a * cadence
	return ceilSliBoundary(start, grid), floorSliBoundary(end, grid)
}

func evaluateDerivedWindow(ctx context.Context, q *dbgen.Queries, sliID pgtype.UUID, revision int64, input slo.WindowInput) (slo.WindowResult, error) {
	if input.Cadence <= 0 || input.Duration <= 0 || input.Duration%input.Cadence != 0 || input.At.IsZero() {
		return slo.WindowResult{}, slo.ErrInvalidCalculation
	}
	params := derivedWindowParams(sliID, revision, input)
	counts, err := q.ReadDerivedWindowTotals(ctx, params)
	if err != nil {
		return slo.WindowResult{}, operationError(ctx, "storage: exact aggregate window read failed", err)
	}
	if counts.Exhausted {
		return slo.WindowResult{}, ErrDerivedSourceFailed
	}
	if !counts.Prepared {
		return slo.WindowResult{}, ErrDerivedPending
	}
	good, goodOK := new(big.Rat).SetString(counts.Good)
	total, totalOK := new(big.Rat).SetString(counts.Total)
	if !goodOK || !totalOK {
		return slo.WindowResult{}, slo.ErrOperationalFacts
	}
	optionalTime := func(value pgtype.Timestamptz) *time.Time {
		if !value.Valid {
			return nil
		}
		return new(value.Time.UTC())
	}
	input.Observations = nil
	return slo.EvaluateOperationalTotals(ctx, input, slo.WindowTotals{
		Good: good, Total: total, Observed: counts.Observed, KnownEmpty: counts.KnownEmpty, ZeroTraffic: counts.ZeroTraffic,
		FirstObservation: optionalTime(counts.FirstObservation), LastObservation: optionalTime(counts.LastObservation), LatestReceipt: optionalTime(counts.LatestReceipt),
	})
}

func derivedWindowParams(sliID pgtype.UUID, revision int64, input slo.WindowInput) dbgen.ReadDerivedWindowTotalsParams {
	start := input.At.Add(-input.Duration)
	for _, bound := range []time.Time{input.ValidFrom, input.SliValidFrom, input.RetentionStart} {
		if bound.After(start) {
			start = bound
		}
	}
	start = ceilSliBoundary(start, input.Cadence)
	if start.After(input.At) {
		start = input.At
	}
	fullStart, fullEnd := derivedAggregateBounds(start, input.At, input.Cadence)
	params := dbgen.ReadDerivedWindowTotalsParams{SliID: sliID, MeasurementRevision: revision, WindowEnd: timestamp(input.At), Now: timestamp(input.Now)}
	addEdge := func(from, to time.Time) {
		if !from.Before(to) {
			return
		}
		params.EdgeStarts = append(params.EdgeStarts, timestamp(from))
		params.EdgeEnds = append(params.EdgeEnds, timestamp(to))
	}
	if fullStart.After(fullEnd) {
		fullStart, fullEnd = input.At, input.At
		addEdge(start, input.At)
	} else {
		addEdge(start, fullStart)
		addEdge(fullEnd, input.At)
	}
	params.FullStart, params.FullEnd = timestamp(fullStart), timestamp(fullEnd)
	return params
}

func hasFailedSourceAt(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, now, at time.Time) (bool, error) {
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	window := time.Duration(row.RollingWindowSeconds) * time.Second
	if 6*time.Hour%cadence == 0 {
		window = max(window, 6*time.Hour)
	}
	input := slo.WindowInput{Now: now, At: at, Duration: window, Cadence: cadence, ValidFrom: row.EvaluationValidFrom.Time, SliValidFrom: row.SliValidFrom.Time, RetentionStart: now.Add(-sli.ObservationRetention)}
	params := derivedWindowParams(row.SliID, row.MeasurementRevision, input)
	return q.HasExhaustedDerivedSource(ctx, dbgen.HasExhaustedDerivedSourceParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision, FullStart: params.FullStart, FullEnd: params.FullEnd, Now: params.Now})
}
