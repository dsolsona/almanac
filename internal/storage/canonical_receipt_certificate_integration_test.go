//go:build integration

package storage

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func canonicalReceiptWindow(s *selfHealFixture) slo.WindowInput {
	return slo.WindowInput{At: s.at, Now: s.now, ValidFrom: s.historyStart, SliValidFrom: s.historyStart, RetentionStart: s.historyStart.Add(-time.Hour), Duration: 3 * time.Hour, Cadence: s.cadence, FreshnessGrace: time.Minute, Target: "0.99"}
}

func TestIntegrationCanonicalReceiptCertificate(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"legacy", "receipt changed", "generation changed", "certified absent"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixture(t, "receipt-certificate", true)
			s.drain(t, "initial certified hours")
			q := dbgen.New(s.conn)
			before, err := readReceiptSQLTotals(t, q, s)
			if err != nil || !before.LatestReceipt.Valid {
				t.Fatalf("baseline %+v %v", before, err)
			}
			// The earliest hour is unknown; later known hours must not turn its unknown
			// maximum into a misleading known lower bound.
			update := "receipt_source_generation=NULL"
			switch mode {
			case "receipt changed":
				update = "latest_receipt_at=latest_receipt_at+interval '1 minute'"
			case "generation changed":
				update = "source_generation=source_generation+1"
			case "certified absent":
				update = "latest_receipt_at=NULL,certified_receipt_at=NULL"
			}
			if _, err := s.conn.Exec(t.Context(), "UPDATE almanac_sli_hourly_aggregates SET "+update+" WHERE sli_id=$1 AND hour_start=$2", s.sliID, s.historyStart); err != nil {
				t.Fatal(err)
			}
			got, err := readReceiptSQLTotals(t, q, s)
			if err != nil {
				t.Fatal(err)
			}
			before.LatestReceipt = pgtype.Timestamptz{}
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("quarantine changed values or exposed receipt: got %+v want %+v", got, before)
			}
			batch, err := q.ReadRetainedWindowBatch(t.Context(), dbgen.ReadRetainedWindowBatchParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1, Now: timestamp(s.now), WindowEnds: []pgtype.Timestamptz{timestamp(s.at)}, FullStarts: []pgtype.Timestamptz{timestamp(s.historyStart)}, FullEnds: []pgtype.Timestamptz{timestamp(s.at)}, EdgeAStarts: []pgtype.Timestamptz{timestamp(s.at)}, EdgeAEnds: []pgtype.Timestamptz{timestamp(s.at)}, EdgeBStarts: []pgtype.Timestamptz{timestamp(s.at)}, EdgeBEnds: []pgtype.Timestamptz{timestamp(s.at)}})
			if err != nil || len(batch) != 1 {
				t.Fatalf("batch read: %+v %v", batch, err)
			}
			result := dbgen.ReadDerivedWindowTotalsRow{Good: batch[0].Good, Total: batch[0].Total, Observed: batch[0].Observed, KnownEmpty: batch[0].KnownEmpty, ZeroTraffic: batch[0].ZeroTraffic, FirstObservation: batch[0].FirstObservation, LastObservation: batch[0].LastObservation, LatestReceipt: batch[0].LatestReceipt, Prepared: batch[0].Prepared, Exhausted: batch[0].Exhausted}
			if !reflect.DeepEqual(result, before) {
				t.Fatalf("batch quarantine: %+v want %+v", result, before)
			}

			rows, err := q.ListLegacyReceiptHours(t.Context(), 10)
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if mode == "certified absent" {
				expected = 0
			}
			if len(rows) != expected {
				t.Fatalf("candidate count %d want %d", len(rows), expected)
			}
		})
	}
}

func TestIntegrationCanonicalReceiptUnknownEmptyDoesNotPoison(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-empty-population", true)
	// These starts exist but supply no eligible observation receipts.
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE almanac_sli_observations SET outcome='failed',good_count=NULL,total_count=NULL,error_code='network',ingestion_generation=0 WHERE sli_id=$1 AND interval_start<$2`, s.sliID, s.historyStart.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.drain(t, "failed and observed hours")
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL,latest_receipt_at=$3 WHERE sli_id=$1 AND hour_start=$2`, s.sliID, s.historyStart, s.at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(s.conn)
	got, err := evaluateDerivedWindow(t.Context(), q, mustPGUUID(s.sliID), 1, canonicalReceiptWindow(s))
	if err != nil || got.LatestReceipt == nil || !got.LatestReceipt.Equal(s.at) || got.Observed != 120 {
		t.Fatalf("empty hour poisoned known receipts: %+v %v", got, err)
	}
	batch, err := evaluateDerivedWindows(t.Context(), q, mustPGUUID(s.sliID), 1, []slo.WindowInput{canonicalReceiptWindow(s)})
	if err != nil || len(batch) != 1 || !reflect.DeepEqual(got, batch[0]) {
		t.Fatalf("batch %+v %v", batch, err)
	}
}

func TestIntegrationCanonicalReceiptRepairQueries(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-repair-query", true)
	s.drain(t, "initial summary")
	q := dbgen.New(s.conn)
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	candidates, err := q.ListLegacyReceiptHours(t.Context(), 2)
	if err != nil || len(candidates) != 2 || !candidates[0].HourStart.Time.Equal(s.at.Add(-time.Hour)) || !candidates[1].HourStart.Time.Equal(s.at.Add(-2*time.Hour)) {
		t.Fatalf("bounded newest candidates: %+v %v", candidates, err)
	}
	c := candidates[0]
	tx, err := s.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	locked := dbgen.New(tx)
	if _, err := locked.LockCanonicalReceiptSli(t.Context(), c.SliID); err != nil {
		t.Fatal(err)
	}
	key := dbgen.LockLegacyReceiptHourParams{SliID: c.SliID, MeasurementRevision: c.MeasurementRevision, HourStart: c.HourStart}
	hour, err := locked.LockLegacyReceiptHour(t.Context(), key)
	if err != nil || !hour.PopulationReady {
		t.Fatal(err)
	}
	other := integrationConnect(t, s.f.databaseURL)
	defer closeIntegrationConn(other)
	if _, err := dbgen.New(other).LockLegacyReceiptHour(t.Context(), key); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("leased row did not skip: %v", err)
	}
	raw, err := locked.ReadCanonicalReceiptPopulation(t.Context(), dbgen.ReadCanonicalReceiptPopulationParams{SliID: c.SliID, MeasurementRevision: c.MeasurementRevision, HourStart: c.HourStart})
	if err != nil || raw.RawRows != 60 || raw.Observed != hour.ObservedIntervals || raw.Good != hour.Good || raw.Total != hour.Total || !raw.LatestReceipt.Time.Equal(s.at) {
		t.Fatalf("bounded raw: %+v %v stored %+v", raw, err, hour)
	}
	params := dbgen.UpdateCanonicalReceiptMetadataParams{SliID: c.SliID, MeasurementRevision: c.MeasurementRevision, HourStart: c.HourStart, SourceGeneration: hour.SourceGeneration + 1, LatestReceiptAt: raw.LatestReceipt}
	if n, err := locked.UpdateCanonicalReceiptMetadata(t.Context(), params); err != nil || n != 0 {
		t.Fatalf("stale generation wrote: %d %v", n, err)
	}
	params.SourceGeneration = hour.SourceGeneration
	if n, err := locked.UpdateCanonicalReceiptMetadata(t.Context(), params); err != nil || n != 1 {
		t.Fatalf("repair: %d %v", n, err)
	}
	if n, err := locked.UpdateCanonicalReceiptMetadata(t.Context(), params); err != nil || n != 0 {
		t.Fatalf("already certified update: %d %v", n, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := q.LockLegacyReceiptHour(t.Context(), key); err != nil || !reflect.DeepEqual(got, hour) {
		t.Fatalf("rollback changed summary: %+v %v", got, err)
	}
	// Certifying unknown is terminal repair metadata, not destructive numeric repair.
	params.LatestReceiptAt = pgtype.Timestamptz{}
	if n, err := q.UpdateCanonicalReceiptMetadata(t.Context(), params); err != nil || n != 1 {
		t.Fatalf("unknown repair: %d %v", n, err)
	}
	var same bool
	if err := s.conn.QueryRow(t.Context(), `SELECT good::text=$4 AND total::text=$5 AND observed_intervals=$6 AND source_generation=$7 AND latest_receipt_at IS NULL AND certified_receipt_at IS NULL AND receipt_source_generation=source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND measurement_revision=$2 AND hour_start=$3`, c.SliID, c.MeasurementRevision, c.HourStart, hour.Good, hour.Total, hour.ObservedIntervals, hour.SourceGeneration).Scan(&same); err != nil || !same {
		t.Fatalf("unknown changed numbers: %v %v", same, err)
	}
	if _, err := q.LockLegacyReceiptHour(t.Context(), key); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown remains repair head: %v", err)
	}
	// Publishing canonical facts later restores an exact receipt and certificate.
	if _, err := q.RebuildDerivedBatch(t.Context(), dbgen.RebuildDerivedBatchParams{SliIds: []pgtype.UUID{c.SliID}, Revisions: []int64{1}, Generations: []int64{hour.SourceGeneration}, Starts: []pgtype.Timestamptz{c.HourStart}}); err != nil {
		t.Fatal(err)
	}
	if err := s.conn.QueryRow(t.Context(), `SELECT latest_receipt_at=$4 AND certified_receipt_at=latest_receipt_at AND receipt_source_generation=source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND measurement_revision=$2 AND hour_start=$3`, c.SliID, c.MeasurementRevision, c.HourStart, s.at).Scan(&same); err != nil || !same {
		t.Fatalf("publication failed certificate: %v %v", same, err)
	}
}

func TestIntegrationCanonicalReceiptDirtyPopulation(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-dirty-population", true)
	s.drain(t, "clean initial hours")
	q := dbgen.New(s.conn)
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkDerivedHoursDirty(t.Context(), dbgen.MarkDerivedHoursDirtyParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1, RangeStart: timestamp(s.historyStart), RangeEnd: timestamp(s.historyStart.Add(time.Hour)), Now: timestamp(s.now)}); err != nil {
		t.Fatal(err)
	}
	for index, want := range []bool{false, true} {
		row, err := q.LockLegacyReceiptHour(t.Context(), dbgen.LockLegacyReceiptHourParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1, HourStart: timestamp(s.historyStart.Add(time.Duration(index) * time.Hour))})
		if err != nil || row.PopulationReady != want {
			t.Fatalf("hour%d ready=%v want%v err=%v", index, row.PopulationReady, want, err)
		}
	}
}

func TestIntegrationCanonicalReceiptRepairCandidateIndex(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "receipt-candidate-index", true)
	s.drain(t, "index fixture")
	// Mostly certified retained history must not require scanning all ready hours
	// to find the one legacy row. These are isolated aggregate-only fixtures.
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO almanac_sli_hourly_aggregates(sli_id,measurement_revision,hour_start,source_generation,good,total,observed_intervals,known_empty_intervals,zero_traffic_intervals,first_interval_start,last_interval_end,latest_receipt_at,receipt_source_generation,certified_receipt_at)
 SELECT $1,1,h,1,0,0,0,0,0,h,h+interval '1 hour',NULL,1,NULL FROM generate_series($2::timestamptz-interval '400 days',$2::timestamptz-interval '1 hour',interval '1 hour') h;`, s.sliID, s.historyStart); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL WHERE sli_id=$1 AND hour_start=$2`, s.sliID, s.historyStart); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `ANALYZE almanac_sli_hourly_aggregates`); err != nil {
		t.Fatal(err)
	}
	capture := &factsQueryCapture{DBTX: s.conn}
	if _, err := dbgen.New(capture).ListLegacyReceiptHours(t.Context(), 1); !errors.Is(err, errFactsQueryCaptured) {
		t.Fatal(err)
	}
	var plan string
	if err := s.conn.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+capture.statement, capture.arguments...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "almanac_canonical_receipt_repair_idx") {
		t.Fatalf("candidate index not used: %s", plan)
	}
	t.Logf("candidate plan: %s", plan)
}

func readReceiptSQLTotals(t *testing.T, q *dbgen.Queries, s *selfHealFixture) (dbgen.ReadDerivedWindowTotalsRow, error) {
	t.Helper()
	return q.ReadDerivedWindowTotals(t.Context(), dbgen.ReadDerivedWindowTotalsParams{SliID: mustPGUUID(s.sliID), MeasurementRevision: 1, FullStart: timestamp(s.historyStart), FullEnd: timestamp(s.at), Now: timestamp(s.now), WindowEnd: timestamp(s.at), EdgeStarts: []pgtype.Timestamptz{}, EdgeEnds: []pgtype.Timestamptz{}})
}
