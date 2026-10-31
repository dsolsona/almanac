package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// rebuildDerivedBatch commits exact aggregates before acknowledging work. Dirty
// rows are never locked for the duration of the calculation. A newer generation
// survives the separate conditional acknowledgment, including after a crash.
func (r *Runtime) rebuildDerivedBatch(ctx context.Context, clock func() time.Time) (count int, resultErr error) {
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
		err := dbgen.New(conn).FailDerivedBatch(cleanup, dbgen.FailDerivedBatchParams{SliIds: ids, Revisions: revisions, Epochs: epochs, Starts: starts, Now: timestamp(clock())})
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
	if err := dbgen.New(tx).RebuildDerivedBatch(bounded, dbgen.RebuildDerivedBatchParams{SliIds: ids, Revisions: revisions, Generations: generations, Starts: starts}); err != nil {
		return len(jobs), err
	}
	if err := tx.Commit(bounded); err != nil {
		return len(jobs), err
	}
	if err := q.AcknowledgeDerivedBatch(bounded, dbgen.AcknowledgeDerivedBatchParams{SliIds: ids, Revisions: revisions, Generations: generations, Epochs: epochs, Starts: starts}); err != nil {
		return len(jobs), err
	}
	r.metrics.sourceKeys.Add(uint64(len(jobs)))
	return len(jobs), nil
}
