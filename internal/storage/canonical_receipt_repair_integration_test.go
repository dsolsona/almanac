//go:build integration

package storage

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationCanonicalReceiptWorkerRepairsBoundedMetadata(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-worker", true)
	s.drain(t, "initial certified hours")
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL,latest_receipt_at=$1`, s.at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for turn, want := range []int{2, 2, 1, 0} {
		if turn >= 2 {
			s.now = s.now.Add(time.Second)
		}
		s.runtime.runDerivedTurn(t.Context(), slog.Default(), s.clock, s.now.Add(time.Hour))
		rows, err := dbgen.New(s.conn).ListLegacyReceiptHours(t.Context(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != want {
			t.Fatalf("turn %d legacy hours=%d want=%d", turn, len(rows), want)
		}
	}
	var good, total string
	var receipt pgtype.Timestamptz
	if err := s.conn.QueryRow(t.Context(), `SELECT good::text,total::text,latest_receipt_at FROM almanac_sli_hourly_aggregates WHERE hour_start=$1`, s.historyStart).Scan(&good, &total, &receipt); err != nil {
		t.Fatal(err)
	}
	if good != "59400.000000000000" || total != "60000.000000000000" || !receipt.Valid || !receipt.Time.Equal(s.historyStart.Add(time.Hour)) {
		t.Fatalf("receipt repair changed counts or retained invalid maximum: good=%s total=%s receipt=%v", good, total, receipt)
	}
	// Cancellation must not mutate another invalid row or acquire work.
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL WHERE hour_start=$1`, s.historyStart); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.runtime.runDerivedTurn(ctx, slog.Default(), s.clock, s.now.Add(time.Hour))
	rows, err := dbgen.New(s.conn).ListLegacyReceiptHours(t.Context(), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("cancelled turn mutated metadata: rows=%v err=%v", rows, err)
	}
}

func TestIntegrationCanonicalReceiptRepairUnknownPreservesRetainedCounts(t *testing.T) {
	for _, mode := range []string{"expired", "partial", "dirty"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixture(t, "receipt-"+mode, true)
			s.drain(t, "initial counts")
			hour := s.at.Add(-time.Hour)
			if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL,latest_receipt_at=$1 WHERE hour_start=$2`, s.at.Add(time.Hour), hour); err != nil {
				t.Fatal(err)
			}
			// Retention removes raw rows without changing their retained aggregate.
			switch mode {
			case "expired":
				if _, err := s.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start>=$2`, s.sliID, hour); err != nil {
					t.Fatal(err)
				}
			case "partial":
				if _, err := s.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2`, s.sliID, hour.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			case "dirty":
				// Even matching summaries cannot certify identity while source work is due.
				if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE almanac_sli_observations SET ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, s.sliID, hour); err != nil {
					t.Fatal(err)
				}
			}
			var before, after string
			snapshot := `SELECT (to_jsonb(a)-'latest_receipt_at'-'receipt_source_generation'-'certified_receipt_at')::text FROM almanac_sli_hourly_aggregates a WHERE hour_start=$1`
			if err := s.conn.QueryRow(t.Context(), snapshot, hour).Scan(&before); err != nil {
				t.Fatal(err)
			}
			repaired, err := s.runtime.repairCanonicalReceipt(t.Context())
			if err != nil || !repaired {
				t.Fatalf("repair: %v %v", repaired, err)
			}
			if err := s.conn.QueryRow(t.Context(), snapshot, hour).Scan(&after); err != nil {
				t.Fatal(err)
			}
			var unknown, certified bool
			if err := s.conn.QueryRow(t.Context(), `SELECT latest_receipt_at IS NULL,receipt_source_generation=source_generation AND certified_receipt_at IS NULL FROM almanac_sli_hourly_aggregates WHERE hour_start=$1`, hour).Scan(&unknown, &certified); err != nil {
				t.Fatal(err)
			}
			if before != after || !unknown || !certified {
				t.Fatalf("%s corrupted retained facts or supplied false receipt: unchanged=%v unknown=%v certified=%v", mode, before == after, unknown, certified)
			}

			if mode != "dirty" {
				input := slo.WindowInput{At: s.at, Now: s.now, ValidFrom: s.historyStart, SliValidFrom: s.historyStart, RetentionStart: s.historyStart, Duration: time.Hour, Cadence: time.Minute, FreshnessGrace: time.Minute, Target: "0.99"}
				value, err := evaluateDerivedWindow(t.Context(), dbgen.New(s.conn), mustPGUUID(s.sliID), 1, input)
				if err != nil {
					t.Fatal(err)
				}
				if value.LatestReceipt != nil || value.Observed != 60 || value.State != "complete" || value.Freshness != "fresh" || value.Good == nil || *value.Good != "59400.000000000000" || value.Burn == nil || *value.Burn != "1.000000000000" {
					t.Fatalf("unknown receipt changed canonical evaluation: %+v", value)
				}
			}
			rows, err := dbgen.New(s.conn).ListLegacyReceiptHours(t.Context(), 10)
			if err != nil || len(rows) != 0 {
				t.Fatalf("unknown result remains queued: %v %v", rows, err)
			}
		})
	}
}

func TestIntegrationCanonicalReceiptRepairSkipsLockedHour(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-locked", true)
	s.drain(t, "initial counts")
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM almanac_sli_hourly_aggregates WHERE hour_start=$1 FOR UPDATE`, s.at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	repaired, err := s.runtime.repairCanonicalReceipt(t.Context())
	if err != nil || !repaired {
		t.Fatalf("locked head blocked another candidate: %v %v", repaired, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := dbgen.New(s.conn).ListLegacyReceiptHours(t.Context(), 10)
	if err != nil || len(rows) != 2 || !rows[0].HourStart.Time.Equal(s.at.Add(-time.Hour)) {
		t.Fatalf("locked row changed or bounded alternative not repaired: %v %v", rows, err)
	}
}

func TestIntegrationCanonicalReceiptRepairCancellationUnderSourceLock(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-source-lock", true)
	s.drain(t, "initial counts")
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `SELECT id FROM almanac_slis WHERE id=$1 FOR UPDATE`, s.sliID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	repaired, err := s.runtime.repairCanonicalReceipt(ctx)
	if repaired || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("source lock did not honor cancellation: repaired=%v err=%v", repaired, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := dbgen.New(s.conn).ListLegacyReceiptHours(t.Context(), 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("cancelled repair mutated retained metadata: %v %v", rows, err)
	}
	repaired, err = s.runtime.repairCanonicalReceipt(t.Context())
	if err != nil || !repaired {
		t.Fatalf("repair cannot resume after lock/cancellation: %v %v", repaired, err)
	}
}

func TestIntegrationCanonicalReceiptRepairConcurrentPublication(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-publication", true)
	s.drain(t, "initial counts")
	hour := s.at.Add(-time.Hour)
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL,latest_receipt_at=$1 WHERE hour_start=$2`, s.at.Add(time.Hour), hour); err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE almanac_sli_observations SET good_count=991,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, s.sliID, hour); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(s.conn)
	generation, err := q.GetDerivedSourceGeneration(t.Context(), dbgen.GetDerivedSourceGenerationParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := s.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(publisher)
	// Hold the normal publisher's changed row uncommitted while repair inspects
	// the previously visible legacy candidate through another pooled connection.
	if _, err := dbgen.New(publisher).RebuildDerivedBatch(t.Context(), dbgen.RebuildDerivedBatchParams{SliIds: []pgtype.UUID{mustPGUUID(s.sliID)}, Revisions: []int64{1}, Generations: []int64{generation}, Starts: []pgtype.Timestamptz{timestamp(hour)}}); err != nil {
		t.Fatal(err)
	}
	repaired, err := s.runtime.repairCanonicalReceipt(t.Context())
	if err != nil || repaired {
		t.Fatalf("repair overwrote or waited for concurrent publication: %v %v", repaired, err)
	}
	if err := publisher.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var good string
	var source, certificate int64
	var receipt pgtype.Timestamptz
	if err := s.conn.QueryRow(t.Context(), `SELECT good::text,source_generation,receipt_source_generation,latest_receipt_at FROM almanac_sli_hourly_aggregates WHERE hour_start=$1`, hour).Scan(&good, &source, &certificate, &receipt); err != nil {
		t.Fatal(err)
	}
	if good != "59401.000000000000" || source != generation || certificate != generation || !receipt.Valid || !receipt.Time.Equal(s.at) {
		t.Fatalf("publication lost or falsely certified: good=%s source=%d certificate=%d receipt=%v", good, source, certificate, receipt)
	}
	rows, err := q.ListLegacyReceiptHours(t.Context(), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("current publication remained in legacy queue: %v %v", rows, err)
	}
}
