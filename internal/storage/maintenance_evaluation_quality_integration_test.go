//go:build integration

package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
)

func TestIntegrationMaintenanceEvaluationQuality(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"mixed", "zero", "all_failed", "all_excluded", "exact_decimal", "expired", "clipped"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, h := hourFixture(t)
			mask := []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}}
			revision := int64(1)
			if kind == "all_excluded" {
				m := f.input("whole")
				m.Start = h.HourStart
				m.End = h.HourStart.Add(time.Hour)
				if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
					t.Fatal(err)
				}
				b := compilerCapture(t, f, nil)
				compilerFinish(t, f, b.ID, 10)
				h.BuildID = b.ID
				revision = 2
				mask = []slo.ExclusionInterval{{Start: m.Start, End: m.End}}
			}
			tx, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			var facts []slo.Observation
			for i := range 60 {
				at := h.HourStart.Add(time.Duration(i) * time.Minute)
				if kind == "mixed" && i == 21 {
					if _, err := tx.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2`, f.sliID, at); err != nil {
						t.Fatal(err)
					}
					continue
				}
				outcome, g, n := "observed", "990", "1000"
				switch kind {
				case "all_failed":
					outcome = "failed"
				case "zero":
					g, n = "0", "0"
				case "exact_decimal":
					g, n = "99999999999999999999999999.999999999998", "99999999999999999999999999.999999999999"
				}
				if kind == "mixed" {
					switch i {
					case 3, 13:
						outcome = "complete_empty"
					case 19, 59:
						outcome = "failed"
					case 18:
						outcome = "incomplete"
					case 40:
						g, n = "0", "0"
					}
				}
				received := at.Add(time.Minute)
				if outcome == "failed" || i == 12 {
					received = h.HourStart.Add(2 * time.Hour)
				}
				o := slo.Observation{Start: at, End: at.Add(time.Minute), Received: received, Outcome: outcome, Good: g, Total: n}
				if outcome != "observed" {
					o.Good, o.Total = "", ""
				}
				facts = append(facts, o)
				if _, err := tx.Exec(t.Context(), `UPDATE almanac_sli_observations SET outcome=$3,good_count=CASE WHEN $3='observed' THEN $4::numeric END,total_count=CASE WHEN $3='observed' THEN $5::numeric END,error_code=CASE WHEN $3='failed' THEN 'network' END,missing_components=CASE WHEN $3='incomplete' THEN ARRAY['total']::text[] ELSE '{}'::text[] END,recorded_at=$6,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, at, outcome, g, n, received); err != nil {
					t.Fatal(err)
				}
			}
			if err := markObservationFixture(t.Context(), tx, f.sliID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.drain(t, "evaluation quality")
			if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, h.HourStart).Scan(&h.SourceGeneration); err != nil {
				t.Fatal(err)
			}
			if kind != "all_excluded" {
				if _, err := hourPrepare(t, f, h); err != nil {
					t.Fatal(err)
				}
			}
			in := evaluationFixtureInput(f, h)
			in.Requests = in.Requests[1:]
			in.Requests[0].RequestedRevision = revision
			if kind == "expired" {
				in.Requests[0].Windows[0].RetentionStart = h.HourStart.Add(time.Hour)
			}
			if kind == "clipped" {
				in.Requests[0].Windows[0].RetentionStart = h.HourStart.Add(15*time.Minute + time.Microsecond)
			}
			oracle := in.Requests[0].Windows[0]
			oracle.Observations = facts
			oracle.Exclusions = mask
			want, err := slo.EvaluateOperationalWindowContext(t.Context(), oracle)
			if err != nil {
				t.Fatal(err)
			}
			got, err := evaluateFixtureMaintenance(t, f, in)
			if err != nil || len(got.Masks) != 1 || len(got.Masks[0].Windows) != 1 || !reflect.DeepEqual(got.Masks[0].Windows[0], want) {
				t.Fatalf("%s: got %+v error %v want %+v", kind, got, err, want)
			}
			if kind == "mixed" && got.Masks[0].Windows[0].ExcludedTotal != nil {
				t.Fatal("incomplete excluded population gained numeric total")
			}
		})
	}
}
func TestIntegrationMaintenanceEvaluationUnknownReceipt(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	w := &in.Requests[0].Windows[0]
	w.At = w.At.Add(time.Hour)
	w.Duration = 2 * time.Hour
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL,certified_receipt_at=NULL WHERE sli_id=$1 AND hour_start=$2`, f.sliID, h.HourStart.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := evaluationRawWindow(t, *w, []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}})
	want.LatestReceipt = nil
	got, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || !reflect.DeepEqual(got.Masks[0].Windows[0], want) {
		t.Fatalf("unknown interior receipt must contaminate exact max only: %+v %v want %+v", got, err, want)
	}
}
func TestIntegrationMaintenanceEvaluationGridEdges(t *testing.T) {
	t.Parallel()
	for _, cadence := range []time.Duration{15 * time.Second, 70 * time.Second} {
		t.Run(cadence.String(), func(t *testing.T) {
			t.Parallel()
			f, h, mask := hourGridFixture(t, cadence)
			if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			in := evaluationFixtureInput(f, h)
			in.Requests = in.Requests[1:]
			start, end := ceilSliBoundary(h.HourStart, cadence), ceilSliBoundary(h.HourStart.Add(time.Hour), cadence)
			w := &in.Requests[0].Windows[0]
			w.Cadence = cadence
			w.At = end
			w.Duration = end.Sub(start)
			want := evaluationRawWindow(t, *w, mask)
			got, err := evaluateFixtureMaintenance(t, f, in)
			if err != nil || !reflect.DeepEqual(got.Masks[0].Windows[0], want) {
				t.Fatalf("whole crossing intervals: %+v %v want %+v", got, err, want)
			}
			in.Limits.MaxFragments = 1
			if cadence == 70*time.Second {
				if _, err := evaluateFixtureMaintenance(t, f, in); err == nil {
					t.Fatal("raw edge fragment budget ignored")
				}
			}
		})
	}
}
