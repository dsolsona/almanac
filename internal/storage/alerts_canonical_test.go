package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/slo"
)

func TestCanonicalAlertWindow(t *testing.T) {
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	identity := alert.SnapshotIdentity{Cutoff: at}
	for _, test := range []struct {
		name, state, freshness string
		observed, empty        int64
		last                   time.Time
		want                   alert.Quality
		invalid                bool
	}{
		{name: "complete", state: "complete", freshness: "fresh", observed: 5, last: at, want: alert.Complete},
		{name: "known empty is not zero traffic", state: "complete", freshness: "fresh", observed: 4, empty: 1, last: at, want: alert.Partial},
		{name: "all known empty", state: "complete", freshness: "unknown", empty: 5, want: alert.Missing},
		{name: "stale source", state: "complete", freshness: "stale", observed: 5, last: at.Add(-10 * time.Minute), want: alert.Stale},
		{name: "stale against scheduling time", state: "complete", freshness: "fresh", observed: 5, last: at, want: alert.Stale},
		{name: "unknown cannot be complete", state: "complete", freshness: "unknown", invalid: true},
		{name: "unknown quality fails closed", state: "pending", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheduled := at
			if test.name == "stale against scheduling time" {
				scheduled = at.Add(2 * time.Minute)
			}
			value := slo.WindowResult{RequestedStart: at.Add(-5 * time.Minute), End: at, State: test.state, Freshness: test.freshness, Observed: test.observed, KnownEmpty: test.empty}
			if !test.last.IsZero() {
				value.LastObservation = &test.last
			}
			got, err := canonicalAlertWindow(identity, scheduled, time.Minute, 5*time.Minute, value)
			if test.invalid {
				if err == nil {
					t.Fatalf("accepted unknown quality: %+v", got)
				}
				return
			}
			if err != nil || got.Quality != test.want {
				t.Fatalf("got %+v %v want %s", got, err, test.want)
			}
		})
	}
}
