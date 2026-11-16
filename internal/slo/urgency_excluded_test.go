package slo

import "testing"

func TestExcludedUrgencyIsWatch(t *testing.T) {
	t.Parallel()
	for _, stale := range []bool{false, true} {
		in := OperationalState{QualityState: "excluded", Freshness: "fresh", PerformanceState: "unknown"}
		group := "watch"
		if stale {
			in.Freshness = "stale"
			group = "needs_attention"
		}
		got := ClassifyOperational(in)
		found := false
		for _, r := range got.Reasons {
			found = found || r.Code == "excluded"
		}
		if got.Group != group || !found {
			t.Fatalf("excluded urgency: %+v want %s with excluded reason", got, group)
		}
	}
}
