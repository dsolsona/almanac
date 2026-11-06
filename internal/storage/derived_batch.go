package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
)

// rebuildDerivedBatch commits exact aggregates before acknowledging work. Dirty
// rows are never locked for the duration of the calculation. A newer generation
// survives the separate conditional acknowledgment, including after a crash.
func (r *Runtime) rebuildDerivedBatch(ctx context.Context, clock func() time.Time) (count int, resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "derived.source.batch")
	defer func() {
		state := "published"
		if count == 0 {
			state = "idle"
		}
		span.SetAttributes(attribute.Int("almanac.batch.count", count))
		finishDerivedTrace(ctx, span, resultErr, state)
	}()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	q := dbgen.New(conn)
	jobs, err := q.ClaimDerivedDirtyBatch(bounded, dbgen.ClaimDerivedDirtyBatchParams{Now: timestamp(clock()), LeaseUntil: timestamp(clock().Add(30 * time.Second)), BatchLimit: 100})
	if err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		return 0, nil
	}
	ids, revisions, generations, epochs, starts := make([]pgtype.UUID, len(jobs)), make([]int64, len(jobs)), make([]int64, len(jobs)), make([]int64, len(jobs)), make([]pgtype.Timestamptz, len(jobs))
	for index, job := range jobs {
		ids[index], revisions[index], generations[index], epochs[index], starts[index] = job.SliID, job.MeasurementRevision, job.Generation, job.LeaseEpoch, job.RangeStart
	}
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if derivedTemporary(ctx, resultErr) {
			err := dbgen.New(conn).DeferDerivedBatch(cleanup, dbgen.DeferDerivedBatchParams{SliIds: ids, Revisions: revisions, Epochs: epochs, Starts: starts,
				NextAttemptAt: timestamp(clock().Add(time.Second)), Reason: "temporary"})
			resultErr = errors.Join(ErrDerivedPending, resultErr, err)
			return
		}
		err := dbgen.New(conn).FailDerivedBatch(cleanup, dbgen.FailDerivedBatchParams{SliIds: ids, Revisions: revisions, Epochs: epochs, Starts: starts, Now: timestamp(clock()), Reason: derivedFailureReason(resultErr, "aggregate_rebuild_failed")})
		resultErr = errors.Join(resultErr, err)
	}()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return len(jobs), err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return len(jobs), err
	}
	incomplete, err := dbgen.New(tx).RebuildDerivedBatch(bounded, dbgen.RebuildDerivedBatchParams{SliIds: ids, Revisions: revisions, Generations: generations, Starts: starts})
	if err != nil {
		return len(jobs), err
	}
	if err := tx.Commit(bounded); err != nil {
		return len(jobs), err
	}
	// Hours whose observation values are not committed yet stay queued and are retried shortly
	// (ADR-0034); the rest are acknowledged as rebuilt.
	waiting := make(map[derivedHourKey]bool, len(incomplete))
	for _, hour := range incomplete {
		waiting[derivedHourKey{hour.SliID, hour.MeasurementRevision, hour.HourStart.Time.UTC()}] = true
	}
	var done, deferred derivedBatchKeys
	for index, job := range jobs {
		target := &done
		if waiting[derivedHourKey{job.SliID, job.MeasurementRevision, job.RangeStart.Time.UTC()}] {
			target = &deferred
		}
		target.add(ids[index], revisions[index], generations[index], epochs[index], starts[index])
	}
	if len(deferred.ids) > 0 {
		if err := q.DeferDerivedBatch(bounded, dbgen.DeferDerivedBatchParams{SliIds: deferred.ids, Revisions: deferred.revisions, Epochs: deferred.epochs, Starts: deferred.starts,
			NextAttemptAt: timestamp(clock().Add(2 * time.Second)), Reason: "observation_values_pending"}); err != nil {
			return len(jobs), err
		}
	}
	if len(done.ids) > 0 {
		if err := q.AcknowledgeDerivedBatch(bounded, dbgen.AcknowledgeDerivedBatchParams{SliIds: done.ids, Revisions: done.revisions, Generations: done.generations, Epochs: done.epochs, Starts: done.starts}); err != nil {
			return len(jobs), err
		}
	}
	r.metrics.sourceKeys.Add(uint64(len(jobs)))
	return len(jobs), nil
}

type derivedHourKey struct {
	sli      pgtype.UUID
	revision int64
	start    time.Time
}

type derivedBatchKeys struct {
	ids                            []pgtype.UUID
	revisions, generations, epochs []int64
	starts                         []pgtype.Timestamptz
}

func (k *derivedBatchKeys) add(id pgtype.UUID, revision, generation, epoch int64, start pgtype.Timestamptz) {
	k.ids, k.revisions, k.generations = append(k.ids, id), append(k.revisions, revision), append(k.generations, generation)
	k.epochs, k.starts = append(k.epochs, epoch), append(k.starts, start)
}
