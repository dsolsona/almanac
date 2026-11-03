package storage

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/google/uuid"
)

func TestRetainedDailyReductionMatchesRawPoints(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var raw, hours retainedSloTile
	for hour := range 24 {
		from := start.Add(time.Duration(hour) * time.Hour)
		var input retainedSloTile
		for minute := range 60 {
			good := "99999999999999999999999" + strconv.Itoa((minute+hour)%10)
			sample := api.SloOperationalSample{EvaluationAt: from.Add(time.Duration(minute+1) * time.Minute), State: "observed", Revision: "1",
				Good: new(good), Total: new("1000000000000000000000000"), Attainment: new("1.000000000000"),
				Quality: api.SloOperationalQuality{State: "complete", Freshness: "fresh"}}
			if hour == 7 && minute >= 58 || hour == 8 && minute < 2 {
				sample.State, sample.Quality.State, sample.Good, sample.Total = "missing", "no_data", nil, nil
			}
			if hour == 12 && minute == 30 {
				sample.Quality.Freshness = "stale"
			}
			input.Attainment = append(input.Attainment, sample)
			input.Budget = append(input.Budget, sample)
			input.Burn1h = append(input.Burn1h, sample)
			input.Burn6h = append(input.Burn6h, sample)
		}
		appendRetainedTile(&raw, input)
		tile, err := compactRetainedTile(input, from, from.Add(time.Hour), "0.99")
		if err != nil {
			t.Fatal(err)
		}
		appendRetainedTile(&hours, tile)
		if hour == 7 {
			body, err := marshalRetainedTileSummary(tile, from, from.Add(time.Hour), "0.99", 7)
			if err != nil {
				t.Fatal(err)
			}
			var summary retainedTileSummary
			var restored []api.SloOperationalSample
			if json.Unmarshal(body, &summary) != nil || len(summary.FineWire) != 5 || json.Unmarshal([]byte(summary.FineWire["attainment"]), &restored) != nil || len(restored) != len(tile.Attainment) {
				t.Fatal("hourly wire dropped finer gap-boundary samples")
			}
		}
	}
	want, err := compactRetainedTile(raw, start, start.Add(24*time.Hour), "0.99")
	if err != nil {
		t.Fatal(err)
	}
	got, err := compactRetainedTile(hours, start, start.Add(24*time.Hour), "0.99")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("hourly composition changed exact extrema, ties or cross-hour gaps: %v", err)
	}
	body, err := marshalRetainedTileSummary(want, start, start.Add(24*time.Hour), "0.99", 7)
	if err != nil {
		t.Fatal(err)
	}
	var summary retainedTileSummary
	if json.Unmarshal(body, &summary) != nil || summary.SourceGeneration != 7 || summary.Transitions != ([4]int32{7, 7, 7, 7}) || summary.FineWire != nil {
		t.Fatal("summary changed the source or independent quality-transition count")
	}
	for _, name := range []string{"attainment", "budget", "burn_1h", "burn_6h"} {
		var lane []api.SloOperationalSample
		if err := json.Unmarshal([]byte(summary.Wire[name]), &lane); err != nil {
			t.Fatal("summary wire is invalid", err)
		}
		if len(lane) != 3 || !lane[0].EvaluationAt.Equal(start.Add(time.Minute)) || !lane[1].EvaluationAt.Equal(start.Add(10*time.Minute)) || !lane[2].EvaluationAt.Equal(start.Add(24*time.Hour)) {
			t.Fatal("summary lost the first, last or exact unrounded extrema")
		}
		for _, sample := range lane {
			if sample.DisplayCoverage == nil || !slices.Contains(sample.DisplayCoverage.States, "missing") || !slices.Contains(sample.DisplayCoverage.States, "observed") || !slices.Contains(sample.DisplayCoverage.Freshness, "stale") {
				t.Fatal("summary hid a gap or a stale observation")
			}
		}
	}
}

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

func TestRetainedDailyComplianceMatchesCanonicalQuality(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	for _, state := range []string{"met", "violated", "partial", "zero", "empty", "missing", "shortened", "expired", "before_validity"} {
		t.Run(state, func(t *testing.T) {
			input := slo.WindowInput{At: end, Now: end, ValidFrom: start, SliValidFrom: start,
				RetentionStart: start, Duration: 24 * time.Hour, Cadence: 12 * time.Hour, FreshnessGrace: 24 * time.Hour, Target: "0.99"}
			for index := range 2 {
				input.Observations = append(input.Observations, slo.Observation{Start: start.Add(time.Duration(index) * 12 * time.Hour),
					End: start.Add(time.Duration(index+1) * 12 * time.Hour), Received: end, Outcome: "observed", Good: "100", Total: "100"})
			}
			switch state {
			case "violated":
				input.Observations[0].Good = "90"
			case "partial":
				input.Observations = input.Observations[:1]
			case "zero":
				for index := range input.Observations {
					input.Observations[index].Good, input.Observations[index].Total = "0", "0"
				}
			case "empty":
				input.Observations[0].Outcome = "complete_empty"
			case "missing":
				input.Observations = nil
			case "shortened":
				input.ValidFrom = start.Add(12 * time.Hour)
			case "expired":
				input.RetentionStart = start.Add(12 * time.Hour)
			case "before_validity":
				input.ValidFrom = end
			}
			result, err := slo.EvaluateOperationalWindowContext(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			want := operationalComplianceDay(result, end, input.ValidFrom, id, "1", input.Target, input.Duration, input.Cadence)
			got := operationalComplianceDay(retainedOperationalResult(operationalValue(result)), end, input.ValidFrom, id, "1", input.Target, input.Duration, input.Cadence)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("retained compliance changed canonical %s result: got %+v, want %+v", state, got, want)
			}
		})
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
