//go:build integration

package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// Runtime-free reads are independent raw-fact oracles, never application paths.
func readOperationalDetailForTest(ctx context.Context, databaseURL string, input OperationalDetailInput) (api.SloOperationalDetail, error) {
	if input.Runtime != nil {
		return ReadSloOperationalDetail(ctx, databaseURL, input)
	}
	id, valid := canonicalUUID(input.ID)
	if !valid || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return api.SloOperationalDetail{}, ErrObjectiveInvalid
	}
	if input.BurnWindow == 0 {
		input.BurnWindow = 3600
	}
	if input.BurnWindow != 3600 && input.BurnWindow != 21600 {
		return api.SloOperationalDetail{}, ErrOperationalRange
	}
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, nil, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	row, err := q.GetSlo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SloOperationalDetail{}, ErrObjectiveNotFound
	}
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	source, err := q.GetSloOperationalContext(ctx, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	bounds, err := resolveOperationalBounds(input, cadence, row.RollingWindowSeconds)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	window := time.Duration(row.RollingWindowSeconds) * time.Second
	start := canonicalFactsStart(bounds.effectiveAt, window, 6*time.Hour, bounds.start, window,
		time.Duration(input.BurnWindow)*time.Second, row.EvaluationValidFrom.Time, row.SliValidFrom.Time,
		input.Now().Add(-sli.ObservationRetention), cadence)
	rows, err := q.ListSloTimelineFacts(ctx, dbgen.ListSloTimelineFactsParams{SliID: row.SliID,
		MeasurementRevision: row.MeasurementRevision, IntervalStart: timestamp(start), IntervalEnd: timestamp(bounds.effectiveAt)})
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	facts := make([]slo.Observation, 0, len(rows))
	for _, fact := range rows {
		facts = append(facts, slo.Observation{Start: fact.IntervalStart.Time.UTC(), End: fact.IntervalEnd.Time.UTC(),
			Received: fact.RecordedAt.Time.UTC(), Outcome: fact.Outcome, Good: fact.Good, Total: fact.Total})
	}
	value, err := buildCanonicalOperationalDetail(ctx, facts, row, source, input)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloOperationalDetail{}, err
	}
	return value, nil
}

func readOperationalOverviewForTest(ctx context.Context, databaseURL string, input OperationalOverviewInput) (api.SloOperationalOverviewPage, error) {
	if input.Runtime != nil {
		return ReadSloOperationalOverview(ctx, databaseURL, input)
	}
	runtime, err := OpenRuntime(ctx, databaseURL)
	if err != nil {
		return api.SloOperationalOverviewPage{}, err
	}
	defer runtime.Close()
	for range 1000 {
		count, err := runtime.rebuildDerivedBatch(ctx, input.Now)
		if err != nil {
			return api.SloOperationalOverviewPage{}, err
		}
		if count == 0 {
			break
		}
	}
	if err := runtime.refreshCurrentProjections(ctx, input.Now); err != nil {
		return api.SloOperationalOverviewPage{}, err
	}
	for range 1000 {
		repaired, err := runtime.repairRetainedSeries(ctx, input.Now)
		if err != nil {
			return api.SloOperationalOverviewPage{}, err
		}
		tiled, err := runtime.prepareRetainedTile(ctx, input.Now)
		if err != nil {
			return api.SloOperationalOverviewPage{}, err
		}
		if !repaired && !tiled {
			break
		}
	}
	if err := runtime.queueDueBudgetHistory(ctx, input.Now()); err != nil {
		return api.SloOperationalOverviewPage{}, err
	}
	for range 1000 {
		worked := false
		if err := runtime.prepareHistory(ctx, input.Now, &worked); err != nil {
			return api.SloOperationalOverviewPage{}, err
		}
		if !worked {
			break
		}
	}
	input.Runtime = runtime
	return ReadSloOperationalOverview(ctx, databaseURL, input)
}
