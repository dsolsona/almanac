package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
)

func TestRetainedTileKeepsUnroundedExtremaAndExactSelectedCounts(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	total := "1000000000000000000000000"
	goods := []string{total, "999999999999999999999998", "999999999999999999999999", total}
	var samples []api.SloOperationalSample
	for index, good := range goods {
		at := start.Add(time.Duration(index+1) * time.Minute)
		samples = append(samples, api.SloOperationalSample{EvaluationAt: at, State: "observed", Revision: "1", Good: new(good), Total: new(total),
			Attainment: new("1.000000000000"), Quality: api.SloOperationalQuality{State: "complete", Freshness: "fresh"}})
	}
	for _, metric := range []string{"attainment", "budget", "burn"} {
		got, err := reduceRetainedLane(samples, start, start.Add(time.Hour), "0.99", metric)
		if err != nil || len(got) != 3 {
			t.Fatalf("%s lost an extremum whose display rounds flat: %d, %v", metric, len(got), err)
		}
		if !got[1].EvaluationAt.Equal(samples[1].EvaluationAt) || *got[1].Good != goods[1] || *got[1].Total != total {
			t.Fatalf("%s changed the exact retained point", metric)
		}
		mark := api.SloOperationalSampleRetainedExtrema("minimum")
		if metric == "burn" {
			mark = "maximum"
		}
		if !reflect.DeepEqual(got[1].RetainedExtrema, []api.SloOperationalSampleRetainedExtrema{mark}) {
			t.Fatalf("%s wrong exact extremum mark: %v", metric, got[1].RetainedExtrema)
		}
	}
}

func TestRetainedTilePreservesGapBoundariesAndDoesNotMutateInput(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var samples []api.SloOperationalSample
	for index := range 8 {
		sample := api.SloOperationalSample{EvaluationAt: start.Add(time.Duration(index+1) * time.Minute), State: "observed", Good: new("99"), Total: new("100"),
			Quality: api.SloOperationalQuality{State: "complete", Freshness: "fresh"}, RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{"maximum"}}
		if index == 3 || index == 4 {
			sample.State, sample.Quality.State, sample.Good, sample.Total = "missing", "no_data", nil, nil
		}
		samples = append(samples, sample)
	}
	got, err := reduceRetainedLane(samples, start, start.Add(time.Hour), "0.99", "budget")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{0, 2, 3, 4, 5, 7}
	if len(got) != len(want) {
		t.Fatalf("gap boundaries were dropped: %d", len(got))
	}
	for index, original := range want {
		if !got[index].EvaluationAt.Equal(samples[original].EvaluationAt) || got[index].BucketStart != start || got[index].BucketEnd != start.Add(time.Hour) {
			t.Fatal("retained sample changed time or stable tile bounds")
		}
	}
	if len(samples[0].RetainedExtrema) != 1 || samples[0].RetainedExtrema[0] != "maximum" || !samples[0].BucketStart.IsZero() {
		t.Fatal("reduction mutated its input")
	}
}

func TestRetainedTileRejectsUnorderedAndMalformedPoints(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	point := api.SloOperationalSample{EvaluationAt: start, State: "observed", Good: new("99"), Total: new("100"), Quality: api.SloOperationalQuality{State: "complete", Freshness: "fresh"}}
	if _, err := reduceRetainedLane([]api.SloOperationalSample{point, point}, start, start.Add(time.Hour), "0.99", "budget"); !errors.Is(err, slo.ErrOperationalFacts) {
		t.Fatal("duplicate endpoints accepted")
	}
	point.Good = new("101")
	if _, err := reduceRetainedLane([]api.SloOperationalSample{point}, start, start.Add(time.Hour), "0.99", "budget"); !errors.Is(err, slo.ErrInvalidCalculation) {
		t.Fatal("invalid counts accepted")
	}
	if _, err := reduceRetainedLane(nil, start, start, "0.99", "budget"); !errors.Is(err, ErrOperationalRange) {
		t.Fatal("empty time range accepted")
	}
}
