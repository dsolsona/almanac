package slo

import (
	"fmt"
	"testing"
	"time"
)

func TestOperationalBurnWindowsUseIndependentExactWindows(t *testing.T) {
	cadence := time.Minute
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	facts := make([]Observation, 360)
	for index := range facts {
		start := now.Add(-time.Duration(360-index) * cadence)
		good, total := "0", "0"
		if index == 0 {
			good, total = "5000", "5000"
		} else if index == len(facts)-1 {
			good, total = "990", "1000"
		}
		facts[index] = Observation{Start: start, End: start.Add(cadence), Received: start.Add(2 * time.Second), Outcome: "observed", Good: good, Total: total}
	}
	base := WindowInput{Now: now, ValidFrom: now.Add(-6 * time.Hour), SliValidFrom: now.Add(-6 * time.Hour),
		RetentionStart: now.Add(-24 * time.Hour), Cadence: cadence, Target: "0.99", Observations: facts, FreshnessGrace: 3 * time.Minute}
	base.Duration = time.Hour
	base.At = now
	base.Observations = facts[len(facts)-60:]
	oneHour, err := EvaluateOperationalWindow(base)
	if err != nil {
		t.Fatal(err)
	}
	if oneHour.Burn == nil || *oneHour.Burn != "1.000000000000" || oneHour.Performance != "met" || oneHour.State != "complete" {
		t.Fatalf("one hour result = %+v", oneHour)
	}
	base.Duration = 6 * time.Hour
	base.Observations = facts
	sixHours, err := EvaluateOperationalWindow(base)
	if err != nil {
		t.Fatal(err)
	}
	if sixHours.Burn == nil || *sixHours.Burn != "0.166666666667" || sixHours.Performance != "met" || sixHours.State != "complete" {
		t.Fatalf("six hour result = %+v", sixHours)
	}
}

func TestOperationalWindowExpiresRollingFactsWithoutGeneratingMissingTicks(t *testing.T) {
	cadence := time.Minute
	start := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	now := start.Add(2 * time.Hour)
	fact := Observation{Start: start, End: start.Add(cadence), Received: start.Add(2 * time.Second), Outcome: "observed", Good: "99", Total: "100"}
	input := WindowInput{Now: now, ValidFrom: start, SliValidFrom: start, RetentionStart: start.Add(-24 * time.Hour),
		Duration: time.Hour, Cadence: cadence, Target: "0.99", FreshnessGrace: 3 * time.Minute, Observations: []Observation{fact}}
	input.At = fact.Start.Add(time.Hour)
	first, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Remaining == nil || *first.Remaining != "0.000000000000" || first.State != "partial" || first.Performance != "unknown" || first.ObservedPerformance != "met" || first.Expected != 60 || first.Observed != 1 || first.Missing != 59 || !first.Provisional {
		t.Fatalf("equal-target result = %+v", first)
	}
	input.At = fact.Start.Add(time.Hour + 2*cadence)
	second, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Observed != 0 || second.Missing != 60 || second.State != "no_data" || second.Performance != "unknown" {
		t.Fatalf("expired observation result = %+v", second)
	}
}

func TestWindowSweepMatchesIndependentWindowEvaluations(t *testing.T) {
	cadence := time.Minute
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	facts := make([]Observation, 24)
	for index := range facts {
		start := base.Add(time.Duration(index-24) * cadence)
		facts[index] = Observation{Start: start, End: start.Add(cadence), Received: start.Add(2 * time.Second), Outcome: "observed", Good: fmt.Sprint(95 + index%5), Total: "100"}
	}
	input := WindowInput{Now: base, ValidFrom: base.Add(-time.Hour), SliValidFrom: base.Add(-time.Hour), RetentionStart: base.Add(-2 * time.Hour),
		Duration: 10 * cadence, Cadence: cadence, Target: "0.99", FreshnessGrace: 3 * time.Minute, Observations: facts}
	sweep, err := NewWindowSweep(input)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 24; index++ {
		at := base.Add(time.Duration(index-24) * cadence)
		got, err := sweep.At(at)
		if err != nil {
			t.Fatal(err)
		}
		input.At = at
		want, err := EvaluateOperationalWindow(input)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != want.State || got.Expected != want.Expected || got.Observed != want.Observed || got.Missing != want.Missing || got.Good == nil != (want.Good == nil) {
			t.Fatalf("at %s: sweep=%+v independent=%+v", at, got, want)
		}
		if got.Good != nil && *got.Good != *want.Good {
			t.Fatalf("at %s: sweep good=%s want %s", at, *got.Good, *want.Good)
		}
	}
}

func TestWindowSweepZeroTrafficKeepsCountsWithoutInventingPerformance(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	cadence := time.Minute
	input := WindowInput{At: at, Now: at, ValidFrom: at.Add(-cadence), SliValidFrom: at.Add(-cadence), RetentionStart: at.Add(-time.Hour),
		Duration: cadence, Cadence: cadence, Target: "0.99", FreshnessGrace: time.Minute,
		Observations: []Observation{{Start: at.Add(-cadence), End: at, Received: at, Outcome: "observed", Good: "0", Total: "0"}}}
	result, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "zero_traffic" || result.Good == nil || *result.Good != "0.000000000000" || result.Total == nil || *result.Total != "0.000000000000" {
		t.Fatalf("zero-traffic quality/counts = %+v", result)
	}
	if result.Attainment != nil || result.Burn != nil || result.Remaining != nil || result.Allowed != nil {
		t.Fatalf("zero traffic fabricated performance values = %+v", result)
	}
}
