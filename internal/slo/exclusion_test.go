package slo

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
)

func exclusionFixture() WindowInput {
	start := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	return WindowInput{At: start.Add(2 * time.Minute), Now: start.Add(2 * time.Minute), ValidFrom: start,
		SliValidFrom: start, RetentionStart: start, Duration: 2 * time.Minute, Cadence: time.Minute,
		FreshnessGrace: 0, Target: "0.99", Observations: []Observation{
			{Start: start, End: start.Add(time.Minute), Received: start.Add(time.Minute), Outcome: "observed", Good: "900", Total: "1000"},
			{Start: start.Add(time.Minute), End: start.Add(2 * time.Minute), Received: start.Add(2 * time.Minute), Outcome: "observed", Good: "100", Total: "1000"},
		}}
}

func exclusionValue(t *testing.T, name string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %s", name, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %s, want %s", name, *got, want)
	}
}

func TestExclusionOccurrencePopulation(t *testing.T) {
	input := exclusionFixture()
	raw, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Exclusions = []ExclusionInterval{{Start: input.Observations[1].Start, End: input.At}}
	got, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	exclusionValue(t, "raw attainment", raw.Attainment, "0.500000000000")
	for name, pair := range map[string]struct {
		got  *string
		want string
	}{
		"good": {got.Good, "900.000000000000"}, "total": {got.Total, "1000.000000000000"},
		"attainment": {got.Attainment, "0.900000000000"}, "allowed": {got.Allowed, "10.000000000000"},
		"bad": {got.Bad, "100.000000000000"}, "remaining": {got.Remaining, "-90.000000000000"},
		"burn": {got.Burn, "10.000000000000"}, "excluded good": {got.ExcludedGood, "100.000000000000"},
		"excluded total": {got.ExcludedTotal, "1000.000000000000"}, "coverage": {got.Coverage, "1.000000000000"},
	} {
		exclusionValue(t, name, pair.got, pair.want)
	}
	if got.Expected != 1 || got.Observed != 1 || got.Excluded != 1 || got.Missing != 0 || got.State != "complete" || got.Freshness != "fresh" || got.Provisional {
		t.Fatalf("adjusted quality = %+v", got)
	}
	if got.FirstObservation == nil || !got.FirstObservation.Equal(input.Observations[0].Start) || got.LastObservation == nil || !got.LastObservation.Equal(input.Observations[0].End) || got.LatestReceipt == nil || !got.LatestReceipt.Equal(input.Observations[0].Received) {
		t.Fatalf("excluded timestamps entered eligible metadata: %+v", got)
	}
}

func TestExclusionUnionAndScopeImmutability(t *testing.T) {
	input := exclusionFixture()
	input.Duration = 4 * time.Minute
	input.At, input.Now = input.At.Add(2*time.Minute), input.Now.Add(2*time.Minute)
	start := input.ValidFrom
	input.Observations = append(input.Observations,
		Observation{Start: start.Add(2 * time.Minute), End: start.Add(3 * time.Minute), Received: input.At, Outcome: "observed", Good: "0", Total: "2000"},
		Observation{Start: start.Add(3 * time.Minute), End: input.At, Received: input.At, Outcome: "observed", Good: "0", Total: "3000"})
	input.Exclusions = []ExclusionInterval{
		{Start: start.Add(2 * time.Minute), End: input.At},
		{Start: start.Add(time.Minute), End: start.Add(3 * time.Minute)},
		{Start: start.Add(time.Minute), End: start.Add(2 * time.Minute)},
		{Start: input.At, End: input.At.Add(time.Minute)},
	}
	facts, mask := slices.Clone(input.Observations), slices.Clone(input.Exclusions)
	got, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Excluded != 3 || got.Expected != 1 || got.Observed != 1 {
		t.Fatalf("union counted overlap twice: %+v", got)
	}
	exclusionValue(t, "excluded total", got.ExcludedTotal, "6000.000000000000")
	if !reflect.DeepEqual(input.Observations, facts) || !reflect.DeepEqual(input.Exclusions, mask) {
		t.Fatal("evaluation mutated source or mask")
	}
	other := input
	other.Exclusions = nil
	raw, err := EvaluateOperationalWindow(other)
	if err != nil {
		t.Fatal(err)
	}
	exclusionValue(t, "independent scope total", raw.Total, "7000.000000000000")
	if raw.Excluded != 0 || raw.ExcludedGood != nil || raw.ExcludedTotal != nil {
		t.Fatalf("unmasked scope changed: %+v", raw)
	}
}

func TestExclusionMissingAndUnknownEventPopulation(t *testing.T) {
	for _, outcome := range []string{"absent", "failed", "incomplete", "complete_empty", "expired"} {
		t.Run(outcome, func(t *testing.T) {
			input := exclusionFixture()
			input.Exclusions = []ExclusionInterval{{Start: input.ValidFrom, End: input.Observations[0].End}}
			if outcome == "absent" {
				input.Observations = input.Observations[1:]
			} else if outcome == "expired" {
				input.RetentionStart = input.Observations[0].End
			} else {
				input.Observations[0].Outcome = outcome
				input.Observations[0].Good, input.Observations[0].Total = "", ""
			}
			got, err := EvaluateOperationalWindow(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "complete" || got.Missing != 0 || got.Expired != 0 || got.Expected != 1 || got.Excluded != 1 || got.ExcludedGood != nil || got.ExcludedTotal != nil {
				t.Fatalf("excluded unknown population became zero or incomplete eligible coverage: %+v", got)
			}
		})
	}
	input := exclusionFixture()
	input.Exclusions = []ExclusionInterval{{Start: input.Observations[1].Start, End: input.At}}
	input.Observations = input.Observations[1:]
	got, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "no_data" || got.Missing != 1 || got.Expected != 1 || got.Burn != nil {
		t.Fatalf("missing eligible observation became success: %+v", got)
	}
}

func TestExclusionAllExcludedAndPreValidity(t *testing.T) {
	input := exclusionFixture()
	input.Exclusions = []ExclusionInterval{{Start: input.ValidFrom, End: input.At}}
	got, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "excluded" || got.Reason != "excluded" || got.Performance != "unknown" || got.Expected != 0 || got.Excluded != 2 || got.Observed != 0 || got.Good != nil || got.Total != nil || got.Attainment != nil || got.Remaining != nil || got.Burn != nil || got.ExactBurn != nil || got.Coverage != nil {
		t.Fatalf("full exclusion fabricated performance: %+v", got)
	}
	exclusionValue(t, "fully excluded traffic", got.ExcludedTotal, "2000.000000000000")
	input.ValidFrom = input.At
	got, err = EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "not_applicable" || got.Excluded != 0 || got.ExcludedTotal != nil {
		t.Fatalf("pre-validity became excluded: %+v", got)
	}
	input = exclusionFixture()
	input.Observations[0].Good, input.Observations[0].Total = "0", "0"
	input.Exclusions = []ExclusionInterval{{Start: input.Observations[1].Start, End: input.At}}
	got, err = EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "zero_traffic" || got.Expected != 1 || got.Burn != nil {
		t.Fatalf("eligible zero traffic became excluded: %+v", got)
	}
}

func TestExclusionTrailingFreshnessDoesNotHideEligibleGap(t *testing.T) {
	input := exclusionFixture()
	input.At, input.Now = input.At.Add(time.Minute), input.Now.Add(time.Minute)
	input.Duration = 3 * time.Minute
	input.Exclusions = []ExclusionInterval{{Start: input.At.Add(-time.Minute), End: input.At}}
	input.Observations = input.Observations[:1]
	got, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "partial" || got.Freshness != "stale" || got.Missing != 1 || got.Expected != 2 {
		t.Fatalf("maintenance concealed eligible gap: %+v", got)
	}
}

func TestExclusionInvalidBounds(t *testing.T) {
	base := exclusionFixture()
	for _, mask := range []ExclusionInterval{
		{Start: base.ValidFrom, End: base.ValidFrom}, {Start: base.At, End: base.ValidFrom},
		{Start: base.ValidFrom.Add(time.Second), End: base.At}, {Start: base.ValidFrom, End: base.At.Add(time.Nanosecond)},
		{End: base.At},
	} {
		input := base
		input.Exclusions = []ExclusionInterval{mask}
		if _, err := EvaluateOperationalWindow(input); !errors.Is(err, ErrInvalidCalculation) {
			t.Errorf("invalid bounds %+v returned %v", mask, err)
		}
	}
	input := base
	input.Exclusions = []ExclusionInterval{{Start: base.ValidFrom, End: base.At}}
	input.Observations = slices.Clone(base.Observations)
	input.Observations[0].Start = input.Observations[0].Start.Add(time.Second)
	input.Observations[0].End = input.Observations[0].End.Add(time.Second)
	if _, err := EvaluateOperationalWindow(input); !errors.Is(err, ErrOperationalFacts) {
		t.Fatalf("unaligned observation accepted under mask: %v", err)
	}
}

func TestExclusionCancellation(t *testing.T) {
	input := exclusionFixture()
	input.Observations = nil
	input.Exclusions = []ExclusionInterval{{Start: input.ValidFrom, End: input.At}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewWindowSweepContext(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("mask preparation ignored cancellation: %v", err)
	}
}

func TestExclusionRollingSweepRetainsSnapshots(t *testing.T) {
	input := exclusionFixture()
	start := input.ValidFrom
	input.Observations = nil
	for i, good := range []string{"100", "90", "80", "70", "60", "50"} {
		input.Observations = append(input.Observations, Observation{Start: start.Add(time.Duration(i) * time.Minute), End: start.Add(time.Duration(i+1) * time.Minute), Received: start.Add(time.Duration(i+1) * time.Minute), Outcome: "observed", Good: good, Total: "100"})
	}
	input.Exclusions = []ExclusionInterval{{Start: start.Add(time.Minute), End: start.Add(3 * time.Minute)}, {Start: start.Add(4 * time.Minute), End: start.Add(5 * time.Minute)}}
	sweep, err := NewWindowSweepContext(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	// The sweep owns its normalized mask, not this caller's mutable slice.
	input.Exclusions[0].End = start.Add(6 * time.Minute)
	var excludedSummary WindowResult
	for i, want := range []struct {
		expected, excluded  int64
		good, excludedTotal string
	}{
		{1, 0, "100.000000000000", ""}, {1, 1, "100.000000000000", "100.000000000000"},
		{0, 2, "", "200.000000000000"}, {1, 1, "70.000000000000", "100.000000000000"},
		{1, 1, "70.000000000000", "100.000000000000"}, {1, 1, "50.000000000000", "100.000000000000"},
	} {
		summary, err := sweep.AtSummaryContext(t.Context(), start.Add(time.Duration(i+1)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if summary.Good != nil || summary.ExcludedGood != nil {
			t.Fatal("summary eagerly formatted event counts")
		}
		if i == 2 {
			excludedSummary = summary
		}
		got := summary.WithValues()
		if got.Expected != want.expected || got.Excluded != want.excluded || got.Missing != 0 {
			t.Fatalf("tick %d quality = %+v", i+1, got)
		}
		if want.good == "" {
			if got.State != "excluded" || got.Good != nil {
				t.Fatalf("tick %d not excluded: %+v", i+1, got)
			}
		} else {
			exclusionValue(t, "rolling eligible good", got.Good, want.good)
		}
		if want.excludedTotal != "" {
			exclusionValue(t, "rolling excluded total", got.ExcludedTotal, want.excludedTotal)
		}
	}
	exclusionValue(t, "retained excluded snapshot", excludedSummary.WithValues().ExcludedGood, "170.000000000000")
}

func TestExclusionWholeGridAndUTCInstants(t *testing.T) {
	input := exclusionFixture()
	start := time.Unix(70*1_000_000, 0).UTC()
	input.ValidFrom, input.SliValidFrom, input.RetentionStart = start, start, start
	input.Cadence, input.Duration, input.At, input.Now = 70*time.Second, 140*time.Second, start.Add(140*time.Second), start.Add(140*time.Second)
	for i := range input.Observations {
		input.Observations[i].Start = start.Add(time.Duration(i) * 70 * time.Second)
		input.Observations[i].End = start.Add(time.Duration(i+1) * 70 * time.Second)
		input.Observations[i].Received = input.Observations[i].End
	}
	zone := time.FixedZone("explicit offset", 3600)
	input.Exclusions = []ExclusionInterval{{Start: start.Add(70 * time.Second).In(zone), End: input.At.In(zone)}}
	got, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Expected != 1 || got.Excluded != 1 || got.Freshness != "fresh" {
		t.Fatalf("70-second grid = %+v", got)
	}
	exclusionValue(t, "70-second eligible total", got.Total, "1000.000000000000")
	input.Exclusions[0].Start = start.Add(time.Minute)
	if _, err := EvaluateOperationalWindow(input); !errors.Is(err, ErrInvalidCalculation) {
		t.Fatalf("minute-rounded 70-second mask accepted: %v", err)
	}
}

func TestExclusionSparseYearAndEmptyMask(t *testing.T) {
	input := exclusionFixture()
	base, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Exclusions = []ExclusionInterval{}
	empty, err := EvaluateOperationalWindow(input)
	if err != nil || !reflect.DeepEqual(base, empty) {
		t.Fatalf("empty mask changes canonical result: %v", err)
	}
	input.Exclusions = []ExclusionInterval{{Start: input.At, End: input.At.Add(time.Hour)}}
	outside, err := EvaluateOperationalWindow(input)
	if err != nil || !reflect.DeepEqual(base, outside) {
		t.Fatalf("future mask changes current result: %v", err)
	}
	input.Observations = nil
	input.Duration = 365 * 24 * time.Hour
	input.ValidFrom, input.SliValidFrom, input.RetentionStart = input.At.Add(-input.Duration), input.At.Add(-input.Duration), input.At.Add(-input.Duration)
	input.Exclusions = []ExclusionInterval{{Start: input.At.Add(-24 * time.Hour), End: input.At}}
	got, err := EvaluateOperationalWindow(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Expected != 524160 || got.Missing != 524160 || got.Excluded != 1440 || got.ExcludedTotal != nil || got.State != "no_data" {
		t.Fatalf("sparse year population = %+v", got)
	}
}
