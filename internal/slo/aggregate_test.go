package slo

import (
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
		})
	}
}
