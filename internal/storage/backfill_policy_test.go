package storage

import (
	"testing"
	"time"
)

func TestBackfillRecoveryLookback(t *testing.T) {
	for _, tc := range []struct{ configured, days int }{
		{0, 30}, {7, 7}, {30, 30}, {90, 30}, {1, 1},
	} {
		want := time.Duration(tc.days) * 24 * time.Hour
		if got := backfillRecoveryLookback(tc.configured); got != want {
			t.Errorf("retention %d: got %s, want %s", tc.configured, got, want)
		}
	}
}
