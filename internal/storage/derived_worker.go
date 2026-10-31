package storage

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const derivedChunkHours = 100

var errNoDerivedWork = errors.New("storage: no derived work due")

// RunDerived maintains exact source aggregates with at most one active drain per
// runtime owner. Failed work stays durable; a restart does not reset its budget.
func (r *Runtime) RunDerived(ctx context.Context, logger *slog.Logger) {
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	var nextCurrent time.Time
	var nextMetrics time.Time
	for {
		if !time.Now().Before(nextCurrent) {
			if err := r.refreshCurrentProjections(ctx, time.Now); err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "current SLO preparation failed")
			}
			nextCurrent = time.Now().Add(time.Second)
		}
		if !time.Now().Before(nextMetrics) {
			if err := r.sampleDerivedMetrics(ctx, time.Now()); err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived metrics sampling failed")
			}
			nextMetrics = time.Now().Add(30 * time.Second)
		}
		count, err := r.rebuildDerivedBatch(ctx, time.Now)
		if err != nil && ctx.Err() == nil {
			r.metrics.preparationErrors[0].Add(1)
			logger.ErrorContext(ctx, "derived aggregate rebuild failed")
		}
		if count == 100 && err == nil && ctx.Err() == nil {
			// Drain bounded batches without imposing a one-second sleep on each
			// hundred keys. Current work retains its independent one-second turn.
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks.C:
		}
	}
}

func (r *Runtime) rebuildNextDerivedRange(ctx context.Context, clock func() time.Time) error {
	conn, err := r.Acquire(ctx, DerivedTraffic)
	if err != nil {
		return err
	}
	q := dbgen.New(conn)
	job, err := q.ClaimDerivedDirtyRange(ctx, dbgen.ClaimDerivedDirtyRangeParams{
		Now: timestamp(clock()), LeaseUntil: timestamp(clock().Add(30 * time.Second)),
	})
	conn.Release()
	if errors.Is(err, pgx.ErrNoRows) {
		return errNoDerivedWork
	}
	if err != nil {
		return operationError(ctx, "storage: derived claim failed", err)
	}

	start := job.RangeStart.Time.UTC().Truncate(time.Hour)
	end := job.RangeEnd.Time.UTC()
	for start.Before(end) {
		chunkEnd := start.Add(derivedChunkHours * time.Hour)
		if chunkEnd.After(end) {
			chunkEnd = end.Truncate(time.Hour).Add(time.Hour)
		}
		if err := r.rebuildDerivedChunk(ctx, job, start, chunkEnd, clock()); err != nil {
			// The request context may be cancelled. Persist retry state using an
			// independent bounded context, without keeping a transaction alive.
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			failureConn, acquireErr := r.Acquire(cleanup, DerivedTraffic)
			if acquireErr != nil {
				return errors.Join(err, acquireErr)
			}
			defer failureConn.Release()
			_, persistErr := dbgen.New(failureConn).FailDerivedDirtyRange(cleanup, dbgen.FailDerivedDirtyRangeParams{
				SliID: job.SliID, WorkStart: job.RangeStart, MeasurementRevision: job.MeasurementRevision, LeaseEpoch: job.LeaseEpoch,
				NextAttemptAt: timestamp(clock().Add(time.Duration(1<<min(job.Attempts, int32(5))) * time.Second)),
				LastError:     pgtype.Text{String: "aggregate_rebuild_failed", Valid: true},
			})
			return errors.Join(err, persistErr)
		}
		start = chunkEnd
	}
	conn, err = r.Acquire(ctx, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	q = dbgen.New(conn)
	deleted, err := q.AcknowledgeDerivedDirtyRange(ctx, dbgen.AcknowledgeDerivedDirtyRangeParams{
		SliID: job.SliID, WorkStart: job.RangeStart, MeasurementRevision: job.MeasurementRevision, Generation: job.Generation, LeaseEpoch: job.LeaseEpoch,
	})
	if err != nil {
		return operationError(ctx, "storage: derived acknowledgment failed", err)
	}
	if deleted == 0 {
		_, err = q.ReleaseDerivedDirtyRange(ctx, dbgen.ReleaseDerivedDirtyRangeParams{
			SliID: job.SliID, WorkStart: job.RangeStart, MeasurementRevision: job.MeasurementRevision, LeaseEpoch: job.LeaseEpoch,
		})
	}
	return err
}

func (r *Runtime) rebuildDerivedChunk(ctx context.Context, job dbgen.AlmanacDerivedDirtyRange, start, end, now time.Time) error {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	q := dbgen.New(conn)
	owned, err := q.RenewDerivedDirtyLease(bounded, dbgen.RenewDerivedDirtyLeaseParams{
		SliID: job.SliID, WorkStart: job.RangeStart, MeasurementRevision: job.MeasurementRevision, Generation: job.Generation,
		LeaseEpoch: job.LeaseEpoch, LeaseUntil: timestamp(now.Add(30 * time.Second)),
	})
	if err != nil {
		return err
	}
	if owned == 0 {
		return nil
	}
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return err
	}
	q = dbgen.New(tx)
	if err := q.RebuildDerivedHours(bounded, dbgen.RebuildDerivedHoursParams{
		SliID: job.SliID, MeasurementRevision: job.MeasurementRevision, SourceGeneration: job.Generation,
		RangeStart: timestamp(start), RangeEnd: timestamp(end),
	}); err != nil {
		return err
	}
	return tx.Commit(bounded)
}
