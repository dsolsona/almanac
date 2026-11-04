package command

import (
	"testing"
	"time"
)

func performanceLagBounds(earliest, observed, previous time.Time, previousQualified bool) (time.Duration, time.Duration) {
	lower := time.Duration(0)
	if !previous.IsZero() && !previousQualified {
		lower = max(0, previous.Sub(earliest))
	}
	return lower, observed.Sub(earliest)
}

func TestPerformancePublicationBoundsPreserveObservationUncertainty(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		previous  time.Duration
		qualified bool
		lower     time.Duration
	}{
		{2 * time.Second, false, 2 * time.Second},
		{2 * time.Second, true, 0}, // Already visible before this event was discovered.
		{-time.Second, false, 0},
	} {
		low, high := performanceLagBounds(start, start.Add(3200*time.Millisecond), start.Add(test.previous), test.qualified)
		if low != test.lower || high != 3200*time.Millisecond {
			t.Fatalf("incorrect publication bounds: %s..%s", low, high)
		}
	}
	low, high := performanceLagBounds(start, start.Add(time.Second), time.Time{}, false)
	if low != 0 || high != time.Second {
		t.Fatal("first observation fabricated an earlier failed probe")
	}
}

// Ten users keep a frequent authenticated visibility cohort while their history
// visits rotate across the entire installation. Publication is observed separately.
func performanceSaturationTarget(actor, turn, size int, reserveDetail bool) (int, string) {
	switch turn % 4 {
	case 0:
		return actor, "list"
	case 2:
		if reserveDetail {
			size--
		}
		return (actor + performanceUsers*(turn/4)) % size, "history"
	default:
		return actor, "status"
	}
}

func TestPerformanceSamplerMaintainsVisibilityCadenceAndFullHistoryCoverage(t *testing.T) {
	for _, size := range []int{10, 100, 1000} {
		history := make(map[int]bool, size)
		var last [performanceUsers]int
		for turn := range 4 * (size / performanceUsers) {
			for actor := range performanceUsers {
				target, kind := performanceSaturationTarget(actor, turn, size, false)
				if target < 0 || target >= size {
					t.Fatalf("size%d selected invalid objective%d", size, target)
				}
				switch kind {
				case "history":
					history[target] = true
				case "status":
					if target != actor || last[actor] > 0 && turn-last[actor] > 2 {
						t.Fatalf("size%d delayed or changed visibility cohort actor%d turn%d", size, actor, turn)
					}
					last[actor] = turn
				}
			}
		}
		if len(history) != size {
			t.Fatalf("size%d sampled only%d historical objectives", size, len(history))
		}
	}
}

func TestPerformanceSamplerReservesFirstBrowserDetailWithoutReducingVisibility(t *testing.T) {
	for _, size := range []int{10, 100, 1000} {
		history := make(map[int]bool)
		status := make(map[int]bool)
		for turn := range 4 * (size / performanceUsers) {
			for actor := range performanceUsers {
				target, kind := performanceSaturationTarget(actor, turn, size, true)
				if target < 0 || target >= size {
					t.Fatal("reserved sampler selected invalid target")
				}
				if kind == "history" {
					history[target] = true
				}
				if kind == "status" {
					status[target] = true
				}
			}
		}
		if history[size-1] || len(history) != size-1 || len(status) != performanceUsers {
			t.Fatalf("size%d lost its reserved detail or frequent status cohort", size)
		}
	}
}
