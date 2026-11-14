package storage

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
)

// All options are explicit internal admission inputs. Production registration
// and public retry defaults require their own acceptance; this turn is unwired.
type alertProcessorOptions struct {
	MaxPolicies, Concurrency                   int
	Lease, Horizon, PendingRetry, FailureRetry time.Duration
	MaxAttempts                                int32
	Limits                                     alert.Limits
}

type alertProcessorResult struct {
	Claimed, Evaluations, Repeated, Gaps, Pending, Failed, Stale int
}

func (options alertProcessorOptions) validate() bool {
	if options.MaxPolicies <= 0 || options.MaxPolicies > 200 || options.Concurrency <= 0 || options.Concurrency > options.MaxPolicies || options.MaxAttempts <= 0 || options.Limits.MaxConditions <= 0 || options.Limits.MaxDecimalBytes <= 0 {
		return false
	}
	for _, duration := range []time.Duration{options.Lease, options.Horizon, options.PendingRetry, options.FailureRetry} {
		if duration <= 0 || duration%time.Microsecond != 0 {
			return false
		}
	}
	return true
}

// processAlertTick executes bounded waves, claiming only workers that can start
// now. PostgreSQL preserves ordered heads and one attempt per policy per tick.
func (r *Runtime) processAlertTick(ctx context.Context, tick time.Time, options alertProcessorOptions, clock func() time.Time) (alertProcessorResult, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "alert.processor.turn")
	defer span.End()
	var total alertProcessorResult
	if tick.IsZero() || clock == nil || !options.validate() {
		return total, ErrAlertInvalid
	}
	var failures []error
	for total.Claimed < options.MaxPolicies {
		if err := ctx.Err(); err != nil {
			return total, errors.Join(append(failures, err)...)
		}
		count := min(options.Concurrency, options.MaxPolicies-total.Claimed)
		claims, err := r.claimAlertWork(ctx, tick, normalizedAgentNow(clock), options.Lease, int32(count))
		if err != nil {
			return total, errors.Join(append(failures, err)...)
		}
		if len(claims) == 0 {
			break
		}
		total.Claimed += len(claims)
		outcomes := make([]struct {
			kind string
			err  error
		}, len(claims))
		var wave sync.WaitGroup
		for index, claim := range claims {
			wave.Go(func() { outcomes[index].kind, outcomes[index].err = r.processAlertClaim(ctx, claim, options, clock) })
		}
		wave.Wait()
		for _, outcome := range outcomes {
			switch outcome.kind {
			case "evaluation":
				total.Evaluations++
			case "repeat":
				total.Repeated++
			case "gap":
				total.Gaps++
			case "pending":
				total.Pending++
			case "failed":
				total.Failed++
			case "stale":
				total.Stale++
			}
			if outcome.err != nil {
				failures = append(failures, outcome.err)
			}
		}
	}
	return total, errors.Join(failures...)
}

func (r *Runtime) processAlertClaim(ctx context.Context, claim alertClaim, options alertProcessorOptions, clock func() time.Time) (string, error) {
	kind, err := r.evaluateAlertClaim(ctx, claim, options, clock)
	if err == nil {
		return kind, nil
	}
	if ctx.Err() != nil {
		return "stale", err
	} // expiry/restart recovers the claim
	if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrAlertStale) {
		reason := "aggregate_pending"
		if errors.Is(err, ErrAlertExclusionsPending) {
			reason = "exclusion_calculation_pending"
		}
		if errors.Is(err, ErrAlertStale) {
			reason = "snapshot_changed"
		}
		deferred := r.deferAlertWork(ctx, claim, clock, options.PendingRetry, reason)
		if errors.Is(deferred, ErrAlertStale) {
			return "stale", nil
		}
		if deferred != nil {
			return "failed", deferred
		}
		return "pending", nil
	}
	reason := "evaluation_failed"
	if errors.Is(err, ErrDerivedSourceFailed) {
		reason = "calculation_failed"
	}
	// Store a safe code, never a raw database/source error or datasource query.
	failed := r.failAlertWork(ctx, claim, options, clock, reason)
	if errors.Is(failed, ErrAlertStale) {
		return "stale", err
	}
	return "failed", errors.Join(err, failed)
}

func (r *Runtime) evaluateAlertClaim(ctx context.Context, claim alertClaim, options alertProcessorOptions, clock func() time.Time) (string, error) {
	if skipped, err := r.recordAlertGap(ctx, claim, clock, options.Horizon); err != nil {
		return "", err
	} else if skipped {
		return "gap", nil
	}
	cutoff, err := r.pinAlertCutoff(ctx, claim, clock)
	if err != nil {
		return "", err
	}
	var cursor dbgen.AlmanacAlertCursor
	err = r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
		id, _ := canonicalUUID(claim.PolicyID)
		var err error
		cursor, err = q.ReadAlertCursor(ctx, dbgen.ReadAlertCursorParams{PolicyID: id, Revision: claim.Revision})
		return err
	})
	if err != nil {
		return "", err
	}
	if cursor.LastCutoff.Valid && cursor.LastCutoff.Time.Equal(cutoff) {
		return "repeat", r.recordAlertRepeatedCutoff(ctx, claim, cutoff, clock)
	}
	prepared, err := r.readAlertEvidence(ctx, claim, cutoff, options.Limits, clock)
	if err != nil {
		return "", err
	}
	_, err = r.commitAlertEvaluation(ctx, claim, prepared.Input, options.Limits, clock)
	return "evaluation", err
}

func (r *Runtime) failAlertWork(ctx context.Context, claim alertClaim, options alertProcessorOptions, clock func() time.Time, reason string) error {
	if !validAlertClaim(claim) || clock == nil || !options.validate() {
		return ErrAlertInvalid
	}
	return r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
		id, _ := canonicalUUID(claim.PolicyID)
		cursor, err := q.LockAlertCursor(ctx, dbgen.LockAlertCursorParams{PolicyID: id, Revision: claim.Revision})
		if err != nil {
			return err
		}
		now := normalizedAgentNow(clock)
		if now.IsZero() || !cursor.ClaimUntil.Valid || !cursor.ClaimUntil.Time.After(now) || cursor.Exhausted {
			return ErrAlertStale
		}
		attempts := cursor.FailureAttempts + 1
		n, err := q.FailAlertCursor(ctx, dbgen.FailAlertCursorParams{PolicyID: id, Revision: claim.Revision, FailureAttempts: attempts, Now: timestamp(now), Exhausted: attempts >= options.MaxAttempts, AvailableAt: timestamp(now.Add(options.FailureRetry)), WaitingReason: reason, Epoch: claim.Epoch, ClaimToken: claim.Token, Version: claim.Version, NextScheduledAt: timestamp(claim.ScheduledAt)})
		if err != nil {
			return err
		}
		if n != 1 || !cursor.ClaimUntil.Time.After(normalizedAgentNow(clock)) {
			return ErrAlertStale
		}
		return nil
	})
}
