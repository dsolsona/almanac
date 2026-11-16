package storage

import (
	"testing"
	"time"
)

func TestMaintenanceComplianceUsesEligibleDenominator(t *testing.T) {
	t.Parallel()
	_, input, want := retainedMaintenanceFixture(t)
	for _, kind := range []string{"partial_exclusion", "fully_excluded", "shortened"} {
		t.Run(kind, func(t *testing.T) {
			value := want
			// 49,500 / 50,000 is 99%, below this fixture's 99.9% target.
			status, reason := "violated", "target_violated"
			if kind == "fully_excluded" {
				value.State = "excluded"
				value.Performance = "unknown"
				value.Expected = 0
				value.Excluded = 60
				status, reason = "unknown", "excluded"
			}
			if kind == "shortened" {
				value.EligibleStart = value.RequestedStart.Add(time.Minute)
				status, reason = "unknown", "shortened_window"
			}
			got, why := dailyComplianceStatus(value, value.End, input.ValidFrom.Add(-24*time.Hour), input.Duration, input.Cadence)
			if string(got) != status || why != reason {
				t.Fatalf("%s: %s/%s want %s/%s", kind, got, why, status, reason)
			}
		})
	}
}

func TestMaintenanceCompliancePreservesUnavailableReason(t *testing.T) {
	t.Parallel()
	_, input, _ := retainedMaintenanceFixture(t)
	for _, reason := range []string{"maintenance_history_unavailable", "maintenance_preparation_failed"} {
		value, err := unavailableMaintenanceWindow(input, reason)
		if err != nil {
			t.Fatal(err)
		}
		status, why := dailyComplianceStatus(value, input.At, input.ValidFrom, input.Duration, input.Cadence)
		if status != "unknown" || why != reason {
			t.Fatalf("stopped preparation lost its reason: %s/%s", status, why)
		}
	}
}
