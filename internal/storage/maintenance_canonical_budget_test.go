package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
)

func TestCanonicalMaintenanceHistoryUsesDistinctInteriors(t *testing.T) {
	t.Parallel()
	// The seeded 35-day fleet stopped on the turn containing this hour,
	// with revision zero and 30-day objectives.
	start := time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC)
	validFrom := time.Date(2026, 10, 13, 20, 47, 0, 0, time.UTC)
	now := start.Add(29 * 24 * time.Hour)
	const cadence = time.Minute
	var inputs []slo.WindowInput
	for at := start.Add(cadence); !at.After(start.Add(time.Hour)); at = at.Add(cadence) {
		for _, duration := range retainedRepairDurations(30*24*time.Hour, cadence) {
			inputs = append(inputs, slo.WindowInput{At: at, Now: now, Duration: duration, Cadence: cadence, ValidFrom: validFrom, Target: "0.999"})
		}
	}
	limits := maintenanceWindowLimits{MaxSourceHours: int(sli.ObservationRetention/time.Hour) + 2, MaxRawRows: sloOperationalFactsLimit, MaxFragments: sloOperationalFactsLimit}
	plan, err := planMaintenanceWindows(t.Context(), inputs, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitMaintenanceEvaluation([]maintenanceEvaluationWork{{plan: plan}}, maintenanceWindowPlan{}, limits); err != nil {
		t.Fatalf("shared 30-day history batch was refused: %v", err)
	}
}

func TestCanonicalMaintenanceAdmissionCountsSeparateReads(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 19, 16, 0, 0, 0, time.UTC)
	first := maintenancePlannedWindow{FullStart: start, FullEnd: start.Add(4 * time.Hour)}
	second := maintenancePlannedWindow{FullStart: start.Add(time.Hour), FullEnd: start.Add(4 * time.Hour)}
	plan := maintenanceWindowPlan{Windows: []maintenancePlannedWindow{first, first, second}}
	work := []maintenanceEvaluationWork{{plan: plan}, {plan: maintenanceWindowPlan{Windows: []maintenancePlannedWindow{first}}}}
	// A duplicate in one SQL request consumes no additional read. An overlapping
	// interior with different bounds, or an identical interior read by another
	// mask request, still consumes its own budget.
	limits := maintenanceWindowLimits{MaxSourceHours: 11, MaxRawRows: 1, MaxFragments: 1}
	if err := admitMaintenanceEvaluation(work, maintenanceWindowPlan{}, limits); err != nil {
		t.Fatalf("11-hour allowance refused: %v", err)
	}
	limits.MaxSourceHours--
	if err := admitMaintenanceEvaluation(work, maintenanceWindowPlan{}, limits); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("10-hour allowance returned %v", err)
	}
}
