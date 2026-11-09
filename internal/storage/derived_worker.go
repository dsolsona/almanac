package storage

import (
	"context"
	"log/slog"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

// RunDerived drains bounded source and current work only when it can advance.
func (r *Runtime) RunDerived(ctx context.Context, logger *slog.Logger) {
	var nextMetrics time.Time
	for ctx.Err() == nil {
		now := time.Now().UTC()
		conn, err := r.Acquire(ctx, DerivedTraffic)
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived scheduling failed", "error", derivedErrorDetail(err))
			}
			if !waitDerived(ctx, now.Add(time.Second)) {
				return
			}
			continue
		}
		due, err := dbgen.New(conn).ReadDerivedDue(ctx, dbgen.ReadDerivedDueParams{Now: timestamp(now), CalculationVersion: operationalCalculationVersion, MetricsAt: timestamp(nextMetrics)})
		conn.Release()
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived scheduling failed", "error", derivedErrorDetail(err))
			}
			if !waitDerived(ctx, now.Add(time.Second)) {
				return
			}
			continue
		}
		if due.CurrentDue {
			if err := r.refreshCurrentProjections(ctx, time.Now); err != nil && ctx.Err() == nil && !derivedTemporary(ctx, err) {
				logger.ErrorContext(ctx, "current SLO preparation failed", "error", derivedErrorDetail(err))
			}
		}
		if due.MetricsDue {
			if err := r.sampleDerivedMetrics(ctx, now); err != nil && ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived metrics sampling failed", "error", derivedErrorDetail(err))
			}
			nextMetrics = now.Add(30 * time.Second)
		}
		if due.AggregateDue {
			count, err := r.rebuildDerivedBatch(ctx, time.Now)
			if err != nil && ctx.Err() == nil && !derivedTemporary(ctx, err) {
				r.metrics.preparationErrors[0].Add(1)
				logger.ErrorContext(ctx, "derived aggregate rebuild failed", "error", derivedErrorDetail(err))
			}
			if count == 100 && err == nil {
				continue
			}
		}
		if !waitDerived(ctx, maxTime(due.NextDue.Time, now.Add(10*time.Millisecond))) {
			return
		}
	}
}

func waitDerived(ctx context.Context, until time.Time) bool {
	timer := time.NewTimer(min(time.Second, max(0, time.Until(until))))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
