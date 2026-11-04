//go:build integration

package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func verifyRetainedTileWire(t *testing.T, conn *pgx.Conn, id pgtype.UUID, hour time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	q := dbgen.New(conn)
	row, err := q.GetSlo(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	params := dbgen.ReadRetainedPartsParams{SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion,
		Starts: []pgtype.Timestamptz{timestamp(hour), timestamp(hour.Add(48 * time.Hour))}, Ends: []pgtype.Timestamptz{timestamp(hour.Add(time.Hour)), timestamp(hour.Add(49 * time.Hour))}, Resolutions: []int32{3600, 3600}}
	var canonical string
	if err := conn.QueryRow(ctx, `SELECT payload::text FROM public.almanac_slo_series_tiles WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND resolution_seconds=3600 AND range_start=$4`, id, row.DefinitionRevision, row.MeasurementRevision, hour).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	parts, err := q.ReadRetainedParts(ctx, params)
	if err != nil || len(parts) != 2 {
		t.Fatalf("bounded tile wire read: %v", err)
	}
	var original, decoded retainedSloTile
	if json.Unmarshal([]byte(canonical), &original) != nil || json.Unmarshal(parts[0].Payload, &decoded) != nil || !reflect.DeepEqual(original, decoded) ||
		len(parts[0].Payload) >= len(canonical) || !parts[0].Ready || parts[1].Ready || string(parts[1].Payload) != "{}" ||
		parts[0].PayloadBytes != int64(len(parts[0].Payload)) || parts[1].PayloadBytes != parts[0].PayloadBytes {
		t.Fatal("null omission changed decoded tile values, missing-tile state or actual wire byte accounting")
	}
	params.ComplianceOnly = true
	compliance, err := q.ReadRetainedParts(ctx, params)
	if err != nil || len(compliance) != 2 {
		t.Fatalf("bounded daily-only wire read: %v", err)
	}
	var tile retainedSloTile
	var day *api.SloDailyComplianceDay
	if json.Unmarshal([]byte(canonical), &tile) != nil || json.Unmarshal(compliance[0].Payload, &day) != nil || !reflect.DeepEqual(day, tile.Compliance) ||
		compliance[0].Ready != parts[0].Ready || compliance[1].Ready || string(compliance[1].Payload) != "{}" ||
		compliance[0].PayloadBytes != int64(len(compliance[0].Payload)) || compliance[1].PayloadBytes != compliance[0].PayloadBytes || len(compliance[0].Payload) >= len(parts[0].Payload) {
		t.Fatal("daily-only wire changed compliance, readiness, missing state or byte accounting")
	}
	params.ComplianceOnly = false
	verifyRetainedCoarseWire(t, conn, params, row.Target)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(ctx, `UPDATE public.almanac_slo_series_tiles SET payload=jsonb_build_object('oversized',repeat('x',20971520)) WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND resolution_seconds=3600 AND range_start=$4`, id, row.DefinitionRevision, row.MeasurementRevision, hour); err != nil {
		t.Fatal(err)
	}
	parts, err = dbgen.New(tx).ReadRetainedParts(ctx, params)
	if err != nil || len(parts) != 2 {
		t.Fatalf("oversized tile wire preflight: %v", err)
	}
	for _, part := range parts {
		if part.PayloadBytes <= 20<<20 || string(part.Payload) != "{}" {
			t.Fatal("oversized tile payload crossed binary wire budget")
		}
	}
}

func verifyRetainedCoarseWire(t *testing.T, conn *pgx.Conn, params dbgen.ReadRetainedPartsParams, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var start, end time.Time
	var payload []byte
	var generation int64
	var published time.Time
	if err := conn.QueryRow(ctx, `SELECT range_start,range_end,payload,source_generation,published_at FROM public.almanac_slo_series_tiles
WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND resolution_seconds=86400
ORDER BY range_start DESC LIMIT 1`, params.SloID, params.DefinitionRevision, params.MeasurementRevision).Scan(&start, &end, &payload, &generation, &published); err != nil {
		t.Fatal("read populated daily tile for projection regression", err)
	}
	start, end = start.UTC(), end.UTC()
	var original retainedSloTile
	if err := json.Unmarshal(payload, &original); err != nil {
		t.Fatal(err)
	}
	// Reuse a real day to cross the lane budget without importing another history.
	const parts = 800
	params.Starts, params.Ends, params.Resolutions = make([]pgtype.Timestamptz, parts), make([]pgtype.Timestamptz, parts), make([]int32, parts)
	for i := range parts {
		params.Starts[i], params.Ends[i], params.Resolutions[i] = timestamp(start), timestamp(end), 86400
	}
	capture := &factsQueryCapture{}
	if _, err := dbgen.New(capture).ReadRetainedParts(ctx, params); !errors.Is(err, errFactsQueryCaptured) {
		t.Fatal("capture retained graph query failed", err)
	}
	var plan []byte
	if err := conn.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+capture.statement, capture.arguments...).Scan(&plan); err != nil {
		t.Fatal("retained graph projection plan failed", err)
	}
	if strings.Contains(string(plan), `"Relation Name": "almanac_sli_coverage`) || strings.Contains(string(plan), `"Relation Name": "almanac_sli_observation_values`) {
		t.Fatal("ready retained graph query scanned canonical observations")
	}
	artifact, err := os.CreateTemp("../../.local", "retained-graph-plan-*.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = artifact.Write(plan)
	if err := errors.Join(err, artifact.Close()); err != nil {
		t.Fatal(err)
	}
	t.Logf("retained graph projection plan: %s", artifact.Name())
	rows, err := dbgen.New(conn).ReadRetainedParts(ctx, params)
	if err != nil || len(rows) != parts {
		t.Fatalf("bounded coarse wire: %v", err)
	}
	var projected retainedSloTile
	if err := json.Unmarshal(rows[0].Payload, &projected); err != nil {
		t.Fatal(err)
	}
	var compliance *api.SloDailyComplianceDay
	if err := json.Unmarshal(rows[0].CompliancePayload, &compliance); err != nil || !reflect.DeepEqual(compliance, original.Compliance) {
		t.Fatal("reusable compliance payload changed the stored daily value", err)
	}
	if rows[0].PayloadBytes != int64(parts*(len(rows[0].Payload)+len(rows[0].CompliancePayload))) || rows[0].PayloadBytes > 20<<20 || !rows[0].Ready || rows[parts-1].Ordinal != parts {
		t.Fatal("coarse wire changed actual byte accounting, readiness or ordinals")
	}
	for _, lane := range []struct {
		metric              string
		original, projected []api.SloOperationalSample
	}{
		{"attainment", original.Attainment, projected.Attainment}, {"budget", original.Budget, projected.Budget},
		{"burn", original.Burn1h, projected.Burn1h}, {"burn", original.Burn6h, projected.Burn6h},
	} {
		extra := 0
		for _, sample := range lane.original {
			if len(sample.RetainedExtrema) == 0 {
				extra++
			}
		}
		want := lane.original
		var err error
		if extra*parts > 1500 {
			want, err = reduceRetainedLaneLimit(lane.original, start, end, target, lane.metric, 4)
		}
		if err != nil {
			t.Fatal(err)
		}
		coverage, err := retainedDisplayCoverage(lane.original)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(coverage.States)
		slices.Sort(coverage.QualityStates)
		slices.Sort(coverage.Freshness)
		if extra*parts > 1500 {
			for i := range want {
				want[i].DisplayCoverage = coverage
			}
		}
		if !reflect.DeepEqual(lane.projected, want) {
			gotJSON, _ := json.Marshal(lane.projected)
			wantJSON, _ := json.Marshal(want)
			t.Fatalf("SQL projection changed exact %s extrema, selected counts or complete coverage sets: got=%s want=%s", lane.metric, gotJSON, wantJSON)
		}
	}
	if !reflect.DeepEqual(projected.Compliance, original.Compliance) {
		t.Fatal("graph projection changed daily compliance")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	if _, err := tx.Exec(ctx, `UPDATE public.almanac_slo_series_tiles SET compact_payload=NULL WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND resolution_seconds=86400 AND range_start=$4`, params.SloID, params.DefinitionRevision, params.MeasurementRevision, start); err != nil {
		t.Fatal(err)
	}
	queued, err := q.QueueRetainedTileSummaries(ctx, dbgen.QueueRetainedTileSummariesParams{Now: timestamp(end.Add(time.Hour)), RetentionStart: timestamp(start.Add(-365 * 24 * time.Hour)), CalculationVersion: operationalCalculationVersion})
	if err != nil || queued != 1 {
		t.Fatalf("queue only the missing summary: %d,%v", queued, err)
	}
	work, err := q.ClaimRetainedTileWork(ctx, dbgen.ClaimRetainedTileWorkParams{Now: timestamp(end.Add(time.Hour)), LeaseUntil: timestamp(end.Add(2 * time.Hour))})
	if err != nil || !work.SummaryOnly || !work.RangeStart.Time.Equal(start) {
		t.Fatalf("claim summary: %+v,%v", work, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE public.almanac_slo_series_tile_work SET attempts=5 WHERE id=$1`, work.ID); err != nil {
		t.Fatal(err)
	}
	available, err := q.ReadRetainedParts(ctx, params)
	if err != nil || len(available) != parts || !available[0].Ready || available[0].Exhausted {
		t.Fatalf("summary work hid existing history: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE public.almanac_slo_series_tile_work SET attempts=1 WHERE id=$1`, work.ID); err != nil {
		t.Fatal(err)
	}
	if err := prepareStoredRetainedTileSummary(ctx, q, work, target); err != nil {
		t.Fatal(err)
	}
	var preserved bool
	if err := tx.QueryRow(ctx, `SELECT payload=$5::jsonb AND source_generation=$6 AND published_at=$7 FROM public.almanac_slo_series_tiles WHERE slo_id=$1 AND definition_revision=$2 AND measurement_revision=$3 AND resolution_seconds=86400 AND range_start=$4`, params.SloID, params.DefinitionRevision, params.MeasurementRevision, start, payload, generation, published).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("format upgrade changed retained data or timestamps: %v", err)
	}
	changed, err := tx.Exec(ctx, `UPDATE public.almanac_sli_hourly_aggregates a SET source_generation=9223372036854775807
FROM public.almanac_slo_definitions d WHERE d.slo_id=$1 AND d.revision=$2 AND a.sli_id=d.sli_id AND a.measurement_revision=$3
AND a.hour_start>=$4::timestamptz-GREATEST(d.rolling_window_seconds,21600)*interval '1 second' AND a.hour_start<$5`, params.SloID, params.DefinitionRevision, params.MeasurementRevision, start, end)
	if err != nil || changed.RowsAffected() == 0 {
		t.Fatalf("invalidate populated daily tile: %v", err)
	}
	stale, err := dbgen.New(tx).ReadRetainedParts(ctx, params)
	if err != nil || len(stale) != parts {
		t.Fatalf("read invalidated daily wire: %v", err)
	}
	for _, part := range stale {
		if part.Ready || part.PayloadBytes != 0 || string(part.Payload) != "{}" {
			t.Fatal("unready tile transferred stale values or affected the display budget")
		}
	}
}
