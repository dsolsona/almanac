package alert

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestCatchUpRetainsOriginalGridAndHorizonBoundary(t *testing.T) {
	t.Parallel()
	origin := time.Date(2026, 11, 14, 12, 0, 17, 0, time.UTC)
	grid := ScheduledGrid{Origin: origin, Cadence: time.Minute}
	for _, test := range []struct {
		name             string
		next, tick, kept time.Time
		horizon          time.Duration
		due, skipped     bool
	}{
		{"exact horizon retained", origin, origin.Add(24 * time.Hour), origin, 24 * time.Hour, true, false},
		{"one nanosecond beyond", origin, origin.Add(24*time.Hour + time.Nanosecond), origin.Add(time.Minute), 24 * time.Hour, true, true},
		{"original phase retained", origin, origin.Add(24*time.Hour + 90*time.Second), origin.Add(2 * time.Minute), 24 * time.Hour, true, true},
		{"within horizon stays oldest", origin, origin.Add(23 * time.Hour), origin, 24 * time.Hour, true, false},
		{"due now", origin, origin, origin, time.Hour, true, false},
		{"not yet due", origin.Add(time.Minute), origin, origin.Add(time.Minute), time.Hour, false, false},
		{"horizon narrower than cadence", origin, origin.Add(30 * time.Second), origin.Add(time.Minute), time.Second, false, true},
		{"long outage", origin, origin.AddDate(200, 0, 0), origin.AddDate(200, 0, -1), 24 * time.Hour, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := PlanCatchUp(grid, test.next, test.tick, test.horizon)
			if err != nil || !got.Next.Equal(test.kept) || got.Due != test.due || (got.Skipped != nil) != test.skipped {
				t.Fatalf("plan=%+v error=%v; want next=%s due=%v skipped=%v", got, err, test.kept, test.due, test.skipped)
			}
			if got.Skipped != nil && (!got.Skipped.Start.Equal(test.next) || !got.Skipped.End.Equal(test.kept) || got.Skipped.Cadence != grid.Cadence) {
				t.Fatalf("gap must cover precisely the skipped scheduled boundaries: %+v", got.Skipped)
			}
			repeated, err := PlanCatchUp(grid, test.next, test.tick, test.horizon)
			if err != nil || !reflect.DeepEqual(got, repeated) {
				t.Fatal("same explicit clock changed plan")
			}
			afterGap, err := PlanCatchUp(grid, got.Next, test.tick, test.horizon)
			if err != nil || afterGap.Skipped != nil || !afterGap.Next.Equal(got.Next) || afterGap.Due != got.Due {
				t.Fatalf("planner consumed retained work: %+v %v", afterGap, err)
			}
		})
	}
}

func TestCatchUpRejectsInvalidOrOverflowingBounds(t *testing.T) {
	t.Parallel()
	origin := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		grid       ScheduledGrid
		next, tick time.Time
		horizon    time.Duration
	}{
		{"zero grid", ScheduledGrid{Cadence: time.Minute}, origin, origin, time.Hour},
		{"zero next", ScheduledGrid{Origin: origin, Cadence: time.Minute}, time.Time{}, origin, time.Hour},
		{"zero tick", ScheduledGrid{Origin: origin, Cadence: time.Minute}, origin, time.Time{}, time.Hour},
		{"zero cadence", ScheduledGrid{Origin: origin}, origin, origin, time.Hour},
		{"negative cadence", ScheduledGrid{Origin: origin, Cadence: -time.Minute}, origin, origin, time.Hour},
		{"zero horizon", ScheduledGrid{Origin: origin, Cadence: time.Minute}, origin, origin, 0},
		{"negative horizon", ScheduledGrid{Origin: origin, Cadence: time.Minute}, origin, origin, -time.Hour},
		{"off grid", ScheduledGrid{Origin: origin, Cadence: time.Minute}, origin.Add(time.Second), origin.Add(time.Hour), time.Hour},
		{"grid duration overflow", ScheduledGrid{Origin: origin, Cadence: time.Minute}, origin.AddDate(400, 0, 0), origin.AddDate(400, 0, 0), time.Hour},
		{"outage duration overflow", ScheduledGrid{Origin: origin, Cadence: time.Minute}, origin, origin.AddDate(400, 0, 0), time.Hour},
		{"rounded duration overflow", ScheduledGrid{Origin: origin, Cadence: time.Minute}, origin, origin.Add(time.Duration(math.MaxInt64)), time.Nanosecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := PlanCatchUp(test.grid, test.next, test.tick, test.horizon)
			if !errors.Is(err, ErrSchedule) || !reflect.DeepEqual(got, CatchUpPlan{}) {
				t.Fatalf("invalid bounds produced work: %+v %v", got, err)
			}
		})
	}
}

func TestCatchUpExplicitOddCadenceAndClockRegression(t *testing.T) {
	t.Parallel()
	origin := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	grid := ScheduledGrid{Origin: origin, Cadence: 70 * time.Second}
	got, err := PlanCatchUp(grid, origin, origin.Add(300*time.Second), 100*time.Second)
	if err != nil || !got.Next.Equal(origin.Add(210*time.Second)) || got.Skipped == nil {
		t.Fatalf("odd grid plan: %+v %v", got, err)
	}
	// Clock rollback cannot move the retained cursor backwards or invent a gap.
	back, err := PlanCatchUp(grid, got.Next, origin.Add(140*time.Second), 100*time.Second)
	if err != nil || back.Due || back.Skipped != nil || !back.Next.Equal(got.Next) {
		t.Fatalf("clock regression: %+v %v", back, err)
	}
}
