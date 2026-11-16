//go:build alertacceptance

package storage

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
)

// AlertAcceptanceConfig belongs only to the opt-in acceptance binary. The
// fixture supplies canonical retention authority; PauseFile stops only alert
// evaluation while source ingestion and shared preparation continue normally.
type AlertAcceptanceConfig struct {
	RetentionStart  time.Time
	PauseFile       string
	SourcePauseFile string
}

// This is a pinned experimental profile, not production configuration. All
// turns use the existing derived pool (two connections), publication fences,
// retry ownership and five-second statement/operation ceilings unchanged.
//
// Shared reads: 1,000 source hours, 20,000 raw rows/fragments, 16 preparation
// requests, 1,000 source hours/request admission and 960 retained proofs.
// Alert turns: 200 policies, two workers, 30s lease, 24h horizon, 1s pending
// retry, fixed 2s failure retry and five attempts; eight conditions/96 decimals.
// Preparation: 32 live/16 historical jobs, two workers, 64 events/page, 30s
// lease, 5s operation, 1s pending/2s failure retry and five attempts.
// Authority: 16 claims, 64 changes, 24 buckets/scan hours, with the same lease,
// operation, retry and attempt bounds. Each loop starts at most once per second.
// All of these candidate inputs are logged before any processor starts.
// Genuine engine failure retry is fixed, as supported by the current processor;
// this does not claim the proposed production exponential backoff is delivered.
func (r *Runtime) StartAlertAcceptance(ctx context.Context, logger *slog.Logger, cfg AlertAcceptanceConfig) (stop func(), err error) {
	return r.startAlertAcceptance(ctx, logger, cfg, time.Now)
}

func (r *Runtime) startAlertAcceptance(ctx context.Context, logger *slog.Logger, cfg AlertAcceptanceConfig, clock func() time.Time) (func(), error) {
	if r == nil || ctx == nil || logger == nil || clock == nil || !validMaintenanceTime(cfg.RetentionStart) || r.pools[DerivedTraffic] == nil || !r.compatible.Load() {
		return nil, ErrAlertInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	windows := maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}
	admission := maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}
	consumer := &maintenanceConsumerOptions{Windows: windows, Preparation: admission, MaxRetainedProofs: 960}
	consumer.pauseSource = func() bool {
		paused, err := alertAcceptancePaused(cfg.SourcePauseFile)
		if err != nil {
			logger.WarnContext(ctx, "alert acceptance source paused", "reason", "pause_file_unreadable")
		}
		return paused
	}
	processor := alertProcessorOptions{MaxPolicies: 200, Concurrency: 2, Lease: 30 * time.Second, Horizon: 24 * time.Hour, PendingRetry: time.Second, FailureRetry: 2 * time.Second, MaxAttempts: 5, Limits: alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}, Maintenance: &alertMaintenanceOptions{Windows: windows, Preparation: admission}}
	preparation := maintenancePreparationTurnOptions{MaxLive: 32, MaxHistorical: 16, Concurrency: 2, PageLimit: 64, Lease: 30 * time.Second, OperationBudget: 5 * time.Second, PendingRetry: time.Second, FailureRetry: 2 * time.Second, MaxAttempts: 5, RetentionStart: cfg.RetentionStart}
	authority := maintenanceAuthorityOptions{MaxClaims: 16, MaxChanges: 64, MaxBuckets: 24, MaxScanHours: 24, Lease: 30 * time.Second, OperationBudget: 5 * time.Second, PendingRetry: time.Second, FailureRetry: 2 * time.Second, MaxAttempts: 5, RetentionStart: cfg.RetentionStart}
	if !processor.validate() || !preparation.valid() || !authority.valid() {
		return nil, ErrAlertInvalid
	}
	// Reject duplicate startup or another owner. Requests may use the profile as
	// soon as this succeeds; shutdown removes only this exact owner's profile.
	if !r.maintenanceConsumers.CompareAndSwap(nil, consumer) {
		return nil, ErrAlertInvalid
	}
	logger.InfoContext(ctx, "alert acceptance candidate installed", "profile", "alertacceptance-v1", "tick", time.Second, "source_hours", windows.MaxSourceHours, "raw_rows", windows.MaxRawRows, "fragments", windows.MaxFragments, "preparation_requests", admission.MaxRequests, "preparation_source_hours", admission.MaxSourceHours, "retained_proofs", consumer.MaxRetainedProofs, "retention_start", cfg.RetentionStart.UTC())
	logger.InfoContext(ctx, "alert acceptance processor bounds", "policies", processor.MaxPolicies, "workers", processor.Concurrency, "lease", processor.Lease, "horizon", processor.Horizon, "pending_retry", processor.PendingRetry, "failure_retry", processor.FailureRetry, "attempts", processor.MaxAttempts, "conditions", processor.Limits.MaxConditions, "decimal_bytes", processor.Limits.MaxDecimalBytes)
	logger.InfoContext(ctx, "alert acceptance preparation bounds", "live_jobs", preparation.MaxLive, "historical_jobs", preparation.MaxHistorical, "workers", preparation.Concurrency, "page_limit", preparation.PageLimit, "lease", preparation.Lease, "operation", preparation.OperationBudget, "pending_retry", preparation.PendingRetry, "failure_retry", preparation.FailureRetry, "attempts", preparation.MaxAttempts)
	logger.InfoContext(ctx, "alert acceptance authority bounds", "claims", authority.MaxClaims, "changes", authority.MaxChanges, "buckets", authority.MaxBuckets, "scan_hours", authority.MaxScanHours, "lease", authority.Lease, "operation", authority.OperationBudget, "pending_retry", authority.PendingRetry, "failure_retry", authority.FailureRetry, "attempts", authority.MaxAttempts)
	life, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	run := func(kind string, turn func(context.Context, time.Time) error) {
		workers.Go(func() {
			timer := time.NewTicker(time.Second)
			defer timer.Stop()
			for life.Err() == nil {
				tick := clock().UTC().Truncate(time.Second)
				if err := turn(life, tick); err != nil && life.Err() == nil {
					// Do not log database/source errors, file paths or credentials.
					logger.WarnContext(life, "alert acceptance turn deferred", "component", kind, "reason", "turn_failed")
				}
				select {
				case <-life.Done():
					return
				case <-timer.C:
				}
			}
		})
	}
	run("authority", func(ctx context.Context, tick time.Time) error {
		_, err := r.runMaintenanceAuthorityTurn(ctx, tick, authority, clock)
		return err
	})
	run("preparation", func(ctx context.Context, tick time.Time) error {
		_, err := r.runMaintenancePreparationTurn(ctx, tick, preparation, clock)
		return err
	})
	run("evaluation", func(ctx context.Context, tick time.Time) error {
		paused, err := alertAcceptancePaused(cfg.PauseFile)
		if err != nil {
			logger.WarnContext(ctx, "alert acceptance evaluation paused", "reason", "pause_file_unreadable")
			return nil
		}
		if paused {
			return nil
		}
		_, err = r.processAlertTick(ctx, tick, processor, clock)
		return err
	})
	done := make(chan struct{})
	go func() {
		workers.Wait()
		r.maintenanceConsumers.CompareAndSwap(consumer, nil)
		close(done)
	}()
	var once sync.Once
	return func() { once.Do(cancel); <-done }, nil
}

func alertAcceptancePaused(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return true, err
}
