//go:build integration

package storage

import (
	"testing"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationServiceSummaryRejectsIncompletePerformance(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "performance-integrity", true)
	id := s.createSLO(t, "performance-integrity")
	s.drain(t, "complete projection")
	for range 10 {
		worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	if err := s.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(id), s.clock); err != nil {
		t.Fatal(err)
	}
	var serviceID string
	if err := s.conn.QueryRow(t.Context(), `SELECT service_id FROM public.almanac_slo_definitions WHERE slo_id=$1 AND revision=1`, id).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98
	read := func() ServiceRecord {
		t.Helper()
		value, _, err := ReadObjective(t.Context(), s.f.databaseURL, DatasourceProof{SessionVerifier: verifier}, serviceID, s.clock, false)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if value := read(); value.SloSummary.OnTrack != 1 {
		t.Fatalf("complete projection: %+v", value.SloSummary)
	}
	assertRepairState := func(pending, upToDate int64) {
		t.Helper()
		q := dbgen.New(s.conn)
		metrics, err := q.ReadDerivedMetrics(t.Context(), dbgen.ReadDerivedMetricsParams{LeaseUntil: timestamp(s.now), CalculationVersion: operationalCalculationVersion})
		if err != nil || metrics.PendingCurrent != pending {
			t.Fatalf("pending current metric = %d, want %d: %v", metrics.PendingCurrent, pending, err)
		}
		states, err := q.ReadSloDerivedStates(t.Context(), dbgen.ReadSloDerivedStatesParams{Now: timestamp(s.now), CalculationVersion: operationalCalculationVersion})
		if err != nil || states.UpToDate != upToDate || states.CatchingUp != pending {
			t.Fatalf("repair consistency state = %+v, want pending=%d up-to-date=%d: %v", states, pending, upToDate, err)
		}
	}
	assertRepairState(0, 1)
	var original []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=payload #- '{current,performance,total}' WHERE slo_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if value := read(); value.SloSummary.State != "unknown" || value.SloSummary.Unknown != 1 || value.SloSummary.OnTrack != 0 {
		t.Fatalf("incomplete projection retained service success: %+v", value.SloSummary)
	}
	assertRepairState(1, 0)
	for _, window := range []string{"current", "burn_1h", "burn_6h"} {
		for _, test := range []struct {
			name, performance string
			complete          bool
		}{
			{"unknown", `{"state":"unknown","budget_unit":"events"}`, true},
			{"not applicable", `{"state":"not_applicable","budget_unit":"events"}`, true},
			{"zero traffic", `{"state":"unknown","budget_unit":"events","good":"0","total":"0","bad":"0","attainment":null,"burn":null}`, true},
			{"missing met values", `{"state":"met","budget_unit":"events"}`, false},
			{"missing violated values", `{"state":"violated","budget_unit":"events"}`, false},
			{"invalid state", `{"state":"healthy","budget_unit":"events"}`, false},
			{"invalid unit", `{"state":"unknown","budget_unit":"seconds"}`, false},
			{"wrong numeric type", `{"state":"unknown","budget_unit":"events","total":123}`, false},
		} {
			t.Run(window+"/"+test.name, func(t *testing.T) {
				var complete bool
				err := s.conn.QueryRow(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=jsonb_set($2::jsonb,ARRAY[$3,'performance'],$4::jsonb) WHERE slo_id=$1 RETURNING performance_complete`, id, original, window, test.performance).Scan(&complete)
				if err != nil || complete != test.complete {
					t.Fatalf("stored performance integrity = %v, want %v: %v", complete, test.complete, err)
				}
			})
		}
	}
	var restored bool
	if err := s.conn.QueryRow(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=$2::jsonb WHERE slo_id=$1 RETURNING performance_complete`, id, original).Scan(&restored); err != nil || !restored {
		t.Fatalf("restored publication remained incomplete: %v", err)
	}
	if value := read(); value.SloSummary.OnTrack != 1 {
		t.Fatalf("restored publication did not restore service classification: %+v", value.SloSummary)
	}
	assertRepairState(0, 1)
}

func TestIntegrationProjectionPerformanceUpgrade(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "performance-upgrade", true)
	valid := s.createSLO(t, "performance-upgrade-valid")
	clones, cleanup := cloneReviewObjectives(t, s.conn, valid, "performance-upgrade-invalid", 1)
	defer cleanup()
	invalid := clones[0].String()
	s.drain(t, "upgrade source")
	for _, id := range []string{valid, invalid} {
		if err := s.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(id), s.clock); err != nil {
			t.Fatal(err)
		}
	}
	// No worker runs while the private fixture models a populated 0054 database.
	s.runtime.Close()
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=payload #- '{current,performance,total}' WHERE slo_id=$1`, invalid); err != nil {
		t.Fatal(err)
	}
	type publication struct{ generation, payload string }
	before := make(map[string]publication)
	for _, id := range []string{valid, invalid} {
		var value publication
		if err := s.conn.QueryRow(t.Context(), `SELECT generation_id::text,payload::text FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&value.generation, &value.payload); err != nil {
			t.Fatal(err)
		}
		before[id] = value
	}
	dropAlertJournalFixture(t, s.conn)
	latest := latestTestSchemaVersion(t)
	tag, err := s.conn.Exec(t.Context(), `
ALTER TABLE public.almanac_slo_current_projections DROP COLUMN performance_complete;
DROP FUNCTION public.almanac_projection_performance_complete(jsonb);
DELETE FROM public.almanac_schema_migrations WHERE version>=55;`)
	if err != nil || tag.RowsAffected() != int64(latest-54) {
		t.Fatalf("prepare populated 0054 fixture: removed=%d err=%v", tag.RowsAffected(), err)
	}
	if state, err := CheckCompatibility(t.Context(), s.f.databaseURL); err != nil || state != CompatibilityPending {
		t.Fatalf("0054 compatibility = %v: %v", state, err)
	}
	upgraded, err := Upgrade(t.Context(), s.f.databaseURL)
	if err != nil || upgraded.Applied != latest-54 || upgraded.DatabaseSchemaVersion != latest {
		t.Fatalf("populated 0054 to current upgrade: %+v %v", upgraded, err)
	}
	for _, id := range []string{valid, invalid} {
		var value publication
		var complete bool
		if err := s.conn.QueryRow(t.Context(), `SELECT generation_id::text,payload::text,performance_complete FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&value.generation, &value.payload, &complete); err != nil {
			t.Fatal(err)
		}
		if value != before[id] || complete != (id == valid) {
			t.Fatalf("upgrade changed publication or misclassified retained evidence: unchanged=%v complete=%v want=%v", value == before[id], complete, id == valid)
		}
	}
}
