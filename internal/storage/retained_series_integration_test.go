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
	"github.com/jackc/pgx/v5/pgtype"
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
	verifyBackfillRangeAvailability(t, databaseURL, pgID, oracle.EffectiveAt)
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
	verifyRetainedRepairBatch(t, databaseURL, proof, id, clock, oracle)
}

func verifyBackfillRangeAvailability(t *testing.T, databaseURL string, id pgtype.UUID, at time.Time) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	row, err := q.GetSlo(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	window := max(time.Duration(row.RollingWindowSeconds)*time.Second, 6*time.Hour)
	old := at.Add(-window - 48*time.Hour).Truncate(time.Hour)
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_slo_series_repair SET cursor_at=$2,end_at=$3,attempts=5 WHERE slo_id=$1`, id, old, at); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `DELETE FROM public.almanac_slo_series_tile_work WHERE slo_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	params := dbgen.ReadRetainedPartsParams{SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion,
		Starts: []pgtype.Timestamptz{timestamp(at.Add(-time.Microsecond).Truncate(time.Hour))}, Ends: []pgtype.Timestamptz{timestamp(at.Add(-time.Microsecond).Truncate(time.Hour).Add(time.Hour))}, Resolutions: []int32{3600}}
	edge := dbgen.ReadRetainedEdgePointsParams{SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(at.Add(-time.Duration(row.CadenceSeconds) * time.Second)), EvaluationAt2: timestamp(at)}
	check := func(ready bool) {
		t.Helper()
		parts, err := q.ReadRetainedParts(t.Context(), params)
		if err != nil || len(parts) != 1 || parts[0].Ready != ready {
			t.Fatalf("backfill tile availability=%+v want %t: %v", parts, ready, err)
		}
		points, err := q.ReadRetainedEdgePoints(t.Context(), edge)
		if err != nil || len(points) != 1 || points[0].Repairing == ready {
			t.Fatalf("backfill endpoint availability=%+v want %t: %v", points, ready, err)
		}
	}
	check(true)
	var generation int64
	if err := tx.QueryRow(t.Context(), `SELECT generation FROM public.almanac_sli_source_generations WHERE sli_id=$1 AND measurement_revision=$2`, row.SliID, row.MeasurementRevision).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	for _, from := range []time.Time{old, at.Add(-time.Hour).Truncate(time.Hour)} {
		if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
            VALUES($1,$2,$3,$4,$4::timestamptz+interval '1 hour',$5,$5)
            ON CONFLICT(sli_id,measurement_revision,range_start) DO UPDATE SET generation=EXCLUDED.generation`, row.SliID, row.MeasurementRevision, generation+1, from, at); err != nil {
			t.Fatal(err)
		}
		check(from.Equal(old))
	}
	// Completed source work must still invalidate stale endpoints until repaired.
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_sli_hourly_aggregates SET source_generation=$3 WHERE sli_id=$1 AND measurement_revision=$2 AND hour_start=$4`, row.SliID, row.MeasurementRevision, generation+1, at.Add(-time.Hour).Truncate(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `DELETE FROM public.almanac_derived_dirty_ranges WHERE sli_id=$1 AND measurement_revision=$2`, row.SliID, row.MeasurementRevision); err != nil {
		t.Fatal(err)
	}
	check(false)
}

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
		canonical, err := ReadSloOperationalDetail(t.Context(), databaseURL, OperationalDetailInput{Proof: proof, ID: id,
			Now: clock, At: &at, Start: &from, End: &at, BurnWindow: 3600})
		if err != nil || len(canonical.AttainmentSeries.Samples) != 1 {
			t.Fatalf("canonical historical oracle failed: %v", err)
		}
		if !reflect.DeepEqual(point.Current, canonical.Current) || !reflect.DeepEqual(point.Burn1h, canonical.Burn1h) ||
			!reflect.DeepEqual(point.Burn6h, canonical.Burn6h) || !reflect.DeepEqual(point.Attainment, canonical.AttainmentSeries.Samples[0]) {
			t.Fatalf("batched historical counts/quality/budget/burn differ from canonical at %s", at)
		}
	}
	var version, epoch int64
	if err := conn.QueryRow(t.Context(), `SELECT version,lease_epoch FROM public.almanac_slo_series_repair
        WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND calculation_version=$4`,
		pgID, row.DefinitionRevision, row.MeasurementRevision, operationalCalculationVersion).Scan(&version, &epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slo_series_repair SET version=version+1 WHERE slo_id=$1`, pgID); err != nil {
		t.Fatal(err)
	}
	advanced, err := q.AdvanceRetainedSeriesRepair(t.Context(), dbgen.AdvanceRetainedSeriesRepairParams{SloID: pgID,
		DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion,
		Version: version, LeaseEpoch: epoch, CursorAt: timestamp(end), NextAttemptAt: timestamp(clock())})
	if err != nil || advanced != 0 {
		t.Fatalf("stale repair acknowledged a newer invalidation: %d, %v", advanced, err)
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
	if err := conn.QueryRow(t.Context(), `SELECT min(ingestion_generation),max(ingestion_generation) FROM public.almanac_sli_coverage WHERE sli_id=$1 AND measurement_revision=$2`, row.SliID, row.MeasurementRevision).Scan(&minimum, &maximum); err != nil || minimum <= 0 || maximum != before {
		t.Fatalf("accepted intervals lack their atomic source watermark: %d..%d / %d: %v", minimum, maximum, before, err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_coverage SELECT * FROM public.almanac_sli_coverage WHERE sli_id=$1 AND measurement_revision=$2 LIMIT 1 ON CONFLICT DO NOTHING`, row.SliID, row.MeasurementRevision); err != nil {
		t.Fatal(err)
	}
	after, err := q.GetDerivedSourceGeneration(t.Context(), params)
	if err != nil || after != before {
		t.Fatal("duplicate coverage advanced its committed source generation")
	}
	var start, end time.Time
	var generation int64
	var good, total string
	if err := conn.QueryRow(t.Context(), `SELECT c.interval_start,c.interval_end,c.ingestion_generation,v.good_count::text,v.total_count::text
        FROM public.almanac_sli_coverage c JOIN public.almanac_sli_observation_values v USING(interval_start,sli_id,measurement_revision)
        WHERE c.sli_id=$1 AND c.measurement_revision=$2 ORDER BY c.ingestion_generation DESC LIMIT 1`, row.SliID, row.MeasurementRevision).Scan(&start, &end, &generation, &good, &total); err != nil {
		t.Fatal(err)
	}
	deltas, err := q.ReadRetainedLateTotals(t.Context(), dbgen.ReadRetainedLateTotalsParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision,
		Starts: []pgtype.Timestamptz{timestamp(start), timestamp(start)}, Ends: []pgtype.Timestamptz{timestamp(end), timestamp(end)}, Generations: []int64{generation - 1, generation}})
	if err != nil || len(deltas) != 2 || deltas[0].Observed != 1 || deltas[0].Good != good || deltas[0].Total != total || deltas[1].Observed != 0 || deltas[1].Good != "0" || deltas[1].Total != "0" {
		t.Fatalf("late totals repeated or lost a newly accepted interval: %+v, %v", deltas, err)
	}
}
