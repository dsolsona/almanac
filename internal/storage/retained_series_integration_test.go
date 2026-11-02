//go:build integration

package storage

import (
	"encoding/json/v2"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
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
	verifyRetainedDailyComposition(t, databaseURL, pgID, oracle.EffectiveAt)
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

func verifyRetainedDailyComposition(t *testing.T, databaseURL string, id pgtype.UUID, at time.Time) {
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
	saved, err := q.ReadRetainedSloPoint(t.Context(), dbgen.ReadRetainedSloPointParams{SloID: id, DefinitionRevision: row.DefinitionRevision,
		MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(at)})
	if err != nil {
		t.Fatal(err)
	}
	var prototype retainedSloPoint
	if err := json.Unmarshal(saved.Payload, &prototype); err != nil {
		t.Fatal(err)
	}
	start := utcDayStart(at).Add(-24 * time.Hour)
	revision := strconv.FormatInt(row.DefinitionRevision, 10)
	var points []retainedSloPoint
	for hour := range 24 {
		from := start.Add(time.Duration(hour) * time.Hour)
		point := prototype
		point.Current.EvaluationAt = from.Add(time.Hour)
		point.Attainment.EvaluationAt, point.Burn1h.End, point.Burn6h.End = point.Current.EvaluationAt, point.Current.EvaluationAt, point.Current.EvaluationAt
		point.Attainment.BucketStart, point.Attainment.BucketEnd = from, point.Current.EvaluationAt
		points = append(points, point)
		payload, err := json.Marshal(point)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.PublishRetainedSloPoint(t.Context(), dbgen.PublishRetainedSloPointParams{SloID: id, DefinitionRevision: row.DefinitionRevision,
			MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(point.Current.EvaluationAt),
			SourceGeneration: saved.SourceGeneration, PublishedAt: timestamp(at), Payload: payload}); err != nil {
			t.Fatal(err)
		}
		tile, err := compactRetainedPoints([]retainedSloPoint{point}, from, from.Add(time.Hour), row.Target, revision)
		if err != nil {
			t.Fatal(err)
		}
		tilePayload, err := json.Marshal(tile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_slo_series_tiles
        (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,point_count,payload,published_at)
        VALUES($1,$2,$3,$4,3600,$5,$6,$7,1,$8,$9)
        ON CONFLICT(slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start)
        DO UPDATE SET payload=EXCLUDED.payload,point_count=EXCLUDED.point_count,source_generation=EXCLUDED.source_generation`, id, row.DefinitionRevision, row.MeasurementRevision,
			operationalCalculationVersion, from, from.Add(time.Hour), saved.SourceGeneration, tilePayload, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(t.Context(), `DELETE FROM public.almanac_slo_series_tile_work WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start>=$2 AND range_start<$3`, id, start, start.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	work := dbgen.AlmanacSloSeriesTileWork{SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
		CalculationVersion: operationalCalculationVersion, ResolutionSeconds: 86400, RangeStart: timestamp(start), RangeEnd: timestamp(start.Add(24 * time.Hour)), SourceGeneration: saved.SourceGeneration}
	want, err := compactRetainedPoints(points, start, start.Add(24*time.Hour), row.Target, revision)
	if err != nil {
		t.Fatal(err)
	}
	check := func(hourly bool) {
		t.Helper()
		got, err := prepareRetainedTileValue(t.Context(), q, work, row.Target)
		if err != nil || got.hourly != hourly || got.count != 24 || got.generation != saved.SourceGeneration || !reflect.DeepEqual(got.tile, want) ||
			got.last == nil || !reflect.DeepEqual(*got.last, points[23].Current) {
			t.Fatalf("daily composition hourly=%t changed count, generation, closing value or exact lanes: %+v, %v", hourly, got, err)
		}
	}
	check(true)
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_slo_series_tile_work
        (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,next_attempt_at)
        VALUES($1,$2,$3,$4,3600,$5,$5::timestamptz+interval '1 hour',$6,$7)`, id, row.DefinitionRevision, row.MeasurementRevision, operationalCalculationVersion, start, saved.SourceGeneration, at); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := tx.Exec(t.Context(), `DELETE FROM public.almanac_slo_series_tile_work WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, id, start); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `DELETE FROM public.almanac_slo_series_tiles WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, id, start); err != nil {
		t.Fatal(err)
	}
	check(false)
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
	// Reproduce a completed day's partial graph tile without a midnight result.
	saved, err := q.ReadRetainedSloPoint(t.Context(), dbgen.ReadRetainedSloPointParams{SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(at)})
	if err != nil {
		t.Fatal(err)
	}
	var point retainedSloPoint
	if err := json.Unmarshal(saved.Payload, &point); err != nil {
		t.Fatal(err)
	}
	dayStart := utcDayStart(at.Add(-time.Microsecond))
	dayEnd := dayStart.Add(24 * time.Hour)
	tile, err := compactRetainedPoints([]retainedSloPoint{point}, dayStart, dayEnd, row.Target, strconv.FormatInt(row.DefinitionRevision, 10))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(tile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_slo_series_tiles
        (slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,point_count,payload,published_at)
        VALUES($1,$2,$3,$4,86400,$5,$6,$7,1,$8,$9)
        ON CONFLICT(slo_id,definition_revision,measurement_revision,exclusion_revision,calculation_version,resolution_seconds,range_start)
        DO UPDATE SET payload=EXCLUDED.payload,source_generation=EXCLUDED.source_generation`, id, row.DefinitionRevision, row.MeasurementRevision, operationalCalculationVersion, dayStart, dayEnd, saved.SourceGeneration, payload, at); err != nil {
		t.Fatal(err)
	}
	reader := retainedSeriesReader{q: q, row: row, now: dayEnd.Add(time.Hour)}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	daily, summary, err := reader.daily(t.Context(), slo.WindowInput{Cadence: cadence, ValidFrom: row.EvaluationValidFrom.Time, SliValidFrom: row.SliValidFrom.Time}, dayStart, dayEnd, dayEnd, id.Bytes, strconv.FormatInt(row.DefinitionRevision, 10), row.Target, time.Duration(row.RollingWindowSeconds)*time.Second, cadence)
	if err != nil || len(daily.Days) != 1 || daily.Days[0].Quality.State != "unavailable" || daily.Days[0].Performance.Attainment != nil || summary.UnknownDays != 1 || !reader.pending || reader.failed {
		t.Fatalf("partial daily graph tile blocked history or fabricated compliance: %+v, %+v, %v", daily, summary, err)
	}
	// Deleted tiles before retention are expired facts, never preparation work.
	retention := reader.now.Add(-365 * 24 * time.Hour)
	expiredEnd := utcDayStart(retention)
	expiredStart := expiredEnd.Add(-24 * time.Hour)
	validFrom := expiredStart.Add(-window)
	reader.pending = false
	daily, summary, err = reader.daily(t.Context(), slo.WindowInput{Now: reader.now, Cadence: cadence, ValidFrom: validFrom, SliValidFrom: validFrom, RetentionStart: retention, Target: row.Target}, expiredStart, expiredEnd, expiredEnd, id.Bytes, strconv.FormatInt(row.DefinitionRevision, 10), row.Target, time.Duration(row.RollingWindowSeconds)*time.Second, cadence)
	if err != nil || len(daily.Days) != 1 || daily.Days[0].Quality.State != "expired_history" || daily.Days[0].Quality.ExpiredIntervals == 0 || daily.Days[0].Explanation != "expired_history" || daily.Days[0].Performance.Attainment != nil || summary.UnknownDays != 1 || reader.pending || reader.failed {
		t.Fatalf("expired daily history became pending or successful: %+v, %+v, %v", daily, summary, err)
	}
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

func verifyIncrementalRetainedAvailability(t *testing.T, databaseURL string, id pgtype.UUID, at time.Time) {
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
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	pointAt := at.Add(-3*time.Hour - cadence)
	full, err := q.ReadRetainedTilePoints(t.Context(), dbgen.ReadRetainedTilePointsParams{SloID: id, DefinitionRevision: row.DefinitionRevision,
		MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(at.Add(-time.Hour)), EvaluationAt_2: timestamp(at)})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) == 0 {
		t.Fatal("compaction fixture has no retained endpoints")
	}
	var tiles [2]retainedSloTile
	var elapsed [2]time.Duration
	for version := range 2 {
		started := time.Now()
		if version == 0 {
			points := make([]retainedSloPoint, len(full))
			for index, stored := range full {
				if err := json.Unmarshal(stored.Payload, &points[index]); err != nil {
					t.Fatal(err)
				}
			}
			tiles[version], err = compactRetainedPoints(points, at.Add(-time.Hour), at, row.Target, strconv.FormatInt(row.DefinitionRevision, 10))
		} else {
			candidates, queryErr := q.ReadRetainedTileCandidates(t.Context(), dbgen.ReadRetainedTileCandidatesParams{SloID: id, DefinitionRevision: row.DefinitionRevision,
				MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(at.Add(-time.Hour)), EvaluationAt_2: timestamp(at)})
			if queryErr != nil || len(candidates) != len(full) {
				t.Fatalf("candidate projection changed retained endpoint count: %d/%d, %v", len(candidates), len(full), queryErr)
			}
			tiles[version], err = compactRetainedPayloads(candidates, at.Add(-time.Hour), at, row.Target, strconv.FormatInt(row.DefinitionRevision, 10))
		}
		if err != nil {
			t.Fatal(err)
		}
		elapsed[version] = time.Since(started)
	}
	if !reflect.DeepEqual(tiles[0], tiles[1]) {
		t.Fatal("selective compaction changed exact lanes, quality, extrema or gaps")
	}
	t.Logf("selective compaction rows=%d full_decode_reduce=%s selective_decode_reduce=%s", len(full), elapsed[0], elapsed[1])
	var prefix, generation int64
	if err := tx.QueryRow(t.Context(), `SELECT coalesce(max(ingestion_generation) FILTER(WHERE interval_end<=$3),0),max(ingestion_generation)
        FROM public.almanac_sli_coverage WHERE sli_id=$1 AND measurement_revision=$2`, row.SliID, row.MeasurementRevision, pointAt).Scan(&prefix, &generation); err != nil || prefix <= 0 || prefix >= generation {
		t.Fatalf("incremental fixture lacks measurements after its historical point: %d/%d, %v", prefix, generation, err)
	}
	// Later measurements do not change this historical calculation's inputs.
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_slo_series_points SET source_generation=$3 WHERE slo_id=$1 AND evaluation_at=$2`, id, pointAt, prefix); err != nil {
		t.Fatal(err)
	}
	start := pointAt.Add(-time.Microsecond).Truncate(time.Hour)
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_slo_series_tiles SET source_generation=$3 WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, id, start, prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_slo_series_repair SET cursor_at=$2,attempts=5 WHERE slo_id=$1`, id, at.Add(-400*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check := func(ready bool) {
		t.Helper()
		points, err := q.ReadRetainedEdgePoints(t.Context(), dbgen.ReadRetainedEdgePointsParams{SloID: id, DefinitionRevision: row.DefinitionRevision,
			MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(pointAt.Add(-cadence)), EvaluationAt2: timestamp(pointAt)})
		if err != nil || len(points) != 1 || points[0].Repairing == ready {
			t.Fatalf("incremental historical point readiness mismatch: want=%t rows=%d err=%v", ready, len(points), err)
		}
		parts, err := q.ReadRetainedParts(t.Context(), dbgen.ReadRetainedPartsParams{SloID: id, DefinitionRevision: row.DefinitionRevision,
			MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion,
			Starts: []pgtype.Timestamptz{timestamp(start)}, Ends: []pgtype.Timestamptz{timestamp(start.Add(time.Hour))}, Resolutions: []int32{3600}})
		if err != nil || len(parts) != 1 || parts[0].Ready != ready {
			t.Fatalf("incremental historical tile readiness mismatch: want=%t rows=%d err=%v", ready, len(parts), err)
		}
	}
	check(true)
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_sli_hourly_aggregates SET source_generation=$3 WHERE sli_id=$1 AND measurement_revision=$2 AND hour_start=$4`, row.SliID, row.MeasurementRevision, generation+1, start); err != nil {
		t.Fatal(err)
	}
	check(false)
}
