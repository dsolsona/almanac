//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"
)

func TestIntegrationSessionPolicyAppliesAtIssuanceAndRotation(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	now := f.base.Add(2 * time.Second)
	clock := func() time.Time { return now }
	policy, err := GetSessionPolicy(t.Context(), f.databaseURL, f.verifier, clock)
	if err != nil || policy.IdleMinutes != 480 || policy.Version != 1 {
		t.Fatalf("initial policy = %+v, %v", policy, err)
	}
	policy, err = UpdateSessionPolicy(t.Context(), f.databaseURL, f.verifier, clock, 90, policy.Version, "update-session-policy")
	if err != nil || policy.IdleMinutes != 90 || policy.Version != 2 {
		t.Fatalf("updated policy = %+v, %v", policy, err)
	}
	if _, err := UpdateSessionPolicy(t.Context(), f.databaseURL, f.verifier, clock, 60, 1, "stale-session-policy"); !errors.Is(err, ErrUserConflict) {
		t.Fatalf("stale update = %v", err)
	}
	if _, err := InspectSession(t.Context(), f.databaseURL, f.verifier, now.Add(90*time.Minute)); err != nil {
		t.Fatalf("existing session lost its original timeout: %v", err)
	}
	issued := [32]byte{120}
	if _, err := IssueSession(t.Context(), f.databaseURL, IssueSessionInput{
		UserID: f.userID, ExpectedCredentialEpoch: f.epoch, ExpectedCredentialRevision: f.revision,
		Verifier: issued, Now: now, RequestID: "issue-with-policy",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSession(t.Context(), f.databaseURL, issued, now.Add(90*time.Minute-time.Microsecond)); err != nil {
		t.Fatalf("new session before idle expiry: %v", err)
	}
	if _, err := InspectSession(t.Context(), f.databaseURL, issued, now.Add(90*time.Minute)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("new session at idle expiry: %v", err)
	}

	policy, err = UpdateSessionPolicy(t.Context(), f.databaseURL, f.verifier, clock, 120, policy.Version, "extend-session-policy")
	if err != nil || policy.IdleMinutes != 120 || policy.Version != 3 {
		t.Fatalf("extended policy = %+v, %v", policy, err)
	}
	if _, err := InspectSession(t.Context(), f.databaseURL, issued, now.Add(90*time.Minute)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session after policy extension: %v", err)
	}
	rotated := [32]byte{121}
	if _, err := RotateLocalPassword(t.Context(), f.databaseURL, RotatePasswordInput{
		CurrentSessionVerifier: f.verifier, ExpectedCredentialRevision: f.revision, ExpectedCredentialEpoch: f.epoch,
		NewPasswordVerifier: testPasswordVerifier, NewSessionVerifier: rotated, Now: now, RequestID: "rotate-with-policy",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSession(t.Context(), f.databaseURL, rotated, now.Add(120*time.Minute-time.Microsecond)); err != nil {
		t.Fatalf("rotated session before idle expiry: %v", err)
	}
	if _, err := InspectSession(t.Context(), f.databaseURL, rotated, now.Add(120*time.Minute)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("rotated session at idle expiry: %v", err)
	}
	conn := integrationConnect(t, f.databaseURL)
	var audits int
	err = conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action='identity.session_policy.updated' AND actor=$1", f.userID).Scan(&audits)
	if err != nil || audits != 2 {
		t.Fatalf("policy audit count = %d, %v", audits, err)
	}
}
