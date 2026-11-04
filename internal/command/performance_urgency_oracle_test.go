package command

import (
	"errors"
	"math/big"
	"testing"
	"time"
)

type performanceOracleWindowView struct {
	Counts    performanceOracleCounts
	End, Last time.Time
	Grace     time.Duration
}

// ADR-0023 triage from independent exact counts, not rounded API percentages.
// This cohort has active definitions, compatible cadence and no expired input.
func performanceOracleUrgency(current, burn performanceOracleWindowView, target string) (string, string, error) {
	ratio, valid := new(big.Rat).SetString(target)
	if !valid || ratio.Sign() < 0 || ratio.Cmp(big.NewRat(1, 1)) >= 0 {
		return "", "", errors.New("invalid urgency oracle target")
	}
	metrics := func(counts performanceOracleCounts) (fraction, burn *big.Rat) {
		if counts.Total.Sign() == 0 {
			return nil, nil
		}
		allowed := new(big.Rat).Mul(counts.Total, new(big.Rat).Sub(big.NewRat(1, 1), ratio))
		used := new(big.Rat).Quo(new(big.Rat).Sub(counts.Total, counts.Good), allowed)
		return new(big.Rat).Sub(big.NewRat(1, 1), used), used
	}
	fraction, _ := metrics(current.Counts)
	_, rate := metrics(burn.Counts)
	partial := func(c performanceOracleCounts) bool { return c.Missing > 0 || c.Empty > 0 }
	stale := func(v performanceOracleWindowView) bool { return !v.Last.IsZero() && v.End.After(v.Last.Add(v.Grace)) }
	negative := fraction != nil && fraction.Sign() < 0
	for _, rule := range []struct {
		match  bool
		reason string
	}{
		{stale(current), "stale_observations"},
		{current.Counts.Observed+current.Counts.Empty == 0 && current.Counts.Missing > 0, "no_data"},
		{negative && !partial(current.Counts), "complete_window_violation"},
		{negative, "negative_observed_budget"},
		{fraction != nil && fraction.Cmp(big.NewRat(1, 5)) < 0, "low_observed_budget"},
		{rate != nil && rate.Cmp(big.NewRat(1, 1)) >= 0, "high_1h_burn"},
	} {
		if rule.match {
			return "needs_attention", rule.reason, nil
		}
	}
	for _, rule := range []struct {
		match  bool
		reason string
	}{
		{current.Counts.Observed > 0 && current.Counts.Zero == current.Counts.Observed && current.Counts.Empty == 0 && current.Counts.Missing == 0, "zero_traffic"},
		{partial(current.Counts), "incomplete_coverage"},
		{rate == nil || partial(burn.Counts) || stale(burn), "short_burn_unavailable"},
		{fraction != nil && fraction.Cmp(big.NewRat(1, 2)) < 0, "budget_below_half"},
	} {
		if rule.match {
			return "watch", rule.reason, nil
		}
	}
	return "healthy", "on_track", nil
}

func TestPerformanceUrgencyOracleUsesExactThresholdsAndQualification(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	view := func(good int64) performanceOracleWindowView {
		return performanceOracleWindowView{Counts: performanceOracleCounts{Good: big.NewRat(good, 1), Total: big.NewRat(1000, 1), Observed: 60}, End: at, Last: at, Grace: 120 * time.Second}
	}
	for _, test := range []struct {
		good, burn    int64
		missing       int64
		stale         bool
		group, reason string
	}{
		{999, 999, 0, false, "healthy", "on_track"},
		{995, 999, 0, false, "needs_attention", "low_observed_budget"},
		{994, 999, 0, false, "needs_attention", "complete_window_violation"},
		{994, 999, 1, false, "needs_attention", "negative_observed_budget"},
		{997, 999, 0, false, "watch", "budget_below_half"},
		{996, 999, 0, false, "watch", "budget_below_half"},
		{999, 995, 0, false, "needs_attention", "high_1h_burn"},
		{999, 999, 1, false, "watch", "incomplete_coverage"},
		{999, 999, 0, true, "needs_attention", "stale_observations"},
	} {
		current, burn := view(test.good), view(test.burn)
		current.Counts.Missing = test.missing
		if test.stale {
			current.Last = at.Add(-121 * time.Second)
		}
		group, reason, err := performanceOracleUrgency(current, burn, "0.995")
		if err != nil || group != test.group || reason != test.reason {
			t.Fatalf("independent urgency mismatch: %s/%s %v", group, reason, err)
		}
	}
	current, burn := view(999), view(999)
	current.Counts.Good, _ = new(big.Rat).SetString("997.5")
	group, _, err := performanceOracleUrgency(current, burn, "0.995")
	if err != nil || group != "healthy" {
		t.Fatal("exactly half the budget was classified below half")
	}
}
