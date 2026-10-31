//go:build integration

package command

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/testpostgres"
)

type performanceRestartReport struct {
	Kind              string                 `json:"kind"`
	Shutdown          time.Duration          `json:"shutdown_ns"`
	OfflineSetup      time.Duration          `json:"offline_setup_ns"`
	Startup           time.Duration          `json:"startup_to_livez_ns"`
	CanonicalRows     int64                  `json:"preserved_canonical_rows"`
	QueuedHours       int64                  `json:"queued_hours"`
	PreparationAndAPI performancePhaseReport `json:"preparation_and_api"`
}

func restartPerformancePhase(t *testing.T, fleet performanceFleet, blankDerived bool, end time.Time) (performanceRestartReport, error) {
	t.Helper()
	report := performanceRestartReport{Kind: "persisted-cache"}
	if blankDerived {
		report.Kind = "blank-derived-rebuild"
	}
	report.Shutdown = fleet.fixture.backend.stop(t)
	if blankDerived {
		started := time.Now()
		report.CanonicalRows, report.QueuedHours = clearPerformanceDerivedFixture(t, fleet)
		report.OfflineSetup = time.Since(started)
	}
	report.Startup = fleet.fixture.backend.start(t)
	phase, err := runPerformanceReadyPhase(fleet.ctx, fleet, report.Kind, end)
	report.PreparationAndAPI = phase
	return report, err
}

func clearPerformanceDerivedFixture(t *testing.T, fleet performanceFleet) (int64, int64) {
	t.Helper()
	f := fleet.fixture
	parsed, err := url.Parse(f.databaseURL)
	if err != nil || parsed.Host != "127.0.0.1:15487" || !strings.HasPrefix(parsed.Path, "/almanac_test_") || f.backend.process != nil {
		t.Fatal("blank-derived setup requires the stopped test-owned backend and disposable fixture database")
	}
	ctx, cancel := context.WithTimeout(fleet.ctx, 5*time.Minute)
	defer cancel()
	db := testpostgres.Connect(t, f.databaseURL)
	defer closePerformanceObserver(db)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal("begin offline derived-cache fixture setup failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var failures int64
	if err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM public.almanac_derived_dirty_ranges WHERE last_error IS NOT NULL OR attempts>=5)+
 (SELECT count(*) FROM public.almanac_slo_current_failures)+
 (SELECT count(*) FROM public.almanac_slo_history_jobs WHERE last_error IS NOT NULL OR state='failed')`).Scan(&failures); err != nil || failures != 0 {
		t.Fatal("refuse to erase existing preparation failure evidence during a blank-cache fixture")
	}
	var before int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.almanac_sli_coverage`).Scan(&before); err != nil {
		t.Fatal("count canonical fixture before cache removal failed")
	}
	// Only disposable derived outputs/work are removed. Canonical measurements,
	// source generations, users, leases, credentials and agent config are retained.
	for _, statement := range []string{
		`DELETE FROM public.almanac_slo_overview_snapshot_items`,
		`DELETE FROM public.almanac_slo_overview_snapshots`,
		`DELETE FROM public.almanac_slo_current_projections`,
		`DELETE FROM public.almanac_slo_history_jobs`,
		`DELETE FROM public.almanac_slo_history_blocks`,
		`DELETE FROM public.almanac_slo_display_blocks`,
		`DELETE FROM public.almanac_slo_derived_generations`,
		`DELETE FROM public.almanac_sli_hourly_aggregates`,
		`DELETE FROM public.almanac_derived_dirty_ranges`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal("remove disposable derived fixture output failed")
		}
	}
	var payloadBytes int64
	if err := tx.QueryRow(ctx, `SELECT payload_bytes FROM public.almanac_derived_cache_usage`).Scan(&payloadBytes); err != nil || payloadBytes != 0 {
		t.Fatal("derived-cache removal did not release its accounted payload budget")
	}
	// Use the same migration contract to enqueue every retained source hour;
	// production workers perform all aggregate/current/history calculation.
	if _, err := tx.Exec(ctx, `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
SELECT c.sli_id,c.measurement_revision,s.generation,
 date_trunc('hour',c.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
 GREATEST((date_trunc('hour',c.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour',max(c.interval_end)),
 CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
FROM public.almanac_sli_coverage c JOIN public.almanac_sli_source_generations s USING(sli_id,measurement_revision)
GROUP BY c.sli_id,c.measurement_revision,s.generation,date_trunc('hour',c.interval_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`); err != nil {
		t.Fatal("enqueue retained fixture hours for production rebuilding failed")
	}
	var after, queued int64
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.almanac_sli_coverage),(SELECT count(*) FROM public.almanac_derived_dirty_ranges)`).Scan(&after, &queued); err != nil || before != after || queued == 0 {
		t.Fatal("blank-derived fixture changed canonical row count or failed to queue rebuilding")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal("commit offline derived-cache fixture setup failed")
	}
	return after, queued
}
