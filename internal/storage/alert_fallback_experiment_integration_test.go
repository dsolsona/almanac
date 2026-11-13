//go:build integration

package storage

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

//go:embed testdata/alert_window_fallback.sql
var experimentFallbackSQL string

// Experimental only: no production caller or generated query is changed.
func experimentFallback(ctx context.Context, conn *pgx.Conn, p dbgen.ReadRetainedWindowBatchParams) ([]dbgen.ReadRetainedWindowBatchRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return experimentFallbackLimited(ctx, conn, p, 8, 7200)
}

func experimentFallbackLimited(ctx context.Context, db dbgen.DBTX, p dbgen.ReadRetainedWindowBatchParams, maxDirty, maxRaw int) ([]dbgen.ReadRetainedWindowBatchRow, error) {
	rows, err := db.Query(ctx, experimentFallbackSQL, p.SliID, p.MeasurementRevision, p.WindowEnds,
		p.FullStarts, p.FullEnds, p.EdgeAStarts, p.EdgeAEnds, p.EdgeBStarts, p.EdgeBEnds, p.Now, maxDirty, maxRaw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []dbgen.ReadRetainedWindowBatchRow
	for rows.Next() {
		var row dbgen.ReadRetainedWindowBatchRow
		var good, total *string
		var dirty, raw int64
		if err := rows.Scan(&row.Ordinal, &good, &total, &row.Observed, &row.KnownEmpty, &row.ZeroTraffic,
			&row.FirstObservation, &row.LastObservation, &row.LatestReceipt, &row.Prepared, &row.Exhausted, &dirty, &raw); err != nil {
			return nil, err
		}
		if row.Exhausted {
			return nil, ErrDerivedSourceFailed
		}
		if !row.Prepared {
			return nil, ErrDerivedPending
		}
		row.Good, row.Total = *good, *total
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func TestIntegrationAlertFallbackEquivalence(t *testing.T) {
	for _, seconds := range []int{15, 60, 70, 7200} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			t.Parallel()
			s := newFallbackFixture(t, time.Duration(seconds)*time.Second)
			inputs := fallbackInputs(s)
			checkFallbackReference(t, s, inputs)
			s.drain(t, "clean baseline")
			checkFallbackReference(t, s, inputs)
			// Correct one accepted value and invalidate its hour: stale aggregate
			// plus replacement would double count, or using it would retain 990.
			if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE public.almanac_sli_observations SET good_count=0,ingestion_generation=0
WHERE sli_id=$1 AND interval_start=(SELECT max(interval_start) FROM public.almanac_sli_observations WHERE sli_id=$1 AND interval_start<$2)`, s.sliID, s.at.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			checkFallbackReference(t, s, inputs)
			// Both ends can be raw fragments; validity/retention clamp counts.
			for i := range inputs {
				inputs[i].At = inputs[i].At.Add(-s.cadence)
				inputs[i].RetentionStart = s.historyStart.Add(4 * time.Hour)
			}
			checkFallbackReference(t, s, inputs)
		})
	}
}

func TestIntegrationAlertFallbackQualityAndLimits(t *testing.T) {
	t.Parallel()
	s := newFallbackFixture(t, time.Minute)
	inputs := fallbackInputs(s)
	for _, outcome := range []string{"complete_empty", "incomplete", "failed", "zero"} {
		t.Run(outcome, func(t *testing.T) {
			statement := `UPDATE public.almanac_sli_observations SET outcome=$2,good_count=NULL,total_count=NULL,
error_code=CASE WHEN $2='failed' THEN 'network' ELSE NULL END,
missing_components=CASE WHEN $2='incomplete' THEN ARRAY['good']::text[] ELSE '{}'::text[] END,ingestion_generation=0 WHERE sli_id=$1`
			if outcome == "zero" {
				statement = `UPDATE public.almanac_sli_observations SET outcome='observed',good_count=0,total_count=0,error_code=NULL,missing_components='{}',ingestion_generation=0 WHERE sli_id=$1 AND $2='zero'`
			}
			if _, err := execObservationFixture(t, s.conn, s.sliID, statement, s.sliID, outcome); err != nil {
				t.Fatal(err)
			}
			checkFallbackReference(t, s, inputs)
		})
	}
	if _, err := s.conn.Exec(t.Context(), "UPDATE public.almanac_sli_observations SET total_count=NULL WHERE sli_id=$1", s.sliID); err == nil {
		t.Fatal("observed null total must be rejected, never coalesced to success")
	}
	p, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range [][2]int{{0, 7200}, {8, 1}} {
		rows, err := experimentFallbackLimited(t.Context(), s.conn, p, budget[0], budget[1])
		if !errors.Is(err, ErrDerivedPending) || rows != nil {
			t.Fatalf("budget %v must defer with no partial totals: %+v, %v", budget, rows, err)
		}
	}
	if _, err := s.conn.Exec(t.Context(), "UPDATE public.almanac_derived_dirty_ranges SET attempts=5,lease_until=NULL WHERE sli_id=$1", s.sliID); err != nil {
		t.Fatal(err)
	}
	if _, err := experimentFallback(t.Context(), s.conn, p); !errors.Is(err, ErrDerivedSourceFailed) {
		t.Fatalf("terminal failure was hidden by fallback: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := experimentFallback(ctx, s.conn, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func newFallbackFixture(t *testing.T, cadence time.Duration) *selfHealFixture {
	t.Helper()
	return newFallbackFixtureSpan(t, cadence, 8*time.Hour)
}

func newFallbackFixtureSpan(t *testing.T, cadence, span time.Duration) *selfHealFixture {
	t.Helper()
	s := newSelfHealFixtureCadence(t, "alert-query", true, cadence)
	s.at = floorSliBoundary(s.historyStart.Add(span), cadence)
	s.now = s.at.Add(10 * time.Second)
	_, err := execObservationFixture(t, s.conn, s.sliID, `WITH proof AS (SELECT * FROM public.almanac_sli_observations WHERE sli_id=$1 LIMIT 1)
INSERT INTO public.almanac_sli_observations
(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,p.sli_id,1,'observed',990,1000,p.result_digest,'{}',gen_random_uuid(),gen_random_uuid(),
p.agent_id,p.session_generation,p.configuration_revision,p.lease_id,point,point+$4*interval '1 second'
FROM proof p CROSS JOIN generate_series($2::timestamptz,$3::timestamptz-$4*interval '1 second',$4*interval '1 second') point ON CONFLICT DO NOTHING`, s.sliID, s.historyStart, s.at, cadence.Seconds())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), "SET statement_timeout='1s'"); err != nil {
		t.Fatal(err)
	}
	return s
}

func fallbackInputs(s *selfHealFixture) []slo.WindowInput {
	var result []slo.WindowInput
	for _, duration := range []time.Duration{5 * time.Minute, time.Hour, 30 * time.Minute, 6 * time.Hour} {
		// Supported inputs must be multiples of the cadence.
		duration = ((duration + s.cadence - 1) / s.cadence) * s.cadence
		result = append(result, slo.WindowInput{At: s.at, Now: s.now, Duration: duration, Cadence: s.cadence,
			ValidFrom: s.historyStart, SliValidFrom: s.historyStart, Target: "0.999"})
	}
	return result
}

func checkFallbackReference(t *testing.T, s *selfHealFixture, inputs []slo.WindowInput) {
	t.Helper()
	p, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, inputs)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := experimentFallback(t.Context(), s.conn, p)
	if err != nil || len(rows) != len(inputs) {
		t.Fatalf("candidate: %v %+v", err, rows)
	}
	// Independent full raw read. No aggregate bounds or replacement helpers.
	facts, err := s.conn.Query(t.Context(), `SELECT interval_start,outcome,good_count::text,total_count::text,recorded_at
FROM public.almanac_sli_observations WHERE sli_id=$1 AND measurement_revision=1 ORDER BY interval_start`, s.sliID)
	if err != nil {
		t.Fatal(err)
	}
	var observations []slo.Observation
	for facts.Next() {
		var o slo.Observation
		var good, total *string
		if err := facts.Scan(&o.Start, &o.Outcome, &good, &total, &o.Received); err != nil {
			t.Fatal(err)
		}
		if good != nil {
			o.Good, o.Total = *good, *total
		}
		o.End = o.Start.Add(s.cadence)
		observations = append(observations, o)
	}
	if err := facts.Err(); err != nil {
		t.Fatal(err)
	}
	facts.Close()
	for i, input := range inputs {
		input.Observations = observations
		want, err := slo.EvaluateOperationalWindowContext(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		// SPEC-0020's count path retains maximum receipt across all outcomes.
		// The older raw sweep omits failed/incomplete receipts. Derive this
		// field independently from raw facts rather than changing that contract.
		want.LatestReceipt = nil
		for _, o := range observations {
			if !o.Start.Before(want.EffectiveStart) && !o.End.After(input.At) &&
				(want.LatestReceipt == nil || o.Received.After(*want.LatestReceipt)) {
				want.LatestReceipt = new(o.Received.UTC())
			}
		}
		r := rows[i]
		got, err := evaluateDerivedCounts(t.Context(), input, dbgen.ReadDerivedWindowTotalsRow{Good: r.Good, Total: r.Total,
			Observed: r.Observed, KnownEmpty: r.KnownEmpty, ZeroTraffic: r.ZeroTraffic, FirstObservation: r.FirstObservation,
			LastObservation: r.LastObservation, LatestReceipt: r.LatestReceipt, Prepared: r.Prepared, Exhausted: r.Exhausted})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("window %s: result mismatch\ngot %+v\nwant %+v\nerror %v", input.Duration, got, want, err)
		}
		// Also independently sum populations, even when quality suppresses display values.
		good, total := new(big.Rat), new(big.Rat)
		for _, o := range observations {
			if o.Start.Before(input.At.Add(-input.Duration)) || o.Start.Before(input.ValidFrom) || o.Start.Before(input.RetentionStart) || o.End.After(input.At) || o.Outcome != "observed" {
				continue
			}
			g, _ := new(big.Rat).SetString(o.Good)
			n, _ := new(big.Rat).SetString(o.Total)
			good.Add(good, g)
			total.Add(total, n)
		}
		g, _ := new(big.Rat).SetString(r.Good)
		n, _ := new(big.Rat).SetString(r.Total)
		if g.Cmp(good) != 0 || n.Cmp(total) != 0 {
			t.Fatalf("exact raw sums differ: %s/%s want %s/%s", g, n, good, total)
		}
	}
}

func TestIntegrationAlertFallbackDirtyHour(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "alert-fallback", true)
	input := slo.WindowInput{At: s.at, Now: s.now, Duration: 3 * time.Hour, Cadence: s.cadence, ValidFrom: s.historyStart, SliValidFrom: s.historyStart}
	p, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluateDerivedWindows(t.Context(), dbgen.New(s.conn), p.SliID, 1, []slo.WindowInput{input}); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("baseline should report dirty source pending, got %v", err)
	}
	rows, err := experimentFallback(t.Context(), s.conn, p)
	if err != nil {
		t.Fatalf("exact raw substitution should evaluate dirty interior: %v", err)
	}
	if len(rows) != 1 || rows[0].Good != "178200.000000000000" || rows[0].Total != "180000.000000000000" || rows[0].Observed != 180 {
		t.Fatalf("want 180 intervals of 990/1000, got %+v", rows)
	}
	var pending int
	if err := s.conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_derived_dirty_ranges WHERE sli_id=$1", p.SliID).Scan(&pending); err != nil || pending != 3 {
		t.Fatalf("read must not repair/acknowledge dirty work: %d, %v", pending, err)
	}
}
