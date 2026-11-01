//go:build integration

package storage

import (
	"encoding/json/v2"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func verifyRetainedCurrentPoint(t *testing.T, runtime *Runtime, databaseURL string, proof DatasourceProof, id string, clock func() time.Time, oracle api.SloOperationalDetail) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	q := dbgen.New(conn)
	pgID, _ := canonicalUUID(id)
	definition, _ := strconv.ParseInt(oracle.Objective.DefinitionRevision, 10, 64)
	measurement, _ := strconv.ParseInt(oracle.Context.MeasurementRevision, 10, 64)
	params := dbgen.ReadRetainedSloPointParams{SloID: pgID, DefinitionRevision: definition, MeasurementRevision: measurement,
		CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(oracle.EffectiveAt)}
	stored, err := q.ReadRetainedSloPoint(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	var point retainedSloPoint
	if err := json.Unmarshal(stored.Payload, &point); err != nil {
		t.Fatal(err)
	}
	end := oracle.EffectiveAt
	start := end.Add(-time.Duration(oracle.Objective.CadenceSeconds) * time.Second)
	interval, err := ReadSloOperationalDetail(t.Context(), databaseURL, OperationalDetailInput{Proof: proof, ID: id, Now: clock, At: &end, Start: &start, End: &end, BurnWindow: 3600})
	if err != nil || len(interval.AttainmentSeries.Samples) != 1 {
		t.Fatalf("canonical interval oracle failed: %v", err)
	}
	expected := oracle.Current
	expected.Window.RequestedStart = end.Add(-time.Duration(oracle.Objective.RollingWindowSeconds) * time.Second)
	if !reflect.DeepEqual(point.Current, expected) || !reflect.DeepEqual(point.Burn1h, oracle.Burn1h) || !reflect.DeepEqual(point.Burn6h, oracle.Burn6h) || !reflect.DeepEqual(point.Attainment, interval.AttainmentSeries.Samples[0]) {
		t.Fatal("durable attainment, budget or burn point differs from canonical evaluation")
	}
	if stored.SourceGeneration <= 0 {
		t.Fatal("retained point lost its accepted-source generation")
	}
	changed, err := q.PublishRetainedSloPoint(t.Context(), dbgen.PublishRetainedSloPointParams{
		SloID: pgID, DefinitionRevision: definition, MeasurementRevision: measurement, CalculationVersion: operationalCalculationVersion,
		EvaluationAt: params.EvaluationAt, SourceGeneration: stored.SourceGeneration - 1, PublishedAt: timestamp(clock()), Payload: []byte(`{}`)})
	if err != nil || changed != 0 {
		t.Fatalf("older source generation overwrote retained point: %d, %v", changed, err)
	}
	again, err := q.ReadRetainedSloPoint(t.Context(), params)
	if err != nil || !reflect.DeepEqual(stored, again) {
		t.Fatal("generation fence failed to preserve the retained point")
	}
	if worked, err := runtime.prepareRetainedTile(t.Context(), clock); err != nil || !worked {
		t.Fatalf("retained point did not admit automatic hourly maintenance: %t, %v", worked, err)
	}
	var tilePayload []byte
	var pointCount int
	if err := conn.QueryRow(t.Context(), `SELECT payload,point_count FROM public.almanac_slo_series_tiles WHERE slo_id=$1 AND resolution_seconds=3600`, pgID).Scan(&tilePayload, &pointCount); err != nil {
		t.Fatal(err)
	}
	var tile retainedSloTile
	if err := json.Unmarshal(tilePayload, &tile); err != nil || pointCount != 1 || len(tile.Attainment) != 1 || len(tile.Budget) != 1 || len(tile.Burn1h) != 1 || len(tile.Burn6h) != 1 {
		t.Fatalf("retained hourly lanes missing or invalid: %v", err)
	}
	if !reflect.DeepEqual(tile.Attainment[0].Good, point.Attainment.Good) || !reflect.DeepEqual(tile.Budget[0].RemainingFraction, point.Current.Performance.RemainingFraction) ||
		!reflect.DeepEqual(tile.Burn1h[0].Burn, point.Burn1h.Performance.Burn) || !reflect.DeepEqual(tile.Burn6h[0].Burn, point.Burn6h.Performance.Burn) {
		t.Fatal("hourly resolution changed exact point values")
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_slo_series_tile_work
        (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,next_attempt_at)
        SELECT slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,$2
        FROM public.almanac_slo_series_tiles WHERE slo_id=$1 AND resolution_seconds=3600`, pgID, clock()); err != nil {
		t.Fatal(err)
	}
	work, err := q.ClaimRetainedTileWork(t.Context(), dbgen.ClaimRetainedTileWorkParams{Now: timestamp(clock()), LeaseUntil: timestamp(clock().Add(time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	arrival := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(arrival)
	// A clock-driven new point changes work even without changing source generation.
	if _, err := arrival.Exec(t.Context(), `UPDATE public.almanac_slo_series_tile_work SET version=version+1 WHERE id=$1`, work.ID); err != nil {
		t.Fatal(err)
	}
	published, err := q.PublishRetainedTile(t.Context(), dbgen.PublishRetainedTileParams{ID: work.ID, Version: work.Version,
		LeaseEpoch: work.LeaseEpoch, PointCount: 0, Payload: []byte(`{}`), PublishedAt: timestamp(clock())})
	if err != nil || published != 0 {
		t.Fatalf("stale tile publisher replaced a newer dirty version: %d, %v", published, err)
	}
	acknowledged, err := q.AcknowledgeRetainedTileWork(t.Context(), dbgen.AcknowledgeRetainedTileWorkParams{ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch})
	if err != nil || acknowledged != 0 {
		t.Fatalf("stale tile acknowledgment lost newer work: %d, %v", acknowledged, err)
	}
	if err := q.ReleaseRetainedTileWork(t.Context(), dbgen.ReleaseRetainedTileWorkParams{ID: work.ID, LeaseEpoch: work.LeaseEpoch}); err != nil {
		t.Fatal(err)
	}
}
