//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationAlertFallbackCrossHourCadence(t *testing.T) {
	t.Parallel()
	s := newFallbackFixtureSpan(t, 70*time.Second, 28*time.Hour)
	s.drain(t, "cross-hour clean source")
	end := time.Unix(s.at.Unix()/25200*25200, 0).UTC()
	input := slo.WindowInput{At: end, Now: s.now, Duration: 14 * time.Hour, Cadence: s.cadence,
		ValidFrom: s.historyStart, SliValidFrom: s.historyStart, Target: "0.999"}
	checkFallbackReference(t, s, []slo.WindowInput{input})
	// Pick a complete 70-second interval that crosses an ordinary hour boundary.
	var crossing time.Time
	if err := s.conn.QueryRow(t.Context(), `SELECT interval_start FROM public.almanac_sli_observations
WHERE sli_id=$1 AND interval_start>=$2 AND interval_start<$3
AND interval_start+interval '70 seconds'>date_trunc('hour',interval_start)+interval '1 hour'
ORDER BY interval_start LIMIT 1`, s.sliID, end.Add(-14*time.Hour), end).Scan(&crossing); err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE public.almanac_sli_observations SET good_count=0,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, s.sliID, crossing); err != nil {
		t.Fatal(err)
	}
	checkFallbackReference(t, s, []slo.WindowInput{input})
	p, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
	if err != nil {
		t.Fatal(err)
	}
	if !p.FullStarts[0].Time.Equal(end.Add(-14*time.Hour)) || !p.FullEnds[0].Time.Equal(end) {
		t.Fatal("fixture must use complete aggregate interior")
	}
	rows, err := experimentFallback(t.Context(), s.conn, p)
	if err != nil || len(rows) != 1 || rows[0].Observed != 720 || rows[0].Good != "711810.000000000000" || rows[0].Total != "720000.000000000000" {
		t.Fatalf("cross-hour interval lost or counted twice: %+v %v", rows, err)
	}
}

func TestIntegrationAlertFallbackSnapshotAndRevision(t *testing.T) {
	t.Parallel()
	s := newFallbackFixture(t, time.Minute)
	s.drain(t, "before snapshot")
	p, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, fallbackInputs(s))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	before, err := experimentFallbackLimited(t.Context(), tx, p, 8, 7200)
	if err != nil {
		t.Fatal(err)
	}
	writer := integrationConnect(t, s.f.databaseURL)
	defer closeIntegrationConn(writer)
	if _, err := execObservationFixture(t, writer, s.sliID, `UPDATE public.almanac_sli_observations SET good_count=985.6,ingestion_generation=0 WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	s.drain(t, "concurrent publication")
	again, err := experimentFallbackLimited(t.Context(), tx, p, 8, 7200)
	if err != nil || !reflect.DeepEqual(before, again) {
		t.Fatalf("snapshot mixed generations: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkFallbackReference(t, s, fallbackInputs(s))
	after, err := experimentFallback(t.Context(), s.conn, p)
	if err != nil || reflect.DeepEqual(before, after) {
		t.Fatalf("new snapshot failed to see correction: %v", err)
	}
	// 14.4 bad per 1000 against 99.9% is exactly 14.4x, independent of display rounding.
	for _, r := range after {
		input := fallbackInputs(s)[r.Ordinal-1]
		result, err := evaluateDerivedCounts(t.Context(), input, dbgen.ReadDerivedWindowTotalsRow{Good: r.Good, Total: r.Total,
			Observed: r.Observed, KnownEmpty: r.KnownEmpty, ZeroTraffic: r.ZeroTraffic, FirstObservation: r.FirstObservation,
			LastObservation: r.LastObservation, LatestReceipt: r.LatestReceipt, Prepared: true})
		if err != nil || result.ExactBurn == nil || result.ExactBurn.RatString() != "72/5" {
			t.Fatalf("threshold equality: %+v %v", result, err)
		}
	}
	// A different revision has different counts and its own invalidation.
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_measurements
SELECT (jsonb_populate_record(NULL::public.almanac_sli_measurements,to_jsonb(m)||'{"revision":2}'::jsonb)).*
FROM public.almanac_sli_measurements m WHERE sli_id=$1 AND revision=1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, s.conn, s.sliID, `INSERT INTO public.almanac_sli_observations
SELECT (jsonb_populate_record(NULL::public.almanac_sli_observations,to_jsonb(o)||'{"measurement_revision":2,"good_count":500,"ingestion_generation":0}'::jsonb)).*
FROM public.almanac_sli_observations o WHERE sli_id=$1 AND measurement_revision=1`, s.sliID); err != nil {
		t.Fatal(err)
	}
	p.MeasurementRevision = 2
	second, err := experimentFallback(t.Context(), s.conn, p)
	if err != nil || second[0].Good != "2500.000000000000" || second[0].Total != "5000.000000000000" {
		t.Fatalf("revision isolation: %+v %v", second, err)
	}
}

func TestIntegrationAlertFallbackMissingAndStale(t *testing.T) {
	t.Parallel()
	s := newFallbackFixture(t, time.Minute)
	if _, err := s.conn.Exec(t.Context(), "DELETE FROM public.almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2", s.sliID, s.at.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	checkFallbackReference(t, s, fallbackInputs(s))
	inputs := fallbackInputs(s)
	for i := range inputs {
		inputs[i].Now = inputs[i].Now.Add(4 * time.Hour)
	}
	checkFallbackReference(t, s, inputs)
}

func TestIntegrationAlertFallbackBudgetCannotHideTerminalFailure(t *testing.T) {
	t.Parallel()
	s := newFallbackFixture(t, time.Minute)
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_derived_dirty_ranges SET attempts=5,lease_until=NULL
WHERE sli_id=$1 AND range_start=(SELECT max(range_start) FROM public.almanac_derived_dirty_ranges WHERE sli_id=$1)`, s.sliID); err != nil {
		t.Fatal(err)
	}
	p, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, fallbackInputs(s))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := experimentFallbackLimited(t.Context(), s.conn, p, 0, 7200); !errors.Is(err, ErrDerivedSourceFailed) {
		t.Fatalf("dirty-hour budget concealed terminal source failure: %v", err)
	}
}

func TestIntegrationAlertFallbackRawScanBudget(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		name          string
		cadence, span time.Duration
	}{{"small", time.Minute, 8 * time.Hour}, {"retained", 15 * time.Second, 14 * 24 * time.Hour}} {
		t.Run(fixture.name, func(t *testing.T) {
			s := newFallbackFixtureSpan(t, fixture.cadence, fixture.span)
			p, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, fallbackInputs(s))
			if err != nil {
				t.Fatal(err)
			}
			for _, maxDirty := range []int{0, 8} {
				var plan []byte
				if err := s.conn.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+experimentFallbackSQL,
					p.SliID, p.MeasurementRevision, p.WindowEnds, p.FullStarts, p.FullEnds, p.EdgeAStarts, p.EdgeAEnds, p.EdgeBStarts, p.EdgeBEnds, p.Now, maxDirty, 7200).Scan(&plan); err != nil {
					t.Fatal(err)
				}
				var decoded []struct{ Plan map[string]any }
				if err := json.Unmarshal(plan, &decoded); err != nil {
					t.Fatal(err)
				}
				var visits float64
				var walk func(map[string]any)
				walk = func(n map[string]any) {
					if relation, _ := n["Relation Name"].(string); strings.HasPrefix(relation, "almanac_sli_observations") {
						rows, _ := n["Actual Rows"].(float64)
						filtered, _ := n["Rows Removed by Filter"].(float64)
						rechecked, _ := n["Rows Removed by Index Recheck"].(float64)
						loops, _ := n["Actual Loops"].(float64)
						visits += (rows + filtered + rechecked) * loops
					}
					if children, ok := n["Plans"].([]any); ok {
						for _, child := range children {
							walk(child.(map[string]any))
						}
					}
				}
				walk(decoded[0].Plan)
				if maxDirty == 0 && visits != 0 {
					t.Fatalf("refused budget scanned %.0f raw rows", visits)
				}
				if maxDirty > 0 && (visits == 0 || visits > 7200) {
					t.Fatalf("admitted budget scanned %.0f raw rows", visits)
				}
				t.Logf("maxDirty=%d: plan raw visits=%.0f (including filtered/rechecked rows)", maxDirty, visits)
			}
		})
	}
}
