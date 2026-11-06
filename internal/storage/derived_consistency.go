package storage

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

// derivedFactsRange is the source range a derivation read. When its aggregates contradict themselves
// (ADR-0034), the hours are marked for rebuilding from the raw observations and the work is retried
// on its usual schedule, so a persistent inconsistency costs no more than any other failure.
type derivedFactsRange struct {
	sli      pgtype.UUID
	revision int64
	from, to time.Time
}

func (f derivedFactsRange) rebuildIfInconsistent(ctx context.Context, q *dbgen.Queries, err error, now time.Time) error {
	if !f.sli.Valid || !errors.Is(err, slo.ErrOperationalFacts) || !f.to.After(f.from) {
		return nil
	}
	return q.MarkDerivedHoursDirty(ctx, dbgen.MarkDerivedHoursDirtyParams{SliID: f.sli, MeasurementRevision: f.revision,
		RangeStart: timestamp(f.from), RangeEnd: timestamp(f.to), Now: timestamp(now)})
}

// checkDerivedConsistency is the periodic check of ADR-0034: it restarts work that stopped after seven
// days of failure once its SLI has new observations, and classifies every active SLO as up to date,
// catching up, failing or stopped. The queues re-create other missing work by themselves.
func (r *Runtime) checkDerivedConsistency(ctx context.Context, now time.Time) (dbgen.ReadSloDerivedStatesRow, int64, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return dbgen.ReadSloDerivedStatesRow{}, 0, err
	}
	defer conn.Release()
	q := dbgen.New(conn)
	restarted, err := q.RestartStoppedDerivedWork(bounded, timestamp(now))
	if err != nil {
		return dbgen.ReadSloDerivedStatesRow{}, 0, err
	}
	states, err := q.ReadSloDerivedStates(bounded, dbgen.ReadSloDerivedStatesParams{Now: timestamp(now), CalculationVersion: operationalCalculationVersion})
	if err != nil {
		return dbgen.ReadSloDerivedStatesRow{}, restarted, err
	}
	r.metrics.sloStates.Store(&states)
	return states, restarted, nil
}

// reportDerivedConsistency runs the check and writes one log line while anything is behind, so a stuck
// backend is visible without opening the database.
func (r *Runtime) reportDerivedConsistency(ctx context.Context, logger *slog.Logger, now time.Time) {
	states, restarted, err := r.checkDerivedConsistency(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			logger.ErrorContext(ctx, "derived consistency check failed", "error", derivedErrorDetail(err))
		}
		return
	}
	attributes := []any{"up_to_date", states.UpToDate, "catching_up", states.CatchingUp, "failing", states.Failing, "stopped", states.Stopped, "restarted", restarted}
	if snapshot := r.metrics.diagnostics.Load(); snapshot != nil {
		attributes = append(attributes, "oldest_unprocessed_hour_seconds", snapshot.OldestDirtySeconds)
	}
	switch {
	case states.Failing > 0 || states.Stopped > 0:
		logger.WarnContext(ctx, "derived SLO data is failing", attributes...)
	case states.CatchingUp > 0 || restarted > 0:
		logger.InfoContext(ctx, "derived SLO data is catching up", attributes...)
	}
}
