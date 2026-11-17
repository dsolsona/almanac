package storage

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/sli"
)

// StartProcessing owns the shared maintenance/calculation processors and, when
// selected, the alert evaluator. It uses the existing derived pool and joins all
// its workers on shutdown. The backend owner must stop it before closing pools.
func (r *Runtime) StartProcessing(ctx context.Context, logger *slog.Logger, alerts bool) (func(), error) {
	return r.startProcessing(ctx, logger, alerts, processingControls{}, time.Now)
}

// Only the tagged acceptance adapter supplies pause controls or a fixed cutoff.
// Ordinary operation follows the same 365-day canonical retention policy as
// source cleanup and current calculations, normalized to database precision.
type processingControls struct {
	RetentionStart  time.Time
	PauseSource     func() bool
	PauseEvaluation func() bool
}

func (c processingControls) retentionStart(now time.Time) time.Time {
	if !c.RetentionStart.IsZero() {
		return c.RetentionStart
	}
	return now.UTC().Truncate(time.Microsecond).Add(-sli.ObservationRetention)
}

func (r *Runtime) startProcessing(ctx context.Context, logger *slog.Logger, alerts bool, controls processingControls, clock func() time.Time) (func(), error) {
	if r == nil || ctx == nil || logger == nil || clock == nil || (!controls.RetentionStart.IsZero() && !validMaintenanceTime(controls.RetentionStart)) || r.pools[DerivedTraffic] == nil || !r.compatible.Load() {
		return nil, ErrAlertInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	windows := maintenanceWindowLimits{MaxSourceHours: int(sli.ObservationRetention/time.Hour) + 2, MaxRawRows: sloOperationalFactsLimit, MaxFragments: sloOperationalFactsLimit}
	admission := maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: windows.MaxSourceHours}
	consumer := &maintenanceConsumerOptions{Windows: windows, Preparation: admission, MaxRetainedProofs: derivedWindowBatchLimit}
	consumer.pauseSource = controls.PauseSource
	processor := alertProcessorOptions{MaxPolicies: 200, Concurrency: 2, Lease: 30 * time.Second, Horizon: 24 * time.Hour, PendingRetry: time.Second, FailureRetry: 2 * time.Second, MaxAttempts: 5, Limits: alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}, Maintenance: &alertMaintenanceOptions{Windows: windows, Preparation: admission}}
	initialTick := clock().UTC().Truncate(time.Second)
	preparation := maintenancePreparationTurnOptions{MaxLive: 32, MaxHistorical: 16, Concurrency: 2, PageLimit: 64, Lease: 30 * time.Second, OperationBudget: 5 * time.Second, PendingRetry: time.Second, FailureRetry: 2 * time.Second, MaxAttempts: 5, RetentionStart: controls.retentionStart(initialTick)}
	authority := maintenanceAuthorityOptions{MaxClaims: 16, MaxChanges: 64, MaxBuckets: 24, MaxScanHours: 24, Lease: 30 * time.Second, OperationBudget: 5 * time.Second, PendingRetry: time.Second, FailureRetry: 2 * time.Second, MaxAttempts: 5, RetentionStart: controls.retentionStart(initialTick)}
	if !processor.validate() || !preparation.valid() || !authority.valid() {
		return nil, ErrAlertInvalid
	}
	// Reject duplicate startup or another owner. Requests may use the profile as
	// soon as this succeeds; shutdown removes only this exact owner's profile.
	if !r.maintenanceConsumers.CompareAndSwap(nil, consumer) {
		return nil, ErrAlertInvalid
	}
	logger.InfoContext(ctx, "processing runtime installed", "profile", "runtime-v1", "alerts", alerts, "tick", time.Second, "source_hours", windows.MaxSourceHours, "raw_rows", windows.MaxRawRows, "fragments", windows.MaxFragments, "preparation_requests", admission.MaxRequests, "preparation_source_hours", admission.MaxSourceHours, "retained_proofs", consumer.MaxRetainedProofs, "retention_start", preparation.RetentionStart)
	logger.InfoContext(ctx, "alert processor bounds", "policies", processor.MaxPolicies, "workers", processor.Concurrency, "lease", processor.Lease, "horizon", processor.Horizon, "pending_retry", processor.PendingRetry, "failure_retry", processor.FailureRetry, "attempts", processor.MaxAttempts, "conditions", processor.Limits.MaxConditions, "decimal_bytes", processor.Limits.MaxDecimalBytes)
	const retentionBatch = 200
	const retentionInterval = 10 * time.Second
	if alerts {
		logger.InfoContext(ctx, "alert retention bounds", "batch_per_stage", retentionBatch, "tick", retentionInterval, "compact_after", 24*time.Hour, "history", sli.ObservationRetention)
	}
	logger.InfoContext(ctx, "preparation processor bounds", "live_jobs", preparation.MaxLive, "historical_jobs", preparation.MaxHistorical, "workers", preparation.Concurrency, "page_limit", preparation.PageLimit, "lease", preparation.Lease, "operation", preparation.OperationBudget, "pending_retry", preparation.PendingRetry, "failure_retry", preparation.FailureRetry, "attempts", preparation.MaxAttempts)
	logger.InfoContext(ctx, "maintenance authority bounds", "claims", authority.MaxClaims, "changes", authority.MaxChanges, "buckets", authority.MaxBuckets, "scan_hours", authority.MaxScanHours, "lease", authority.Lease, "operation", authority.OperationBudget, "pending_retry", authority.PendingRetry, "failure_retry", authority.FailureRetry, "attempts", authority.MaxAttempts)
	life, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	run := func(kind string, interval time.Duration, turn func(context.Context, time.Time) error) {
		workers.Go(func() {
			timer := time.NewTicker(interval)
			defer timer.Stop()
			for life.Err() == nil {
				tick := clock().UTC().Truncate(time.Second)
				if err := turn(life, tick); err != nil && life.Err() == nil {
					// Calculation failures contain no credential payload. Preserve the
					// bounded cause just as the existing derived workers do.
					logger.WarnContext(life, "processing turn deferred", "component", kind, "error", derivedErrorDetail(err))
				}
				select {
				case <-life.Done():
					return
				case <-timer.C:
				}
			}
		})
	}
	run("authority", time.Second, func(ctx context.Context, tick time.Time) error {
		options := authority
		options.RetentionStart = controls.retentionStart(tick)
		_, err := r.runMaintenanceAuthorityTurn(ctx, tick, options, clock)
		return err
	})
	run("preparation", time.Second, func(ctx context.Context, tick time.Time) error {
		options := preparation
		options.RetentionStart = controls.retentionStart(tick)
		_, err := r.runMaintenancePreparationTurn(ctx, tick, options, clock)
		return err
	})
	if alerts {
		run("retention", retentionInterval, func(ctx context.Context, tick time.Time) error {
			options := alertRetentionOptions{Batch: retentionBatch, CompactBefore: tick.Add(-24 * time.Hour), HistoryBefore: tick.Add(-sli.ObservationRetention)}
			_, err := r.maintainAlertEvidence(ctx, options, clock)
			return err
		})
		run("evaluation", time.Second, func(ctx context.Context, tick time.Time) error {
			if controls.PauseEvaluation != nil && controls.PauseEvaluation() {
				return nil
			}
			_, err := r.processAlertTick(ctx, tick, processor, clock)
			return err
		})
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		r.maintenanceConsumers.CompareAndSwap(consumer, nil)
		close(done)
	}()
	var once sync.Once
	return func() { once.Do(cancel); <-done }, nil
}
