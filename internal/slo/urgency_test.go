package slo

import (
	"testing"
)

func TestClassifyOperationalThresholds(t *testing.T) {
	for _, test := range []struct {
		name, fraction, burn, want string
	}{
		{name: "below twenty percent", fraction: "0.199999999999", burn: "0.1", want: "needs_attention"},
		{name: "at twenty percent", fraction: "0.2", burn: "0.1", want: "watch"},
		{name: "below half", fraction: "0.499999999999", burn: "0.1", want: "watch"},
		{name: "at half", fraction: "0.5", burn: "0.1", want: "healthy"},
		{name: "below one burn", fraction: "0.8", burn: "0.999999999999", want: "healthy"},
		{name: "at one burn", fraction: "0.8", burn: "1", want: "needs_attention"},
		{name: "zero budget", fraction: "0", burn: "0.1", want: "needs_attention"},
	} {
		t.Run(test.name, func(t *testing.T) {
			remaining := test.fraction
			burn := test.burn
			state := OperationalState{QualityState: "complete", Freshness: "fresh", PerformanceState: "met", RemainingFraction: &remaining, Burn1h: &burn, Burn1hAvailable: true}
			got := ClassifyOperational(state)
			if got.Group != test.want {
				t.Fatalf("group = %q, want %q; reasons=%+v", got.Group, test.want, got.Reasons)
			}
		})
	}
}

func TestClassifyOperationalKeepsSecondaryViolationAndMarksProvisional(t *testing.T) {
	remaining := "-1"
	state := OperationalState{QualityState: "partial", Freshness: "stale", PerformanceState: "unknown", Provisional: true, RemainingBudget: &remaining}
	got := ClassifyOperational(state)
	if got.Group != "needs_attention" || len(got.Reasons) != 4 {
		t.Fatalf("unexpected urgency: %+v", got)
	}
	if got.Reasons[0].Code != "stale_observations" || got.Reasons[1].Code != "negative_observed_budget" || !got.Reasons[1].Provisional || got.Reasons[2].Code != "incomplete_coverage" || got.Reasons[3].Code != "short_burn_unavailable" {
		t.Fatalf("unexpected reasons: %+v", got.Reasons)
	}
}

func TestClassifyOperationalUsesUnroundedRationalThresholds(t *testing.T) {
	target := "0.99"
	for _, test := range []struct {
		name, budgetGood, burnGood string
		want                       string
	}{
		{name: "displayed point two is below", budgetGood: "991999999999999", burnGood: "999000000000000", want: "needs_attention"},
		{name: "displayed point five is below", budgetGood: "994999999999999", burnGood: "999000000000000", want: "watch"},
		{name: "displayed burn one is below", budgetGood: "995000000000000", burnGood: "990000000000001", want: "healthy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calculation, err := CalculateOccurrences(test.budgetGood, "1000000000000000", target, false)
			if err != nil {
				t.Fatal(err)
			}
			burnCalculation, err := CalculateOccurrences(test.burnGood, "1000000000000000", target, false)
			if err != nil {
				t.Fatal(err)
			}
			state := OperationalState{QualityState: "complete", Freshness: "fresh", PerformanceState: "met",
				RemainingFraction: &calculation.RemainingFraction, RemainingFractionExact: calculation.ExactRemainingFraction,
				RemainingBudget: &calculation.Remaining, RemainingExact: calculation.ExactRemaining,
				Burn1h: &burnCalculation.Burn, Burn1hExact: burnCalculation.ExactConsumed, Burn1hAvailable: true}
			got := ClassifyOperational(state)
			if got.Group != test.want {
				t.Fatalf("group=%s rounded fraction=%s reasons=%+v, want %s", got.Group, calculation.RemainingFraction, got.Reasons, test.want)
			}
		})
	}
	nearExhaustion, err := CalculateOccurrences("0.000000000001", "0.000000000001", "0.5", false)
	if err != nil || nearExhaustion.Remaining != "0.000000000000" || nearExhaustion.ExactRemaining.Sign() <= 0 {
		t.Fatalf("tiny positive budget = %+v, %v", nearExhaustion, err)
	}
}

func TestShortenedEligibilityIsWatchUnlessAnotherAttentionReasonApplies(t *testing.T) {
	state := OperationalState{QualityState: "complete", Freshness: "fresh", PerformanceState: "violated", ShortenedEligibility: true, Burn1hAvailable: false}
	got := ClassifyOperational(state)
	if got.Group != "watch" || got.Reasons[0].Code != "incomplete_coverage" {
		t.Fatalf("shortened eligibility = %+v, want watch/incomplete_coverage", got)
	}
	remaining := "-1"
	state.RemainingBudget = &remaining
	got = ClassifyOperational(state)
	if got.Group != "needs_attention" || len(got.Reasons) != 3 || got.Reasons[0].Code != "negative_observed_budget" || !got.Reasons[0].Provisional {
		t.Fatalf("shortened provisional negative budget = %+v", got)
	}
}

func TestCompleteCoverageWithProvisionalPerformanceIsWatch(t *testing.T) {
	burn := "0.1"
	state := OperationalState{QualityState: "complete", Freshness: "fresh", PerformanceState: "unknown", Provisional: true, Burn1h: &burn, Burn1hAvailable: true}
	got := ClassifyOperational(state)
	if got.Group != "watch" || len(got.Reasons) != 1 || got.Reasons[0].Code != "incomplete_coverage" {
		t.Fatalf("complete but provisional classification = %+v", got)
	}
}

func TestIncompleteOneHourBurnCannotEstablishHealthyAndQualifiesAttention(t *testing.T) {
	remaining, partialBurn, highBurn := "0.9", "0.1", "1.2"
	base := OperationalState{QualityState: "complete", Freshness: "fresh", PerformanceState: "met", RemainingFraction: &remaining,
		Burn1hAvailable: true, Burn1hQualityState: "partial", Burn1hFreshness: "fresh", Burn1hProvisional: true}
	base.Burn1h = &partialBurn
	got := ClassifyOperational(base)
	if got.Group != "watch" || !hasOperationalReason(got.Reasons, "short_burn_unavailable", false) {
		t.Fatalf("partial low 1 h burn classified as %+v; want qualified watch", got)
	}
	base.Burn1h = &highBurn
	got = ClassifyOperational(base)
	if got.Group != "needs_attention" || !hasOperationalReason(got.Reasons, "high_1h_burn", true) {
		t.Fatalf("partial high 1 h burn classified as %+v; want provisional attention", got)
	}
	base.Burn1hQualityState = "complete"
	base.Burn1hProvisional = false
	base.Burn1hShortenedEligibility = true
	got = ClassifyOperational(base)
	if got.Group != "needs_attention" || !hasOperationalReason(got.Reasons, "high_1h_burn", true) || !hasOperationalReason(got.Reasons, "short_burn_unavailable", false) {
		t.Fatalf("shortened high 1 h burn classified as %+v; want qualified attention and watch", got)
	}
}

func hasOperationalReason(reasons []OperationalReason, code string, provisional bool) bool {
	for _, reason := range reasons {
		if reason.Code == code && reason.Provisional == provisional {
			return true
		}
	}
	return false
}
