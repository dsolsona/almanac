//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationSchedulerPublicationMetadataRefresh(t *testing.T) {
	t.Parallel()
	s, id := schedulerMetadataFixture(t)
	q := dbgen.New(s.conn)
	assertDue := func(want bool) {
		t.Helper()
		assertSchedulerMetadataDue(t, s, id, want)
	}
	assertDue(false)

	// Republishing a covered hour changes no source generation or cadence cutoff.
	// Both scheduling paths must still notice its newer publication.
	published := s.now.Add(time.Microsecond)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema SET published_at=$2
WHERE slo_id=$1 AND resolution_seconds=3600`, id, published); err != nil {
		t.Fatal(err)
	}
	assertDue(true)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections
SET payload=jsonb_set(payload,'{_budget_cache,published_at}',to_jsonb($2::timestamptz)) WHERE slo_id=$1`, id, published); err != nil {
		t.Fatal(err)
	}
	assertDue(false)

	// Missing metadata remains conservative, including removal of the entire cache.
	for _, path := range []string{"{_budget_cache,published_at}", "{_budget_cache}"} {
		if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=payload#-$2::text[] WHERE slo_id=$1`, id, path); err != nil {
			t.Fatal(err)
		}
		assertDue(true)
	}

	// A due retained-history refresh still respects the existing retry deadline.
	if err := q.DeferCurrentPreparation(t.Context(), dbgen.DeferCurrentPreparationParams{
		SloID: mustPGUUID(id), DefinitionRevision: 1, MeasurementRevision: 1,
		CalculationVersion: operationalCalculationVersion, Column5: timestamp(s.now),
	}); err != nil {
		t.Fatal(err)
	}
	assertDue(false)
}

func TestIntegrationSchedulerPublicationMetadataDefersParsing(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"source mismatch", "calculation version mismatch", "backoff", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s, id := schedulerMetadataFixture(t)
			assertSchedulerMetadataDue(t, s, id, false)
			if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections
SET payload=jsonb_set(payload,'{_budget_cache,published_at}','"not-a-timestamp"'::jsonb) WHERE slo_id=$1`, id); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source mismatch", "backoff":
				if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_sli_source_generations SET generation=generation+1 WHERE sli_id=$1`, s.sliID); err != nil {
					t.Fatal(err)
				}
			case "calculation version mismatch":
				if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_derived_generations SET calculation_version='old-calculation'
WHERE id=(SELECT generation_id FROM public.almanac_slo_current_projections WHERE slo_id=$1)`, id); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "backoff" || mode == "exhausted" {
				attempts := 1
				if mode == "exhausted" {
					attempts = 5
				}
				if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_slo_current_failures
(slo_id,definition_revision,measurement_revision,calculation_version,attempts,next_attempt_at,last_error,failing_since)
SELECT slo_id,revision,measurement_revision,$2,$3,$4::timestamptz+interval '1 minute','fixture_failure',$4::timestamptz-interval '8 days'
FROM public.almanac_slo_definitions WHERE slo_id=$1`, id, operationalCalculationVersion, attempts, s.now); err != nil {
					t.Fatal(err)
				}
			}
			// Moving the timestamp cast into the materialized projection would throw
			// before these existing branches could decide whether work is due.
			assertSchedulerMetadataDue(t, s, id, mode == "source mismatch" || mode == "calculation version mismatch")
		})
	}
}

func schedulerMetadataFixture(t *testing.T) (*selfHealFixture, string) {
	t.Helper()
	s := newSelfHealFixture(t, "scheduler-metadata", true)
	id := s.createSLO(t, "scheduler-metadata")
	s.drain(t, "scheduler metadata source")
	for range 10 {
		worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	var hours int
	var marker bool
	if err := s.conn.QueryRow(t.Context(), `SELECT
(SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600),
(SELECT payload#>>'{_budget_cache,published_at}' IS NOT NULL FROM public.almanac_slo_current_projections WHERE slo_id=$1)`, id).Scan(&hours, &marker); err != nil {
		t.Fatal(err)
	}
	if hours == 0 || !marker {
		t.Fatal("fixture must publish retained history and its current cache marker")
	}
	return s, id
}

func assertSchedulerMetadataDue(t *testing.T, s *selfHealFixture, id string, want bool) {
	t.Helper()
	q := dbgen.New(s.conn)
	due, err := q.ReadDerivedDue(t.Context(), dbgen.ReadDerivedDueParams{
		Now: timestamp(s.now), CalculationVersion: operationalCalculationVersion, MetricsAt: timestamp(s.now.Add(time.Minute)),
	})
	if err != nil || due.CurrentDue != want {
		t.Fatalf("current due = %v, want %v: %v", due.CurrentDue, want, err)
	}
	ids, err := q.ListDueDerivedObjectives(t.Context(), dbgen.ListDueDerivedObjectivesParams{
		Now: timestamp(s.now), CalculationVersion: operationalCalculationVersion, BatchLimit: 100,
	})
	if err != nil || (want && (len(ids) != 1 || ids[0] != mustPGUUID(id))) || (!want && len(ids) != 0) {
		t.Fatalf("selected %v, want only fixture objective when due=%v: %v", ids, want, err)
	}
}
