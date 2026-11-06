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
	"github.com/jackc/pgx/v5/pgtype"
)

// Repair one bounded calculation batch, publishing points and moving its cursor in
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
	// The source range this batch reads, for rebuilding it if its aggregates prove inconsistent.
	var facts derivedFactsRange
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		resultErr = errors.Join(resultErr, facts.rebuildIfInconsistent(cleanup, dbgen.New(conn), resultErr, clock()))
		if derivedTemporary(ctx, resultErr) {
			var reason pgtype.Text
			if errors.Is(resultErr, ErrDerivedSourceFailed) {
				reason = pgtype.Text{String: "source_aggregate_failed", Valid: true}
			}
			err := dbgen.New(conn).DeferRetainedSeriesRepair(cleanup, dbgen.DeferRetainedSeriesRepairParams{SloID: work.SloID,
				DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision,
				CalculationVersion: work.CalculationVersion, LeaseEpoch: work.LeaseEpoch, NextAttemptAt: timestamp(clock().Add(time.Second)), LastError: reason})
			resultErr = errors.Join(resultErr, err)
			return
		}
		err := dbgen.New(conn).FailRetainedSeriesRepair(cleanup, dbgen.FailRetainedSeriesRepairParams{SloID: work.SloID,
			DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision,
			CalculationVersion: work.CalculationVersion, LeaseEpoch: work.LeaseEpoch, Column6: timestamp(clock()), Reason: derivedFailureReason(resultErr, "retained_series_repair_failed")})
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
	var coherentGeneration pgtype.Int8
	if row.DefinitionRevision == work.DefinitionRevision && row.MeasurementRevision == work.MeasurementRevision && work.CalculationVersion == operationalCalculationVersion {
		now := clock().UTC()
		cadence := time.Duration(row.CadenceSeconds) * time.Second
		start := maxTime(work.CursorAt.Time.UTC(), ceilSliBoundary(maxTime(now.Add(-sli.ObservationRetention), row.EvaluationValidFrom.Time, row.SliValidFrom.Time), cadence))
		if chunkEnd := start.Add(retainedRepairSpan(time.Duration(row.RollingWindowSeconds)*time.Second, cadence)); chunkEnd.Before(through) {
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
		coherentGeneration = pgtype.Int8{Int64: generation, Valid: true}
		facts = derivedFactsRange{sli: row.SliID, revision: row.MeasurementRevision, from: start.Add(-max(time.Duration(row.RollingWindowSeconds)*time.Second, 6*time.Hour)), to: through}
		points, err := buildRetainedRepairPoints(bounded, q, row, source, start, through, now)
		if err != nil {
			return true, err
		}
		if err := preserveRetainedPrefixes(bounded, q, row, source, start, through, now, points); err != nil {
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
		tiles, err := q.PublishRetainedSloPointBatch(bounded, batch)
		if err != nil {
			return true, err
		}
		if err := publishRetainedRepairTiles(bounded, q, tiles, points, start, through, row.Target, generation, now); err != nil {
			return true, err
		}
	}
	advanced, err := q.AdvanceRetainedSeriesRepair(bounded, dbgen.AdvanceRetainedSeriesRepairParams{SloID: work.SloID,
		DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision,
		CalculationVersion: work.CalculationVersion, Version: work.Version, LeaseEpoch: work.LeaseEpoch,
		CursorAt: timestamp(through), NextAttemptAt: timestamp(clock()), CoherentSourceGeneration: coherentGeneration})
	if err != nil {
		return true, err
	}
	if advanced != 1 {
		return true, ErrDerivedPending
	}
	return true, tx.Commit(bounded)
}

// Complete hours reuse the exact calculated points before their metadata is
// serialized; partial hours remain queued for the ordinary tile worker.
func publishRetainedRepairTiles(ctx context.Context, q *dbgen.Queries, work []dbgen.AlmanacSloSeriesTileWork, points []retainedSloPoint, start, end time.Time, target string, generation int64, now time.Time) error {
	for _, item := range work {
		if item.ResolutionSeconds != 3600 || item.RangeStart.Time.Before(start) || item.RangeEnd.Time.After(end) {
			continue
		}
		var selected []retainedSloPoint
		for _, point := range points {
			if point.Current.EvaluationAt.After(item.RangeStart.Time) && !point.Current.EvaluationAt.After(item.RangeEnd.Time) {
				selected = append(selected, point)
			}
		}
		tile, err := compactRetainedPoints(selected, item.RangeStart.Time.UTC(), item.RangeEnd.Time.UTC(), target, strconv.FormatInt(item.DefinitionRevision, 10))
		if err != nil {
			return err
		}
		payload, err := json.Marshal(tile)
		if err != nil {
			return err
		}
		if len(payload) > 20<<20 {
			return ErrOperationalResource
		}
		summary, err := marshalRetainedTileSummary(tile, item.RangeStart.Time.UTC(), item.RangeEnd.Time.UTC(), target, generation)
		if err != nil {
			return err
		}
		published, err := q.PublishRetainedTile(ctx, dbgen.PublishRetainedTileParams{ID: item.ID, Version: item.Version, LeaseEpoch: item.LeaseEpoch,
			SourceGeneration: generation, PublishedAt: timestamp(now), PointCount: int32(len(selected)), Payload: payload, CompactPayload: summary})
		if err != nil {
			return err
		}
		acknowledged, err := q.AcknowledgeRetainedTileWork(ctx, dbgen.AcknowledgeRetainedTileWorkParams{ID: item.ID, Version: item.Version, LeaseEpoch: item.LeaseEpoch})
		if err != nil {
			return err
		}
		if published != 1 || acknowledged != 1 {
			return ErrDerivedPending
		}
	}
	return nil
}

func retainedRepairDurations(window, cadence time.Duration) []time.Duration {
	durations := []time.Duration{window, cadence}
	if time.Hour%cadence == 0 {
		durations = append(durations, time.Hour)
	}
	if 6*time.Hour%cadence == 0 {
		durations = append(durations, 6*time.Hour)
	}
	return durations
}

func retainedRepairSpan(window, cadence time.Duration) time.Duration {
	// Fill the existing input budget; a sparse cadence must not multiply jobs.
	points := max(1, min(derivedWindowBatchLimit/len(retainedRepairDurations(window, cadence)), int(24*time.Hour/cadence)))
	return time.Duration(points) * cadence
}

func buildRetainedRepairPoints(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, start, end, now time.Time) ([]retainedSloPoint, error) {
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	base := slo.WindowInput{Now: now, Cadence: cadence, ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(),
		RetentionStart: now.Add(-sli.ObservationRetention), Target: sli.NormalizeDecimal(row.Target),
		FreshnessGrace: cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))}
	var inputs []slo.WindowInput
	durations := retainedRepairDurations(time.Duration(row.RollingWindowSeconds)*time.Second, cadence)
	for at := start.Add(cadence); !at.After(end); at = at.Add(cadence) {
		if len(inputs) >= derivedWindowBatchLimit {
			return nil, ErrOperationalResource
		}
		for _, duration := range durations {
			input := base
			input.At, input.Duration = at, duration
			inputs = append(inputs, input)
		}
	}
	if len(inputs) == 0 {
		return []retainedSloPoint{}, nil
	}
	values, err := evaluateDerivedWindows(ctx, q, row.SliID, row.MeasurementRevision, inputs)
	if err != nil {
		return nil, err
	}
	points := make([]retainedSloPoint, len(inputs)/len(durations))
	revision := strconv.FormatInt(row.DefinitionRevision, 10)
	for index, value := range values {
		input := inputs[index]
		point := &points[index/len(durations)]
		switch index % len(durations) {
		case 0:
			point.Current = operationalValue(value)
			applyOperationalDefinitionWindow(&point.Current, row.ActivatedAt.Time.UTC(), row.EvaluationValidFrom.Time.UTC(), row.HistoricalRequestedBy, row.HistoricalRequestedAt)
			point.Burn1h, point.Burn6h = unavailableBurn(3600, input.At, "cadence_mismatch"), unavailableBurn(21600, input.At, "cadence_mismatch")
		case 1:
			point.Attainment = operationalAttainmentSample(value, input.At.Add(-cadence), input.At, revision)
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
