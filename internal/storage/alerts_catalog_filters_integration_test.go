//go:build integration

package storage

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
)

func TestIntegrationAlertCatalogEvidenceAndDuration(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	a, b, c := f.policy(t, "A"), f.policy(t, "B"), f.policy(t, "C")
	claims, err := f.runtime.claimAlertWork(t.Context(), f.at, f.at, time.Minute, 3)
	if err != nil || len(claims) != 3 {
		t.Fatalf("initial claims: %d %v", len(claims), err)
	}
	var openedA alertCommitResult
	for _, claim := range claims {
		opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock)
		if err != nil {
			t.Fatal(err)
		}
		if claim.PolicyID == a {
			openedA = opened
		}
	}
	f.now = f.at.Add(time.Second)
	if _, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, AcknowledgeAlertInput{AlertCommandInput: alertManagementCommand(f, "filters-ack"), ID: openedA.Occurrence.ID, ExpectedVersion: openedA.Version}); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Minute)
	claims, err = f.runtime.claimAlertWork(t.Context(), f.now, f.now, time.Minute, 3)
	if err != nil || len(claims) != 3 {
		t.Fatalf("next claims: %d %v", len(claims), err)
	}
	for _, claim := range claims {
		switch claim.PolicyID {
		case a:
			err = f.runtime.recordAlertRepeatedCutoff(t.Context(), claim, f.at, f.clock)
		case b:
			_, err = f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Missing, false), f.limits, f.clock)
		case c:
			err = f.runtime.deferAlertWork(t.Context(), claim, f.clock, 2*time.Second, "aggregate_pending")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	f.now = f.at.Add(15 * time.Minute)
	input := alertCatalogInput(f)
	input.GroupBy, input.Limit = "slo", 1
	for _, tc := range []struct {
		evidence      string
		total, firing int64
		names         map[string]bool
	}{
		{"", 3, 2, map[string]bool{"A": true, "B": true, "C": true}},
		{"matching", 2, 1, map[string]bool{"A": true, "C": true}},
		{"unavailable", 1, 1, map[string]bool{"B": true}},
		{"waiting", 1, 1, map[string]bool{"C": true}},
	} {
		t.Run("evidence="+tc.evidence, func(t *testing.T) {
			current := input
			current.Filter.Evidence = tc.evidence
			seen := map[string]bool{}
			for range 4 {
				page, err := ListAlerts(t.Context(), f.f.databaseURL, current)
				if err != nil || page.Total != tc.total || len(page.Items) != 1 || len(page.Groups) != 1 || page.Groups[0].Total != tc.total || page.Groups[0].Firing != tc.firing {
					t.Fatalf("filtered page: total=%d groups=%+v items=%d error=%v", page.Total, page.Groups, len(page.Items), err)
				}
				name := page.Items[0].PolicyName
				if !tc.names[name] || seen[name] {
					t.Fatalf("unexpected/duplicate policy %q", name)
				}
				seen[name] = true
				if page.NextCursor == "" {
					break
				}
				current.Cursor = page.NextCursor
			}
			if len(seen) != len(tc.names) {
				t.Fatalf("missing policies: %v", seen)
			}
		})
	}
	t.Run("duration boundary and moving clock", func(t *testing.T) {
		current := input
		current.Filter.MinimumDurationSeconds = 900
		page, err := ListAlerts(t.Context(), f.f.databaseURL, current)
		if err != nil || page.Total != 3 || page.NextCursor == "" {
			t.Fatalf("inclusive age boundary: %+v %v", page, err)
		}
		current.Cursor = page.NextCursor
		f.now = f.now.Add(time.Second)
		next, err := ListAlerts(t.Context(), f.f.databaseURL, current)
		if err != nil || next.Total != 3 || len(next.Items) != 1 || next.Items[0].Alert.Occurrence.ID == page.Items[0].Alert.Occurrence.ID {
			t.Fatalf("clock invalidated cursor: %+v %v", next, err)
		}
		for _, minimum := range []int64{902, math.MaxInt64} {
			current.Cursor = ""
			current.Filter.MinimumDurationSeconds = minimum
			empty, err := ListAlerts(t.Context(), f.f.databaseURL, current)
			if err != nil || empty.Total != 0 || len(empty.Items) != 0 || len(empty.Groups) != 0 {
				t.Fatalf("duration %d ignored/overflowed: %+v %v", minimum, empty, err)
			}
		}
		for _, changed := range []AlertFilter{{Scope: "active", MinimumDurationSeconds: 901}, {Scope: "active", MinimumDurationSeconds: 900, Evidence: "matching"}} {
			current.Filter, current.Cursor = changed, page.NextCursor
			if _, err := ListAlerts(t.Context(), f.f.databaseURL, current); !errors.Is(err, ErrAlertInvalid) {
				t.Fatalf("changed filter reused cursor: %v", err)
			}
		}
	})
	t.Run("invalid filters", func(t *testing.T) {
		for _, filter := range []AlertFilter{{Scope: "active", Evidence: "partial"}, {Scope: "active", MinimumDurationSeconds: -1}} {
			current := input
			current.Filter = filter
			if _, err := ListAlerts(t.Context(), f.f.databaseURL, current); !errors.Is(err, ErrAlertInvalid) {
				t.Fatalf("invalid filter accepted: %+v %v", filter, err)
			}
		}
	})
	t.Run("exhausted work is not waiting", func(t *testing.T) {
		if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_alert_cursors SET exhausted=true,failure_attempts=1,failure_since=$2,last_failure_at=$2 WHERE policy_id=$1`, c, f.now); err != nil {
			t.Fatal(err)
		}
		current := input
		current.Filter.Evidence = "waiting"
		page, err := ListAlerts(t.Context(), f.f.databaseURL, current)
		if err != nil || page.Total != 0 || len(page.Items) != 0 {
			t.Fatalf("exhausted cursor treated as waiting: %+v %v", page, err)
		}
	})
	t.Run("terminal effective duration", func(t *testing.T) {
		if _, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "filters-disable"), ID: a, ExpectedVersion: 1, EffectiveAt: f.at.Add(2 * time.Minute), Reason: "Ended earlier"}); err != nil {
			t.Fatal(err)
		}
		current := input
		current.Filter.Scope = "history"
		for _, tc := range []struct{ seconds, total int64 }{{900, 0}, {120, 1}, {121, 0}} {
			current.Filter.MinimumDurationSeconds = tc.seconds
			page, err := ListAlerts(t.Context(), f.f.databaseURL, current)
			if err != nil || page.Total != tc.total || int64(len(page.Items)) != tc.total {
				t.Fatalf("terminal duration %d: %+v %v", tc.seconds, page, err)
			}
			if tc.total == 1 && (len(page.Groups) != 1 || page.Groups[0].Firing != 0) {
				t.Fatalf("terminal firing count: %+v", page.Groups)
			}
		}
		current.Filter.MinimumDurationSeconds = 0
		current.Filter.Evidence = "waiting"
		page, err := ListAlerts(t.Context(), f.f.databaseURL, current)
		if err != nil || page.Total != 0 {
			t.Fatalf("terminal cancellation is not waiting: %+v %v", page, err)
		}
	})
}
