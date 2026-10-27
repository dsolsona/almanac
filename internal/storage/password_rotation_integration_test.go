//go:build integration

package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const rotationTestPasswordVerifier2 = "$argon2id$v=19$m=19456,t=2,p=1$MTIzNDU2Nzg5YWJjZGVmZw$MDEyMzQ1Njc4OWFiY2RlZzAxMjM0NTY3ODlhYmNkZWY"

func TestIntegrationRotatePasswordCompletesOnboardingAndInvalidatesSessions(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 123456789, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{
		Login: "rotate-owner", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "rotate-owner")
	if err != nil {
		t.Fatal(err)
	}
	var current, restricted [32]byte
	current[0], restricted[0] = 41, 42
	for _, verifier := range [][32]byte{current, restricted} {
		if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
			UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
			ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier,
			Now: now, RequestID: "issue",
		}); err != nil {
			t.Fatal(err)
		}
	}
	conn := integrationConnect(t, databaseURL)
	var otherUserID string
	if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_users
(login, password_verifier, role, enabled, must_change_password, credential_revision, created_at, updated_at)
VALUES ('rotation-collision-user', $1, 'editor', true, false, 1, $2, $2) RETURNING id::text`, testPasswordVerifier, now).Scan(&otherUserID); err != nil {
		t.Fatal("create second user for collision isolation failed")
	}
	var otherSession [32]byte
	otherSession[0] = 47
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: otherUserID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: 1, Verifier: otherSession, Now: now, RequestID: "issue-other",
	}); err != nil {
		t.Fatal(err)
	}
	otherBefore := snapshotRotationUser(t, databaseURL, otherUserID)
	for label, collision := range map[string][32]byte{"same-user session": restricted, "other-user session": otherSession} {
		before := snapshotRotationState(t, conn, owner.State.AdministratorID)
		if _, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
			CurrentSessionVerifier: current, ExpectedCredentialRevision: identity.CredentialRevision,
			ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
			NewSessionVerifier: collision, Now: now.Add(time.Second), RequestID: "collision",
		}); !errors.Is(err, ErrInvalidSessionInput) {
			t.Fatalf("%s collision error = %v; want invalid input", label, err)
		}
		if after := snapshotRotationState(t, conn, owner.State.AdministratorID); !reflect.DeepEqual(after, before) {
			t.Fatalf("%s collision changed state fields: %v", label, changedRotationSnapshotFields(before, after))
		}
	}
	installationBefore := readRotationInstallation(t, databaseURL)
	var next [32]byte
	next[0] = 43
	rotated, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
		CurrentSessionVerifier: current, ExpectedCredentialRevision: identity.CredentialRevision,
		ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
		NewSessionVerifier: next, Now: now.Add(time.Second), RequestID: "rotate-first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if otherAfterFirst := snapshotRotationUser(t, databaseURL, otherUserID); otherAfterFirst != otherBefore {
		t.Fatalf("initial rotation changed another user's state fields: %v", changedRotationUserFields(otherBefore, otherAfterFirst))
	}
	if rotated.Restricted || rotated.CredentialRevision != identity.CredentialRevision+1 ||
		!rotated.CreatedAt.Equal(now.Add(time.Second).Truncate(time.Microsecond)) ||
		!rotated.AbsoluteExpiresAt.Equal(rotated.CreatedAt.Add(12*time.Hour)) {
		t.Fatalf("rotated session = %+v", rotated)
	}
	for _, old := range [][32]byte{current, restricted} {
		if _, err := AuthenticateSession(t.Context(), databaseURL, old, now.Add(2*time.Second)); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("old session authenticated: %v", err)
		}
	}
	if _, err := AuthenticateSession(t.Context(), databaseURL, otherSession, now.Add(2*time.Second)); err != nil {
		t.Fatalf("other user's session was not preserved: %v", err)
	}
	otherBeforeSecond := snapshotRotationUser(t, databaseURL, otherUserID)
	if _, err := AuthenticateSession(t.Context(), databaseURL, next, now.Add(2*time.Second)); err != nil {
		t.Fatalf("new session is unusable: %v", err)
	}
	after := readRotationInstallation(t, databaseURL)
	if after.id != installationBefore.id || after.epoch != installationBefore.epoch || !after.onboardingAt.Valid || !after.onboardingAt.Time.Equal(rotated.CreatedAt) {
		t.Fatalf("installation identity/completion changed unexpectedly: before=%+v after=%+v", installationBefore, after)
	}
	closeIntegrationConn(conn)
	conn = integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var verifier string
	var revision int64
	var mustChange bool
	if err := conn.QueryRow(t.Context(), "SELECT password_verifier, credential_revision, must_change_password FROM public.almanac_users WHERE id=$1", owner.State.AdministratorID).Scan(&verifier, &revision, &mustChange); err != nil {
		t.Fatal(err)
	}
	if verifier != rotationTestPasswordVerifier2 || revision != identity.CredentialRevision+1 || mustChange {
		t.Fatalf("stored rotation state verifier=%t revision=%d must_change=%t", verifier == rotationTestPasswordVerifier2, revision, mustChange)
	}
	var audits int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_audit_events
WHERE subject_id=$1 AND request_id='rotate-first' AND action IN
('identity.password.changed','identity.session.issued','identity.onboarding.completed')`, owner.State.AdministratorID).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("first rotation audit count = %d, error=%v; want 3", audits, err)
	}
	assertRotationAudits(t, conn, owner.State.AdministratorID, "rotate-first", rotated.CreatedAt,
		passwordChangedAction, sessionIssuedAction, onboardingCompletedAction)
	sameVerifierInput := RotatePasswordInput{
		CurrentSessionVerifier: next, ExpectedCredentialRevision: rotated.CredentialRevision,
		ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
		NewSessionVerifier: [32]byte{44}, Now: now.Add(2 * time.Second), RequestID: "same-password",
	}
	beforeSameVerifier := snapshotRotationState(t, conn, owner.State.AdministratorID)
	if _, err := RotateLocalPassword(t.Context(), databaseURL, sameVerifierInput); !errors.Is(err, ErrInvalidSessionInput) {
		t.Fatalf("same encoded verifier error = %v; want invalid session input", err)
	}
	if after := snapshotRotationState(t, conn, owner.State.AdministratorID); !reflect.DeepEqual(after, beforeSameVerifier) {
		t.Fatalf("same encoded verifier changed state fields: %v", changedRotationSnapshotFields(beforeSameVerifier, after))
	}
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_rotation_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.request_id='rotate-retry' THEN RAISE EXCEPTION 'audit insert blocked'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_rotation_audit BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW EXECUTE FUNCTION public.fail_rotation_audit()`); err != nil {
		t.Fatal("install rotation audit fault failed")
	}
	var retryDigest [32]byte
	retryDigest[0] = 45
	if _, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
		CurrentSessionVerifier: next, ExpectedCredentialRevision: rotated.CredentialRevision,
		ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: testPasswordVerifier,
		NewSessionVerifier: retryDigest, Now: now.Add(3 * time.Second), RequestID: "rotate-retry",
	}); err == nil {
		t.Fatal("rotation with injected audit failure succeeded")
	}
	if _, err := AuthenticateSession(t.Context(), databaseURL, next, now.Add(4*time.Second)); err != nil {
		t.Fatalf("audit failure invalidated prior session: %v", err)
	}
	var failedAudits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id='rotate-retry'").Scan(&failedAudits); err != nil || failedAudits != 0 {
		t.Fatalf("failed rotation left %d audits, error=%v", failedAudits, err)
	}
	if _, err := conn.Exec(t.Context(), "DROP TRIGGER fail_rotation_audit ON public.almanac_audit_events; DROP FUNCTION public.fail_rotation_audit()"); err != nil {
		t.Fatal("remove rotation audit fault failed")
	}
	var afterRetry [32]byte
	afterRetry[0] = 46
	second, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
		CurrentSessionVerifier: next, ExpectedCredentialRevision: rotated.CredentialRevision,
		ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: testPasswordVerifier,
		NewSessionVerifier: afterRetry, Now: now.Add(5 * time.Second), RequestID: "rotate-retry",
	})
	if err != nil {
		t.Fatalf("rotation retry failed: %v", err)
	}
	if second.Restricted || second.CredentialRevision != rotated.CredentialRevision+1 ||
		readRotationInstallation(t, databaseURL).onboardingAt != after.onboardingAt {
		t.Fatalf("subsequent rotation changed onboarding/revision: session=%+v", second)
	}
	afterSecond := readRotationInstallation(t, databaseURL)
	if afterSecond.id != after.id || afterSecond.epoch != after.epoch || afterSecond.onboardingAt != after.onboardingAt {
		t.Fatalf("subsequent rotation changed installation identity/completion: first=%+v second=%+v", after, afterSecond)
	}
	if otherAfter := snapshotRotationUser(t, databaseURL, otherUserID); otherAfter != otherBeforeSecond {
		t.Fatalf("subsequent rotation changed another user's state fields: %v", changedRotationUserFields(otherBeforeSecond, otherAfter))
	}
	var retryAudits int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_audit_events
WHERE subject_id=$1 AND request_id='rotate-retry' AND action IN
('identity.password.changed','identity.session.issued','identity.onboarding.completed')`, owner.State.AdministratorID).Scan(&retryAudits); err != nil || retryAudits != 2 {
		t.Fatalf("subsequent audit count = %d, error=%v; want 2", retryAudits, err)
	}
	assertRotationAudits(t, conn, owner.State.AdministratorID, "rotate-retry", second.CreatedAt,
		passwordChangedAction, sessionIssuedAction)
}

func TestIntegrationConcurrentInitialRotationsHaveOneWinner(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 13, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{
		Login: "concurrent-rotate", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "concurrent-rotate")
	if err != nil {
		t.Fatal(err)
	}
	var current [32]byte
	current[0] = 51
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: current,
		Now: now, RequestID: "issue",
	}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type outcome struct {
		access SessionAccess
		err    error
	}
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for i := range 2 {
		workers.Go(func() {
			<-start
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var fresh [32]byte
			fresh[0] = byte(52 + i)
			access, err := RotateLocalPassword(ctx, databaseURL, RotatePasswordInput{
				CurrentSessionVerifier: current, ExpectedCredentialRevision: identity.CredentialRevision,
				ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
				NewSessionVerifier: fresh, Now: now.Add(time.Second), RequestID: "concurrent-rotation",
			})
			results <- outcome{access: access, err: err}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var winners, authorityFailures int
	for result := range results {
		if result.err == nil {
			winners++
		} else if errors.Is(result.err, ErrInvalidSession) {
			authorityFailures++
		} else {
			t.Fatalf("unexpected rotation error: %v", result.err)
		}
	}
	if winners != 1 || authorityFailures != 1 {
		t.Fatalf("concurrent rotations winners=%d invalid-authority=%d; want 1 each", winners, authorityFailures)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var sessions, completedAudits int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions WHERE user_id=$1", owner.State.AdministratorID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_audit_events WHERE subject_id=$1
AND request_id='concurrent-rotation' AND action IN ('identity.password.changed','identity.session.issued','identity.onboarding.completed')`, owner.State.AdministratorID).Scan(&completedAudits); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || completedAudits != 3 {
		t.Fatalf("concurrent rotation left sessions=%d success audits=%d; want 1 and 3", sessions, completedAudits)
	}
	assertRotationAudits(t, conn, owner.State.AdministratorID, "concurrent-rotation", now.Add(time.Second),
		passwordChangedAction, sessionIssuedAction, onboardingCompletedAction)
}

func TestIntegrationPasswordRotationLockCancellationCanRetry(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{
		Login: "cancel-rotate", PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, "cancel-rotate")
	if err != nil {
		t.Fatal(err)
	}
	var current [32]byte
	current[0] = 61
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: current,
		Now: now, RequestID: "issue",
	}); err != nil {
		t.Fatal(err)
	}
	blocker := integrationConnect(t, databaseURL)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), "UPDATE public.almanac_installation SET created_at=created_at WHERE singleton"); err != nil {
		t.Fatal("lock installation row failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := RotateLocalPassword(ctx, databaseURL, RotatePasswordInput{
			CurrentSessionVerifier: current, ExpectedCredentialRevision: identity.CredentialRevision,
			ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
			NewSessionVerifier: [32]byte{62}, Now: now.Add(time.Second), RequestID: "cancel",
		})
		result <- err
	}()
	observer := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(observer)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		select {
		case <-deadline.C:
			cancel()
			t.Fatal("rotation never appeared blocked on the installation row")
		case <-ticker.C:
			var count int
			if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
WHERE datname=current_database() AND wait_event_type='Lock' AND query ILIKE '%almanac_installation%'`).Scan(&count); err != nil {
				cancel()
				t.Fatal("observe blocked password rotation failed")
			}
			waiting = count > 0
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("cancelled rotation error = %v; want ErrCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled rotation did not return promptly")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal("release installation lock failed")
	}
	var retry [32]byte
	retry[0] = 63
	if _, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
		CurrentSessionVerifier: current, ExpectedCredentialRevision: identity.CredentialRevision,
		ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
		NewSessionVerifier: retry, Now: now.Add(2 * time.Second), RequestID: "retry",
	}); err != nil {
		t.Fatalf("rotation after cancelled lock retry failed: %v", err)
	}
}

func TestIntegrationRotatePasswordDenialsPreserveState(t *testing.T) {
	cases := []string{
		"unknown session", "revoked session", "idle expired", "absolute expired", "pre-creation",
		"disabled user", "stale session epoch", "stale session revision", "stale proof epoch",
		"stale proof revision", "missing installation", "non-owner before onboarding",
		"bootstrap owner demoted", "null current verifier", "unsupported current verifier", "revision overflow",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newRotationFixture(t, "denial-"+strings.ReplaceAll(name, " ", "-"))
			conn := integrationConnect(t, fixture.databaseURL)
			defer closeIntegrationConn(conn)
			input := fixture.input
			switch name {
			case "unknown session":
				input.CurrentSessionVerifier[0] = 99
			case "revoked session":
				if _, err := conn.Exec(t.Context(), "DELETE FROM public.almanac_sessions WHERE verifier=$1", input.CurrentSessionVerifier[:]); err != nil {
					t.Fatal("revoke test session failed")
				}
			case "idle expired":
				input.Now = fixture.now.Add(sessionIdleLifetime)
			case "absolute expired":
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_sessions SET last_seen_at=$2 WHERE verifier=$1", input.CurrentSessionVerifier[:], fixture.now.Add(sessionAbsoluteLifetime-time.Minute)); err != nil {
					t.Fatal("set absolute-expiry session last-seen boundary failed")
				}
				input.Now = fixture.now.Add(sessionAbsoluteLifetime)
			case "pre-creation":
				input.Now = fixture.now.Add(-time.Microsecond)
			case "disabled user":
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET enabled=false WHERE id=$1", fixture.userID); err != nil {
					t.Fatal("disable test user failed")
				}
			case "stale session epoch":
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_sessions SET credential_epoch=gen_random_uuid() WHERE verifier=$1", input.CurrentSessionVerifier[:]); err != nil {
					t.Fatal("stale the test session epoch failed")
				}
			case "stale session revision":
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_sessions SET credential_revision=credential_revision+1 WHERE verifier=$1", input.CurrentSessionVerifier[:]); err != nil {
					t.Fatal("stale the test session revision failed")
				}
			case "stale proof epoch":
				input.ExpectedCredentialEpoch = "abcdefab-cdef-4abc-8def-abcdefabcdee"
			case "stale proof revision":
				input.ExpectedCredentialRevision++
			case "missing installation":
				if _, err := conn.Exec(t.Context(), "DELETE FROM public.almanac_installation WHERE singleton"); err != nil {
					t.Fatal("remove installation row failed")
				}
			case "non-owner before onboarding":
				var userID string
				if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_users
(login, password_verifier, role, enabled, must_change_password, credential_revision, created_at, updated_at)
VALUES ('rotation-non-owner', $1, 'editor', true, false, 1, $2, $2) RETURNING id::text`, testPasswordVerifier, fixture.now).Scan(&userID); err != nil {
					t.Fatal("create non-owner user failed")
				}
				var verifier [32]byte
				verifier[0] = 73
				if _, err := IssueSession(t.Context(), fixture.databaseURL, IssueSessionInput{
					UserID: userID, ExpectedCredentialEpoch: fixture.identity.CredentialEpoch,
					ExpectedCredentialRevision: 1, Verifier: verifier, Now: fixture.now, RequestID: "issue-non-owner",
				}); err != nil {
					t.Fatal("issue non-owner session failed")
				}
				input.CurrentSessionVerifier = verifier
				input.ExpectedCredentialRevision = 1
			case "bootstrap owner demoted":
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET role='editor' WHERE id=$1", fixture.userID); err != nil {
					t.Fatal("demote bootstrap user failed")
				}
			case "null current verifier":
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET password_verifier=NULL WHERE id=$1", fixture.userID); err != nil {
					t.Fatal("clear current verifier failed")
				}
			case "unsupported current verifier":
				if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET password_verifier='unsupported' WHERE id=$1", fixture.userID); err != nil {
					t.Fatal("replace current verifier with malformed value failed")
				}
			case "revision overflow":
				if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_users SET credential_revision=9223372036854775807 WHERE id=$1`, fixture.userID); err != nil {
					t.Fatal("set user revision to maximum failed")
				}
				if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_sessions SET credential_revision=9223372036854775807 WHERE verifier=$1`, input.CurrentSessionVerifier[:]); err != nil {
					t.Fatal("set matching session revision to maximum failed")
				}
				input.ExpectedCredentialRevision = int64(^uint64(0) >> 1)
			}
			before := snapshotRotationState(t, conn, fixture.userID)
			if _, err := RotateLocalPassword(t.Context(), fixture.databaseURL, input); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("rotation error = %v; want ErrInvalidSession", err)
			}
			after := snapshotRotationState(t, conn, fixture.userID)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("denied rotation changed state fields: %v", changedRotationSnapshotFields(before, after))
			}
		})
	}
}

func TestIntegrationInitialOnboardingAuditFailureRollsBackAndRetries(t *testing.T) {
	fixture := newRotationFixture(t, "completion-audit-failure")
	fixture.input.RequestID = "completion-failure"
	conn := integrationConnect(t, fixture.databaseURL)
	defer closeIntegrationConn(conn)
	for i := range 2 {
		var verifier [32]byte
		verifier[0] = byte(81 + i)
		if _, err := IssueSession(t.Context(), fixture.databaseURL, IssueSessionInput{
			UserID: fixture.userID, ExpectedCredentialEpoch: fixture.identity.CredentialEpoch,
			ExpectedCredentialRevision: fixture.identity.CredentialRevision,
			Verifier:                   verifier, Now: fixture.now, RequestID: "issue-completion-test",
		}); err != nil {
			t.Fatal("issue prior onboarding session failed")
		}
	}
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_onboarding_completion_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.action='identity.onboarding.completed' AND NEW.request_id='completion-failure' THEN RAISE EXCEPTION 'completion audit blocked'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_onboarding_completion_audit BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW EXECUTE FUNCTION public.fail_onboarding_completion_audit()`); err != nil {
		t.Fatal("install onboarding audit fault failed")
	}
	before := snapshotRotationState(t, conn, fixture.userID)
	if _, err := RotateLocalPassword(t.Context(), fixture.databaseURL, fixture.input); err == nil {
		t.Fatal("rotation succeeded while onboarding completion audit was faulted")
	}
	after := snapshotRotationState(t, conn, fixture.userID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("completion audit failure did not roll back state fields: %v", changedRotationSnapshotFields(before, after))
	}
	if _, err := conn.Exec(t.Context(), "DROP TRIGGER fail_onboarding_completion_audit ON public.almanac_audit_events; DROP FUNCTION public.fail_onboarding_completion_audit()"); err != nil {
		t.Fatal("remove onboarding audit fault failed")
	}
	access, err := RotateLocalPassword(t.Context(), fixture.databaseURL, fixture.input)
	if err != nil {
		t.Fatalf("retry after onboarding audit fault failed: %v", err)
	}
	if access.Restricted || access.CredentialRevision != fixture.identity.CredentialRevision+1 {
		t.Fatalf("retried completion session = %+v", access)
	}
	assertRotationAudits(t, conn, fixture.userID, fixture.input.RequestID, access.CreatedAt,
		passwordChangedAction, sessionIssuedAction, onboardingCompletedAction)
}

func TestIntegrationRotatePasswordSchemaAndOutageErrorsAreSafe(t *testing.T) {
	t.Run("missing schema", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		input := validRotationInput()
		_, err := RotateLocalPassword(t.Context(), databaseURL, input)
		if err == nil || !strings.Contains(err.Error(), "schema compatibility read failed") ||
			strings.Contains(err.Error(), "almanac_f1_t05_local_only") || strings.Contains(err.Error(), input.NewPasswordVerifier) {
			t.Fatalf("missing-schema error = %v; want a redacted fail-closed error", err)
		}
		state, compatibilityErr := CheckCompatibility(t.Context(), databaseURL)
		if compatibilityErr != nil || state != CompatibilityMissing {
			t.Fatalf("missing schema compatibility = %v, %v; want missing", state, compatibilityErr)
		}
	})
	t.Run("pending schema", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		conn := integrationConnect(t, databaseURL)
		if _, err := conn.Exec(t.Context(), embeddedBootstrapSQL); err != nil {
			t.Fatal("create pending migration history failed")
		}
		closeIntegrationConn(conn)
		_, err := RotateLocalPassword(t.Context(), databaseURL, validRotationInput())
		assertRotationSchemaError(t, err, CompatibilityPending)
		conn = integrationConnect(t, databaseURL)
		defer closeIntegrationConn(conn)
		var history int
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_schema_migrations").Scan(&history); err != nil || history != 0 {
			t.Fatalf("pending schema history changed to %d rows, error=%v", history, err)
		}
	})
	t.Run("newer schema", func(t *testing.T) {
		fixture := newRotationFixture(t, "newer-schema")
		migrations, err := LoadMigrations(embeddedMigrations)
		if err != nil {
			t.Fatal(err)
		}
		futureSQL := "SELECT 1"
		futureVersion := len(migrations) + 1
		future := Migration{Version: futureVersion, Name: "0013_future.sql", SQL: futureSQL, Checksum: sha256.Sum256([]byte(futureSQL))}
		if _, err := UpgradeWithMigrations(t.Context(), fixture.databaseURL, append(migrations, future)); err != nil {
			t.Fatal("mark database newer than application failed")
		}
		conn := integrationConnect(t, fixture.databaseURL)
		defer closeIntegrationConn(conn)
		before := snapshotRotationState(t, conn, fixture.userID)
		_, err = RotateLocalPassword(t.Context(), fixture.databaseURL, fixture.input)
		assertRotationSchemaError(t, err, CompatibilityNewer)
		if after := snapshotRotationState(t, conn, fixture.userID); !reflect.DeepEqual(after, before) {
			t.Fatalf("newer schema rejection mutated state fields: %v", changedRotationSnapshotFields(before, after))
		}
	})
	t.Run("database outage", func(t *testing.T) {
		fixture := newRotationFixture(t, "outage")
		beforeConn := integrationConnect(t, fixture.databaseURL)
		defer closeIntegrationConn(beforeConn)
		before := snapshotRotationState(t, beforeConn, fixture.userID)
		parsed, err := url.Parse(fixture.databaseURL)
		if err != nil {
			t.Fatal("parse fixture URL failed")
		}
		parsed.Host = "127.0.0.1:1"
		unavailableURL := parsed.String()
		_, err = RotateLocalPassword(t.Context(), unavailableURL, fixture.input)
		if err == nil || !strings.Contains(err.Error(), "database connection failed") ||
			strings.Contains(err.Error(), "almanac_f1_t05_local_only") || strings.Contains(err.Error(), fixture.identity.PasswordVerifier) ||
			strings.Contains(err.Error(), fixture.input.NewPasswordVerifier) || strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Fatalf("outage error leaked or had wrong classification: %v", err)
		}
		if after := snapshotRotationState(t, beforeConn, fixture.userID); !reflect.DeepEqual(after, before) {
			t.Fatalf("outage handling mutated state fields: %v", changedRotationSnapshotFields(before, after))
		}
	})
}

type rotationFixture struct {
	databaseURL string
	userID      string
	identity    LocalIdentitySnapshot
	now         time.Time
	input       RotatePasswordInput
}

func newRotationFixture(t *testing.T, login string) rotationFixture {
	t.Helper()
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 16, 0, 0, 123456789, time.UTC)
	owner, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{
		Login: login, PasswordVerifier: testPasswordVerifier, Now: now, RequestID: "bootstrap-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ReadLocalIdentity(t.Context(), databaseURL, login)
	if err != nil {
		t.Fatal(err)
	}
	var verifier, next [32]byte
	verifier[0], next[0] = 91, 92
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: owner.State.AdministratorID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: identity.CredentialRevision, Verifier: verifier, Now: now, RequestID: "issue-fixture",
	}); err != nil {
		t.Fatal(err)
	}
	return rotationFixture{
		databaseURL: databaseURL, userID: owner.State.AdministratorID, identity: identity, now: now,
		input: RotatePasswordInput{
			CurrentSessionVerifier: verifier, ExpectedCredentialRevision: identity.CredentialRevision,
			ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
			NewSessionVerifier: next, Now: now.Add(time.Second), RequestID: "denial-fixture",
		},
	}
}

func validRotationInput() RotatePasswordInput {
	return RotatePasswordInput{
		CurrentSessionVerifier: [32]byte{1}, ExpectedCredentialRevision: 1,
		ExpectedCredentialEpoch: "abcdefab-cdef-4abc-8def-abcdefabcdee",
		NewPasswordVerifier:     rotationTestPasswordVerifier2, NewSessionVerifier: [32]byte{2},
		Now: time.Date(2026, 10, 25, 16, 0, 1, 0, time.UTC), RequestID: "schema-boundary",
	}
}

func assertRotationSchemaError(t *testing.T, err error, want Compatibility) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "database schema is "+want.String()) ||
		strings.Contains(err.Error(), "almanac_f1_t05_local_only") || strings.Contains(err.Error(), rotationTestPasswordVerifier2) {
		t.Fatalf("schema error = %v; want redacted %s classification", err, want.String())
	}
}

type rotationStateSnapshot struct {
	user, sessions, installation, audits string
}

func snapshotRotationState(t *testing.T, conn *pgx.Conn, userID string) rotationStateSnapshot {
	t.Helper()
	var snapshot rotationStateSnapshot
	if err := conn.QueryRow(t.Context(), `SELECT COALESCE(string_agg(concat_ws('|', id::text,
COALESCE(password_verifier,'<null>'), credential_revision::text, must_change_password::text,
enabled::text, role, updated_at::text), E'\n' ORDER BY (id=$1::uuid)::int DESC, id::text),'')
FROM public.almanac_users`, userID).Scan(&snapshot.user); err != nil {
		t.Fatalf("snapshot user revision state failed: %v", err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT COALESCE(string_agg(concat_ws('|', id::text, user_id::text,
encode(verifier,'hex'), credential_revision::text, credential_epoch::text, restricted::text,
created_at::text, last_seen_at::text), E'\n' ORDER BY id::text),'') FROM public.almanac_sessions`).Scan(&snapshot.sessions); err != nil {
		t.Fatal("snapshot sessions failed")
	}
	if err := conn.QueryRow(t.Context(), `SELECT COALESCE(string_agg(concat_ws('|', installation_id::text,
credential_epoch::text, COALESCE(bootstrap_admin_id::text,''), COALESCE(onboarding_completed_at::text,'')), E'\n' ORDER BY singleton::text),'')
FROM public.almanac_installation`).Scan(&snapshot.installation); err != nil {
		t.Fatal("snapshot installation state failed")
	}
	if err := conn.QueryRow(t.Context(), `SELECT COALESCE(string_agg(concat_ws('|', actor, subject_id::text, action,
outcome, occurred_at::text, request_id), E'\n' ORDER BY id::text),'') FROM public.almanac_audit_events`).Scan(&snapshot.audits); err != nil {
		t.Fatal("snapshot audit state failed")
	}
	return snapshot
}

type rotationAudit struct {
	actor, subject, action, outcome, requestID string
	occurredAt                                 time.Time
}

func assertRotationAudits(t *testing.T, conn *pgx.Conn, userID, requestID string, now time.Time, actions ...string) {
	t.Helper()
	rows, err := conn.Query(t.Context(), `SELECT actor, subject_id::text, action, outcome, occurred_at, request_id
FROM public.almanac_audit_events WHERE request_id=$1 ORDER BY action`, requestID)
	if err != nil {
		t.Fatal("read exact rotation audits failed")
	}
	defer rows.Close()
	got := make([]rotationAudit, 0, len(actions))
	for rows.Next() {
		var event rotationAudit
		if err := rows.Scan(&event.actor, &event.subject, &event.action, &event.outcome, &event.occurredAt, &event.requestID); err != nil {
			t.Fatal("scan rotation audit failed")
		}
		got = append(got, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal("read rotation audits failed")
	}
	if len(got) != len(actions) {
		t.Fatalf("request %q has %d audit rows, want exactly %d: %+v", requestID, len(got), len(actions), got)
	}
	wantActions := append([]string(nil), actions...)
	slices.Sort(wantActions)
	wantTime := now.UTC().Truncate(time.Microsecond)
	for i, event := range got {
		if event.actor != userID || event.subject != userID || event.action != wantActions[i] ||
			event.outcome != "success" || event.requestID != requestID || !event.occurredAt.Equal(wantTime) {
			t.Fatalf("audit %d = %+v; want actor/subject %s, action %s, success, time %s, request %s", i, event, userID, wantActions[i], wantTime, requestID)
		}
	}
}

type rotationInstallation struct {
	id, epoch    string
	onboardingAt pgtype.Timestamptz
}

func readRotationInstallation(t *testing.T, databaseURL string) rotationInstallation {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var value rotationInstallation
	if err := conn.QueryRow(t.Context(), `SELECT installation_id::text, credential_epoch::text,
onboarding_completed_at FROM public.almanac_installation WHERE singleton`).Scan(&value.id, &value.epoch, &value.onboardingAt); err != nil {
		t.Fatal("read installation state failed")
	}
	return value
}

type rotationUserSnapshot struct {
	user, sessions string
}

func snapshotRotationUser(t *testing.T, databaseURL, userID string) rotationUserSnapshot {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var snapshot rotationUserSnapshot
	if err := conn.QueryRow(t.Context(), `SELECT concat_ws('|', id::text, login, COALESCE(password_verifier,'<null>'),
role, enabled::text, must_change_password::text, credential_revision::text, created_at::text, updated_at::text)
FROM public.almanac_users WHERE id=$1`, userID).Scan(&snapshot.user); err != nil {
		t.Fatal("snapshot other user row failed")
	}
	if err := conn.QueryRow(t.Context(), `SELECT COALESCE(string_agg(concat_ws('|', id::text, encode(verifier,'hex'),
credential_revision::text, credential_epoch::text, restricted::text, created_at::text, last_seen_at::text),
E'\n' ORDER BY id::text),'') FROM public.almanac_sessions WHERE user_id=$1`, userID).Scan(&snapshot.sessions); err != nil {
		t.Fatal("snapshot other user sessions failed")
	}
	return snapshot
}

func changedRotationSnapshotFields(before, after rotationStateSnapshot) []string {
	changed := make([]string, 0, 4)
	if before.user != after.user {
		changed = append(changed, "users")
	}
	if before.sessions != after.sessions {
		changed = append(changed, "sessions")
	}
	if before.installation != after.installation {
		changed = append(changed, "installation")
	}
	if before.audits != after.audits {
		changed = append(changed, "audits")
	}
	return changed
}

func changedRotationUserFields(before, after rotationUserSnapshot) []string {
	changed := make([]string, 0, 2)
	if before.user != after.user {
		changed = append(changed, "user")
	}
	if before.sessions != after.sessions {
		changed = append(changed, "sessions")
	}
	return changed
}
