//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationCurrentStatusDuringPreparation(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "current-pending", true)
	id := s.createSLO(t, "current-pending")
	s.drain(t, "initial")
	if err := s.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(id), s.clock); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98
	input := CurrentSloInput{Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, ID: id, Now: s.clock}
	before, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	// Corruption in an otherwise current projection is not a usable result either.
	var originalPayload []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{`jsonb_build_object('_current_valid_until',payload->'_current_valid_until')`, `jsonb_set(payload,'{generation,source_generation}','"999999"')`} {
		if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=`+replacement+` WHERE slo_id=$1`, id); err != nil {
			t.Fatal(err)
		}
		got, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
		if err != nil || got.CurrentState == nil || *got.CurrentState != "unavailable" || got.Current.Performance.Total != nil {
			t.Fatalf("corrupt current projection was presented as evidence: %+v %v", got.CurrentState, err)
		}
		if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=$2 WHERE slo_id=$1`, id, originalPayload); err != nil {
			t.Fatal(err)
		}
	}
	// A late observation invalidates the published source generation and an interior
	// hour. Its repair must not remove the last confirmed values from the detail.
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE public.almanac_sli_observations SET good_count=900,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, s.sliID, s.at.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.now = s.now.Add(time.Second)
	got, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
	if err != nil {
		t.Fatalf("normal detail read failed during preparation: %v", err)
	}
	if !reflect.DeepEqual(got.Current, before.Current) || !reflect.DeepEqual(got.Burn6h, before.Burn6h) || got.Generation.GenerationId != before.Generation.GenerationId || got.Urgency == "healthy" {
		t.Fatalf("stale values or provenance changed: %+v", got)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		State string `json:"current_state"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.State != "stale" || got.Generation.CalculationLagSeconds <= 0 {
		t.Fatalf("staleness not disclosed: %s", payload)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_derived_dirty_ranges SET attempts=5,lease_until=NULL WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	failed, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
	if err != nil || failed.CurrentState == nil || *failed.CurrentState != "failed" || !reflect.DeepEqual(failed.Current, before.Current) || failed.Generation.GenerationId != before.Generation.GenerationId {
		t.Fatalf("failed preparation erased provenance: %+v %v", failed, err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_derived_dirty_ranges SET attempts=0 WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	s.drain(t, "late observation repair")
	if err := s.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(id), s.clock); err != nil {
		t.Fatal(err)
	}
	ready, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
	if err != nil || ready.CurrentState == nil || *ready.CurrentState != "ready" || ready.Generation.GenerationId == before.Generation.GenerationId || reflect.DeepEqual(ready.Burn6h, before.Burn6h) {
		t.Fatalf("repair did not publish new exact values: %+v %v", ready, err)
	}
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE public.almanac_sli_observations SET good_count=800,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, s.sliID, s.at.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ name, assignment, restore string }{
		{"measurement revision", "measurement_revision=2", "measurement_revision=1"},
		{"calculation version", "calculation_version='obsolete'", "calculation_version='" + ready.Generation.CalculationVersion + "'"},
		{"future source generation", "source_generation=999999", "source_generation=" + ready.Generation.SourceGeneration},
		{"expired evaluation", "evaluated_through=evaluated_through-interval '366 days'", "evaluated_through=evaluated_through+interval '366 days'"},
	} {
		t.Run(change.name, func(t *testing.T) {
			if _, err := s.conn.Exec(t.Context(), "UPDATE public.almanac_slo_derived_generations SET "+change.assignment+" WHERE id=$1", ready.Generation.GenerationId); err != nil {
				t.Fatal(err)
			}
			got, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
			if err != nil || got.CurrentState == nil || *got.CurrentState != "unavailable" || got.Current.Performance.Total != nil || got.Urgency == "healthy" {
				t.Fatalf("incompatible snapshot was reused: %+v %v", got, err)
			}
			if _, err := s.conn.Exec(t.Context(), "UPDATE public.almanac_slo_derived_generations SET "+change.restore+" WHERE id=$1", ready.Generation.GenerationId); err != nil {
				t.Fatal(err)
			}
		})
	}
	var payloadBefore []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&payloadBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload='{}' WHERE slo_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	corrupt, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
	if err != nil || corrupt.CurrentState == nil || *corrupt.CurrentState != "unavailable" || corrupt.Current.Performance.Total != nil {
		t.Fatalf("corrupt published result did not become explicitly unavailable: %+v %v", corrupt, err)
	}
	for _, field := range []string{"current", "burn_6h", "generation", "current.window", "current.quality", "exhaustion.current_quality", "exhaustion.one_hour_quality", "reasons.0.code"} {
		if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=$2::jsonb #- string_to_array($3,'.') WHERE slo_id=$1`, id, payloadBefore, field); err != nil {
			t.Fatal(err)
		}
		got, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
		if err != nil || got.CurrentState == nil || *got.CurrentState != "unavailable" || got.Current.Performance.Total != nil {
			t.Fatalf("incomplete %s payload became an apparent result: %v %v", field, got.CurrentState, err)
		}
	}
	if _, err := s.conn.Exec(t.Context(), `DELETE FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	unavailable, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
	if err != nil || unavailable.CurrentState == nil || *unavailable.CurrentState != "unavailable" || unavailable.Current.Performance.Total != nil || unavailable.Urgency == "healthy" {
		t.Fatalf("no published result fabricated success: %+v %v", unavailable, err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_derived_dirty_ranges SET attempts=5,lease_until=NULL WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	sourceFailed, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input)
	if err != nil || sourceFailed.Current.Quality.Reason == nil || *sourceFailed.Current.Quality.Reason != "source_aggregate_failed" {
		t.Fatalf("source failure cause was erased: %+v %v", sourceFailed.Current.Quality, err)
	}

	if _, err := RevokeSession(t.Context(), s.f.databaseURL, verifier, s.now, "pending-status-revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, input); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("preparation fallback bypassed revoked session: %v", err)
	}

}

func TestIntegrationCurrentPublicationTimestampPrecision(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "current-precision", true)
	id := s.createSLO(t, "current-precision")
	s.drain(t, "initial")
	s.now = s.now.Add(123 * time.Nanosecond)
	if err := s.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(id), s.clock); err != nil {
		t.Fatal(err)
	}
	row, err := dbgen.New(s.conn).GetPublishedCurrentProjection(t.Context(), dbgen.GetPublishedCurrentProjectionParams{SloID: mustPGUUID(id), Now: timestamp(s.now), CalculationVersion: operationalCalculationVersion})
	if err != nil {
		t.Fatal(err)
	}
	got, err := publishedCurrentStatus(row)
	if err != nil || got.Current.Performance.Total == nil {
		t.Fatalf("valid nanosecond publication rejected: %v", err)
	}
}
