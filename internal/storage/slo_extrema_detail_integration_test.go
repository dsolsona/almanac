//go:build integration

package storage

import (
	"reflect"
	"testing"
	"time"
)

func TestIntegrationDetailAndPinnedValuesUseExtremaWithoutLegacyPreparation(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-endpoint", true)
	id := s.createSLO(t, "extrema-endpoint")
	s.now = s.at.Add(7 * time.Minute)
	s.drain(t, "extrema endpoint source")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		worked, err := s.runtime.buildSloExtrema(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	input := OperationalDetailInput{Runtime: s.runtime, Proof: proof, ID: id, Period: "7d", BurnWindow: 3600, Now: s.clock}
	prepared, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
	if err != nil || prepared.HistoryPreparation != nil || prepared.Generation == nil {
		t.Fatalf("extrema detail still waits for legacy series preparation: %+v, %v", prepared.HistoryPreparation, err)
	}
	values, err := ReadSloHistoryValues(t.Context(), s.f.databaseURL, SloHistoryValuesInput{Runtime: s.runtime, Proof: proof,
		ID: id, GenerationID: prepared.Generation.GenerationId.String(), Lane: "budget", Limit: 3, Now: s.clock})
	if err != nil || !reflect.DeepEqual(values.Samples, prepared.BudgetSeries.Samples[:min(3, len(prepared.BudgetSeries.Samples))]) || values.Generation.GenerationId != prepared.Generation.GenerationId {
		t.Fatalf("extrema detail values lost the pinned generation: %v", err)
	}
	var jobs int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_history_jobs WHERE slo_id=$1`, id).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("extrema navigation admitted graph preparation: %d, %v", jobs, err)
	}
}
