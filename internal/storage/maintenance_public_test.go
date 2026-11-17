package storage

import (
	"github.com/dsolsona/almanac/internal/slo"
	"testing"
	"time"
)

func TestMaintenancePublicMetadata(t *testing.T) {
	t.Parallel()
	mark, input, value := retainedMaintenanceFixture(t)
	quality := operationalQuality(value)
	if quality.ExcludedIntervals == nil || *quality.ExcludedIntervals != 10 || string(quality.ExcludedGood) != `"9900.000000000000"` || string(quality.ExcludedTotal) != `"10000.000000000000"` {
		t.Fatalf("lost excluded population: %+v", quality)
	}
	lane := extremaLane{Marks: []extremaMark{mark}}
	samples, err := lane.samples(t.Context(), input, input.At.Add(-time.Hour), input.At, "1", "budget")
	if err != nil || len(samples) != 1 || samples[0].CalculationProvenance == nil || samples[0].CalculationProvenance.ComputedRevision != "1" || samples[0].CalculationProvenance.RequestedRevision != "1" || samples[0].CalculationProvenance.SourceGeneration != "7" {
		t.Fatalf("retained public provenance lost: %+v %v", samples, err)
	}
	burn, err := lane.samples(t.Context(), input, input.At.Add(-time.Hour), input.At, "1", "burn")
	if err != nil || len(burn) != 1 || burn[0].Quality.ExcludedIntervals == nil || *burn[0].Quality.ExcludedIntervals != 10 || string(burn[0].Quality.ExcludedTotal) != `"10000.000000000000"` {
		t.Fatalf("burn projection lost excluded population: %+v %v", burn, err)
	}
}

func TestMaintenancePublicNoEligibleProvenance(t *testing.T) {
	t.Parallel()
	mark, window, _ := retainedMaintenanceFixture(t)
	window.ValidFrom = window.At
	value, err := slo.EvaluateOperationalWindowContext(t.Context(), window)
	if err != nil || value.Expected != 0 || value.Excluded != 0 || value.Performance != "not_applicable" {
		t.Fatalf("no-eligible fixture: %+v %v", value, err)
	}
	scope := mark.Maintenance.Scope
	sample := operationalSample(value, window.At.Add(-window.Duration), window.At, "1")
	mark = extremaMark{At: window.At, Good: "0", Total: "0", State: sample.State, Quality: sample.Quality.State, Freshness: sample.Quality.Freshness}
	in := maintenanceEvaluationInput{SloID: scope.SloID, SliID: scope.SliID, DefinitionRevision: scope.DefinitionRevision, MeasurementRevision: scope.MeasurementRevision, Requests: []maintenanceMaskWindows{{Windows: []slo.WindowInput{window}}}}
	result := maintenanceEvaluationResult{SourceGeneration: 7, Masks: []maintenanceEvaluatedMask{{Windows: []slo.WindowResult{value}, Unavailable: []string{""}}}}
	t.Run("current", func(t *testing.T) {
		batch := maintenanceConsumerBatch{input: in, result: result}
		if p, err := batch.provenance(0); err != nil || p != nil {
			t.Fatalf("no eligible interval acquired calculation provenance: %+v %v", p, err)
		}
	})
	t.Run("retained_writer", func(t *testing.T) {
		converted, err := mark.withMaintenanceInputs(in, result, 0, 0)
		if err != nil || converted.Maintenance != nil {
			t.Fatalf("no eligible interval acquired retained maintenance inputs: %+v %v", converted.Maintenance, err)
		}
	})
	t.Run("retained_reader", func(t *testing.T) {
		mark.Good, mark.Total, mark.Observed, mark.KnownEmpty, mark.ZeroTraffic = "0", "0", 0, 0, 0
		mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = nil, nil, nil
		mark.Maintenance = &retainedMaintenanceInputs{Version: 1, Scope: scope, SourceGeneration: 7}
		samples, err := (extremaLane{Marks: []extremaMark{mark}}).samples(t.Context(), window, window.At.Add(-time.Hour), window.At, "1", "budget")
		if err != nil || len(samples) != 1 || samples[0].CalculationProvenance != nil || samples[0].Quality.ExcludedIntervals != nil {
			t.Fatalf("no eligible retained interval emitted inconsistent metadata: %+v %v", samples, err)
		}
	})
}

func TestMaintenancePublicExcludedSample(t *testing.T) {
	t.Parallel()
	mark, input, _ := retainedMaintenanceFixture(t)
	mark.Good, mark.Total, mark.Observed = "0", "0", 0
	mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = nil, nil, nil
	m := mark.Maintenance
	m.ExcludedObserved = 60
	m.ExcludedGood, m.ExcludedTotal = new("59400"), new("60000")
	start := input.At.Add(-time.Hour)
	m.Coverage = &slo.WindowCoverage{LatestEligibleEnd: start, Points: []slo.ExclusionCoveragePoint{{At: start.Add(-time.Minute), ExcludedBefore: 0}, {At: start, ExcludedBefore: 0}, {At: input.At, ExcludedBefore: 60}}}

	value, err := mark.result(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	sample := operationalSample(value, input.At.Add(-time.Hour), input.At, "1")
	if sample.State != "excluded" || sample.Quality.State != "excluded" || sample.Burn != nil || sample.Good != nil || sample.Quality.ExcludedIntervals == nil || *sample.Quality.ExcludedIntervals != 60 {
		t.Fatalf("fully excluded sample became numeric/observed: %+v", sample)
	}
}
