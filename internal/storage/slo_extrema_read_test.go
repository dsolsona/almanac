package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
)

func TestExtremaReadPlanBoundsYearRowsAndLiveEndpoints(t *testing.T) {
	for _, seconds := range []int{15, 60, 70, 7200} {
		cadence := time.Duration(seconds) * time.Second
		for _, days := range []int{7, 28, 90, 365} {
			for _, offset := range []time.Duration{0, time.Minute, 12*time.Hour + 7*time.Minute, 23*time.Hour + 59*time.Minute} {
				end := floorSliBoundary(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(offset), cadence)
				start := ceilSliBoundary(end.Add(-time.Duration(days)*24*time.Hour), cadence)
				for _, trim := range []time.Duration{0, 2 * time.Hour} {
					bound := operationalBounds{start: start.Add(trim), end: end, effectiveAt: end}
					input := slo.WindowInput{Now: end.Add(7 * time.Second), Cadence: cadence}
					plan, err := planExtremaRead(input, bound)
					if err != nil {
						t.Fatal(err)
					}
					if days > 28 && len(plan.stored) > 400 {
						t.Fatalf("year/range read exceeds row budget: %d", len(plan.stored))
					}
					live, next := 1, bound.start
					for _, part := range plan.parts {
						if !part.start.Equal(next) || !part.start.Before(part.end) {
							t.Fatalf("read plan has a hole or overlap: %+v", plan.parts)
						}
						if part.resolution == 0 {
							live += int(floorSliBoundary(part.end, cadence).Sub(floorSliBoundary(part.start, cadence)) / cadence)
						}
						next = part.end
					}
					if !next.Equal(end) || live > 7200 {
						t.Fatalf("read plan exceeds live budget or omits its end: live=%d through=%s", live, next)
					}
					seen := make(map[retainedReadPart]bool)
					for _, part := range plan.stored {
						if seen[part] {
							t.Fatal("graph and compliance read the same row twice")
						}
						seen[part] = true
					}
				}
			}
		}
	}
}

func TestExtremaReadPlanKeepsFirstPartialDayHourlyWithinYearBudget(t *testing.T) {
	end := time.Date(2026, 11, 1, 23, 59, 0, 0, time.UTC)
	bound := operationalBounds{start: end.Add(-365 * 24 * time.Hour), end: end, effectiveAt: end}
	for _, offset := range []time.Duration{time.Minute, 2 * time.Hour, 24 * time.Hour, 25 * time.Hour, 364 * 24 * time.Hour} {
		valid := bound.start.Add(offset)
		input := slo.WindowInput{Cadence: 15 * time.Second, ValidFrom: valid}
		plan, err := planExtremaRead(input, bound)
		if err != nil || len(plan.stored) > 400 {
			t.Fatalf("first partial day exceeded year budget: %d rows, %v", len(plan.stored), err)
		}
		live := 1
		for _, part := range plan.parts {
			if part.resolution == 86400 && part.start.Before(valid) && part.end.After(valid) {
				t.Fatal("first partial day lost its hourly inputs")
			}
			if part.resolution == 0 {
				live += int(floorSliBoundary(part.end, input.Cadence).Sub(floorSliBoundary(part.start, input.Cadence)) / input.Cadence)
			}
		}
		if live > 7200 {
			t.Fatalf("first partial day exceeded live endpoint budget: %d", live)
		}
	}
}
