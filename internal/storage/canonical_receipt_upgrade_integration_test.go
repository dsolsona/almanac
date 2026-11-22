//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationCanonicalReceiptPopulatedUpgrade(t *testing.T) {
	t.Parallel()
	latest := latestTestSchemaVersion(t)
	s := newSelfHealFixture(t, "receipt-upgrade", true)
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE almanac_sli_observations SET outcome='failed',good_count=NULL,total_count=NULL,error_code='network',ingestion_generation=0 WHERE sli_id=$1 AND interval_start<$2`, s.sliID, s.historyStart.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.drain(t, "populated receipt upgrade")
	s.runtime.Close()
	dropMaintenanceCompilerFixture(t, s.conn)
	// Recreate actual legacy metadata (including an all-failed non-null receipt).
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET latest_receipt_at=$1`, s.at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `DROP INDEX almanac_canonical_receipt_repair_idx;
 ALTER TABLE almanac_sli_hourly_aggregates DROP COLUMN receipt_source_generation,DROP COLUMN certified_receipt_at,ALTER COLUMN latest_receipt_at SET NOT NULL;
 DROP INDEX public.almanac_maintenance_revision_range_idx; DELETE FROM almanac_schema_migrations WHERE version>=61;`); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var result string
		if err := s.conn.QueryRow(t.Context(), `SELECT jsonb_build_object(
   'hours',(SELECT jsonb_agg(to_jsonb(h)-'receipt_source_generation'-'certified_receipt_at' ORDER BY hour_start) FROM almanac_sli_hourly_aggregates h),
   'generations',(SELECT jsonb_agg(to_jsonb(g) ORDER BY sli_id,measurement_revision) FROM almanac_sli_source_generations g),
   'observations',(SELECT jsonb_agg(to_jsonb(o)-'backfill_chunk_id' ORDER BY interval_start) FROM almanac_sli_observations o),
   'ledger',(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM almanac_schema_migrations m WHERE version<61))::text`).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	result, err := Upgrade(t.Context(), s.f.databaseURL)
	if err != nil || result.Applied != latest-60 || result.DatabaseSchemaVersion != latest {
		t.Fatalf("upgrade %+v %v", result, err)
	}
	var historical int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_sli_observations WHERE backfill_chunk_id IS NOT NULL`).Scan(&historical); err != nil || historical != 0 {
		t.Fatalf("legacy observations acquired historical provenance: count %d, error %v", historical, err)
	}
	if snapshot() != before {
		t.Fatal("receipt migration changed measurements, stored data or prior ledger")
	}
	q := dbgen.New(s.conn)
	rows, err := q.ListLegacyReceiptHours(t.Context(), 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("legacy population %+v %v", rows, err)
	}
	totals, err := q.ReadDerivedWindowTotals(t.Context(), dbgen.ReadDerivedWindowTotalsParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1, FullStart: timestamp(s.historyStart), FullEnd: timestamp(s.historyStart.Add(time.Hour)), WindowEnd: timestamp(s.historyStart.Add(time.Hour)), Now: timestamp(s.now), EdgeStarts: []pgtype.Timestamptz{}, EdgeEnds: []pgtype.Timestamptz{}})
	if err != nil || totals.LatestReceipt.Valid || totals.Observed != 0 || totals.Good != "0" || totals.Total != "0" {
		t.Fatalf("failed population quarantine %+v %v", totals, err)
	}
	var generation int64
	if err := s.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, s.sliID, s.historyStart).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if n, err := q.UpdateCanonicalReceiptMetadata(t.Context(), dbgen.UpdateCanonicalReceiptMetadataParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1, HourStart: timestamp(s.historyStart), SourceGeneration: generation}); err != nil || n != 1 {
		t.Fatalf("nullable repair %d %v", n, err)
	}
}
