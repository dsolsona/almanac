package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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
		rows, err := q.ReadRetainedTilePoints(bounded, dbgen.ReadRetainedTilePointsParams{SloID: work.SloID,
			DefinitionRevision: work.DefinitionRevision, MeasurementRevision: work.MeasurementRevision,
			CalculationVersion: work.CalculationVersion, EvaluationAt: work.RangeStart, EvaluationAt_2: work.RangeEnd})
		if err != nil {
			return true, err
		}
		if len(rows) > 5760 {
			return true, ErrOperationalRangeDense
		}
		points := make([]retainedSloPoint, len(rows))
		for index, row := range rows {
			if err := json.Unmarshal(row.Payload, &points[index]); err != nil || !points[index].Current.EvaluationAt.Equal(row.EvaluationAt.Time) {
				return true, ErrDerivedFailed
			}
		}
		tile, err := compactRetainedPoints(points, work.RangeStart.Time.UTC(), work.RangeEnd.Time.UTC(), definition.Target, strconv.FormatInt(work.DefinitionRevision, 10))
		if err != nil {
			return true, err
		}
		payload, err := json.Marshal(tile)
		if err != nil {
			return true, err
		}
		if len(payload) > 20<<20 {
			return true, ErrOperationalResource
		}
		if _, err := q.PublishRetainedTile(bounded, dbgen.PublishRetainedTileParams{ID: work.ID, Version: work.Version,
			LeaseEpoch: work.LeaseEpoch, PublishedAt: timestamp(clock()), PointCount: int32(len(points)), Payload: payload}); err != nil {
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
