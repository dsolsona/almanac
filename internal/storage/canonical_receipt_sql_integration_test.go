//go:build integration

package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationCanonicalReceiptContributions(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, first, second string }{{"observed and failed", "observed", "failed"}, {"known empty and incomplete", "complete_empty", "incomplete"}, {"only failures", "failed", "failed"}, {"observed then known empty", "observed", "complete_empty"}, {"failed then known empty", "failed", "complete_empty"}, {"zero traffic and failed", "observed", "failed"}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixture(t, "receipt-contributions", true)
			start := s.historyStart
			if _, err := execObservationFixture(t, s.conn, s.sliID, `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND (interval_start<$2 OR interval_start>=$2::timestamptz+interval '2 minutes')`, s.sliID, start); err != nil {
				t.Fatal(err)
			}
			good, total := "990", "1000"
			if c.name == "zero traffic and failed" {
				good, total = "0", "0"
			}
			for index, outcome := range []string{c.first, c.second} {
				if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE almanac_sli_observations SET outcome=$3,
 good_count=CASE WHEN $3='observed' THEN $5::numeric END,total_count=CASE WHEN $3='observed' THEN $6::numeric END,
 error_code=CASE WHEN $3='failed' THEN 'network' END,
 missing_components=CASE WHEN $3='incomplete' THEN ARRAY['total']::text[] ELSE '{}'::text[] END,
 recorded_at=$4,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, s.sliID, start.Add(time.Duration(index)*time.Minute), outcome, start.Add(time.Duration(1+2*index)*time.Minute), good, total); err != nil {
					t.Fatal(err)
				}
			}
			s.drain(t, "receipt source preparation")
			tx, err := s.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			facts := []slo.Observation{{Start: start, End: start.Add(time.Minute), Received: start.Add(time.Minute), Outcome: c.first}, {Start: start.Add(time.Minute), End: start.Add(2 * time.Minute), Received: start.Add(3 * time.Minute), Outcome: c.second}}
			for i := range facts {
				if facts[i].Outcome == "observed" {
					facts[i].Good = good
					facts[i].Total = total
				}
			}
			for _, duration := range []time.Duration{2 * time.Minute, time.Hour} {
				input := slo.WindowInput{At: start.Add(duration), Now: s.now, ValidFrom: start, SliValidFrom: start, RetentionStart: start.Add(-24 * time.Hour), Duration: duration, Cadence: time.Minute, FreshnessGrace: time.Minute, Target: "0.99", Observations: facts}
				want, err := slo.EvaluateOperationalWindowContext(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				var expectedReceipt *time.Time
				if c.first == "observed" || c.first == "complete_empty" {
					expectedReceipt = new(start.Add(time.Minute))
				}
				if c.second == "observed" || c.second == "complete_empty" {
					expectedReceipt = new(start.Add(3 * time.Minute))
				}
				if !reflect.DeepEqual(want.LatestReceipt, expectedReceipt) {
					t.Fatalf("independent oracle receipt=%v want=%v", want.LatestReceipt, expectedReceipt)
				}
				single, err := evaluateDerivedWindow(t.Context(), dbgen.New(tx), mustPGUUID(s.sliID), 1, input)
				if err != nil {
					t.Errorf("single %s: %v", duration, err)
				} else if !reflect.DeepEqual(single.LatestReceipt, want.LatestReceipt) || !reflect.DeepEqual(single.Good, want.Good) || !reflect.DeepEqual(single.Total, want.Total) || single.Observed != want.Observed || single.KnownEmpty != want.KnownEmpty {
					t.Errorf("single %s receipt=%v want=%v counts observed=%d/%d empty=%d/%d good=%v/%v total=%v/%v", duration, single.LatestReceipt, want.LatestReceipt, single.Observed, want.Observed, single.KnownEmpty, want.KnownEmpty, single.Good, want.Good, single.Total, want.Total)
				}
				if err == nil && (single.State != want.State || single.Freshness != want.Freshness || single.Performance != want.Performance || single.Reason != want.Reason || !reflect.DeepEqual(single.Burn, want.Burn)) {
					t.Errorf("single %s changed quality/freshness/truth: %+v versus %+v", duration, single, want)
				}
				batch, err := evaluateDerivedWindows(t.Context(), dbgen.New(tx), mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
				if err != nil {
					t.Errorf("batch %s: %v", duration, err)
				} else if len(batch) != 1 || !reflect.DeepEqual(batch[0].LatestReceipt, want.LatestReceipt) || !reflect.DeepEqual(batch[0].Good, want.Good) || !reflect.DeepEqual(batch[0].Total, want.Total) || batch[0].Observed != want.Observed || batch[0].KnownEmpty != want.KnownEmpty {
					t.Errorf("batch %s result=%+v want receipt=%v observed=%d empty=%d", duration, batch, want.LatestReceipt, want.Observed, want.KnownEmpty)
				}
				if err == nil && len(batch) == 1 && (batch[0].State != want.State || batch[0].Freshness != want.Freshness || batch[0].Performance != want.Performance || batch[0].Reason != want.Reason || !reflect.DeepEqual(batch[0].Burn, want.Burn)) {
					t.Errorf("batch %s changed quality/freshness/truth", duration)
				}

			}
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
