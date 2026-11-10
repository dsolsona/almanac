//go:build integration

package storage

import (
	"testing"
	"time"
)

func TestIntegrationSloDetailWithOptionalRuntimeKeepsMissAndHitReadable(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "optional-runtime", true)
	id := s.createSLO(t, "optional-runtime")
	if err := s.buildHistory(t, id); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98
	start, end := s.at.Add(-time.Hour), s.at
	input := OperationalDetailInput{Proof: DatasourceProof{SessionVerifier: verifier}, ID: id,
		At: &end, Start: &start, End: &end, BurnWindow: 3600, Now: s.clock}
	var generation string
	for read := range 2 {
		value, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
		if err != nil {
			t.Fatal(err)
		}
		if value.HistoryPreparation != nil || value.Generation == nil || value.Current.Performance.Good == nil ||
			*value.Current.Performance.Good != "59400.000000000000" || value.Current.Performance.Total == nil ||
			*value.Current.Performance.Total != "60000.000000000000" {
			t.Fatalf("runtime-free read %d lost exact prepared values: %+v", read, value.Current)
		}
		if read == 0 {
			generation = value.Generation.GenerationId.String()
		} else if value.Generation.GenerationId.String() != generation {
			t.Fatal("runtime-free cache hit did not reuse the pinned generation")
		}
	}
	hits := s.runtime.metrics.historyHits.Load()
	input.Runtime = s.runtime
	value, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
	if err != nil || value.Generation == nil || value.Generation.GenerationId.String() != generation || s.runtime.metrics.historyHits.Load() != hits+1 {
		t.Fatalf("pooled read lost its cached generation or hit metric: %v", err)
	}
}
