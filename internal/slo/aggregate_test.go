package slo

import (
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"
)

func TestAggregateEvaluationMatchesCanonicalIntervals(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, variant := range []string{"complete", "missing", "known_empty", "zero_traffic", "expired", "large_fractional"} {
		t.Run(variant, func(t *testing.T) {
			input := WindowInput{At: start.Add(5 * time.Minute), Now: start.Add(5 * time.Minute),
				ValidFrom: start, SliValidFrom: start, RetentionStart: start,
				Duration: 5 * time.Minute, Cadence: time.Minute, FreshnessGrace: time.Minute, Target: "0.99"}
			if variant == "expired" {
				input.RetentionStart = start.Add(time.Minute)
			}
			totals := WindowTotals{Good: new(big.Rat), Total: new(big.Rat)}
			for i := range 5 {
				if variant == "missing" && i == 4 {
					continue
				}
				fact := Observation{Start: start.Add(time.Duration(i) * time.Minute), End: start.Add(time.Duration(i+1) * time.Minute),
					Received: start.Add(time.Duration(i+1) * time.Minute), Outcome: "observed", Good: "99", Total: "100"}
				if variant == "known_empty" && i == 2 {
					fact.Outcome, fact.Good, fact.Total = "complete_empty", "", ""
				}
				if variant == "zero_traffic" {
					fact.Good, fact.Total = "0", "0"
				}
				if variant == "large_fractional" {
					fact.Good, fact.Total = "99999999999999999999999999.999999999998", "99999999999999999999999999.999999999999"
				}
				input.Observations = append(input.Observations, fact)
				if fact.Start.Before(input.RetentionStart) {
					continue
				}
				if fact.Outcome == "observed" {
					good, _ := new(big.Rat).SetString(fact.Good)
					total, _ := new(big.Rat).SetString(fact.Total)
					totals.Good.Add(totals.Good, good)
					totals.Total.Add(totals.Total, total)
					totals.Observed++
					if total.Sign() == 0 {
						totals.ZeroTraffic++
					}
					if totals.FirstObservation == nil {
						totals.FirstObservation = new(fact.Start)
					}
					totals.LastObservation = new(fact.End)
				} else {
					totals.KnownEmpty++
				}
				totals.LatestReceipt = new(fact.Received)
			}
			canonical, err := EvaluateOperationalWindowContext(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			input.Observations = nil
			aggregated, err := EvaluateOperationalTotals(t.Context(), input, totals)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(canonical, aggregated) {
				t.Fatalf("aggregate differs from canonical evaluator:\ncanonical=%+v\naggregate=%+v", canonical, aggregated)
			}
			summary, err := EvaluateOperationalTotalsSummary(t.Context(), input, totals)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Good != nil || summary.Attainment != nil || summary.Coverage != nil {
				t.Fatal("summary eagerly formatted public values")
			}
			// A returned summary owns its counts even when its caller reuses totals.
			totals.Good.SetInt64(0)
			totals.Total.SetInt64(0)
			if got := summary.WithValues(); !reflect.DeepEqual(canonical, got) {
				t.Fatalf("deferred aggregate differs from canonical evaluator: got=%+v want=%+v", got, canonical)
			}
		})
	}
}

// Receipt time is diagnostic metadata. Retained counts and observation bounds
// remain usable when expired raw history prevents certifying that maximum.
func TestAggregateUnknownReceiptPreservesMeasurement(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	input := WindowInput{At: start.Add(time.Minute), Now: start.Add(time.Minute), ValidFrom: start, SliValidFrom: start, RetentionStart: start, Duration: time.Minute, Cadence: time.Minute, FreshnessGrace: time.Minute, Target: "0.99"}
	totals := WindowTotals{Good: big.NewRat(99, 1), Total: big.NewRat(100, 1), Observed: 1, FirstObservation: new(start), LastObservation: new(input.At), LatestReceipt: new(input.At)}
	want, err := EvaluateOperationalTotals(t.Context(), input, totals)
	if err != nil {
		t.Fatal(err)
	}
	want.LatestReceipt = nil
	totals.LatestReceipt = nil
	got, err := EvaluateOperationalTotals(t.Context(), input, totals)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unknown receipt changed measurements: got=%+v want=%+v", got, want)
	}
	for _, missing := range []string{"first", "last"} {
		invalid := totals
		if missing == "first" {
			invalid.FirstObservation = nil
		} else {
			invalid.LastObservation = nil
		}
		if _, err := EvaluateOperationalTotals(t.Context(), input, invalid); err == nil {
			t.Errorf("accepted missing %s observation bound", missing)
		}
	}
}

func TestAggregateRejectsPositiveAllZeroTraffic(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	input := WindowInput{At: start.Add(time.Minute), Now: start.Add(time.Minute), ValidFrom: start, SliValidFrom: start, RetentionStart: start, Duration: time.Minute, Cadence: time.Minute, FreshnessGrace: time.Minute, Target: "0.99"}
	totals := WindowTotals{Good: big.NewRat(99, 1), Total: big.NewRat(100, 1), Observed: 1, ZeroTraffic: 1, FirstObservation: new(start), LastObservation: new(input.At), LatestReceipt: new(input.At)}
	coverage := WindowCoverage{LatestEligibleEnd: input.At, Points: []ExclusionCoveragePoint{{At: start}, {At: input.At}}}
	for _, covered := range []bool{false, true} {
		var err error
		if covered {
			_, err = EvaluateOperationalCoveredTotals(t.Context(), input, totals, coverage)
		} else {
			_, err = EvaluateOperationalTotals(t.Context(), input, totals)
		}
		if !errors.Is(err, ErrOperationalFacts) {
			t.Errorf("covered=%v accepted positive counts from all-zero-traffic population: %v", covered, err)
		}
	}
}
