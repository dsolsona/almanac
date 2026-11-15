//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceHourQualityAndReceipt(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"mixed", "all_failed", "zero", "all_excluded", "exact_decimal"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			f, in := hourFixture(t)
			h := in.HourStart
			if variant == "all_excluded" {
				m := f.input("full")
				m.Start = h
				m.End = h.Add(time.Hour)
				if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
					t.Fatal(err)
				}
				b := compilerCapture(t, f, nil)
				compilerFinish(t, f, b.ID, 10)
				in.BuildID = b.ID
			}
			facts := make([]slo.Observation, 60)
			tx, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			for i := range facts {
				outcome, good, total := "observed", "990", "1000"
				if variant == "all_failed" {
					outcome = "failed"
				} else if variant == "zero" {
					good, total = "0", "0"
				} else if variant == "exact_decimal" {
					good, total = "99999999999999999999999999.999999999998", "99999999999999999999999999.999999999999"
				} else if i == 3 || i == 13 {
					outcome = "complete_empty"
				} else if i == 59 || i == 19 {
					outcome = "failed"
				} else if i == 18 {
					outcome = "incomplete"
				}
				received := h.Add(time.Duration(i+1) * time.Minute)
				if outcome == "failed" {
					received = h.Add(2 * time.Hour)
				}
				if i == 12 {
					received = h.Add(90 * time.Minute)
				} // excluded valid receipt must not leak
				facts[i] = slo.Observation{Start: h.Add(time.Duration(i) * time.Minute), End: h.Add(time.Duration(i+1) * time.Minute), Received: received, Outcome: outcome, Good: good, Total: total}
				if outcome != "observed" {
					facts[i].Good, facts[i].Total = "", ""
				}
				if _, err := tx.Exec(t.Context(), `UPDATE almanac_sli_observations SET outcome=$3,good_count=CASE WHEN $3='observed' THEN $4::numeric END,total_count=CASE WHEN $3='observed' THEN $5::numeric END,error_code=CASE WHEN $3='failed' THEN 'network' END,missing_components=CASE WHEN $3='incomplete' THEN ARRAY['total']::text[] ELSE '{}'::text[] END,recorded_at=$6,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, facts[i].Start, outcome, good, total, received); err != nil {
					t.Fatal(err)
				}
			}
			if err := markObservationFixture(t.Context(), tx, f.sliID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.drain(t, "quality source")
			if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, h).Scan(&in.SourceGeneration); err != nil {
				t.Fatal(err)
			}
			got, err := hourPrepare(t, f, in)
			if err != nil {
				t.Fatal(err)
			}
			mask := []slo.ExclusionInterval{{Start: h.Add(10 * time.Minute), End: h.Add(20 * time.Minute)}}
			if variant == "all_excluded" {
				mask = []slo.ExclusionInterval{{Start: h, End: h.Add(time.Hour)}}
			}
			input := slo.WindowInput{At: h.Add(time.Hour), Now: f.now, Duration: time.Hour, Cadence: time.Minute, Target: "0.99", ValidFrom: h, RetentionStart: h, Observations: facts, Exclusions: mask}
			want, err := slo.EvaluateOperationalWindowContext(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			input.Observations = nil
			totals := hourTotals(got)
			actual, err := slo.EvaluateOperationalTotals(t.Context(), input, totals)
			if err != nil || !reflect.DeepEqual(actual, want) {
				t.Fatalf("%s exact oracle differs: %+v want %+v error %v", variant, actual, want, err)
			}
			if variant == "mixed" && actual.ExcludedTotal != nil {
				t.Fatal("partial excluded population manufactured a whole-population total")
			}
		})
	}
}
func hourTotals(r maintenanceHourRecord) slo.WindowTotals {
	return slo.WindowTotals{Good: r.Eligible.Good, Total: r.Eligible.Total, Observed: r.Eligible.Observed, KnownEmpty: r.Eligible.KnownEmpty, ZeroTraffic: r.Eligible.ZeroTraffic, FirstObservation: r.Eligible.FirstObservation, LastObservation: r.Eligible.LastObservation, LatestReceipt: r.Eligible.LatestReceipt, Excluded: slo.ExclusionTotals{Good: r.Excluded.Good, Total: r.Excluded.Total, Observed: r.Excluded.Observed, KnownEmpty: r.Excluded.KnownEmpty}}
}
func TestIntegrationMaintenanceHourHistoricalScopeAndPreservation(t *testing.T) {
	t.Parallel()
	f, in := hourFixture(t)
	original, err := hourPrepare(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	newer := f.input("new-mask")
	newer.Start = in.HourStart.Add(30 * time.Minute)
	newer.End = in.HourStart.Add(40 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, newer); err != nil {
		t.Fatal(err)
	}
	// Later authority is not a reason to relabel or reject a completed old mask.
	historical, err := hourPrepare(t, f, in)
	if err != nil || !reflect.DeepEqual(historical, original) {
		t.Fatalf("historical mask changed: %+v %v", historical, err)
	}
	var build dbgen.AlmanacMaintenanceBuild
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		build, err = captureMaintenanceBuild(t.Context(), tx, mustPGUUID(f.ids[1]), nil, f.now)
		return err
	})
	compilerFinish(t, f, build.ID, 10)
	other := in
	other.SloID = mustPGUUID(f.ids[1])
	other.BuildID = build.ID
	unmasked, err := hourPrepare(t, f, other)
	if err != nil || unmasked.Excluded.Expected != 0 || unmasked.Eligible.Observed != 60 {
		t.Fatalf("same SLI other SLO leaked mask: %+v %v", unmasked, err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start>=$2 AND interval_start<$2::timestamptz+interval '1 hour'`, f.sliID, in.HourStart); err != nil {
		t.Fatal(err)
	}
	in.RetentionStart = in.HourStart.Add(time.Hour)
	saved, err := hourPrepare(t, f, in)
	if err != nil || !reflect.DeepEqual(saved, original) {
		t.Fatalf("expired raw overwrote retained numbers: %+v %v", saved, err)
	}
	// A different mask has no prepared evidence and cannot reconstruct expired raw.
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	in.BuildID = b.ID
	if _, err := hourPrepare(t, f, in); !errors.Is(err, ErrMaintenanceHourUnavailable) {
		t.Fatalf("expired raw reconstructed: %v", err)
	}
}
