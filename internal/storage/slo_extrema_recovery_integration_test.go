//go:build integration

package storage

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationExtremaDiagnosticsAndAuditedRetry(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-retry", true)
	id := s.createSLO(t, "extrema-retry")
	var job pgtype.UUID
	if err := s.conn.QueryRow(t.Context(), `UPDATE public.almanac_slo_extrema_cursor
SET attempts=5,failing_since=$2::timestamptz-interval '8 days',lease_until=$2::timestamptz+interval '30 seconds',last_error='extrema_build_failed'
WHERE slo_id=$1 RETURNING id`, id, s.now).Scan(&job); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	page, err := ReadSloPreparation(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: id, Now: s.clock})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.Id == job.Bytes {
			found = item.Stage == "extrema" && item.State == "failed" && item.Attempts == 5
		}
	}
	if !found {
		t.Fatal("exhausted extrema work missing from preparation diagnostics")
	}
	retry := RetrySloPreparationInput{Runtime: s.runtime, Proof: proof, SloID: id, JobID: job.String(), RequestID: "extrema-retry", Now: s.clock}
	if err := RetrySloPreparation(t.Context(), s.f.databaseURL, retry); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("retry stole an active lease: %v", err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET lease_until=NULL WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	if err := RetrySloPreparation(t.Context(), s.f.databaseURL, retry); err != nil {
		t.Fatal(err)
	}
	if err := RetrySloPreparation(t.Context(), s.f.databaseURL, retry); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("retry replay replenished a running budget: %v", err)
	}
	var audits, attempts int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_audit_events
WHERE subject_id=$1 AND action='slo.extrema.retry' AND request_id=$2`, job, retry.RequestID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("retry did not atomically commit one audit: %d, %v", audits, err)
	}
	if err := s.conn.QueryRow(t.Context(), `SELECT attempts FROM public.almanac_slo_extrema_cursor WHERE id=$1`, job).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("retry did not reset the addressed budget: %d, %v", attempts, err)
	}
}

func TestIntegrationExtremaAbandonedFifthLeaseBacksOff(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-crash", true)
	id := s.createSLO(t, "extrema-crash")
	q := dbgen.New(s.conn)
	claim := func() (dbgen.ClaimSloExtremaCursorRow, error) {
		return q.ClaimSloExtremaCursor(t.Context(), dbgen.ClaimSloExtremaCursorParams{
			CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), ClosedThrough: timestamp(s.now.Truncate(time.Hour)), LeaseUntil: timestamp(s.now.Add(30 * time.Second))})
	}
	for range 5 {
		if _, err := claim(); err != nil {
			t.Fatal(err)
		}
		s.now = s.now.Add(31 * time.Second)
	}
	if _, err := claim(); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fifth abandoned lease did not back off: %v", err)
	}
	var next time.Time
	var failing pgtype.Timestamptz
	if err := s.conn.QueryRow(t.Context(), `SELECT next_attempt_at,failing_since FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&next, &failing); err != nil || next.Before(s.now.Add(5*time.Minute)) || !failing.Valid {
		t.Fatalf("crash exhaustion did not record a bounded failure: next=%s failing=%+v, %v", next, failing, err)
	}
}

func TestIntegrationExtremaArrivalsPreserveFailureBackoff(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-arrival-backoff", true)
	id := s.createSLO(t, "extrema-arrival-backoff")
	for index, attempts := range []int{1, 5} {
		next := s.now.Add(5 * time.Minute)
		failing := s.now.Add(-time.Hour)
		if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor
SET attempts=$2,failing_since=$3,last_error='extrema_build_failed',next_attempt_at=$4 WHERE slo_id=$1`, id, attempts, failing, next); err != nil {
			t.Fatal(err)
		}
		if _, err := s.conn.Exec(t.Context(), extremaCoverageInsert, s.sliID, s.at.Add(time.Duration(index+1)*time.Minute), s.now); err != nil {
			t.Fatal(err)
		}
		var gotAttempts int
		var gotNext time.Time
		var gotFailing pgtype.Timestamptz
		var reason pgtype.Text
		if err := s.conn.QueryRow(t.Context(), `SELECT attempts,next_attempt_at,failing_since,last_error FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&gotAttempts, &gotNext, &gotFailing, &reason); err != nil {
			t.Fatal(err)
		}
		if gotAttempts != attempts || !gotNext.Equal(next) || !gotFailing.Valid || !gotFailing.Time.Equal(failing) || !reason.Valid || reason.String != "extrema_build_failed" {
			t.Fatalf("arrival reset a failing cursor: attempts=%d next=%s failing=%+v reason=%+v", gotAttempts, gotNext, gotFailing, reason)
		}
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET failing_since=$2::timestamptz-interval '8 days' WHERE slo_id=$1`, id, s.now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), extremaCoverageInsert, s.sliID, s.at.Add(3*time.Minute), s.now); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var failing pgtype.Timestamptz
	if err := s.conn.QueryRow(t.Context(), `SELECT attempts,failing_since FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&attempts, &failing); err != nil || attempts != 0 || failing.Valid {
		t.Fatalf("new coverage did not restart stopped work: attempts=%d failing=%+v err=%v", attempts, failing, err)
	}
}

func TestIntegrationExtremaFirstDayBoundsPrefixQueries(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixtureCadence(t, "extrema-first-day", true, 15*time.Second)
	day := utcDayStart(s.at)
	s.now = day.Add(24*time.Hour + 7*time.Minute)
	s.at = s.now
	id := s.createSLOWithHistory(t, "extrema-first-day", time.Hour, day.Add(23*time.Hour))
	s.drain(t, "first-day sources")
	before := s.runtime.metrics.queries[DerivedTraffic].count.Load()
	worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
	if err != nil || !worked {
		t.Fatalf("first closed day did not publish in one bounded turn: worked=%v err=%v", worked, err)
	}
	queries := s.runtime.metrics.queries[DerivedTraffic].count.Load() - before
	if queries > 40 {
		t.Fatalf("pre-validity prefix queried source windows: %d queries", queries)
	}
	var hours, days int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE resolution_seconds=3600),count(*) FILTER(WHERE resolution_seconds=86400)
FROM public.almanac_slo_extrema WHERE slo_id=$1`, id).Scan(&hours, &days); err != nil || hours != 24 || days != 1 {
		t.Fatalf("first day incomplete: hours=%d days=%d err=%v", hours, days, err)
	}
}

func TestIntegrationConsistencyRecreatesMissingExtremaCursor(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-missing", true)
	id := s.createSLO(t, "extrema-missing")
	if _, err := s.conn.Exec(t.Context(), `DELETE FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.runtime.checkDerivedConsistency(t.Context(), s.now); err != nil {
		t.Fatal(err)
	}
	var through time.Time
	if err := s.conn.QueryRow(t.Context(), `SELECT built_through FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&through); err != nil || !through.Equal(s.historyStart.Truncate(time.Hour)) {
		t.Fatalf("consistency check did not recreate bounded history work: %s, %v", through, err)
	}
}

func TestIntegrationExtremaVersionConflictsReleaseOnlyTheirLease(t *testing.T) {
	for _, priorAttempts := range []int{0, 4, 5} {
		t.Run(strconv.Itoa(priorAttempts), func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixture(t, "extrema-version-conflict", true)
			id := s.createSLO(t, "extrema-version-conflict")
			if priorAttempts > 0 {
				if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET attempts=$3,failing_since=$2::timestamptz-interval '1 hour',last_error='extrema_build_failed' WHERE slo_id=$1`, id, s.now, priorAttempts); err != nil {
					t.Fatal(err)
				}
			}
			q := dbgen.New(s.conn)
			for range 6 {
				work, err := q.ClaimSloExtremaCursor(t.Context(), dbgen.ClaimSloExtremaCursorParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), ClosedThrough: timestamp(s.now.Truncate(time.Hour)), LeaseUntil: timestamp(s.now.Add(30 * time.Second))})
				if err != nil {
					t.Fatal(err)
				}
				// Ingestion changes version after claim, before the worker's snapshot.
				if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET version=version+1 WHERE id=$1`, work.ID); err != nil {
					t.Fatal(err)
				}
				deferWork := dbgen.DeferSloExtremaCursorParams{ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch, PriorAttempts: work.PriorAttempts, Now: timestamp(s.now), NextAttemptAt: timestamp(s.now.Add(time.Second))}
				if err := q.DeferSloExtremaCursor(t.Context(), deferWork); err != nil {
					t.Fatal(err)
				}
				var lease pgtype.Timestamptz
				var attempts int
				var next time.Time
				if err := s.conn.QueryRow(t.Context(), `SELECT lease_until,attempts,next_attempt_at FROM public.almanac_slo_extrema_cursor WHERE id=$1`, work.ID).Scan(&lease, &attempts, &next); err != nil {
					t.Fatal(err)
				}
				if lease.Valid || attempts != priorAttempts || priorAttempts == 5 && next.Before(s.now.Add(5*time.Minute)) {
					t.Fatalf("version conflict consumed or reset the failure budget: prior=%d lease=%+v attempts=%d next=%s", priorAttempts, lease, attempts, next)
				}
				s.now = next
			}
		})
	}
}

func TestIntegrationExtremaCleanupKeepsBoundaryAndUnexpiredRows(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-cleanup", true)
	id := s.createSLO(t, "extrema-cleanup")
	cutoff := s.now.Add(-365 * 24 * time.Hour)
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_slo_extrema
(slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
SELECT $1,1,1,$2,3600,point,point+interval '1 hour',0,$3,$4,'{}'
FROM generate_series($3::timestamptz-interval '2 hours',$3::timestamptz+interval '1 hour',interval '1 hour') point`, id, operationalCalculationVersion, cutoff.Truncate(time.Hour), s.now); err != nil {
		t.Fatal(err)
	}
	s.now = s.now.Add(7 * time.Minute)
	if err := s.runtime.cleanupDerivedSnapshots(t.Context(), s.now); err != nil {
		t.Fatal(err)
	}
	var expired, retained int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE range_end<=$2),count(*) FILTER(WHERE range_end>$2)
FROM public.almanac_slo_extrema WHERE slo_id=$1`, id, s.now.Add(-365*24*time.Hour)).Scan(&expired, &retained); err != nil || expired != 0 || retained != 2 {
		t.Fatalf("cleanup removed a retained boundary or kept expired rows: expired=%d retained=%d, %v", expired, retained, err)
	}
}
