//go:build integration

package command

import (
	"context"
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
	Maintenance       time.Duration          `json:"automatic_maintenance_ns"`
	PreparationAndAPI performancePhaseReport `json:"preparation_and_api"`
}

type performanceBrowserRestartReport struct {
	Result           string        `json:"result"`
	Target           string        `json:"target_slo_id"`
	Shutdown         time.Duration `json:"shutdown_ns"`
	OfflineSetup     time.Duration `json:"offline_setup_ns"`
	Startup          time.Duration `json:"startup_to_livez_ns"`
	EvictedHistory   int64         `json:"evicted_history_responses"`
	EvictedSnapshots int64         `json:"evicted_overview_snapshots"`
	PreservedPoints  int64         `json:"preserved_target_points"`
	PreservedTiles   int64         `json:"preserved_target_tiles"`
	PreservedCurrent int64         `json:"preserved_current_summaries"`
	NeverVisited     bool          `json:"never_visited_detail"`
}

// This deliberately evicts response caches, not maintained source/history data.
// No application read or preparation wait is allowed between restart and browser.
func restartPerformanceForBrowser(t *testing.T, fleet performanceFleet) performanceBrowserRestartReport {
	t.Helper()
	f := fleet.fixture
	report := performanceBrowserRestartReport{Target: fleet.objectives[len(fleet.objectives)-1]}
	report.Shutdown = f.backend.stop(t)
	if f.backend.deployment == nil {
		t.Fatal("response eviction requires stopped test-owned backend and disposable database")
	}
	f.backend.deployment.requireStoppedDatabase(t, f.databaseURL)
	started := time.Now()
	ctx, cancel := context.WithTimeout(fleet.ctx, 30*time.Second)
	defer cancel()
	db := testpostgres.Connect(t, f.databaseURL)
	defer closePerformanceObserver(db)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal("begin stopped fixture response eviction failed")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	type preserved struct{ points, tiles, current, source int64 }
	read := func() preserved {
		var value preserved
		if err := tx.QueryRow(ctx, `SELECT
(SELECT count(*) FROM public.almanac_slo_series_points WHERE slo_id=$1),
(SELECT count(*) FROM public.almanac_slo_series_tiles WHERE slo_id=$1),
(SELECT count(*) FROM public.almanac_slo_current_projections WHERE slo_id=ANY($2::uuid[])),
			(SELECT generation FROM public.almanac_sli_source_generations WHERE sli_id=$3 AND measurement_revision=1)`, report.Target, fleet.objectives, fleet.slis[len(fleet.slis)-1]).Scan(&value.points, &value.tiles, &value.current, &value.source); err != nil {
			t.Fatal("verify preserved browser fixture state failed")
		}
		return value
	}
	before := read()
	if before.points == 0 || before.tiles == 0 || before.current != int64(len(fleet.objectives)) || before.source <= 0 {
		t.Fatal("browser cache fixture omitted maintained history/current state")
	}
	var priorDetails int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.almanac_slo_history_blocks b JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id WHERE g.slo_id=$1 AND b.kind='retained_detail'`, report.Target).Scan(&priorDetails); err != nil || priorDetails != 0 || !fleet.reserveDetail {
		t.Fatal("first browser detail target was visited before response eviction")
	}
	report.NeverVisited = true
	if _, err := tx.Exec(ctx, `DELETE FROM public.almanac_slo_overview_snapshot_items`); err != nil {
		t.Fatal("evict fixture overview references failed")
	}
	tag, err := tx.Exec(ctx, `DELETE FROM public.almanac_slo_overview_snapshots`)
	if err != nil {
		t.Fatal("evict fixture overview snapshots failed")
	}
	report.EvictedSnapshots = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM public.almanac_slo_history_blocks b USING public.almanac_slo_derived_generations g WHERE b.generation_id=g.id AND g.slo_id=$1`, report.Target)
	if err != nil {
		t.Fatal("evict target history response cache failed")
	}
	report.EvictedHistory = tag.RowsAffected()
	if after := read(); after != before {
		t.Fatal("response eviction changed maintained history/current/source state")
	}
	var remaining int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.almanac_slo_history_blocks b JOIN public.almanac_slo_derived_generations g ON g.id=b.generation_id WHERE g.slo_id=$1`, report.Target).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("target history response cache was not empty")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal("commit fixture response eviction failed")
	}
	report.PreservedPoints, report.PreservedTiles, report.PreservedCurrent = before.points, before.tiles, before.current
	report.OfflineSetup = time.Since(started)
	report.Startup = f.backend.start(t)
	report.Result = "restarted-with-retained-data"
	return report
}

func restartPerformancePhase(t *testing.T, fleet performanceFleet, blankDerived bool, end time.Time) (performanceRestartReport, error) {
	t.Helper()
	report := performanceRestartReport{Kind: "persisted-cache"}
	if blankDerived {
		report.Kind = "source-and-response-cache-rebuild"
	}
	report.Shutdown = fleet.fixture.backend.stop(t)
	if blankDerived {
		started := time.Now()
		report.CanonicalRows, report.QueuedHours = clearPerformanceDerivedFixture(t, fleet)
		report.OfflineSetup = time.Since(started)
	}
	report.Startup = fleet.fixture.backend.start(t)
	if blankDerived {
		var err error
		report.Maintenance, err = awaitPerformanceHistory(t, fleet.ctx, fleet)
		if err != nil {
			return report, err
		}
	}
	phase, err := runPerformanceReadyPhase(fleet.ctx, fleet, report.Kind, end)
	report.PreparationAndAPI = phase
	return report, err
}

func clearPerformanceDerivedFixture(t *testing.T, fleet performanceFleet) (int64, int64) {
	t.Helper()
	f := fleet.fixture
	if f.backend.deployment == nil {
		t.Fatal("blank-derived setup requires the stopped test-owned backend and disposable fixture database")
	}
	f.backend.deployment.requireStoppedDatabase(t, f.databaseURL)
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
	// Rebuild source summaries and response caches; durable SLO points/tiles remain.
	// Canonical measurements, generations, identities and agent config also remain.
	for _, statement := range []string{
		`DELETE FROM public.almanac_slo_overview_snapshot_items`,
		`DELETE FROM public.almanac_slo_overview_snapshots`,
		`DELETE FROM public.almanac_slo_current_projections`,
		`DELETE FROM public.almanac_slo_projection_payloads`,
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
