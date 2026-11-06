package storage

import (
	"context"
	"log/slog"
	"time"
)

// RunDerived maintains exact source aggregates with at most one active drain per
// runtime owner. Failed work stays durable; a restart does not reset its budget.
func (r *Runtime) RunDerived(ctx context.Context, logger *slog.Logger) {
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	var nextCurrent time.Time
	var nextMetrics time.Time
	for {
		if !time.Now().Before(nextCurrent) {
			if err := r.refreshCurrentProjections(ctx, time.Now); err != nil && ctx.Err() == nil && !derivedTemporary(ctx, err) {
				logger.ErrorContext(ctx, "current SLO preparation failed", "error", derivedErrorDetail(err))
			}
			nextCurrent = time.Now().Add(time.Second)
		}
		if !time.Now().Before(nextMetrics) {
			if err := r.sampleDerivedMetrics(ctx, time.Now()); err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived metrics sampling failed", "error", derivedErrorDetail(err))
			}
			nextMetrics = time.Now().Add(30 * time.Second)
		}
		count, err := r.rebuildDerivedBatch(ctx, time.Now)
		if err != nil && ctx.Err() == nil && !derivedTemporary(ctx, err) {
			r.metrics.preparationErrors[0].Add(1)
			logger.ErrorContext(ctx, "derived aggregate rebuild failed", "error", derivedErrorDetail(err))
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
