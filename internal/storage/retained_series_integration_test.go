//go:build integration

package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func verifyRetainedRepairBatch(t *testing.T, databaseURL string, proof DatasourceProof, id string, clock func() time.Time, oracle api.SloOperationalDetail) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	q := dbgen.New(conn)
	pgID, _ := canonicalUUID(id)
	row, err := q.GetSlo(t.Context(), pgID)
	if err != nil {
		t.Fatal(err)
	}
	verifyRetainedIngestionWatermark(t, databaseURL, row)
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	end := oracle.EffectiveAt
	start := end.Add(-3 * cadence)
	points, err := buildRetainedRepairPoints(t.Context(), q, row, source, start, end, clock())
	if err != nil || len(points) != 3 {
		t.Fatalf("batched retained repair failed: %d points, %v", len(points), err)
	}
	for _, point := range points {
		at := point.Current.EvaluationAt
		from := at.Add(-cadence)
		canonical, err := readOperationalDetailForTest(t.Context(), databaseURL, OperationalDetailInput{Proof: proof, ID: id,
			Now: clock, At: &at, Start: &from, End: &at, BurnWindow: 3600})
		if err != nil || len(canonical.AttainmentSeries.Samples) != 1 {
			t.Fatalf("canonical historical oracle failed: %v", err)
		}
		if !reflect.DeepEqual(point.Current, canonical.Current) || !reflect.DeepEqual(point.Burn1h, canonical.Burn1h) ||
			!reflect.DeepEqual(point.Burn6h, canonical.Burn6h) || !reflect.DeepEqual(point.Attainment, canonical.AttainmentSeries.Samples[0]) {
			t.Fatalf("batched historical counts/quality/budget/burn differ from canonical at %s", at)
		}
	}
	span := retainedRepairSpan(time.Duration(row.RollingWindowSeconds)*time.Second, cadence)
	if span > max(time.Hour, cadence) {
		from := end.Add(-span)
		whole, err := buildRetainedRepairPoints(t.Context(), q, row, source, from, end, clock())
		if err != nil {
			t.Fatal(err)
		}
		var hourly []retainedSloPoint
		for cursor := from; cursor.Before(end); {
			through := floorSliBoundary(cursor.Add(max(time.Hour, cadence)), cadence)
			if through.After(end) {
				through = end
			}
			part, err := buildRetainedRepairPoints(t.Context(), q, row, source, cursor, through, clock())
			if err != nil {
				t.Fatal(err)
			}
			hourly = append(hourly, part...)
			cursor = through
		}
		if len(whole) != int(span/cadence) || !reflect.DeepEqual(whole, hourly) {
			t.Fatal("full repair batch changed exact values or quality across hourly boundaries")
		}
	}
}

func verifyRetainedIngestionWatermark(t *testing.T, databaseURL string, row dbgen.GetSloRow) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	q := dbgen.New(conn)
	params := dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision}
	before, err := q.GetDerivedSourceGeneration(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	var minimum, maximum int64
	if err := conn.QueryRow(t.Context(), `SELECT min(ingestion_generation),max(ingestion_generation) FROM public.almanac_sli_observations WHERE sli_id=$1 AND measurement_revision=$2`, row.SliID, row.MeasurementRevision).Scan(&minimum, &maximum); err != nil || minimum <= 0 || maximum != before {
		t.Fatalf("accepted intervals lack their atomic source watermark: %d..%d / %d: %v", minimum, maximum, before, err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_observations SELECT * FROM public.almanac_sli_observations WHERE sli_id=$1 AND measurement_revision=$2 LIMIT 1 ON CONFLICT DO NOTHING`, row.SliID, row.MeasurementRevision); err != nil {
		t.Fatal(err)
	}
	after, err := q.GetDerivedSourceGeneration(t.Context(), params)
	if err != nil || after != before {
		t.Fatal("duplicate coverage advanced its committed source generation")
	}
}
