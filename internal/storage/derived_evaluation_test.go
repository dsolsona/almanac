package storage

import (
	"testing"
	"time"
)

func TestDerivedHourEdgesStayExactForIrregularAndLongCadences(t *testing.T) {
	for _, seconds := range []int{15, 17, 60, 90, 420, 3600, 3601, 86399, 86400} {
		cadence := time.Duration(seconds) * time.Second
		for _, instant := range []time.Time{time.Date(2026, 10, 30, 12, 37, 0, 0, time.UTC), time.Unix(-123456789, 0).UTC()} {
			end := floorSliBoundary(instant, cadence)
			for _, ticks := range []int{1, 17, 7201, 100000} {
				start := end.Add(-time.Duration(ticks) * cadence)
				from, to := derivedAggregateBounds(start, end, cadence)
				if !floorSliBoundary(from, cadence).Equal(from) || !floorSliBoundary(to, cadence).Equal(to) || !from.Truncate(time.Hour).Equal(from) || !to.Truncate(time.Hour).Equal(to) {
					t.Fatalf("cadence %ds produced an inexact full-hour boundary: %s %s", seconds, from, to)
				}
				edges := end.Sub(start)
				if !from.After(to) {
					edges = from.Sub(start) + end.Sub(to)
				}
				if edges/cadence > 7200 {
					t.Fatalf("cadence %ds left %d raw intervals", seconds, edges/cadence)
				}
			}
		}
	}
}
