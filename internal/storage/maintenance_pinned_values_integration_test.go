//go:build integration

package storage

import (
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenancePinnedValuesAuthority(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	q := dbgen.New(f.conn)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("pinned-first")); err != nil {
		t.Fatal(err)
	}
	// The fixture has sixty observed 990/1000 intervals in the full rolling
	// hour. Its displayed bucket is narrower than the calculation/proof window.
	// Only the separate revision-zero generation retains legacy field omission.
	sample := api.SloOperationalSample{
		BucketStart: f.at.Add(-time.Minute), BucketEnd: f.at, EvaluationAt: f.at,
		State: "observed", Revision: "1", Good: new("59400"), Total: new("60000"),
		Attainment: new("0.99"), AllowedBudget: new("600"), RemainingBudget: new("0"),
		RemainingFraction: new("0"), Burn: new("1"), ReceivedAt: new(f.at),
		RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{"last"},
		Quality: api.SloOperationalQuality{State: "complete", Freshness: "fresh",
			ExpectedIntervals: 60, ObservedIntervals: 60, Coverage: new("1"),
			FirstObservationAt: new(f.at.Add(-time.Hour)), LastObservationAt: new(f.at), LatestReceiptAt: new(f.at)},
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	proof, err := readMaintenanceCompatibility(t.Context(), tx, mustPGUUID(f.ids[0]), 0, 1, f.at.Add(-time.Hour), f.at)
	if err != nil || proof.Kind != "indexed_absence" {
		t.Fatalf("future maintenance did not prove the full source window unchanged: %+v, %v", proof, err)
	}
	source, err := dbgen.New(tx).GetDerivedSourceGeneration(t.Context(), dbgen.GetDerivedSourceGenerationParams{SliID: mustPGUUID(f.sliID), MeasurementRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	adjusted := sample
	adjusted.Quality.ExcludedIntervals = new(int64(0))
	adjusted.Quality.ExcludedGood, adjusted.Quality.ExcludedTotal = jsontext.Value(`"0"`), jsontext.Value(`"0"`)
	adjusted.CalculationProvenance = &api.SloCalculationProvenance{
		RequestedRevision: "1", ComputedRevision: "0", SourceGeneration: strconv.FormatInt(source, 10),
		Compatibility: &api.SloMaintenanceCompatibility{Kind: "indexed_absence", ComputedRevision: "0", RequestedRevision: "1",
			IndexedThrough: strconv.FormatInt(proof.IndexedThrough, 10), Start: proof.Start.UTC(), End: proof.End.UTC()},
	}
	insert := func(revision int64, saved api.SloOperationalSample) string {
		t.Helper()
		payload, err := marshalReceiptMetadata(struct {
			Budget api.SloOperationalSeries `json:"budget_series"`
		}{Budget: api.SloOperationalSeries{Kind: "rolling_budget", Samples: []api.SloOperationalSample{saved}}})
		if err != nil {
			t.Fatal(err)
		}
		id, err := q.InsertDerivedGeneration(t.Context(), dbgen.InsertDerivedGenerationParams{
			SloID: mustPGUUID(f.ids[0]), DefinitionRevision: 1, MeasurementRevision: 1,
			ExclusionRevision: revision, SourceGeneration: source, CalculationVersion: operationalCalculationVersion,
			EvaluatedThrough: timestamp(f.at), PublishedAt: timestamp(f.now), RetentionStart: timestamp(f.historyStart),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := q.InsertPreparedSloHistory(t.Context(), dbgen.InsertPreparedSloHistoryParams{
			GenerationID: id, RangeStart: timestamp(f.at.Add(-time.Minute)), RangeEnd: timestamp(f.at),
			BurnWindowSeconds: 3600, Payload: payload, Kind: "retained_detail",
		}); err != nil {
			t.Fatal(err)
		}
		return id.String()
	}
	pinned, legacy := insert(1, adjusted), insert(0, sample)
	second := f.input("pinned-second")
	second.Start, second.End = f.at.Add(3*time.Hour), f.at.Add(4*time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, second); err != nil {
		t.Fatal(err)
	}
	if revision, err := q.ReadMaintenanceState(t.Context(), mustPGUUID(f.ids[0])); err != nil || revision != 2 {
		t.Fatalf("fixture did not advance authority: R=%d, %v", revision, err)
	}
	input := SloHistoryValuesInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], GenerationID: pinned, Lane: "budget", Limit: 100, Now: f.clock}
	for _, tc := range []struct {
		id, revision string
		saved        api.SloOperationalSample
	}{{pinned, "1", adjusted}, {legacy, "0", sample}} {
		input.GenerationID = tc.id
		page, err := ReadSloHistoryValues(t.Context(), f.f.databaseURL, input)
		if err != nil {
			t.Fatal(err)
		}
		if page.Generation.ExclusionRevision != tc.revision || page.Generation.GenerationId.String() != tc.id {
			t.Fatalf("pinned authority changed: got R=%s generation=%s, want R=%s generation=%s (current R=2)", page.Generation.ExclusionRevision, page.Generation.GenerationId, tc.revision, tc.id)
		}
		if page.Generation.SourceGeneration != strconv.FormatInt(source, 10) || !reflect.DeepEqual(page.Samples, []api.SloOperationalSample{tc.saved}) || page.TotalValues != 1 || page.NextOffset != nil {
			t.Fatalf("saved sample/counts/receipts changed: %+v", page)
		}
	}
	input.GenerationID = pinned
	input.Now = func() time.Time { return f.now.Add(5*time.Minute + time.Microsecond) }
	if _, err := ReadSloHistoryValues(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrOperationalSnapshotOld) {
		t.Fatalf("expired generation accepted: %v", err)
	}
	input.Now = f.clock
	if _, err := RevokeSession(t.Context(), f.f.databaseURL, f.proof.SessionVerifier, f.now, "pinned-revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSloHistoryValues(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked reader accepted: %v", err)
	}
}
