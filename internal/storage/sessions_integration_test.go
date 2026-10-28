//go:build integration

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIntegrationPersistentSessionLifecycleAndExpiryBoundaries(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 123456789, time.FixedZone("test", 3600))
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{
		Login: "SessionOwner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "sessionowner")
	if err != nil || !identity.Found || identity.UserID != owner.State.AdministratorID || identity.PasswordVerifier != testPasswordVerifier {
		t.Fatalf("identity found=%t id=%q verifier-present=%t error=%v", identity.Found, identity.UserID, identity.PasswordVerifier != "", err)
	}
	unknown, err := ReadLocalIdentity(t.Context(), databaseURL, "missing")
	if err != nil || unknown.Found || unknown.PasswordVerifier != "" {
		t.Fatalf("unknown identity found=%t error=%v", unknown.Found, err)
	}
	var verifier [32]byte
	verifier[0] = 1
	issued, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: identity.UserID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "issue-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: identity.UserID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "duplicate-session",
	}); err == nil {
		t.Fatal("duplicate session verifier issuance succeeded")
	}
	wantNow := now.UTC().Truncate(time.Microsecond)
	if issued.SessionID == "" || issued.UserID != identity.UserID || issued.Login != identity.Login || issued.Role != "administrator" || !issued.Restricted || !issued.CreatedAt.Equal(wantNow) || !issued.LastSeenAt.Equal(wantNow) || !issued.AbsoluteExpiresAt.Equal(wantNow.Add(12*time.Hour)) {
		t.Fatalf("issued session = %+v", issued)
	}
	conn := integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_users (login, role, enabled, must_change_password, created_at, updated_at)
VALUES ('null-verifier', 'editor', false, false, $1, $1)`, now); err != nil {
		t.Fatal("insert disabled null-verifier user failed")
	}
	withoutVerifier, err := ReadLocalIdentity(t.Context(), databaseURL, "NULL-VERIFIER")
	if err != nil || !withoutVerifier.Found || withoutVerifier.Enabled || withoutVerifier.PasswordVerifier != "" || withoutVerifier.Role != "editor" {
		t.Fatalf("null-verifier snapshot found=%t enabled=%t verifier-present=%t role=%q error=%v", withoutVerifier.Found, withoutVerifier.Enabled, withoutVerifier.PasswordVerifier != "", withoutVerifier.Role, err)
	}
	var savedVerifier []byte
	if err := conn.QueryRow(t.Context(), "SELECT verifier FROM public.almanac_sessions WHERE id = $1", issued.SessionID).Scan(&savedVerifier); err != nil {
		t.Fatal("read persisted verifier failed")
	}
	if len(savedVerifier) != len(verifier) || string(savedVerifier) != string(verifier[:]) {
		t.Fatal("session verifier was not persisted exactly")
	}
	var unrelatedVerifier [32]byte
	unrelatedVerifier[0] = 11
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: identity.UserID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: unrelatedVerifier,
		Now: now, RequestID: "issue-unrelated-session",
	}); err != nil {
		t.Fatal("issue unrelated session failed")
	}
	var unknownVerifier [32]byte
	unknownVerifier[0] = 255
	if _, err := AuthenticateSession(t.Context(), databaseURL, unknownVerifier, wantNow); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unknown verifier authentication = %v; want invalid session", err)
	}
	if revoked, err := RevokeSession(t.Context(), databaseURL, unknownVerifier, wantNow, "unknown-revoke"); err != nil || revoked {
		t.Fatalf("unknown verifier revoke = %t, %v; want false", revoked, err)
	}
	if _, err := AuthenticateSession(t.Context(), databaseURL, verifier, wantNow.Add(-time.Microsecond)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("authentication before CreatedAt = %v; want invalid session", err)
	}
	if revoked, err := RevokeSession(t.Context(), databaseURL, verifier, wantNow.Add(-time.Microsecond), "before-created-revoke"); err != nil || revoked {
		t.Fatalf("revoke before CreatedAt = %t, %v; want false", revoked, err)
	}
	var initialLastSeen time.Time
	var initialSessionCount, initialAuditCount int
	if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", verifier[:]).Scan(&initialLastSeen); err != nil {
		t.Fatal("read untouched session timestamp failed")
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions").Scan(&initialSessionCount); err != nil {
		t.Fatal("count sessions after no-op operations failed")
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events").Scan(&initialAuditCount); err != nil {
		t.Fatal("count audit events after no-op operations failed")
	}
	if !initialLastSeen.Equal(wantNow) || initialSessionCount != 2 || initialAuditCount != 3 {
		t.Fatalf("no-op operations changed last_seen=%v sessions=%d audits=%d; want createdAt, 2, 3", initialLastSeen, initialSessionCount, initialAuditCount)
	}
	inspected, err := InspectSession(t.Context(), databaseURL, verifier, wantNow.Add(time.Minute))
	if err != nil || !inspected.LastSeenAt.Equal(wantNow) {
		t.Fatalf("read-only inspection = %+v, %v; last_seen should remain unchanged", inspected, err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", verifier[:]).Scan(&initialLastSeen); err != nil || !initialLastSeen.Equal(wantNow) {
		t.Fatalf("inspection changed persisted last_seen=%v error=%v", initialLastSeen, err)
	}
	beforeIdle := wantNow.Add(30*time.Minute - time.Microsecond)
	touched, err := AuthenticateSession(t.Context(), databaseURL, verifier, beforeIdle)
	if err != nil || !touched.LastSeenAt.Equal(beforeIdle) || !touched.AbsoluteExpiresAt.Equal(wantNow.Add(12*time.Hour)) {
		t.Fatalf("before-idle authentication = %+v, %v", touched, err)
	}
	if _, err := AuthenticateSession(t.Context(), databaseURL, verifier, beforeIdle.Add(30*time.Minute)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("exact idle boundary error = %v, want invalid session", err)
	}
	if revoked, err := RevokeSession(t.Context(), databaseURL, verifier, beforeIdle.Add(30*time.Minute), "expired-revoke"); err != nil || revoked {
		t.Fatalf("revoke at idle expiry = %t, %v; want false", revoked, err)
	}
	if revoked, err := RevokeSession(t.Context(), databaseURL, verifier, beforeIdle, "revoke-session"); err != nil || !revoked {
		t.Fatalf("revoke = %t, %v; want true", revoked, err)
	}
	if revoked, err := RevokeSession(t.Context(), databaseURL, verifier, beforeIdle, "revoke-again"); err != nil || revoked {
		t.Fatalf("repeat revoke = %t, %v; want false", revoked, err)
	}
	if _, err := AuthenticateSession(t.Context(), databaseURL, verifier, beforeIdle); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("authentication after successful revoke = %v; want invalid session", err)
	}
	other, err := AuthenticateSession(t.Context(), databaseURL, unrelatedVerifier, wantNow.Add(time.Minute))
	if err != nil || other.UserID != identity.UserID {
		t.Fatalf("unrelated session after target revoke = %+v, %v", other, err)
	}
	var sessionCount, auditCount int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions").Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 || auditCount != 4 {
		t.Fatalf("after lifecycle session count=%d audit count=%d; want 1 and 4", sessionCount, auditCount)
	}
	var issuedAudits, revokedAudits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FILTER (WHERE action='identity.session.issued'), count(*) FILTER (WHERE action='identity.session.revoked') FROM public.almanac_audit_events").Scan(&issuedAudits, &revokedAudits); err != nil || issuedAudits != 2 || revokedAudits != 1 {
		t.Fatalf("session audit counts issue=%d revoke=%d error=%v", issuedAudits, revokedAudits, err)
	}
}

func TestIntegrationInspectSessionDoesNotExtendIdleExpiry(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 123456789, time.FixedZone("test", 3600))
	_, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{
		Login: "InspectOwner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap-inspect",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "inspectowner")
	if err != nil || !identity.Found {
		t.Fatalf("read inspect identity found=%t error=%v", identity.Found, err)
	}
	wantNow := now.UTC().Truncate(time.Microsecond)
	var verifier [32]byte
	verifier[0] = 44
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: identity.UserID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "issue-inspect-expiry",
	}); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	var beforeInspect time.Time
	if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", verifier[:]).Scan(&beforeInspect); err != nil {
		t.Fatal("read persisted last_seen_at failed")
	}
	if access, err := InspectSession(t.Context(), databaseURL, verifier, wantNow.Add(29*time.Minute)); err != nil || !access.LastSeenAt.Equal(wantNow) {
		t.Fatalf("read-only inspection near idle expiry = %+v, %v", access, err)
	}
	if _, err := InspectSession(t.Context(), databaseURL, verifier, wantNow.Add(30*time.Minute)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("inspection extended idle expiry: %v", err)
	}
	var afterInspect time.Time
	if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", verifier[:]).Scan(&afterInspect); err != nil {
		t.Fatal("read persisted last_seen_at after inspection failed")
	}
	if !beforeInspect.Equal(wantNow) || !afterInspect.Equal(beforeInspect) {
		t.Fatalf("Inspect changed persisted last_seen_at: before=%v after=%v", beforeInspect, afterInspect)
	}
}

func TestIntegrationSessionUsesCurrentRoleAndNeverUpgradesRestriction(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "role-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "role-owner")
	if err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 3
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: identity.UserID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "issue",
	}); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET role='viewer', must_change_password=false WHERE id=$1", owner.State.AdministratorID); err != nil {
		t.Fatal("complete onboarding and change current role failed")
	}
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET onboarding_completed_at=$1 WHERE singleton", now); err != nil {
		t.Fatal("complete installation onboarding failed")
	}
	access, err := AuthenticateSession(t.Context(), databaseURL, verifier, now.Add(time.Minute))
	if err != nil || access.Role != "viewer" || !access.Restricted {
		t.Fatalf("authenticated access role=%q restricted=%t error=%v; want viewer and still restricted", access.Role, access.Restricted, err)
	}
}

func TestIntegrationSessionAbsoluteExpiryIsNotExtendedByActivity(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "expiry-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "expiry-owner")
	if err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 4
	issued, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := AuthenticateSession(t.Context(), databaseURL, verifier, now.Add(29*time.Minute))
	if err != nil || !first.LastSeenAt.Equal(now.Add(29*time.Minute)) {
		t.Fatalf("first touch = %+v, %v", first, err)
	}
	backwards, err := AuthenticateSession(t.Context(), databaseURL, verifier, now.Add(time.Minute))
	if err != nil || !backwards.LastSeenAt.Equal(first.LastSeenAt) {
		t.Fatalf("backwards touch = %+v, %v; want last_seen unchanged", backwards, err)
	}
	for n := 2; n <= 24; n++ {
		if _, err := AuthenticateSession(t.Context(), databaseURL, verifier, now.Add(time.Duration(n)*29*time.Minute)); err != nil {
			t.Fatalf("activity touch %d failed: %v", n, err)
		}
	}
	boundary := issued.AbsoluteExpiresAt
	if _, err := AuthenticateSession(t.Context(), databaseURL, verifier, boundary.Add(-time.Microsecond)); err != nil {
		t.Fatalf("authentication just before absolute expiry failed: %v", err)
	}
	if _, err := AuthenticateSession(t.Context(), databaseURL, verifier, boundary); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("exact absolute boundary error = %v, want invalid session", err)
	}
}

func TestIntegrationSessionsInvalidateOnDisableRevisionAndEpochChanges(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "fence-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	for n, mutation := range []string{
		"UPDATE public.almanac_users SET enabled=false WHERE id=$1",
		"UPDATE public.almanac_users SET credential_revision=credential_revision+1 WHERE id=$1",
		"UPDATE public.almanac_installation SET credential_epoch=gen_random_uuid() WHERE singleton",
	} {
		identity, err := ReadLocalIdentity(t.Context(), databaseURL, "fence-owner")
		if err != nil || !identity.Found {
			t.Fatalf("read identity before mutation %d = found %t, error %v", n, identity.Found, err)
		}
		var verifier [32]byte
		verifier[0] = byte(10 + n)
		if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
			UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
			ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
			Now: now, RequestID: "issue-fenced",
		}); err != nil {
			t.Fatalf("issue before mutation %d: %v", n, err)
		}
		var mutationErr error
		if n == 2 {
			_, mutationErr = conn.Exec(t.Context(), mutation)
		} else {
			_, mutationErr = conn.Exec(t.Context(), mutation, owner.State.AdministratorID)
		}
		if mutationErr != nil {
			t.Fatalf("apply authority mutation %d: %v", n, mutationErr)
		}
		if _, err := AuthenticateSession(t.Context(), databaseURL, verifier, now.Add(time.Minute)); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("authentication after mutation %d = %v; want invalid session", n, err)
		}
		if revoked, err := RevokeSession(t.Context(), databaseURL, verifier, now.Add(time.Minute), "revoke-stale"); err != nil || revoked {
			t.Fatalf("revoke after authority mutation %d = %t, %v; want false", n, revoked, err)
		}
		var savedLastSeen time.Time
		var sessionRows, revocationAudits int
		if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", verifier[:]).Scan(&savedLastSeen); err != nil {
			t.Fatalf("read stale session after logout no-op %d failed: %v", n, err)
		}
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions WHERE verifier=$1", verifier[:]).Scan(&sessionRows); err != nil {
			t.Fatalf("count stale session after logout no-op %d failed: %v", n, err)
		}
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action='identity.session.revoked'").Scan(&revocationAudits); err != nil {
			t.Fatalf("count stale revocation audits after no-op %d failed: %v", n, err)
		}
		if !savedLastSeen.Equal(now) || sessionRows != 1 || revocationAudits != 0 {
			t.Fatalf("stale logout %d changed last_seen=%v sessionRows=%d revocationAudits=%d", n, savedLastSeen, sessionRows, revocationAudits)
		}
		var rejectedVerifier [32]byte
		rejectedVerifier[0] = byte(90 + n)
		if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
			UserID: identity.UserID, ExpectedCredentialEpoch: identity.CredentialEpoch,
			ExpectedCredentialRevision: identity.CredentialRevision, Verifier: rejectedVerifier,
			Now: now, RequestID: "issue-stale",
		}); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("issuance after authority mutation %d = %v; want invalid session", n, err)
		}
		if n == 0 {
			if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET enabled=true WHERE id=$1", owner.State.AdministratorID); err != nil {
				t.Fatal("restore enabled user failed")
			}
		}
	}
	current, err := ReadLocalIdentity(t.Context(), databaseURL, "fence-owner")
	if err != nil {
		t.Fatal("read identity before unprovisioned rejection failed")
	}
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET bootstrap_admin_id=NULL, bootstrapped_at=NULL WHERE singleton"); err != nil {
		t.Fatal("clear test provisioning markers failed")
	}
	var verifier [32]byte
	verifier[0] = 99
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: current.UserID, ExpectedCredentialEpoch: current.CredentialEpoch,
		ExpectedCredentialRevision: current.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "issue-unprovisioned",
	}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("issuance before provisioning = %v; want invalid session", err)
	}
	var sessions, issuedAudits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions").Scan(&sessions); err != nil {
		t.Fatal("count sessions after rejected issuance failed")
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action='identity.session.issued'").Scan(&issuedAudits); err != nil {
		t.Fatal("count issuance audits after rejection failed")
	}
	if sessions != 3 || issuedAudits != 3 {
		t.Fatalf("rejected issuance left sessions=%d issuance audits=%d; want 3 each", sessions, issuedAudits)
	}
}

func TestIntegrationSessionOperationsRejectMissingInstallationAuthority(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "missing-installation-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "missing-installation-owner")
	if err != nil {
		t.Fatal(err)
	}
	var existingVerifier [32]byte
	existingVerifier[0] = 31
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: existingVerifier,
		Now: now, RequestID: "issue-existing",
	}); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), "DELETE FROM public.almanac_installation WHERE singleton"); err != nil {
		t.Fatal("remove installation singleton failed")
	}
	var issueVerifier [32]byte
	issueVerifier[0] = 32
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: issueVerifier,
		Now: now.Add(time.Minute), RequestID: "issue-missing-installation",
	}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("issue without installation authority = %v; want ErrInvalidSession", err)
	}
	if _, err := AuthenticateSession(t.Context(), databaseURL, existingVerifier, now.Add(time.Minute)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("authenticate without installation authority = %v; want ErrInvalidSession", err)
	}
	if revoked, err := RevokeSession(t.Context(), databaseURL, existingVerifier, now.Add(time.Minute), "revoke-missing-installation"); err != nil || revoked {
		t.Fatalf("revoke without installation authority = %t, %v; want false,nil", revoked, err)
	}
	var sessionRows, issuedAudits, revokedAudits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions").Scan(&sessionRows); err != nil {
		t.Fatal("count sessions after missing installation operations failed")
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FILTER (WHERE action='identity.session.issued'), count(*) FILTER (WHERE action='identity.session.revoked') FROM public.almanac_audit_events").Scan(&issuedAudits, &revokedAudits); err != nil {
		t.Fatal("count audits after missing installation operations failed")
	}
	if sessionRows != 1 || issuedAudits != 1 || revokedAudits != 0 {
		t.Fatalf("missing installation operations left sessions=%d issued audits=%d revoked audits=%d", sessionRows, issuedAudits, revokedAudits)
	}
}

func TestIntegrationConcurrentSessionTouchesPersistGreatestValidTime(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "touch-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "touch-owner")
	if err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 5
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "issue",
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	ready := make(chan struct{}, 8)
	results := make(chan error, 8)
	for n := range 8 {
		go func() {
			ready <- struct{}{}
			<-start
			_, err := AuthenticateSession(ctx, databaseURL, verifier, now.Add(time.Duration(n+1)*time.Minute))
			results <- err
		}()
	}
	for range 8 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("concurrent touch workers did not become ready: %v", ctx.Err())
		}
	}
	close(start)
	for range 8 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent touch failed: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("concurrent touches did not finish: %v", ctx.Err())
		}
	}
	conn := integrationConnect(t, databaseURL)
	var lastSeen time.Time
	if err := conn.QueryRow(ctx, "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", verifier[:]).Scan(&lastSeen); err != nil {
		t.Fatal("read concurrent last_seen failed")
	}
	if !lastSeen.Equal(now.Add(8 * time.Minute)) {
		t.Fatalf("concurrent last_seen=%v, want greatest time %v", lastSeen, now.Add(8*time.Minute))
	}
}

func TestIntegrationSessionRevocationAuditFailureRollsBackDeletion(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "revoke-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "revoke-owner")
	if err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 6
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "issue",
	}); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_revoke_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='identity.session.revoked' THEN RAISE EXCEPTION 'audit insert blocked'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_revoke_audit BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW EXECUTE FUNCTION public.fail_revoke_audit()`); err != nil {
		t.Fatal("install revoke audit trigger failed")
	}
	if revoked, err := RevokeSession(t.Context(), databaseURL, verifier, now.Add(time.Minute), "revoke"); err == nil || revoked || strings.Contains(err.Error(), databasePassword(databaseURL)) {
		t.Fatalf("revoke with audit failure = %t, %v; want safe failure", revoked, err)
	}
	var sessions, revocationAudits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions WHERE verifier=$1", verifier[:]).Scan(&sessions); err != nil {
		t.Fatal("read session after failed revoke failed")
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action='identity.session.revoked'").Scan(&revocationAudits); err != nil {
		t.Fatal("read revoke audit after failure failed")
	}
	if sessions != 1 || revocationAudits != 0 {
		t.Fatalf("failed revoke left sessions=%d revocation audits=%d; want 1 and 0", sessions, revocationAudits)
	}
	if _, err := conn.Exec(t.Context(), "DROP TRIGGER fail_revoke_audit ON public.almanac_audit_events; DROP FUNCTION public.fail_revoke_audit()"); err != nil {
		t.Fatal("remove revoke audit trigger failed")
	}
	if revoked, err := RevokeSession(t.Context(), databaseURL, verifier, now.Add(time.Minute), "revoke"); err != nil || !revoked {
		t.Fatalf("revoke retry = %t, %v; want true", revoked, err)
	}
}

func TestIntegrationSessionLockCancellationCleansUpAndCanRetry(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "cancel-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "cancel-owner")
	if err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 7
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
		Now: now, RequestID: "issue",
	}); err != nil {
		t.Fatal(err)
	}
	blocker := integrationConnect(t, databaseURL)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal("begin test row lock failed")
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), "SELECT id FROM public.almanac_users WHERE id=$1 FOR UPDATE", owner.State.AdministratorID); err != nil {
		t.Fatal("lock session user failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := AuthenticateSession(ctx, databaseURL, verifier, now.Add(time.Minute))
		result <- err
	}()
	observer := integrationConnect(t, databaseURL)
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		var count int
		if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
WHERE datname=current_database() AND state='active' AND wait_event_type='Lock'
AND query LIKE '%FROM public.almanac_users%'`).Scan(&count); err != nil {
			t.Fatal("observe blocked session operation failed")
		}
		if count > 0 {
			waiting = true
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-t.Context().Done():
			t.Fatalf("test context ended while observing blocked session: %v", t.Context().Err())
		}
	}
	if !waiting {
		t.Fatal("session operation did not reach the blocked user row lock")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("cancelled authentication error = %v, want ErrCancelled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled session operation did not return within bound")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal("release test row lock failed")
	}
	if _, err := AuthenticateSession(t.Context(), databaseURL, verifier, now.Add(time.Minute)); err != nil {
		t.Fatalf("retry after cancelled lock wait failed: %v", err)
	}
}

func TestIntegrationSessionOperationsFailClosedForIncompatibleOrMissingSchema(t *testing.T) {
	t.Run("pending schema", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		migrations, err := LoadMigrations(embeddedMigrations)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:2]); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadLocalIdentity(t.Context(), databaseURL, "owner"); err == nil || strings.Contains(err.Error(), databasePassword(databaseURL)) {
			t.Fatalf("read on pending schema = %v; want safe failure", err)
		}
	})
	t.Run("newer schema", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		if _, err := Upgrade(t.Context(), databaseURL); err != nil {
			t.Fatal(err)
		}
		conn := integrationConnect(t, databaseURL)
		if _, err := conn.Exec(t.Context(), "INSERT INTO public.almanac_schema_migrations (version, filename, checksum) VALUES (18, '0018_future.sql', decode(repeat('ab', 32), 'hex'))"); err != nil {
			t.Fatal("install future schema marker failed")
		}
		closeIntegrationConn(conn)
		if _, err := ReadLocalIdentity(t.Context(), databaseURL, "owner"); err == nil || strings.Contains(err.Error(), databasePassword(databaseURL)) {
			t.Fatalf("read on newer schema = %v; want safe failure", err)
		}
	})
	t.Run("missing schema", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		if _, err := ReadLocalIdentity(t.Context(), databaseURL, "owner"); err == nil || strings.Contains(err.Error(), databasePassword(databaseURL)) {
			t.Fatalf("read on missing schema = %v; want safe failure", err)
		}
	})
	t.Run("missing installation row", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		if _, err := Upgrade(t.Context(), databaseURL); err != nil {
			t.Fatal(err)
		}
		conn := integrationConnect(t, databaseURL)
		if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_users (login, password_verifier, role, enabled, must_change_password, created_at, updated_at)
VALUES ('owner', $1, 'viewer', true, false, now(), now())`, testPasswordVerifier); err != nil {
			t.Fatal("insert test identity failed")
		}
		if _, err := conn.Exec(t.Context(), "DELETE FROM public.almanac_installation WHERE singleton"); err != nil {
			t.Fatal("remove test installation row failed")
		}
		if _, err := ReadLocalIdentity(t.Context(), databaseURL, "owner"); err == nil {
			t.Fatal("identity read treated missing installation state as unknown user")
		}
	})
	t.Run("unavailable database", func(t *testing.T) {
		secretURL := "postgres://session-secret:session-db-secret@127.0.0.1:1/session?sslmode=disable"
		if _, err := ReadLocalIdentity(t.Context(), secretURL, "owner"); err == nil || strings.Contains(err.Error(), "session-db-secret") {
			t.Fatalf("read on unavailable database = %v; want safe failure", err)
		}
	})
}

func TestIntegrationSchemaTwoUpgradePreservesOwnerAndAudit(t *testing.T) {
	databaseURL := integrationDatabase(t)
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:2]); err != nil || result.DatabaseSchemaVersion != 2 || result.Applied != 2 {
		t.Fatalf("schema2 upgrade = %+v, %v", result, err)
	}
	conn := integrationConnect(t, databaseURL)
	var userID, epoch, installationID string
	if err := conn.QueryRow(t.Context(), "SELECT credential_epoch, installation_id FROM public.almanac_installation WHERE singleton").Scan(&epoch, &installationID); err != nil {
		t.Fatal("read schema2 installation failed")
	}
	if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_users (login, password_verifier, role, enabled, must_change_password, credential_revision, created_at, updated_at)
VALUES ('owner-v2', $1, 'administrator', true, true, 1, $2, $2) RETURNING id`, testPasswordVerifier, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)).Scan(&userID); err != nil {
		t.Fatal("create schema2 owner failed")
	}
	provisionedAt := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET bootstrap_admin_id=$1, bootstrapped_at=$2 WHERE singleton", userID, provisionedAt); err != nil {
		t.Fatal("mark schema2 owner provisioned failed")
	}
	if _, err := conn.Exec(t.Context(), "INSERT INTO public.almanac_audit_events (actor, subject_id, action, outcome, occurred_at, request_id) VALUES ('system', $1, 'identity.bootstrap.provision', 'success', $2, 'v2-audit')", userID, provisionedAt); err != nil {
		t.Fatal("insert schema2 owner audit failed")
	}
	closeIntegrationConn(conn)
	result, err := Upgrade(t.Context(), databaseURL)
	if err != nil || result.Applied != 15 || result.DatabaseSchemaVersion != 17 {
		t.Fatalf("schema2 to 13 upgrade = %+v, %v", result, err)
	}
	conn = integrationConnect(t, databaseURL)
	var savedVerifier, savedEpoch, savedInstallation string
	var auditCount int
	if err := conn.QueryRow(t.Context(), "SELECT password_verifier FROM public.almanac_users WHERE id=$1", userID).Scan(&savedVerifier); err != nil {
		t.Fatal("read preserved owner failed")
	}
	if err := conn.QueryRow(t.Context(), "SELECT credential_epoch, installation_id FROM public.almanac_installation WHERE singleton").Scan(&savedEpoch, &savedInstallation); err != nil {
		t.Fatal("read preserved installation failed")
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id='v2-audit'").Scan(&auditCount); err != nil {
		t.Fatal("read preserved audit failed")
	}
	if savedVerifier != testPasswordVerifier || savedEpoch != epoch || savedInstallation != installationID || auditCount != 1 {
		t.Fatalf("schema2 data changed verifier=%t epoch=%t installation=%t auditCount=%d", savedVerifier == testPasswordVerifier, savedEpoch == epoch, savedInstallation == installationID, auditCount)
	}
}

func TestIntegrationSessionIssueRejectsStaleEpochAndAuditFailureRollsBack(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "session-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "session-owner")
	if err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 2
	input := IssueSessionInput{UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch, ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier, Now: now, RequestID: "issue"}
	conn := integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_session_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'identity.session.%' THEN RAISE EXCEPTION 'audit insert blocked'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_session_audit BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW EXECUTE FUNCTION public.fail_session_audit()`); err != nil {
		t.Fatal("install session audit trigger failed")
	}
	if _, err := IssueSession(t.Context(), databaseURL, input); err == nil || strings.Contains(err.Error(), testPasswordVerifier) || strings.Contains(err.Error(), databasePassword(databaseURL)) {
		t.Fatalf("issue with audit failure = %v; want safe storage error", err)
	}
	var sessions, audits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action LIKE 'identity.session.%'").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || audits != 0 {
		t.Fatalf("audit failure left sessions=%d session audits=%d", sessions, audits)
	}
	if _, err := conn.Exec(t.Context(), "DROP TRIGGER fail_session_audit ON public.almanac_audit_events; DROP FUNCTION public.fail_session_audit()"); err != nil {
		t.Fatal("remove session audit trigger failed")
	}
	if _, err := IssueSession(t.Context(), databaseURL, input); err != nil {
		t.Fatalf("valid issuance retry after audit rollback failed: %v", err)
	}
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET credential_epoch = gen_random_uuid() WHERE singleton"); err != nil {
		t.Fatal("change installation epoch failed")
	}
	if _, err := IssueSession(t.Context(), databaseURL, input); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("stale epoch issue error = %v; want invalid session", err)
	}
}
