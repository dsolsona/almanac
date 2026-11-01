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

func verifyRetainedCurrentPoint(t *testing.T, databaseURL string, proof DatasourceProof, id string, clock func() time.Time, oracle api.SloOperationalDetail) {
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
}
