//go:build integration

package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	closeIntegrationConn(conn)
	var otherSession [32]byte
	otherSession[0] = 47
	if _, err := IssueSession(t.Context(), databaseURL, IssueSessionInput{
		UserID: otherUserID, ExpectedCredentialEpoch: identity.CredentialEpoch,
		ExpectedCredentialRevision: 1, Verifier: otherSession, Now: now, RequestID: "issue-other",
	}); err != nil {
		t.Fatal(err)
	}
	for label, collision := range map[string][32]byte{"same-user session": restricted, "other-user session": otherSession} {
		if _, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
			CurrentSessionVerifier: current, ExpectedCredentialRevision: identity.CredentialRevision,
			ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
			NewSessionVerifier: collision, Now: now.Add(time.Second), RequestID: "collision",
		}); !errors.Is(err, ErrInvalidSessionInput) {
			t.Fatalf("%s collision error = %v; want invalid input", label, err)
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
	if _, err := AuthenticateSession(t.Context(), databaseURL, next, now.Add(2*time.Second)); err != nil {
		t.Fatalf("new session is unusable: %v", err)
	}
	after := readRotationInstallation(t, databaseURL)
	if after.id != installationBefore.id || after.epoch != installationBefore.epoch || !after.onboardingAt.Valid || !after.onboardingAt.Time.Equal(rotated.CreatedAt) {
		t.Fatalf("installation identity/completion changed unexpectedly: before=%+v after=%+v", installationBefore, after)
	}
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
	if _, err := RotateLocalPassword(t.Context(), databaseURL, RotatePasswordInput{
		CurrentSessionVerifier: next, ExpectedCredentialRevision: rotated.CredentialRevision,
		ExpectedCredentialEpoch: identity.CredentialEpoch, NewPasswordVerifier: rotationTestPasswordVerifier2,
		NewSessionVerifier: [32]byte{44}, Now: now.Add(2 * time.Second), RequestID: "same-password",
	}); !errors.Is(err, ErrInvalidSessionInput) {
		t.Fatalf("same encoded verifier error = %v; want invalid session input", err)
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
	var retryAudits int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_audit_events
WHERE subject_id=$1 AND request_id='rotate-retry' AND action IN
('identity.password.changed','identity.session.issued','identity.onboarding.completed')`, owner.State.AdministratorID).Scan(&retryAudits); err != nil || retryAudits != 2 {
		t.Fatalf("subsequent audit count = %d, error=%v; want 2", retryAudits, err)
	}
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
