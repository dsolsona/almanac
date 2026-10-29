package slo

import "math/big"

type OperationalState struct {
	QualityState, Freshness, PerformanceState           string
	Provisional                                         bool
	ShortenedEligibility                                bool
	RemainingBudget, RemainingFraction, Burn1h          *string
	RemainingExact, RemainingFractionExact, Burn1hExact *big.Rat
	Burn1hAvailable                                     bool
	Burn1hQualityState, Burn1hFreshness                 string
	Burn1hProvisional, Burn1hShortenedEligibility       bool
}

type OperationalReason struct {
	Code        string
	Provisional bool
}

type OperationalUrgency struct {
	Group   string
	Reasons []OperationalReason
}

// ClassifyOperational applies ADR-0023 thresholds to exact, unrounded values.
func ClassifyOperational(input OperationalState) OperationalUrgency {
	attention := make(map[string]bool)
	watch := make(map[string]bool)
	add := func(target map[string]bool, reason string) { target[reason] = true }

	switch input.Freshness {
	case "stale":
		add(attention, "stale_observations")
	}
	switch input.QualityState {
	case "no_data":
		add(attention, "no_data")
	case "expired_history":
		add(attention, "expired_history")
	case "not_yet_eligible":
		add(watch, "not_yet_eligible")
	case "zero_traffic":
		add(watch, "zero_traffic")
	case "unavailable":
		add(watch, "evaluation_unavailable")
	case "partial":
		add(watch, "incomplete_coverage")
	case "not_applicable":
		add(watch, "incomplete_coverage")
	}
	if input.ShortenedEligibility {
		add(watch, "incomplete_coverage")
	}
	if input.Provisional && input.QualityState == "complete" {
		add(watch, "incomplete_coverage")
	}
	if input.PerformanceState == "violated" && input.QualityState == "complete" && !input.Provisional && !input.ShortenedEligibility {
		add(attention, "complete_window_violation")
	}
	remainingExact := input.RemainingExact
	if remainingExact == nil && input.RemainingBudget != nil {
		remainingExact = parseSignedDecimal(*input.RemainingBudget)
	}
	if remainingExact != nil {
		if remainingExact.Sign() < 0 {
			add(attention, "negative_observed_budget")
		}
	}
	remainingFractionExact := input.RemainingFractionExact
	if remainingFractionExact == nil && input.RemainingFraction != nil {
		remainingFractionExact = parseSignedDecimal(*input.RemainingFraction)
	}
	if remainingFractionExact != nil {
		if remainingFractionExact.Cmp(big.NewRat(1, 5)) < 0 {
			add(attention, "low_observed_budget")
		} else if remainingFractionExact.Cmp(big.NewRat(1, 2)) < 0 {
			add(watch, "budget_below_half")
		}
	}
	if input.Burn1hAvailable && input.Burn1h != nil {
		burnExact := input.Burn1hExact
		if burnExact == nil {
			burnExact = parseSignedDecimal(*input.Burn1h)
		}
		if burnExact != nil && burnExact.Cmp(big.NewRat(1, 1)) >= 0 {
			add(attention, "high_1h_burn")
		}
		if input.Burn1hQualityState != "" && (input.Burn1hQualityState != "complete" || input.Burn1hFreshness != "fresh" || input.Burn1hProvisional || input.Burn1hShortenedEligibility) {
			add(watch, "short_burn_unavailable")
		}
	} else {
		add(watch, "short_burn_unavailable")
	}
	if input.PerformanceState == "unknown" && input.QualityState == "unavailable" {
		add(watch, "evaluation_unavailable")
	}

	group := "healthy"
	if len(attention) > 0 {
		group = "needs_attention"
	} else if len(watch) > 0 {
		group = "watch"
	}

	order := []string{
		"stale_observations", "no_data", "expired_history", "complete_window_violation",
		"negative_observed_budget", "low_observed_budget", "high_1h_burn",
		"evaluation_unavailable", "not_yet_eligible", "zero_traffic", "incomplete_coverage",
		"short_burn_unavailable", "budget_below_half", "on_track",
	}
	provisional := input.Provisional || input.ShortenedEligibility || input.QualityState == "partial" || input.QualityState == "unavailable"
	reasons := make([]OperationalReason, 0, len(attention)+len(watch)+1)
	for _, code := range order {
		if attention[code] || watch[code] {
			reasonProvisional := provisional && (code == "negative_observed_budget" || code == "low_observed_budget")
			if code == "high_1h_burn" {
				reasonProvisional = input.Burn1hProvisional || input.Burn1hShortenedEligibility || input.Burn1hQualityState != "" && (input.Burn1hQualityState != "complete" || input.Burn1hFreshness != "fresh")
			}
			reasons = append(reasons, OperationalReason{Code: code, Provisional: reasonProvisional})
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, OperationalReason{Code: "on_track"})
	}
	return OperationalUrgency{Group: group, Reasons: reasons}
}

func parseSignedDecimal(value string) *big.Rat {
	result, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil
	}
	return result
}
