//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"
)

func TestIntegrationSloEvaluationMaintenanceGuard(t *testing.T) {
	t.Parallel()
	for _, full := range []bool{false, true} {
		name := "partial"
		if full {
			name = "full"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			in := EvaluateSloInput{Proof: f.proof, ID: f.ids[0], At: &f.at, Now: f.clock}
			before, err := EvaluateSlo(t.Context(), f.f.databaseURL, in)
			if err != nil || before.Performance.Good == nil || *before.Performance.Good != "59400" || before.Performance.Total == nil || *before.Performance.Total != "60000" || before.Quality.ExpectedIntervals != 60 || before.Quality.ObservedIntervals != 60 {
				t.Fatalf("revision-zero evaluation changed: %+v %v", before, err)
			}
			change := f.input("legacy-" + name)
			change.Start, change.End = f.at.Add(-20*time.Minute), f.at.Add(-10*time.Minute)
			if full {
				change.Start, change.End = f.at.Add(-time.Hour), f.at
			}
			if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, change); err != nil {
				t.Fatal(err)
			}
			got, err := EvaluateSlo(t.Context(), f.f.databaseURL, in)
			if !errors.Is(err, ErrSloEvaluationMaintenanceUnsupported) || got.Performance.Good != nil || got.Performance.Total != nil || len(got.Timeline.Items) != 0 {
				t.Fatalf("nonzero maintenance returned raw evaluation success: performance=%+v timeline=%d err=%v", got.Performance, len(got.Timeline.Items), err)
			}
		})
	}
}

func TestIntegrationSloEvaluationMaintenanceRace(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	calls := 0
	clock := func() time.Time {
		calls++
		if calls == 2 {
			// The evaluator has already read the R0 definition in its RR snapshot.
			// Commit an actual authority change before it publishes that result.
			change := f.input("legacy-racing-maintenance")
			change.Start, change.End = f.at.Add(-time.Hour), f.at
			if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, change); err != nil {
				t.Fatal(err)
			}
		}
		return f.now
	}
	got, err := EvaluateSlo(t.Context(), f.f.databaseURL, EvaluateSloInput{Proof: f.proof, ID: f.ids[0], At: &f.at, Now: clock})
	if calls < 3 || !errors.Is(err, ErrSloEvaluationMaintenanceUnsupported) || got.Performance.Good != nil || got.Performance.Total != nil || len(got.Timeline.Items) != 0 {
		t.Fatalf("maintenance committed during read escaped the final fence: calls=%d performance=%+v timeline=%d err=%v", calls, got.Performance, len(got.Timeline.Items), err)
	}
}

func TestIntegrationSloEvaluationMaintenanceAuthorityPriority(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("legacy-auth-priority")); err != nil {
		t.Fatal(err)
	}
	in := EvaluateSloInput{Proof: f.proof, ID: f.ids[0], At: &f.at, Now: f.clock}
	for _, role := range []string{"administrator", "editor", "viewer"} {
		setIntegrationUserRole(t, f.f, role)
		if _, err := EvaluateSlo(t.Context(), f.f.databaseURL, in); !errors.Is(err, ErrSloEvaluationMaintenanceUnsupported) {
			t.Fatalf("authorized %s did not receive the legacy limitation: %v", role, err)
		}
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sessions SET restricted=true WHERE verifier=$1`, f.proof.SessionVerifier[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateSlo(t.Context(), f.f.databaseURL, in); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("restricted session lost authority error priority: %v", err)
	}
	if _, err := RevokeSession(t.Context(), f.f.databaseURL, f.proof.SessionVerifier, f.now, "legacy-revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateSlo(t.Context(), f.f.databaseURL, in); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked session lost authority error priority: %v", err)
	}
}
