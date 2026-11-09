package storage

import (
	"encoding/json/v2"
	"math/big"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
)

func TestExtremaBuildEndBoundsEvaluationWork(t *testing.T) {
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name                                 string
		cadence                              time.Duration
		startHour, availableHours, wantHours int
	}{
		{"no closed hours", time.Minute, 0, 0, 0},
		{"small backlog", time.Minute, 0, 2, 2},
		{"15 seconds", 15 * time.Second, 0, 48, 1},
		{"60 seconds", time.Minute, 0, 48, 4},
		{"70 seconds", 70 * time.Second, 0, 48, 9},
		{"7200 seconds", 2 * time.Hour, 0, 48, 24},
		{"day boundary", time.Minute, 23, 48, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := day.Add(time.Duration(test.startHour) * time.Hour)
			end := extremaBuildEnd(start, start.Add(time.Duration(test.availableHours)*time.Hour), 28*24*time.Hour, test.cadence)
			if !end.Equal(start.Add(time.Duration(test.wantHours) * time.Hour)) {
				t.Fatalf("build ends at %s, want %d hours after %s", end, test.wantHours, start)
			}
			inputs := 0
			for at := floorSliBoundary(start, test.cadence).Add(test.cadence); !at.After(end); at = at.Add(test.cadence) {
				inputs += len(retainedRepairDurations(28*24*time.Hour, test.cadence))
			}
			if inputs > derivedWindowBatchLimit {
				t.Fatalf("one turn evaluates %d inputs, limit %d", inputs, derivedWindowBatchLimit)
			}
		})
	}
}

func TestDailyExtremaMarkKeepsOriginalCutoff(t *testing.T) {
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	at := day.Add(time.Hour)
	original := day.Add(-3 * time.Hour)
	input := slo.WindowInput{Now: day.Add(24 * time.Hour), At: at, Duration: 4 * time.Hour, Cadence: time.Minute,
		ValidFrom: day.Add(-24 * time.Hour), SliValidFrom: day.Add(-24 * time.Hour), RetentionStart: original, Target: "0.99", FreshnessGrace: time.Minute}
	value, err := slo.EvaluateOperationalTotals(t.Context(), input, slo.WindowTotals{Good: new(big.Rat), Total: new(big.Rat), KnownEmpty: 240, LatestReceipt: new(at)})
	if err != nil {
		t.Fatal(err)
	}
	lane, err := makeExtremaLane([]api.SloOperationalSample{operationalSample(value, day, at, "1")}, day, at, input.Target, "budget")
	if err != nil {
		t.Fatal(err)
	}
	lane.Marks[0].RetentionStart = original
	input.RetentionStart = original.Add(time.Hour)
	samples, err := lane.samples(t.Context(), input, day, at, "1", "budget")
	if err != nil || len(samples) != 1 || samples[0].Quality.KnownEmptyIntervals != 240 || samples[0].Quality.ExpiredIntervals != 0 || samples[0].Quality.MissingIntervals != 0 {
		t.Fatalf("daily composition changed an untouched mark's retained evidence: %+v, %v", samples, err)
	}
}

func TestExtremaMarksReproduceCanonicalSamples(t *testing.T) {
	for _, seconds := range []int{15, 60, 70, 7200} {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) {
			t.Parallel()
			cadence := time.Duration(seconds) * time.Second
			start := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
			end := start.Add(time.Hour)
			base := slo.WindowInput{Now: end, Cadence: cadence, ValidFrom: start.Add(-24 * time.Hour),
				SliValidFrom: start.Add(-24 * time.Hour), RetentionStart: start.Add(-24 * time.Hour), Target: "0.999", FreshnessGrace: 2 * cadence}
			random := rand.New(rand.NewPCG(17, 29))
			for from := floorSliBoundary(start.Add(-24*time.Hour), cadence); from.Before(end); from = from.Add(cadence) {
				fact := slo.Observation{Start: from, End: from.Add(cadence), Received: from.Add(3 * cadence),
					Outcome: "observed", Good: "12345678901234567890123455.123456789012", Total: "12345678901234567890123456.123456789012"}
				switch random.IntN(10) {
				case 0, 1:
					continue
				case 2:
					fact.Outcome, fact.Good, fact.Total = "complete_empty", "", ""
				case 3:
					fact.Good, fact.Total = "0", "0"
				case 4:
					fact.Good = "0.123456789012"
				}
				// Receipt order differs from interval order.
				fact.Received = fact.Received.Add(time.Duration(random.IntN(10)) * cadence)
				base.Observations = append(base.Observations, fact)
			}
			for _, lane := range []struct {
				duration time.Duration
				metric   string
			}{{12 * cadence, "budget"}, {cadence, "attainment"}} {
				base.Duration = lane.duration
				sweep, err := slo.NewWindowSweepContext(t.Context(), base)
				if err != nil {
					t.Fatal(err)
				}
				var samples []api.SloOperationalSample
				for at := floorSliBoundary(start, cadence).Add(cadence); !at.After(end); at = at.Add(cadence) {
					value, err := sweep.AtContext(t.Context(), at)
					if err != nil {
						t.Fatal(err)
					}
					sample := operationalSample(value, start, end, "1")
					if lane.metric == "attainment" {
						sample = operationalAttainmentSample(value, start, end, "1")
					}
					samples = append(samples, sample)
				}
				stored, err := makeExtremaLane(samples, start, end, base.Target, lane.metric)
				if err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(stored)
				if err != nil {
					t.Fatal(err)
				}
				var replay extremaLane
				if err := json.Unmarshal(body, &replay); err != nil {
					t.Fatal(err)
				}
				input := base
				input.Observations = nil
				got, err := replay.samples(t.Context(), input, start, end, "1", lane.metric)
				want, reduceErr := reduceRetainedLaneLimit(samples, start, end, base.Target, lane.metric, sloOperationalSeriesLimit)
				if err != nil || reduceErr != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("cadence %ds %s replay changed exact inputs, labels or quality: %v / %v\ngot=%+v\nwant=%+v", seconds, lane.metric, err, reduceErr, got, want)
				}
			}
		})
	}
}

func TestExtremaDenseTransitionsKeepPresence(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var samples []api.SloOperationalSample
	for i := range 1600 {
		state, quality := api.SloOperationalSampleState("missing"), api.SloOperationalQualityState("no_data")
		if i%2 == 0 {
			state, quality = "known_empty", "complete"
		}
		samples = append(samples, api.SloOperationalSample{EvaluationAt: start.Add(time.Duration(i+1) * time.Second),
			State: state, Quality: api.SloOperationalQuality{State: quality, Freshness: "unknown"}})
	}
	lane, err := makeExtremaLane(samples, start, start.Add(time.Hour), "0.99", "budget")
	if err != nil || !lane.Coarse || len(lane.Marks) != 2 ||
		!slices.Contains(lane.States, api.SloDisplayCoverageStates("missing")) ||
		!slices.Contains(lane.States, api.SloDisplayCoverageStates("known_empty")) {
		t.Fatalf("dense transitions lost presence or exceeded labelled marks: %+v, %v", lane, err)
	}
}
