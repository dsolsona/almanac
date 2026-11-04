package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func TestUnavailableOverviewRowNeverInventsCurrentSuccessOrSourceGaps(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 23, 0, time.UTC)
	objective := api.Slo{CadenceSeconds: 60, RollingWindowSeconds: 86400, Target: "0.99",
		ActivatedAt: now.Add(-48 * time.Hour), EvaluationValidFrom: now.Add(-48 * time.Hour)}
	for _, reason := range []string{"current_calculation_pending", "current_calculation_failed", "source_aggregate_failed"} {
		item := unavailableOverviewItem(api.SloOperationalOverviewItem{Slo: objective, BudgetSparkline: []api.SloOperationalSample{}}, now, reason)
		if item.Urgency != "watch" || item.LeadingReason.Code != "evaluation_unavailable" || item.Generation != nil ||
			item.Current.Quality.State != "unavailable" || item.Current.Quality.Freshness != "unknown" || item.Current.Quality.MissingIntervals != 0 ||
			item.Current.Quality.Coverage != nil || item.Current.Performance.State != "unknown" || !item.Current.Performance.Provisional ||
			item.Current.Performance.Good != nil || item.Current.Performance.Total != nil || item.Current.Performance.RemainingBudget != nil ||
			item.Current.Quality.Reason == nil || *item.Current.Quality.Reason != reason || item.Exhaustion.State != "unavailable" || len(item.BudgetSparkline) != 0 {
			t.Fatalf("%s fabricated success, counts, source gaps or exhaustion: %+v", reason, item)
		}
		if !item.Current.EvaluationAt.Equal(now.Truncate(time.Minute)) || !item.Current.Window.ActivatedAt.Equal(objective.ActivatedAt) ||
			!item.Current.Window.EvaluationValidFrom.Equal(objective.EvaluationValidFrom) || item.Burn1h.Performance.Burn != nil || item.Burn6h.Performance.Burn != nil {
			t.Fatal("unavailable row changed provenance or retained a numeric burn")
		}
		wantHistory := api.SloOperationalOverviewItemBudgetHistoryState("unavailable")
		if reason == "current_calculation_pending" {
			wantHistory = "preparing"
		}
		if item.BudgetHistoryState == nil || *item.BudgetHistoryState != wantHistory {
			t.Fatal("failure was presented as pending preparation")
		}
	}
}

func TestUnavailableCurrentOverviewPreservesSeparatelyQualifiedHistory(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 23, 0, time.UTC)
	stamp := &api.DerivedGeneration{EvaluatedThrough: now.Add(-time.Minute), SourceGeneration: "7"}
	previous := api.SloOperationalOverviewItem{Slo: api.Slo{CadenceSeconds: 60, RollingWindowSeconds: 86400},
		BudgetSparkline:         []api.SloOperationalSample{{EvaluationAt: now.Add(-time.Minute), RemainingFraction: new("0.125"), State: "observed"}},
		BudgetHistoryGeneration: stamp, Generation: stamp}
	for _, reason := range []string{"current_calculation_pending", "current_calculation_failed", "source_aggregate_failed"} {
		got := unavailableOverviewItem(previous, now, reason)
		if len(got.BudgetSparkline) != 1 || got.BudgetSparkline[0].RemainingFraction == nil || *got.BudgetSparkline[0].RemainingFraction != "0.125" || got.BudgetHistoryGeneration != stamp {
			t.Fatalf("%s erased previously published history or its provenance", reason)
		}
		if got.Generation != nil || got.Current.Performance.RemainingBudget != nil || got.Current.Quality.State != "unavailable" || got.BudgetHistoryState == nil || *got.BudgetHistoryState == "ready" {
			t.Fatalf("%s presented old history as current results", reason)
		}
	}
}
