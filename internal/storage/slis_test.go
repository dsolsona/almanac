package storage

import (
	"testing"
	"time"
)

func TestSLIActivationBoundaryIsFutureCanonicalBoundary(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"on boundary", time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 27, 12, 0, 15, 0, time.UTC)},
		{"between boundaries", time.Date(2026, 10, 27, 12, 0, 8, 400_000_000, time.UTC), time.Date(2026, 10, 27, 12, 0, 30, 0, time.UTC)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := nextSliActivationBoundary(test.now, 15)
			if !got.Equal(test.want) || !got.After(test.now) || got.Unix()%15 != 0 {
				t.Fatalf("activation boundary = %s, want future canonical %s", got, test.want)
			}
		})
	}
}

func TestMigrationSQLStateDiagnosticRejectsUntrustedText(t *testing.T) {
	for _, value := range []string{"42P01", "23505"} {
		if !safeSQLState(value) {
			t.Errorf("valid SQLSTATE %q rejected", value)
		}
	}
	for _, value := range []string{"", "secret", "42p01", "42P01\nsecret", "abcde", "1234"} {
		if safeSQLState(value) {
			t.Errorf("unsafe SQLSTATE %q accepted", value)
		}
	}
}
