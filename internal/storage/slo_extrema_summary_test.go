package storage

import (
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
)

func TestExtremaSummaryMatchesEagerCanonicalSelection(t *testing.T) {
	for _, variant := range []string{"mixed", "flat", "zero", "empty", "missing", "expired", "before_validity"} {
		t.Run(variant, func(t *testing.T) { checkExtremaSummarySelection(t, variant) })
	}
}

func checkExtremaSummarySelection(t *testing.T, variant string) {
	t.Helper()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	input := slo.WindowInput{Now: start.Add(time.Hour), Duration: 10 * time.Minute, Cadence: time.Minute, Target: "0.999", ValidFrom: start, SliValidFrom: start, RetentionStart: start, FreshnessGrace: time.Minute}
	if variant == "expired" {
		input.RetentionStart = start.Add(15 * time.Minute)
	}
	if variant == "before_validity" {
		input.ValidFrom = start.Add(2 * time.Hour)
	}
	for index := range 60 {
		fact := slo.Observation{Start: start.Add(time.Duration(index) * time.Minute), End: start.Add(time.Duration(index+1) * time.Minute), Received: start.Add(time.Duration(index+1) * time.Minute), Outcome: "observed", Good: "99999999999999999999.999999999998", Total: "99999999999999999999.999999999999"}
		switch {
		case variant == "missing":
			continue
		case variant == "empty":
			fact.Outcome, fact.Good, fact.Total = "complete_empty", "", ""
		case variant == "zero":
			fact.Good, fact.Total = "0", "0"
		case variant == "mixed" || variant == "expired":
			switch index {
			case 10, 11:
				fact.Good = "0"
			case 20:
				fact.Outcome, fact.Good, fact.Total = "complete_empty", "", ""
			case 30:
				continue
			case 40, 41:
				fact.Good, fact.Total = "0", "0"
			}
		}
		input.Observations = append(input.Observations, fact)
	}
	sweep, err := slo.NewWindowSweepContext(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	var summaries []slo.WindowResult
	var eager []api.SloOperationalSample
	for index := range 60 {
		result, err := sweep.AtSummaryContext(t.Context(), start.Add(time.Duration(index+1)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		summaries = append(summaries, result)
		eager = append(eager, operationalSample(result.WithValues(), start, start.Add(time.Hour), "1"))
	}
	for _, metric := range []string{"budget", "attainment", "burn"} {
		want, err := makeExtremaLane(eager, start, start.Add(time.Hour), input.Target, metric)
		if err != nil {
			t.Fatal(err)
		}
		got, err := makeExtremaSummaryLane(summaries, start, start.Add(time.Hour), input.Target, metric, "1")
		if err != nil {
			t.Fatal(err)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Fatalf("%s changed retained canonical evidence", metric)
		}
		if variant == "flat" {
			if len(got.Marks) != 2 || !got.Marks[0].At.Equal(start.Add(time.Minute)) || !got.Marks[1].At.Equal(start.Add(time.Hour)) {
				t.Fatal("flat lane must retain its first and last endpoints")
			}
			if len(got.Marks[0].Labels) != 3 || got.Marks[0].Labels[0] != "first" || got.Marks[0].Labels[1] != "minimum" || got.Marks[0].Labels[2] != "maximum" {
				t.Fatal("equal extrema must select the first endpoint")
			}
		}
		if variant == "zero" || variant == "empty" || variant == "missing" || variant == "before_validity" {
			wantQuality := map[string]string{"zero": "zero_traffic", "empty": "complete", "missing": "no_data", "before_validity": "not_applicable"}[variant]
			if len(got.Marks) != 2 || string(got.Marks[0].Quality) != wantQuality || len(got.Marks[0].Labels) != 1 || got.Marks[0].Labels[0] != "first" {
				t.Fatalf("%s must preserve quality without numeric extrema: %+v", variant, got.Marks)
			}
		}
	}
}
