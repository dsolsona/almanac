//go:build integration

package storage

import (
	"slices"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationOverviewIncompletePerformanceNeverCountsHealthy(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "integrity", true)
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE public.almanac_sli_observations SET good_count=total_count WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	broken := s.createSLO(t, "integrity-broken")
	others, cleanup := cloneReviewObjectives(t, s.conn, broken, "integrity-healthy", 1)
	defer cleanup()
	healthy := others[0].String()
	s.drain(t, "complete successful observations")
	for _, id := range []string{broken, healthy} {
		if err := s.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(id), s.clock); err != nil {
			t.Fatal(err)
		}
	}
	var verifier [32]byte
	verifier[0] = 98
	input := OperationalOverviewInput{Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, Query: "integrity-", Limit: 10, Now: s.clock}
	before, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
	if err != nil || before.GroupCounts != (api.SloUrgencyCounts{Healthy: 2}) {
		t.Fatalf("initial healthy rows: counts=%+v err=%v", before.GroupCounts, err)
	}
	var original []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, broken).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, window := range []string{"current", "burn_1h", "burn_6h"} {
		for _, field := range []string{"good", "total", "bad", "attainment", "allowed_budget", "remaining_budget", "consumed_fraction", "remaining_fraction", "burn"} {
			t.Run(window+"/"+field, func(t *testing.T) {
				if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=$2::jsonb #- string_to_array($3,'.') WHERE slo_id=$1`, broken, original, window+".performance."+field); err != nil {
					t.Fatal(err)
				}
				page, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
				if err != nil || len(page.Items) != 2 || page.GroupCounts != (api.SloUrgencyCounts{Healthy: 1, Watch: 1}) {
					t.Fatalf("incomplete projection remained Healthy or broke other rows: counts=%+v err=%v", page.GroupCounts, err)
				}
				for _, item := range page.Items {
					if item.Slo.Id.String() == broken && (item.CurrentState == nil || *item.CurrentState != "unavailable" || item.Current.Performance.Total != nil) {
						t.Fatalf("incomplete evidence retained usable numbers: %+v", item.CurrentState)
					}
				}
				filtered := input
				filtered.Urgency = "healthy"
				only, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, filtered)
				if err != nil || len(only.Items) != 1 || only.Items[0].Slo.Id.String() != healthy {
					t.Fatalf("global Healthy filtering retained corrupt evidence: %v", err)
				}
				q := dbgen.New(s.conn)
				due, err := q.ReadDerivedDue(t.Context(), dbgen.ReadDerivedDueParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), MetricsAt: timestamp(s.now.Add(time.Hour))})
				if err != nil || !due.CurrentDue {
					t.Fatalf("incomplete performance escaped repair due scan: %+v %v", due, err)
				}
				ids, err := q.ListDueDerivedObjectives(t.Context(), dbgen.ListDueDerivedObjectivesParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), BatchLimit: 100})
				if err != nil || !slices.Contains(ids, mustPGUUID(broken)) {
					t.Fatalf("incomplete performance was not scheduled for repair: %v", err)
				}
			})
		}
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=$2::jsonb WHERE slo_id=$1`, broken, original); err != nil {
		t.Fatal(err)
	}
	restored, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
	if err != nil || restored.GroupCounts != before.GroupCounts {
		t.Fatalf("restored evidence did not restore Healthy classification: %+v %v", restored.GroupCounts, err)
	}
}
