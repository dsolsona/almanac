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
		var nextDue time.Time
		nextMetrics, nextDue = r.runDerivedTurn(ctx, logger, time.Now, nextMetrics)
		if !waitDerived(ctx, nextDue) {
			return
		}
	}
}

// runDerivedTurn returns the next metrics deadline and scheduler wakeup.
func (r *Runtime) runDerivedTurn(ctx context.Context, logger *slog.Logger, clock func() time.Time, nextMetrics time.Time) (time.Time, time.Time) {
	now := clock().UTC()
	conn, err := r.Acquire(ctx, DerivedTraffic)
	if err != nil {
		if ctx.Err() == nil {
			logger.ErrorContext(ctx, "derived scheduling failed", "error", derivedErrorDetail(err))
		}
		return nextMetrics, now.Add(time.Second)
	}
	due, err := dbgen.New(conn).ReadDerivedDue(ctx, dbgen.ReadDerivedDueParams{Now: timestamp(now), CalculationVersion: operationalCalculationVersion, MetricsAt: timestamp(nextMetrics)})
	conn.Release()
	if err != nil {
		if ctx.Err() == nil {
			logger.ErrorContext(ctx, "derived scheduling failed", "error", derivedErrorDetail(err))
		}
		return nextMetrics, now.Add(time.Second)
	}
	moreSource := false
	if due.AggregateDue {
		count, sourceErr := r.rebuildDerivedBatch(ctx, clock)
		if sourceErr != nil && ctx.Err() == nil && !derivedTemporary(ctx, sourceErr) {
			r.metrics.preparationErrors[0].Add(1)
			logger.ErrorContext(ctx, "derived aggregate rebuild failed", "error", derivedErrorDetail(sourceErr))
		}
		moreSource = count == 100 && sourceErr == nil
		// A source batch can publish prerequisites or cross a current deadline.
		// Re-read eligibility after releasing its connection, even on failure.
		now = clock().UTC()
		conn, err := r.Acquire(ctx, DerivedTraffic)
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived scheduling failed", "error", derivedErrorDetail(err))
			}
			return nextMetrics, now.Add(time.Second)
		}
		due, err = dbgen.New(conn).ReadDerivedDue(ctx, dbgen.ReadDerivedDueParams{Now: timestamp(now), CalculationVersion: operationalCalculationVersion, MetricsAt: timestamp(nextMetrics)})
		conn.Release()
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "derived scheduling failed", "error", derivedErrorDetail(err))
			}
			return nextMetrics, now.Add(time.Second)
		}
	}
	if due.CurrentDue {
		if err := r.refreshCurrentProjections(ctx, clock); err != nil && ctx.Err() == nil && !derivedTemporary(ctx, err) {
			logger.ErrorContext(ctx, "current SLO preparation failed", "error", derivedErrorDetail(err))
		}
	}
	if due.MetricsDue {
		if err := r.sampleDerivedMetrics(ctx, now); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "derived metrics sampling failed", "error", derivedErrorDetail(err))
		}
		nextMetrics = now.Add(30 * time.Second)
	}
	// A full source batch must not skip current work or metrics before draining more.
	if moreSource {
		return nextMetrics, now
	}
	return nextMetrics, maxTime(due.NextDue.Time, now.Add(10*time.Millisecond))
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
