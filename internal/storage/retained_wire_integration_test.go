//go:build integration

package storage

import (
	"context"
	"testing"
	"time"

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
