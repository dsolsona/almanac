package storage

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// Mandatory implementation bounds, not production defaults or registration.
// RetentionStart is supplied by the owner of canonical retention authority.
type maintenancePreparationTurnOptions struct {
	MaxLive, MaxHistorical, Concurrency, PageLimit     int
	Lease, OperationBudget, PendingRetry, FailureRetry time.Duration
	MaxAttempts                                        int32
	RetentionStart                                     time.Time
}
type maintenancePreparationTurnResult struct{ Claimed, Complete, Pending, Unavailable, Obsolete, Failed, Stale int }

func (o maintenancePreparationTurnOptions) valid() bool {
	if o.MaxLive < 0 || o.MaxHistorical < 0 || o.MaxLive+o.MaxHistorical < 1 || o.MaxLive > 1000 || o.MaxHistorical > 1000 || o.Concurrency < 1 || o.Concurrency > 2 || o.PageLimit < 1 || o.PageLimit > 1000 || o.MaxAttempts < 1 || o.MaxAttempts > 1000 || !validMaintenanceTime(o.RetentionStart) {
		return false
	}
	if o.Lease > 5*time.Minute || o.OperationBudget > 5*time.Second || o.OperationBudget > o.Lease || o.PendingRetry > 24*time.Hour || o.FailureRetry > 24*time.Hour {
		return false
	}
	for _, d := range []time.Duration{o.Lease, o.OperationBudget, o.PendingRetry, o.FailureRetry} {
		if d <= 0 || d%time.Microsecond != 0 {
			return false
		}
	}
	return true
}

// No queue row/connection survives into resource execution. Claim only the wave
// that can start immediately. Each priority has its explicit caller allowance.
func (r *Runtime) runMaintenancePreparationTurn(ctx context.Context, tick time.Time, o maintenancePreparationTurnOptions, clock func() time.Time) (maintenancePreparationTurnResult, error) {
	var total maintenancePreparationTurnResult
	if r == nil || clock == nil || tick.IsZero() || !o.valid() {
		return total, ErrMaintenanceInvalid
	}
	var failures []error
	for _, lane := range []struct {
		priority maintenancePreparationPriority
		limit    int
	}{{maintenancePreparationLive, o.MaxLive}, {maintenancePreparationHistorical, o.MaxHistorical}} {
		for claimed := 0; claimed < lane.limit; {
			if err := ctx.Err(); err != nil {
				return total, errors.Join(append(failures, err)...)
			}
			var jobs []dbgen.AlmanacMaintenancePreparation
			err := r.maintenancePreparationTransaction(ctx, o.OperationBudget, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				jobs, err = claimMaintenancePreparation(ctx, tx, lane.priority, tick, o.Lease, min(o.Concurrency, lane.limit-claimed), clock)
				return err
			})
			if err != nil {
				return total, errors.Join(append(failures, err)...)
			}
			if len(jobs) == 0 {
				break
			}
			claimed += len(jobs)
			total.Claimed += len(jobs)
			results := make([]struct {
				state string
				err   error
			}, len(jobs))
			var wave sync.WaitGroup
			for i, job := range jobs {
				wave.Go(func() { results[i].state, results[i].err = r.processMaintenancePreparation(ctx, job, o, clock) })
			}
			wave.Wait()
			for _, result := range results {
				switch result.state {
				case "complete":
					total.Complete++
				case "pending":
					total.Pending++
				case "unavailable":
					total.Unavailable++
				case "obsolete":
					total.Obsolete++
				case "failed":
					total.Failed++
				case "stale":
					total.Stale++
				}
				if result.err != nil {
					failures = append(failures, result.err)
				}
			}
		}
	}
	return total, errors.Join(failures...)
}

func (r *Runtime) maintenancePreparationTransaction(ctx context.Context, budget time.Duration, fn func(context.Context, pgx.Tx) error) error {
	if budget <= 0 {
		return ErrMaintenancePreparationStale
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	conn, err := r.Acquire(ctx, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	// Preserve existing derived statement/lock ceilings in addition to the caller
	// operation budget; no new pool or deadline increase.
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return err
	}
	if err = fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Runtime) processMaintenancePreparation(ctx context.Context, job dbgen.AlmanacMaintenancePreparation, o maintenancePreparationTurnOptions, clock func() time.Time) (string, error) {
	now := normalizedAgentNow(clock)
	if now.IsZero() || !job.LeaseUntil.Valid || !now.Before(job.LeaseUntil.Time) {
		return "stale", ErrMaintenancePreparationStale
	}
	state, reason := "pending", "dependency_pending"
	var err error
	if job.Kind == "extrema_hour" {
		// This stage releases its RR connection before opening RC publication.
		// Do not hold an unused outer derived connection across those phases.
		state, err = r.executeMaintenanceExtremaHour(ctx, job, o, clock)
	} else if job.Kind == "extrema_day" {
		state, err = r.executeMaintenanceExtremaDay(ctx, job, o, clock)
	} else {
		err = r.maintenancePreparationTransaction(ctx, min(o.OperationBudget, job.LeaseUntil.Time.Sub(now)), func(ctx context.Context, tx pgx.Tx) error {
			var err error
			state, err = executeMaintenancePreparation(ctx, tx, job, o, clock)
			if err != nil {
				return err
			}
			finished := normalizedAgentNow(clock)
			if finished.IsZero() || !finished.Before(job.LeaseUntil.Time) {
				return ErrMaintenancePreparationStale
			}
			return nil
		})
	}
	retry := o.PendingRetry
	original := err
	if err != nil {
		if ctx.Err() != nil {
			return "stale", err
		}
		switch {
		case errors.Is(err, ErrMaintenanceHourStale):
			state, reason = "obsolete", "source_obsolete"
			original = nil
		case errors.Is(err, ErrMaintenanceCompileUnavailable), errors.Is(err, ErrMaintenanceHourUnavailable), errors.Is(err, ErrDerivedSourceFailed):
			state, reason = "unavailable", "history_unavailable"
			original = nil
		case errors.Is(err, slo.ErrOperationalFacts), errors.Is(err, ErrMaintenanceInvalid):
			state, reason = "unavailable", "invalid_facts"
			original = nil
		case errors.Is(err, ErrMaintenancePreparationStale):
			return "stale", err
		case errors.Is(err, ErrDerivedPending), errors.Is(err, ErrMaintenanceCompilePending), errors.Is(err, ErrMaintenanceCompileStale):
			state = "pending"
			original = nil
		case alertBackpressure(err):
			state, reason = "pending", "database_backpressure"
			original = nil
		default:
			state, reason = "failed", "preparation_failed"
			retry = o.FailureRetry
		}
	} else if state == "complete" {
		reason = ""
	} else if state == "unavailable" {
		reason = "history_unavailable"
	}
	ack := r.maintenancePreparationTransaction(ctx, o.OperationBudget, func(ctx context.Context, tx pgx.Tx) error {
		return finishMaintenancePreparation(ctx, tx, job, state, reason, retry, o.MaxAttempts, clock)
	})
	if errors.Is(ack, ErrMaintenancePreparationStale) {
		return "stale", errors.Join(original, ack)
	}
	return state, errors.Join(original, ack)
}

func executeMaintenancePreparation(ctx context.Context, tx pgx.Tx, job dbgen.AlmanacMaintenancePreparation, o maintenancePreparationTurnOptions, clock func() time.Time) (string, error) {
	q := dbgen.New(tx)
	switch job.Kind {
	case "mask":
		b, err := q.ReadMaintenanceBuild(ctx, job.BuildID)
		if err != nil {
			return "", err
		}
		switch b.Phase {
		case "complete":
			return "complete", nil
		case "unavailable", "cancelled":
			return "unavailable", nil
		}
		claim, err := claimMaintenanceBuild(ctx, tx, job.BuildID, o.Lease, clock)
		if err != nil {
			return "", err
		}
		p, err := advanceMaintenanceBuild(ctx, tx, claim, o.PageLimit, clock)
		if err != nil {
			return "", err
		}
		switch p.Phase {
		case "complete":
			return "complete", nil
		case "unavailable":
			return "unavailable", nil
		}
		return "pending", nil
	case "compatibility":
		b, err := q.ReadMaintenanceBuild(ctx, job.BuildID)
		if err != nil {
			return "", err
		}
		if b.Phase == "cancelled" {
			return "unavailable", nil
		}
		p, err := advanceMaintenanceCompatibility(ctx, tx, job.BuildID, o.PageLimit)
		if err != nil {
			return "", err
		}
		return p.State, nil
	case "comparison":
		c, err := prepareMaintenanceComparison(ctx, tx, job.BuildID, job.SecondBuild, normalizedAgentNow(clock))
		if err != nil {
			return "", err
		}
		if c.State == "pending" {
			c, err = advanceMaintenanceComparison(ctx, tx, c.ID, o.PageLimit, normalizedAgentNow(clock))
			if err != nil {
				return "", err
			}
		}
		switch c.State {
		case "equal", "different":
			return "complete", nil
		case "cancelled":
			return "unavailable", nil
		}
		return "pending", nil
	case "scoped_hour":
		_, err := prepareMaintenanceHour(ctx, tx, maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SloID: job.SloID, SliID: job.SliID, BuildID: job.BuildID, DefinitionRevision: job.DefinitionRevision.Int64, MeasurementRevision: job.MeasurementRevision.Int64, HourStart: job.HourStart.Time, SourceGeneration: job.SourceGeneration.Int64}, RetentionStart: o.RetentionStart, Deadline: job.LeaseUntil.Time}, clock)
		return "complete", err
	default:
		return "", ErrMaintenanceInvalid
	}
}
