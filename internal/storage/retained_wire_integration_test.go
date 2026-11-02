//go:build integration

package storage

import (
	"context"
	"encoding/json/v2"
	"reflect"
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
	if string(parts[0].Payload) != canonical || !parts[0].Ready || parts[1].Ready || string(parts[1].Payload) != "{}" || parts[0].PayloadBytes != int64(len(canonical)) || parts[1].PayloadBytes != parts[0].PayloadBytes {
		t.Fatal("binary tile wire changed canonical UTF-8, missing-tile state or shared byte accounting")
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
