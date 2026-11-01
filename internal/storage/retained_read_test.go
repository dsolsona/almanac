package storage

import (
	"testing"
	"time"
)

func TestRetainedYearReadBoundsCoverageAndEdgeWork(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{0, time.Second, 15*time.Minute + 15*time.Second, 23*time.Hour + 59*time.Minute} {
		for _, duration := range []time.Duration{time.Minute, time.Hour, 7*24*time.Hour + 15*time.Minute, 365 * 24 * time.Hour} {
			start := base.Add(offset)
			end := start.Add(duration)
			parts, err := retainedReadParts(start, end)
			if err != nil || len(parts) > 413 || len(parts) == 0 {
				t.Fatalf("unbounded retained range: %d parts, %v", len(parts), err)
			}
			next, edges := start, 0
			for _, part := range parts {
				if !part.start.Equal(next) || !part.start.Before(part.end) {
					t.Fatal("retained read overlaps or loses a requested interval")
				}
				if part.resolution == 0 {
					edges++
					if part.end.Sub(part.start) > time.Hour {
						t.Fatal("raw edge exceeds one hour")
					}
				} else if part.end.Sub(part.start) != time.Duration(part.resolution)*time.Second || !part.start.Equal(part.start.Truncate(time.Duration(part.resolution)*time.Second)) {
					t.Fatal("retained tile is not on its stable UTC grid")
				}
				next = part.end
			}
			if !next.Equal(end) || edges > 2 {
				t.Fatal("retained read loses its end or requires unbounded raw edges")
			}
		}
	}
}
