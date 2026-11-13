//go:build integration

package storage

import (
	"slices"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationOverviewInvalidDeadlinePreservesOtherRows(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "deadline", true)
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE public.almanac_sli_observations SET good_count=total_count WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	broken := s.createSLO(t, "deadline-broken")
	others, cleanup := cloneReviewObjectives(t, s.conn, broken, "deadline-healthy", 1)
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
	input := OperationalOverviewInput{Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, Query: "deadline-", Limit: 10, Now: s.clock}
	before, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
	if err != nil || before.GroupCounts != (api.SloUrgencyCounts{Healthy: 2}) {
		t.Fatalf("initial healthy rows: counts=%+v err=%v", before.GroupCounts, err)
	}
	var original []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, broken).Scan(&original); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(s.conn)
	for _, marker := range []struct{ name, value string }{
		{"missing", ""}, {"malformed", `"bad-date"`}, {"nonfinite", `"infinity"`}, {"overflow", `"2026-99-99T00:00:00Z"`},
	} {
		t.Run(marker.name, func(t *testing.T) {
			if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=CASE WHEN $3='' THEN $2::jsonb-'_current_valid_until' ELSE jsonb_set($2::jsonb,'{_current_valid_until}',$3::jsonb) END WHERE slo_id=$1`, broken, original, marker.value); err != nil {
				t.Fatal(err)
			}
			page, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
			if err != nil || len(page.Items) != 2 || page.GroupCounts != (api.SloUrgencyCounts{Healthy: 1, Watch: 1}) {
				t.Fatalf("invalid deadline broke list or retained Healthy: counts=%+v err=%v", page.GroupCounts, err)
			}
			row, err := q.GetPublishedCurrentProjection(t.Context(), dbgen.GetPublishedCurrentProjectionParams{SloID: mustPGUUID(broken), CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now)})
			if err != nil || row.Current {
				t.Fatalf("invalid deadline remained current: current=%v err=%v", row.Current, err)
			}
			due, err := q.ReadDerivedDue(t.Context(), dbgen.ReadDerivedDueParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), MetricsAt: timestamp(s.now.Add(time.Hour))})
			if err != nil || !due.CurrentDue {
				t.Fatalf("invalid deadline aborted or escaped due scan: %+v err=%v", due, err)
			}
			ids, err := q.ListDueDerivedObjectives(t.Context(), dbgen.ListDueDerivedObjectivesParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(s.now), BatchLimit: 100})
			if err != nil || !slices.Contains(ids, mustPGUUID(broken)) {
				t.Fatalf("invalid projection was not scheduled for repair: err=%v", err)
			}
			if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=$2::jsonb WHERE slo_id=$1`, broken, original); err != nil {
				t.Fatal(err)
			}
			restored, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
			if err != nil || restored.GroupCounts != before.GroupCounts {
				t.Fatalf("restoring publication did not restore classification: %+v err=%v", restored.GroupCounts, err)
			}
		})
	}
}
