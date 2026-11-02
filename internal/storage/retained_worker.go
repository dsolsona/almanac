package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
)

func (r *Runtime) prepareRetainedTile(ctx context.Context, clock func() time.Time) (worked bool, resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "slo.series.tile")
	defer func() { finishDerivedTrace(ctx, span, resultErr, "tile") }()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	work, err := dbgen.New(conn).ClaimRetainedTileWork(bounded, dbgen.ClaimRetainedTileWorkParams{Now: timestamp(clock()), LeaseUntil: timestamp(clock().Add(30 * time.Second))})
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
		if conflict, ok := errors.AsType[*pgconn.PgError](resultErr); ok && conflict.Code == "40001" {
			err := dbgen.New(conn).DeferRetainedTileWork(cleanup, dbgen.DeferRetainedTileWorkParams{
				ID: work.ID, LeaseEpoch: work.LeaseEpoch, NextAttemptAt: timestamp(clock().Add(time.Second))})
			resultErr = errors.Join(resultErr, err)
			return
		}
		err := dbgen.New(conn).FailRetainedTileWork(cleanup, dbgen.FailRetainedTileWorkParams{ID: work.ID,
			LeaseEpoch: work.LeaseEpoch, Column3: timestamp(clock()), LastError: pgtype.Text{String: "retained_tile_failed", Valid: true}})
		resultErr = errors.Join(resultErr, err)
	}()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return true, err
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return true, err
	}
	definition, err := q.GetSlo(bounded, work.SloID)
	if err != nil {
		return true, err
	}
	if definition.DefinitionRevision == work.DefinitionRevision && definition.MeasurementRevision == work.MeasurementRevision && work.CalculationVersion == operationalCalculationVersion {
		prepared, err := prepareRetainedTileValue(bounded, q, work, definition.Target)
		if err != nil {
			return true, err
		}
		span.SetAttributes(attribute.Bool("almanac.series.hourly_input", prepared.hourly))
		tile := prepared.tile
		if work.ResolutionSeconds == 86400 && prepared.last != nil {
			last := *prepared.last
			cadence := time.Duration(definition.CadenceSeconds) * time.Second
			if last.EvaluationAt.Equal(floorSliBoundary(work.RangeEnd.Time, cadence)) {
				day := operationalComplianceDay(retainedOperationalResult(last), work.RangeEnd.Time.UTC(), definition.EvaluationValidFrom.Time.UTC(),
					definition.ID.Bytes, strconv.FormatInt(definition.DefinitionRevision, 10), definition.Target, time.Duration(definition.RollingWindowSeconds)*time.Second, cadence)
				tile.Compliance = &day
			}
		}
		payload, err := json.Marshal(tile)
		if err != nil {
			return true, err
		}
		if len(payload) > 20<<20 {
			return true, ErrOperationalResource
		}
		if _, err := q.PublishRetainedTile(bounded, dbgen.PublishRetainedTileParams{ID: work.ID, Version: work.Version,
			LeaseEpoch: work.LeaseEpoch, SourceGeneration: prepared.generation, PublishedAt: timestamp(clock()), PointCount: prepared.count, Payload: payload}); err != nil {
			return true, err
		}
	}
	deleted, err := q.AcknowledgeRetainedTileWork(bounded, dbgen.AcknowledgeRetainedTileWorkParams{ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch})
	if err != nil {
		return true, err
	}
	if deleted == 0 {
		if err := q.ReleaseRetainedTileWork(bounded, dbgen.ReleaseRetainedTileWorkParams{ID: work.ID, LeaseEpoch: work.LeaseEpoch}); err != nil {
			return true, err
		}
	}
	return true, tx.Commit(bounded)
}

type preparedRetainedTile struct {
	tile       retainedSloTile
	last       *api.SloOperationalValue
	count      int32
	generation int64
	hourly     bool
}

func prepareRetainedTileValue(ctx context.Context, q *dbgen.Queries, work dbgen.AlmanacSloSeriesTileWork, target string) (preparedRetainedTile, error) {
	prepared := preparedRetainedTile{generation: work.SourceGeneration}
	revision := strconv.FormatInt(work.DefinitionRevision, 10)
	if work.ResolutionSeconds == 86400 {
		reader := retainedSeriesReader{q: q, row: dbgen.GetSloRow{ID: work.SloID, DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision}}
		parts := make([]retainedReadPart, 24)
		for index := range parts {
			start := work.RangeStart.Time.UTC().Add(time.Duration(index) * time.Hour)
			parts[index] = retainedReadPart{start: start, end: start.Add(time.Hour), resolution: 3600}
		}
		rows, err := reader.readParts(ctx, parts)
		if err != nil {
			return prepared, err
		}
		complete := true
		for _, row := range rows {
			complete = complete && row.Ready && row.PointCount.Valid && row.PointCount.Int32 > 0 && row.PointCount.Int32 <= 5760
		}
		if complete {
			// Exact hourly extrema and both sides of quality changes are sufficient
			// for the same daily reduction; never average plotted values.
			for index, row := range rows {
				tile, err := decodeRetainedTile(row.Payload, parts[index], revision)
				if err != nil {
					return prepared, err
				}
				appendRetainedTile(&prepared.tile, tile)
				prepared.count += row.PointCount.Int32
				prepared.generation = min(prepared.generation, row.SourceGeneration)
			}
			if prepared.count > 5760 {
				return prepared, ErrOperationalRangeDense
			}
			lastAt := prepared.tile.Budget[len(prepared.tile.Budget)-1].EvaluationAt
			prepared.last, err = retainedTileClosingValue(ctx, q, work, lastAt)
			if err != nil {
				return prepared, err
			}
			prepared.hourly = true
			prepared.tile, err = compactRetainedTile(prepared.tile, work.RangeStart.Time.UTC(), work.RangeEnd.Time.UTC(), target)
			return prepared, err
		}
	}
	rows, err := q.ReadRetainedTileCandidates(ctx, dbgen.ReadRetainedTileCandidatesParams{SloID: work.SloID,
		DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision,
		CalculationVersion: work.CalculationVersion, EvaluationAt: work.RangeStart, EvaluationAt_2: work.RangeEnd})
	if err != nil {
		return prepared, err
	}
	if len(rows) > 5760 {
		return prepared, ErrOperationalRangeDense
	}
	for _, row := range rows {
		prepared.generation = min(prepared.generation, row.SourceGeneration)
	}
	prepared.count = int32(len(rows))
	if work.ResolutionSeconds == 86400 && len(rows) > 0 {
		prepared.last, err = retainedTileClosingValue(ctx, q, work, rows[len(rows)-1].EvaluationAt.Time)
		if err != nil {
			return prepared, err
		}
	}
	prepared.tile, err = compactRetainedPayloads(rows, work.RangeStart.Time.UTC(), work.RangeEnd.Time.UTC(), target, revision)
	return prepared, err
}

func retainedTileClosingValue(ctx context.Context, q *dbgen.Queries, work dbgen.AlmanacSloSeriesTileWork, at time.Time) (*api.SloOperationalValue, error) {
	row, err := q.ReadRetainedSloPoint(ctx, dbgen.ReadRetainedSloPointParams{SloID: work.SloID, DefinitionRevision: work.DefinitionRevision,
		MeasurementRevision: work.MeasurementRevision, CalculationVersion: work.CalculationVersion, EvaluationAt: timestamp(at)})
	if err != nil {
		return nil, err
	}
	var point retainedSloPoint
	if err := json.Unmarshal(row.Payload, &point); err != nil || !point.Current.EvaluationAt.Equal(at) {
		return nil, ErrDerivedFailed
	}
	return &point.Current, nil
}
