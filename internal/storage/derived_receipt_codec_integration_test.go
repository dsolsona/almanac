//go:build integration

package storage

import (
	"encoding/json/v2"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func TestIntegrationReceiptMetadataLegacyProjection(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-codec", true)
	id := s.createSLO(t, "receipt-codec")
	s.drain(t, "receipt codec source")
	if err := s.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(id), s.clock); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := s.conn.QueryRow(t.Context(), `SELECT payload FROM public.almanac_slo_current_projections WHERE slo_id=$1`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var legacy api.SloOperationalOverviewItem
	if err := json.Unmarshal(payload, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Current.Quality.LatestReceiptAt == nil {
		t.Fatal("fixture has no receipt")
	}
	// Typed API encoding omits all private persistence markers, modeling legacy JSON.
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET payload=$2::jsonb || jsonb_build_object('_current_valid_until', $3::text) WHERE slo_id=$1`, id, raw, s.now.Add(time.Second).Format("2006-01-02T15:04:05Z07:00")); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	current, err := ReadCurrentSloStatus(t.Context(), s.f.databaseURL, CurrentSloInput{Runtime: s.runtime, Proof: proof, ID: id, Now: s.clock})
	if err != nil {
		t.Fatal(err)
	}
	want := legacy.Current
	want.Quality.LatestReceiptAt = nil
	if !reflect.DeepEqual(current.Current, want) {
		t.Fatalf("legacy receipt was not quarantined without changing evidence: got %+v want %+v", current.Current, want)
	}
	page, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, OperationalOverviewInput{Runtime: s.runtime, Proof: proof, Limit: 10, Now: s.clock})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("overview: %v, %d", err, len(page.Items))
	}
	if !reflect.DeepEqual(page.Items[0].Current, want) {
		t.Fatal("overview leaked legacy receipt or changed other current fields")
	}
}

func TestIntegrationReceiptMetadataHistoryValues(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-history", true)
	id := s.createSLO(t, "receipt-history")
	s.drain(t, "receipt history source")
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
	input := OperationalDetailInput{Runtime: s.runtime, Proof: proof, ID: id, Period: "window", BurnWindow: 3600, Now: s.clock}
	detail, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
	if err != nil || detail.Generation == nil || len(detail.BudgetSeries.Samples) == 0 {
		t.Fatalf("prepared detail: %v", err)
	}
	valuesInput := SloHistoryValuesInput{Runtime: s.runtime, Proof: proof, ID: id, GenerationID: detail.Generation.GenerationId.String(), Lane: "budget", Limit: 100, Now: s.clock}
	page, err := ReadSloHistoryValues(t.Context(), s.f.databaseURL, valuesInput)
	if err != nil || !reflect.DeepEqual(page.Samples, detail.BudgetSeries.Samples[:min(100, len(detail.BudgetSeries.Samples))]) {
		t.Fatalf("new extracted samples changed: %v", err)
	}
	// Model a valid legacy payload, including its immutable-content digest. Updating
	// JSON alone correctly fails the existing corruption fence. PostgreSQL jsonb
	// normalizes numeric marker spacing.
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_history_blocks SET payload=replace(payload::text,'"_receipt_metadata_version": 2','"_receipt_metadata_version": 1')::jsonb, payload_digest=sha256(convert_to(replace(payload::text,'"_receipt_metadata_version": 2','"_receipt_metadata_version": 1'),'UTF8')) WHERE generation_id=$1`, detail.Generation.GenerationId.String()); err != nil {
		t.Fatal(err)
	}
	cached, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, input)
	if err != nil || cached.Generation == nil || cached.Generation.GenerationId != detail.Generation.GenerationId {
		t.Fatalf("legacy metadata forced a rebuild: %v", err)
	}
	if cached.Current.Quality.LatestReceiptAt != nil {
		t.Fatal("history current receipt leaked")
	}
	legacyPage, err := ReadSloHistoryValues(t.Context(), s.f.databaseURL, valuesInput)
	if err != nil {
		t.Fatal(err)
	}
	receiptCount := 0
	for i := range page.Samples {
		if page.Samples[i].ReceivedAt != nil {
			receiptCount++
		}
		page.Samples[i].ReceivedAt = nil
		page.Samples[i].Quality.LatestReceiptAt = nil
	}
	if receiptCount == 0 {
		t.Fatal("history fixture contains no receipt metadata")
	}
	if !reflect.DeepEqual(legacyPage.Samples, page.Samples) {
		t.Fatal("extracted legacy history page leaked receipts or changed evidence")
	}
	if !reflect.DeepEqual(legacyPage.Generation, page.Generation) {
		t.Fatal("legacy marker changed generation provenance")
	}
}
