package storage

import (
	"context"
	"log/slog"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
)

// RunDerivedMaintenance has its own schedule so continuous historical work
// cannot indefinitely retain expired snapshots. It shares the bounded derived
// pool and performs bounded, cancellable batches without adding connections.
func (r *Runtime) RunDerivedMaintenance(ctx context.Context, logger *slog.Logger) {
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	for {
		if err := r.cleanupDerivedSnapshots(ctx, time.Now()); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "derived snapshot cleanup failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks.C:
		}
	}
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
	if err := q.DeleteExpiredSloOverviewSnapshots(bounded, dbgen.DeleteExpiredSloOverviewSnapshotsParams{ExpiresAt: timestamp(now), Limit: sloOverviewCleanupBatch}); err != nil {
		return err
	}
	if _, err := q.DeleteExpiredDerivedGenerations(bounded, timestamp(now.Add(-5*time.Minute))); err != nil {
		return err
	}
	if _, err := q.DeleteExpiredSloDisplayBlocks(bounded, timestamp(now.Add(-5*time.Minute))); err != nil {
		return err
	}
	_, err = q.DeleteExpiredSloProjectionPayloads(bounded, timestamp(now.Add(-5*time.Minute)))
	return err
}
