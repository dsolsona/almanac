package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repair one bounded hour, publishing its exact points and moving its cursor in
// the same transaction. Coverage arriving during calculation changes the work
// version; the old writer cannot acknowledge or commit over that invalidation.
func (r *Runtime) repairRetainedSeries(ctx context.Context, clock func() time.Time) (worked bool, resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "slo.series.repair")
	defer func() { finishDerivedTrace(ctx, span, resultErr, "repair") }()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	work, err := dbgen.New(conn).ClaimRetainedSeriesRepair(bounded, dbgen.ClaimRetainedSeriesRepairParams{Now: timestamp(clock()), LeaseUntil: timestamp(clock().Add(30 * time.Second))})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		conflict, serialization := errors.AsType[*pgconn.PgError](resultErr)
		if errors.Is(resultErr, ErrDerivedPending) || errors.Is(resultErr, ErrDerivedSourceFailed) || serialization && conflict.Code == "40001" {
			err := dbgen.New(conn).DeferRetainedSeriesRepair(cleanup, dbgen.DeferRetainedSeriesRepairParams{SloID: work.SloID,
				DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision,
				CalculationVersion: work.CalculationVersion, LeaseEpoch: work.LeaseEpoch, NextAttemptAt: timestamp(clock().Add(time.Second))})
			resultErr = errors.Join(resultErr, err)
			return
		}
		err := dbgen.New(conn).FailRetainedSeriesRepair(cleanup, dbgen.FailRetainedSeriesRepairParams{SloID: work.SloID,
			DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision,
			CalculationVersion: work.CalculationVersion, LeaseEpoch: work.LeaseEpoch, Column6: timestamp(clock())})
		resultErr = errors.Join(resultErr, err)
	}()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return true, err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return true, err
	}
	q := dbgen.New(tx)
	row, err := q.GetSlo(bounded, work.SloID)
	if err != nil {
		return true, err
	}
	through := work.EndAt.Time.UTC()
	if row.DefinitionRevision == work.DefinitionRevision && row.MeasurementRevision == work.MeasurementRevision && work.CalculationVersion == operationalCalculationVersion {
		now := clock().UTC()
		cadence := time.Duration(row.CadenceSeconds) * time.Second
		start := maxTime(work.CursorAt.Time.UTC(), ceilSliBoundary(maxTime(now.Add(-sli.ObservationRetention), row.EvaluationValidFrom.Time, row.SliValidFrom.Time), cadence))
		if chunkEnd := floorSliBoundary(start.Add(max(time.Hour, cadence)), cadence); chunkEnd.Before(through) {
			through = chunkEnd
		}
		source, err := q.GetSloOperationalContext(bounded, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
		if err != nil {
			return true, err
		}
		generation, err := q.GetDerivedSourceGeneration(bounded, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
		if errors.Is(err, pgx.ErrNoRows) {
			generation = 0
		} else if err != nil {
			return true, err
		}
		points, err := buildRetainedRepairPoints(bounded, q, row, source, start, through, now)
		if err != nil {
			return true, err
		}
		batch := dbgen.PublishRetainedSloPointBatchParams{SloID: row.ID, DefinitionRevision: row.DefinitionRevision,
			MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion,
			SourceGeneration: generation, PublishedAt: timestamp(now)}
		for _, point := range points {
			payload, err := json.Marshal(point)
			if err != nil {
				return true, err
			}
			batch.EvaluationAts = append(batch.EvaluationAts, timestamp(point.Current.EvaluationAt))
			batch.Payloads = append(batch.Payloads, string(payload))
		}
		if err := q.PublishRetainedSloPointBatch(bounded, batch); err != nil {
			return true, err
		}
	}
	advanced, err := q.AdvanceRetainedSeriesRepair(bounded, dbgen.AdvanceRetainedSeriesRepairParams{SloID: work.SloID,
		DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision,
		CalculationVersion: work.CalculationVersion, Version: work.Version, LeaseEpoch: work.LeaseEpoch,
		CursorAt: timestamp(through), NextAttemptAt: timestamp(clock())})
	if err != nil {
		return true, err
	}
	if advanced != 1 {
		return true, ErrDerivedPending
	}
	return true, tx.Commit(bounded)
}

func buildRetainedRepairPoints(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, start, end, now time.Time) ([]retainedSloPoint, error) {
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	base := slo.WindowInput{Now: now, Cadence: cadence, ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(),
		RetentionStart: now.Add(-sli.ObservationRetention), Target: sli.NormalizeDecimal(row.Target),
		FreshnessGrace: cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))}
	var inputs []slo.WindowInput
	params := dbgen.ReadRetainedWindowBatchParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision, Now: timestamp(now)}
	durations := []time.Duration{time.Duration(row.RollingWindowSeconds) * time.Second, cadence}
	if time.Hour%cadence == 0 {
		durations = append(durations, time.Hour)
	}
	if 6*time.Hour%cadence == 0 {
		durations = append(durations, 6*time.Hour)
	}
	for at := start.Add(cadence); !at.After(end); at = at.Add(cadence) {
		if len(inputs) >= 960 {
			return nil, ErrOperationalResource
		}
		for _, duration := range durations {
			input := base
			input.At, input.Duration = at, duration
			inputs = append(inputs, input)
			p := derivedWindowParams(row.SliID, row.MeasurementRevision, input)
			params.WindowEnds = append(params.WindowEnds, p.WindowEnd)
			params.FullStarts, params.FullEnds = append(params.FullStarts, p.FullStart), append(params.FullEnds, p.FullEnd)
			a, b, c, d := p.WindowEnd, p.WindowEnd, p.WindowEnd, p.WindowEnd
			if len(p.EdgeStarts) > 0 {
				a, b = p.EdgeStarts[0], p.EdgeEnds[0]
			}
			if len(p.EdgeStarts) > 1 {
				c, d = p.EdgeStarts[1], p.EdgeEnds[1]
			}
			params.EdgeAStarts, params.EdgeAEnds = append(params.EdgeAStarts, a), append(params.EdgeAEnds, b)
			params.EdgeBStarts, params.EdgeBEnds = append(params.EdgeBStarts, c), append(params.EdgeBEnds, d)
		}
	}
	if len(inputs) == 0 {
		return []retainedSloPoint{}, nil
	}
	counts, err := q.ReadRetainedWindowBatch(ctx, params)
	if err != nil {
		return nil, err
	}
	if len(counts) != len(inputs) {
		return nil, slo.ErrOperationalFacts
	}
	points := make([]retainedSloPoint, len(inputs)/len(durations))
	revision := strconv.FormatInt(row.DefinitionRevision, 10)
	for index, count := range counts {
		if count.Ordinal != int32(index+1) {
			return nil, slo.ErrOperationalFacts
		}
		input := inputs[index]
		value, err := evaluateDerivedCounts(ctx, input, dbgen.ReadDerivedWindowTotalsRow{Good: count.Good, Total: count.Total, Observed: count.Observed,
			KnownEmpty: count.KnownEmpty, ZeroTraffic: count.ZeroTraffic, FirstObservation: count.FirstObservation,
			LastObservation: count.LastObservation, LatestReceipt: count.LatestReceipt, Prepared: count.Prepared, Exhausted: count.Exhausted})
		if err != nil {
			return nil, err
		}
		point := &points[index/len(durations)]
		switch index % len(durations) {
		case 0:
			point.Current = operationalValue(value)
			applyOperationalDefinitionWindow(&point.Current, row.ActivatedAt.Time.UTC(), row.EvaluationValidFrom.Time.UTC(), row.HistoricalRequestedBy, row.HistoricalRequestedAt)
			point.Burn1h, point.Burn6h = unavailableBurn(3600, input.At, "cadence_mismatch"), unavailableBurn(21600, input.At, "cadence_mismatch")
		case 1:
			point.Attainment = operationalSample(value, input.At.Add(-cadence), input.At, revision)
		default:
			if input.Duration == time.Hour {
				point.Burn1h = burnValue(value, 3600)
			} else {
				point.Burn6h = burnValue(value, 21600)
			}
		}
	}
	return points, nil
}
