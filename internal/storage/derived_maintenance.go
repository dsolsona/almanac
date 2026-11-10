package storage

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
)

// RunDerivedMaintenance has its own schedule so continuous historical work
// cannot indefinitely retain expired snapshots. It shares the bounded derived
// pool and performs bounded, cancellable batches without adding connections.
func (r *Runtime) RunDerivedMaintenance(ctx context.Context, logger *slog.Logger) {
	var nextDiscovery, nextPartitions, nextConsistency time.Time
	for ctx.Err() == nil {
		now := time.Now().UTC()
		conn, err := r.Acquire(ctx, DerivedTraffic)
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived maintenance scheduling failed", "error", derivedErrorDetail(err))
			}
			if !waitDerived(ctx, now.Add(time.Second)) {
				return
			}
			continue
		}
		due, err := dbgen.New(conn).ReadDerivedMaintenanceDue(ctx, dbgen.ReadDerivedMaintenanceDueParams{Now: timestamp(now), DiscoveryAt: timestamp(nextDiscovery), PartitionsAt: timestamp(nextPartitions), ConsistencyAt: timestamp(nextConsistency)})
		conn.Release()
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived maintenance scheduling failed", "error", derivedErrorDetail(err))
			}
			if !waitDerived(ctx, now.Add(time.Second)) {
				return
			}
			continue
		}
		if due.CleanupDue {
			if err := r.cleanupDerivedSnapshots(ctx, now); err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived snapshot cleanup failed", "error", derivedErrorDetail(err))
			}
		}
		if due.PartitionsDue {
			if err := r.prepareObservationPartitions(ctx, now); err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "observation partition maintenance failed", "error", derivedErrorDetail(err))
				nextPartitions = now.Add(time.Second)
			} else {
				nextPartitions = now.Add(time.Hour)
			}
		}
		if due.DiscoveryDue {
			if err := r.discoverRetention(ctx, now); err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "retention discovery failed", "error", derivedErrorDetail(err))
			}
			nextDiscovery = now.Add(time.Minute)
		}
		if due.ConsistencyDue {
			r.reportDerivedConsistency(ctx, logger, now)
			nextConsistency = now.Add(time.Minute)
		}
		if due.RetentionDue {
			if _, err := r.applyRetentionPreview(ctx, now); err != nil && !errors.Is(err, errRetentionBlocked) && ctx.Err() == nil {
				logger.ErrorContext(ctx, "retention maintenance failed", "error", derivedErrorDetail(err))
			}
		}
		if !waitDerived(ctx, maxTime(due.NextDue.Time, now.Add(10*time.Millisecond))) {
			return
		}
	}
}

func (r *Runtime) prepareObservationPartitions(ctx context.Context, now time.Time) error {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'"); err != nil {
		return err
	}
	if err := prepareSliPartitions(bounded, tx, now); err != nil {
		return err
	}
	return tx.Commit(bounded)
}

func (r *Runtime) cleanupDerivedSnapshots(ctx context.Context, now time.Time) (resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "derived.maintenance")
	defer func() { finishDerivedTrace(ctx, span, resultErr, "cleanup") }()
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	q := dbgen.New(conn)
	if _, err := q.DeleteExpiredSloExtrema(bounded, timestamp(now.Add(-365*24*time.Hour))); err != nil {
		return err
	}
	if _, err := q.DeleteExpiredDerivedGenerations(bounded, timestamp(now.Add(-5*time.Minute))); err != nil {
		return err
	}
	return nil
}
