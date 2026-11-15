package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
)

func TestMaintenanceWindowPlanSharedWork(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 10, 0, 0, time.UTC)
	base := slo.WindowInput{At: at, Now: at, Duration: 6 * time.Hour, Cadence: time.Minute, ValidFrom: at.Add(-24 * time.Hour), SliValidFrom: at.Add(-24 * time.Hour), RetentionStart: at.Add(-24 * time.Hour), Target: "0.99"}
	inputs := []slo.WindowInput{base, base, base}
	inputs[1].Duration = time.Hour
	inputs[2].Duration = 5 * time.Minute
	before := append([]slo.WindowInput(nil), inputs...)
	limits := maintenanceWindowLimits{MaxSourceHours: 5, MaxRawRows: 7200, MaxFragments: 7202}
	plan, err := planMaintenanceWindows(t.Context(), inputs, limits)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inputs, before) {
		t.Fatal("planner changed caller windows")
	}
	if len(plan.SourceHours) != 5 || len(plan.Edges) != 2 {
		t.Fatalf("shared work not deduplicated: hours=%v edges=%v", plan.SourceHours, plan.Edges)
	}
	want := []maintenanceWindowRange{{Start: at.Add(-6 * time.Hour), End: time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)}, {Start: time.Date(2026, 11, 1, 11, 10, 0, 0, time.UTC), End: at}}
	if !reflect.DeepEqual(plan.Edges, want) {
		t.Fatalf("edges=%v want=%v", plan.Edges, want)
	}
	limits.MaxSourceHours = 4
	if _, err := planMaintenanceWindows(t.Context(), inputs, limits); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("admitted more than four source hours: %v", err)
	}
}

func TestMaintenanceWindowPlanClipsAndUsesCommonGrid(t *testing.T) {
	at := ceilSliBoundary(time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC), 70*time.Second)
	input := slo.WindowInput{At: at, Now: at, Duration: 16*time.Hour + 6*time.Minute, Cadence: 70 * time.Second, ValidFrom: at.Add(-20 * time.Hour), SliValidFrom: at.Add(-20 * time.Hour), RetentionStart: at.Add(-20 * time.Hour), Target: "0.99"}
	// Duration is an exact source cadence multiple; the physical hour/cadence
	// common grid is seven hours. Source ownership is never rounded to 60s.
	input.Duration = (input.Duration / input.Cadence) * input.Cadence
	plan, err := planMaintenanceWindows(t.Context(), []slo.WindowInput{input}, maintenanceWindowLimits{MaxSourceHours: 24, MaxRawRows: 7200, MaxFragments: 7202})
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range plan.Windows {
		if window.FullStart.Unix()%int64((7*time.Hour)/time.Second) != 0 || window.FullEnd.Unix()%int64((7*time.Hour)/time.Second) != 0 {
			t.Fatalf("interior is not common-grid aligned: %+v", window)
		}
	}
	input.RetentionStart = at.Add(-30*time.Minute + time.Second)
	plan, err = planMaintenanceWindows(t.Context(), []slo.WindowInput{input}, maintenanceWindowLimits{MaxSourceHours: 1, MaxRawRows: 7200, MaxFragments: 7202})
	if err != nil || len(plan.SourceHours) != 0 || len(plan.Edges) != 1 || plan.Edges[0].Start.Before(input.RetentionStart) {
		t.Fatalf("clipped window: %+v %v", plan, err)
	}
}

func TestMaintenanceWindowPlanRejectsUnsafeBatch(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	input := slo.WindowInput{At: at, Now: at, Duration: time.Hour, Cadence: time.Minute, Target: "0.99"}
	limits := maintenanceWindowLimits{MaxSourceHours: 1, MaxRawRows: 7200, MaxFragments: 7202}
	mixed := input
	mixed.Now = mixed.Now.Add(time.Second)
	if _, err := planMaintenanceWindows(t.Context(), []slo.WindowInput{input, mixed}, limits); err == nil {
		t.Fatal("mixed source snapshots accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := planMaintenanceWindows(ctx, []slo.WindowInput{input}, limits); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if plan, err := planMaintenanceWindows(t.Context(), nil, maintenanceWindowLimits{}); err != nil || len(plan.Windows) != 0 {
		t.Fatalf("empty: %+v %v", plan, err)
	}
}

func TestMaintenanceWindowPlanRejectsUnsafeUnits(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	input := slo.WindowInput{At: at, Now: at, Duration: time.Hour, Cadence: 100 * time.Nanosecond, Target: "0.99"}
	limits := maintenanceWindowLimits{MaxSourceHours: 1, MaxRawRows: 7200, MaxFragments: 7202}
	if _, err := planMaintenanceWindows(t.Context(), []slo.WindowInput{input}, limits); err == nil {
		t.Fatal("accepted sub-microsecond storage cadence")
	}
	input.Cadence = time.Duration(9000000000) * time.Second
	input.Duration = input.Cadence
	if _, err := planMaintenanceWindows(t.Context(), []slo.WindowInput{input}, limits); err == nil {
		t.Fatal("accepted unsupported overflowing common-grid cadence")
	}
	input.Cadence = time.Minute
	input.Duration = time.Hour
	input.RetentionStart = time.Date(1000000000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := planMaintenanceWindows(t.Context(), []slo.WindowInput{input}, limits); err == nil {
		t.Fatal("accepted unrepresentable retention timestamp")
	}
}
