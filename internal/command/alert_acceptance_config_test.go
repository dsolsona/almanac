//go:build alertacceptance

package command

import "testing"

func TestAlertAcceptanceRetention(t *testing.T) {
	for _, input := range []string{"", "today", "2026-11-16", "0001-01-01T00:00:00Z", "2026-11-16T10:00:00+01:00"} {
		if _, err := alertAcceptanceRetention(input); err == nil {
			t.Errorf("accepted invalid fixture retention %q", input)
		}
	}
	got, err := alertAcceptanceRetention("2026-10-01T00:00:00Z")
	if err != nil || got.Format("2006-01-02T15:04:05Z07:00") != "2026-10-01T00:00:00Z" {
		t.Fatalf("explicit fixture retention: %v %v", got, err)
	}
}
