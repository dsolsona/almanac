//go:build integration

package storage

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationDerivedSchedulerPublishesAfterSourceInOneTurn(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "scheduler-source-first", true)
	id := s.createSLO(t, "scheduler-source-first")
	s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Minute))
	var dirty, waiting, projections int
	if err := s.conn.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM public.almanac_derived_dirty_ranges),
 (SELECT count(*) FROM public.almanac_slo_current_failures WHERE slo_id=$1),
 (SELECT count(*) FROM public.almanac_slo_current_projections WHERE slo_id=$1)`, id).Scan(&dirty, &waiting, &projections); err != nil {
		t.Fatal(err)
	}
	if dirty != 0 || waiting != 0 || projections != 1 {
		t.Fatalf("one turn acknowledged source but did not publish current: dirty=%d waiting=%d projections=%d", dirty, waiting, projections)
	}
	var good, total string
	if err := s.conn.QueryRow(t.Context(), `SELECT payload#>>'{current,performance,good}',payload#>>'{current,performance,total}'
FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&good, &total); err != nil {
		t.Fatal(err)
	}
	// A one-hour window contains 60 observations of 990 good / 1000 total.
	if good != "59400.000000000000" || total != "60000.000000000000" {
		t.Fatalf("published current counts = %s/%s, want 59400/60000", good, total)
	}
}

func TestIntegrationDerivedSchedulerFullBatchDoesNotSkipCurrent(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "scheduler-backlog", true)
	id := s.createSLO(t, "scheduler-backlog")
	// Three recent source hours plus 101 older hours. One batch must leave four.
	addSchedulerOldHours(t, s, 101)
	nextMetrics, nextDue := s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, time.Time{})
	var remaining, published int
	if err := s.conn.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM public.almanac_derived_dirty_ranges),
 (SELECT count(*) FROM public.almanac_slo_current_projections WHERE slo_id=$1)`, id).Scan(&remaining, &published); err != nil {
		t.Fatal(err)
	}
	if remaining != 4 || published != 1 || s.runtime.metrics.sourceKeys.Load() != 100 {
		t.Fatalf("backlog skipped current or exceeded one batch: remaining=%d published=%d keys=%d", remaining, published, s.runtime.metrics.sourceKeys.Load())
	}
	if !nextMetrics.Equal(s.now.Add(30*time.Second)) || nextDue.After(s.now) {
		t.Fatalf("full batch skipped metrics or delayed remaining source: metrics=%s wake=%s", nextMetrics, nextDue)
	}
}

func TestIntegrationDerivedSchedulerSourceBlockDoesNotSkipHealthyCurrent(t *testing.T) {
	for _, mode := range []string{"leased", "locked", "temporary", "failed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixture(t, "scheduler-"+mode, true)
			s.drain(t, "prepare healthy source")
			id := s.createSLO(t, "scheduler-"+mode)
			addSchedulerOldHours(t, s, 1)
			switch mode {
			case "leased":
				if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_derived_dirty_ranges SET lease_until=$1`, s.now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			case "locked":
				tx, err := s.conn.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer rollbackTransaction(tx)
				if _, err := tx.Exec(t.Context(), `SELECT 1 FROM public.almanac_derived_dirty_ranges FOR UPDATE`); err != nil {
					t.Fatal(err)
				}
			case "temporary", "failed":
				code := "40001"
				if mode == "failed" {
					code = "23514"
				}
				if _, err := s.conn.Exec(t.Context(), `CREATE FUNCTION reject_scheduler_aggregate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'scheduler source failure' USING ERRCODE='`+code+`'; END $$;
CREATE TRIGGER reject_scheduler_aggregate BEFORE INSERT ON public.almanac_sli_hourly_aggregates
FOR EACH STATEMENT EXECUTE FUNCTION reject_scheduler_aggregate()`); err != nil {
					t.Fatal(err)
				}
			}
			s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Minute))
			var published, attempts int
			if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&published); err != nil {
				t.Fatal(err)
			}
			// Use another connection while the locked case owns s.conn's transaction.
			conn := integrationConnect(t, s.f.databaseURL)
			defer closeIntegrationConn(conn)
			if err := conn.QueryRow(t.Context(), `SELECT attempts FROM public.almanac_derived_dirty_ranges`).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			wantAttempts := 0
			if mode == "failed" {
				wantAttempts = 1
			}
			if published != 1 || attempts != wantAttempts {
				t.Fatalf("source %s blocked healthy current or changed attempt accounting: published=%d attempts=%d", mode, published, attempts)
			}
		})
	}
}

func TestIntegrationDerivedSchedulerRechecksDeadlineAfterSource(t *testing.T) {
	for _, advance := range []time.Duration{time.Second, time.Minute} {
		t.Run(advance.String(), func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixture(t, "scheduler-deadline", true)
			s.drain(t, "initial source")
			id := s.createSLO(t, "scheduler-deadline")
			if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
				t.Fatal(err)
			}
			if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections
SET payload=jsonb_set(payload,'{_current_valid_until}',to_jsonb($2::timestamptz)) WHERE slo_id=$1`, id, s.now.Add(advance)); err != nil {
				t.Fatal(err)
			}
			addSchedulerOldHours(t, s, 1)
			due, err := dbgen.New(s.conn).ReadDerivedDue(t.Context(), dbgen.ReadDerivedDueParams{Now: timestamp(s.now), CalculationVersion: operationalCalculationVersion, MetricsAt: timestamp(s.now.Add(time.Minute))})
			if err != nil || due.CurrentDue || !due.AggregateDue {
				t.Fatalf("deadline fixture should initially need only source: %+v %v", due, err)
			}
			before := s.runtime.metrics.sourceKeys.Load()
			clock := func() time.Time {
				if s.runtime.metrics.sourceKeys.Load() > before {
					return s.now.Add(advance)
				}
				return s.now
			}
			s.runtime.runDerivedTurn(t.Context(), slog.Default(), clock, s.now.Add(time.Minute))
			var publishedAt time.Time
			if err := s.conn.QueryRow(t.Context(), `SELECT g.published_at FROM public.almanac_slo_current_projections p
JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id WHERE p.slo_id=$1`, id).Scan(&publishedAt); err != nil {
				t.Fatal(err)
			}
			if !publishedAt.Equal(s.now.Add(advance)) {
				t.Fatalf("deadline crossed during source work was not published: %s", publishedAt)
			}
		})
	}
}

func TestIntegrationDerivedSchedulerPreservesDeferredRepairWakeup(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "scheduler-repair", true)
	id := s.createSLO(t, "scheduler-repair")
	pgID, _ := canonicalUUID(id)
	if err := s.runtime.refreshCurrentProjection(t.Context(), pgID, s.clock); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("dirty source did not defer current: %v", err)
	}
	s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Minute))
	var attempts, published int
	var retry time.Time
	if err := s.conn.QueryRow(t.Context(), `SELECT attempts,next_attempt_at FROM public.almanac_slo_current_failures WHERE slo_id=$1`, id).Scan(&attempts, &retry); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || !retry.Equal(s.now.Add(2*time.Second)) {
		t.Fatalf("source preparation changed temporary deferral: attempts=%d retry=%s", attempts, retry)
	}
	s.now = retry
	s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Minute))
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&published); err != nil || published != 1 {
		t.Fatalf("repaired source did not publish at retry without a new observation: %d %v", published, err)
	}
}

func TestIntegrationDerivedSchedulerEmptyTurnReleasesConnectionAndCancels(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	runtime, err := OpenRuntime(t.Context(), f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	now := f.base
	metricsAt := now.Add(time.Minute)
	nextMetrics, nextDue := runtime.runDerivedTurn(t.Context(), slog.Default(), func() time.Time { return now }, metricsAt)
	if nextMetrics != metricsAt || !nextDue.Equal(now.Add(time.Second)) || runtime.pools[DerivedTraffic].Stat().AcquiredConns() != 0 {
		t.Fatalf("idle turn retained resources or lost deadlines: metrics=%s wake=%s", nextMetrics, nextDue)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if waitDerived(ctx, time.Now().Add(time.Hour)) {
		t.Fatal("idle wait ignored cancellation")
	}
}

func TestIntegrationDerivedSchedulerNewRevisionIgnoresOldFailureBackoff(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "scheduler-revision", true)
	s.drain(t, "prepare source")
	id := s.createSLO(t, "scheduler-revision")
	s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Minute))
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_slo_current_failures
(slo_id,definition_revision,measurement_revision,calculation_version,attempts,next_attempt_at,last_error,failing_since)
VALUES($1,1,1,$2,5,$3,'current_preparation_failed',$4)`, id, operationalCalculationVersion, s.now.Add(time.Hour), s.now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `WITH revised AS (
INSERT INTO public.almanac_slo_definitions
(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at)
SELECT slo_id,2,service_id,sli_id,measurement_revision,0.98,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at
FROM public.almanac_slo_definitions WHERE slo_id=$1 AND revision=1 RETURNING slo_id)
UPDATE public.almanac_slos SET definition_revision=2 WHERE id IN (SELECT slo_id FROM revised)`, id); err != nil {
		t.Fatal(err)
	}
	s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Minute))
	var revision int
	var target string
	if err := s.conn.QueryRow(t.Context(), `SELECT g.definition_revision,p.payload#>>'{slo,target}'
FROM public.almanac_slo_current_projections p JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
WHERE p.slo_id=$1`, id).Scan(&revision, &target); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || target != "0.98" {
		t.Fatalf("old revision backoff prevented new exact publication: revision=%d target=%s", revision, target)
	}
}

func TestIntegrationDerivedSchedulerDoesNotPublishAcrossNewerSourceWork(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "scheduler-generation", true)
	id := s.createSLO(t, "scheduler-generation")
	// Advance one claimed key between its aggregate write and conditional ack.
	// This uses real transactions; the trigger only supplies the race boundary.
	if _, err := s.conn.Exec(t.Context(), `CREATE FUNCTION advance_scheduler_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN UPDATE public.almanac_derived_dirty_ranges SET generation=generation+1
WHERE sli_id=NEW.sli_id AND measurement_revision=NEW.measurement_revision AND range_start=NEW.hour_start;
RETURN NEW; END $$;
CREATE TRIGGER advance_scheduler_source AFTER INSERT ON public.almanac_sli_hourly_aggregates
FOR EACH ROW EXECUTE FUNCTION advance_scheduler_source()`); err != nil {
		t.Fatal(err)
	}
	s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Minute))
	var remaining, published int
	if err := s.conn.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM public.almanac_derived_dirty_ranges WHERE lease_until IS NULL),
 (SELECT count(*) FROM public.almanac_slo_current_projections WHERE slo_id=$1)`, id).Scan(&remaining, &published); err != nil {
		t.Fatal(err)
	}
	if remaining != 3 || published != 0 {
		t.Fatalf("newer source work was acknowledged or published as current: remaining=%d published=%d", remaining, published)
	}
	if _, err := s.conn.Exec(t.Context(), `DROP TRIGGER advance_scheduler_source ON public.almanac_sli_hourly_aggregates`); err != nil {
		t.Fatal(err)
	}
	s.now = s.now.Add(2 * time.Second)
	s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Minute))
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&published); err != nil || published != 1 {
		t.Fatalf("newer generation did not recover on the next bounded turn: %d %v", published, err)
	}
}

func addSchedulerOldHours(t *testing.T, s *selfHealFixture, hours int) {
	t.Helper()
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
SELECT $1,1,1,hour,hour+interval '1 hour',$3,$3 FROM generate_series(
$2::timestamptz-$4*interval '1 hour',$2::timestamptz-interval '1 hour',interval '1 hour') hour`, s.sliID, s.historyStart, s.now, hours); err != nil {
		t.Fatal(err)
	}
}
