package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestLateRepairPreservesExactPrefixAfterRawExpiry(t *testing.T) {
	at := time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC)
	start := at.Add(-28 * 24 * time.Hour)
	input := slo.WindowInput{At: at, Now: at, ValidFrom: start, SliValidFrom: start,
		RetentionStart: start, Duration: 28 * 24 * time.Hour, Cadence: time.Minute, FreshnessGrace: 2 * time.Minute, Target: "0.995"}
	first := slo.Observation{Start: start, End: start.Add(time.Minute), Received: start.Add(time.Minute), Outcome: "observed", Good: "99999999999999999999999.123456789012", Total: "99999999999999999999999.987654321098"}
	last := slo.Observation{Start: at.Add(-time.Minute), End: at, Received: at, Outcome: "observed", Good: "90", Total: "100"}
	input.Observations = []slo.Observation{first, last}
	original, err := slo.EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	saved := operationalValue(original)
	lateAt := at.Add(-time.Hour)
	late := slo.Observation{Start: lateAt, End: lateAt.Add(time.Minute), Received: at.Add(364 * 24 * time.Hour), Outcome: "observed", Good: "0.000000000001", Total: "12.000000000003"}
	for _, outcome := range []string{"observed", "complete_empty"} {
		t.Run(outcome, func(t *testing.T) {
			fact := late
			fact.Outcome = outcome
			oracleInput := input
			oracleInput.Now = late.Received
			oracleInput.Observations = []slo.Observation{first, fact, last}
			oracle, err := slo.EvaluateOperationalWindow(oracleInput)
			if err != nil {
				t.Fatal(err)
			}
			// Raw retention has advanced well past the first observation.
			clipped := oracleInput
			clipped.RetentionStart = clipped.Now.Add(-365 * 24 * time.Hour)
			lost, err := slo.EvaluateOperationalWindow(clipped)
			if err != nil || lost.Expired == 0 || reflect.DeepEqual(lost.Good, oracle.Good) {
				t.Fatal("fixture did not expose lost historical prefix")
			}
			delta := dbgen.ReadRetainedLateTotalsRow{Good: fact.Good, Total: fact.Total, Observed: 1,
				FirstObservation: timestamp(fact.Start), LastObservation: timestamp(fact.End), LatestReceipt: timestamp(fact.Received)}
			if outcome == "complete_empty" {
				delta.Good, delta.Total, delta.Observed, delta.KnownEmpty = "0", "0", 0, 1
				delta.FirstObservation.Valid, delta.LastObservation.Valid = false, false
			}
			repairInput := input
			repairInput.Now, repairInput.Observations = late.Received, nil
			repaired, err := mergeRetainedWindow(t.Context(), repairInput, saved.Quality, saved.Performance, delta)
			if err != nil || !reflect.DeepEqual(operationalValue(repaired), operationalValue(oracle)) {
				t.Fatalf("repair lost old counts/quality or rounded the late delta: %v", err)
			}
			zero := dbgen.ReadRetainedLateTotalsRow{Good: "0", Total: "0"}
			unchanged, err := mergeRetainedWindow(t.Context(), repairInput, operationalValue(repaired).Quality, operationalValue(repaired).Performance, zero)
			if err != nil || !reflect.DeepEqual(operationalValue(unchanged), operationalValue(repaired)) {
				t.Fatalf("a repair with no new accepted intervals changed stored totals: %v", err)
			}
		})
	}
	if _, err := mergeRetainedWindow(t.Context(), input, saved.Quality, saved.Performance,
		dbgen.ReadRetainedLateTotalsRow{Good: "0", Total: "1", Observed: saved.Quality.MissingIntervals + 1}); !errors.Is(err, slo.ErrOperationalFacts) {
		t.Fatal("late repair accepted overlapping or duplicate observations")
	}
}
