//go:build integration

package storage

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
)

func TestIntegrationAgentAccessAuthenticatesIssuedTokenAndEnforcesCredentialState(t *testing.T) {
	f := newAgentFixture(t)
	issued, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue test token")
	}
	input := f.input("access-agent", "access-create")
	input.CredentialID = issued.ID
	input.CredentialVerifier = issued.Verifier
	created, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal("could not persist issued test credential")
	}
	now := f.base.Add(4 * time.Second)
	clock := func() time.Time { return now }
	authenticate := func(token string) (AgentAccessPrincipal, error) {
		t.Helper()
		id, verifier, err := identity.AgentTokenVerifier(token)
		if err != nil {
			return AgentAccessPrincipal{}, ErrInvalidAgentCredential
		}
		return AuthenticateAgentCredential(t.Context(), f.databaseURL, id, verifier, clock)
	}
	principal, err := authenticate(issued.Token)
	if err != nil || principal.AgentID != created.Agent.ID || principal.CredentialID != issued.ID || !principal.ExpiresAt.Equal(created.Credential.ExpiresAt) {
		t.Fatalf("issued credential principal=%+v err=%v", principal, err)
	}
	other, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not create wrong-secret token")
	}
	wrongSecret := strings.Replace(issued.Token, issued.Token[len(issued.Token)-43:], other.Token[len(other.Token)-43:], 1)
	if got, err := authenticate(wrongSecret); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("wrong-secret token was not rejected safely")
	}
	unknown, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not create unknown token")
	}
	if got, err := authenticate(unknown.Token); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("unknown token was not rejected safely")
	}
	if _, err := authenticate(issued.Token + "x"); !errors.Is(err, ErrInvalidAgentCredential) {
		t.Fatal("malformed token was not rejected")
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agent_credentials SET issued_at=$2 WHERE id=$1", issued.ID, now.Add(time.Second)); err != nil {
		t.Fatal("could not set future issued boundary")
	}
	closeIntegrationConn(conn)
	if got, err := authenticate(issued.Token); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("unissued credential was not rejected")
	}
	conn = integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agent_credentials SET issued_at=$2 WHERE id=$1", issued.ID, now); err != nil {
		t.Fatal("could not restore issued boundary")
	}
	closeIntegrationConn(conn)
	if got, err := authenticate(issued.Token); err != nil || got.AgentID != created.Agent.ID {
		t.Fatalf("issued_at equality was rejected: principal=%+v err=%v", got, err)
	}
	expiresAt := now.Add(time.Hour)
	conn = integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agent_credentials SET expires_at=$2 WHERE id=$1", issued.ID, expiresAt); err != nil {
		t.Fatal("could not set expiry boundary")
	}
	closeIntegrationConn(conn)
	now = expiresAt
	if got, err := authenticate(issued.Token); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("exact expiry was not rejected")
	}
}

func TestIntegrationAgentAccessRetirementAndRevocation(t *testing.T) {
	f := newAgentFixture(t)
	issued, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue test token")
	}
	input := f.input("access-lifecycle-agent", "access-lifecycle-create")
	input.CredentialID = issued.ID
	input.CredentialVerifier = issued.Verifier
	created, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal("could not persist test credential")
	}
	proof := f.input("unused", "unused").Proof
	rotatedToken, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not create replacement token")
	}
	rotateAt := f.base.Add(5 * time.Second)
	overlap := 10 * time.Minute
	if _, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{
		Proof: proof, AgentID: created.Agent.ID, ExpectedVersion: 1, Overlap: &overlap,
		CredentialID: rotatedToken.ID, CredentialVerifier: rotatedToken.Verifier,
		IdempotencyKey: "access-rotate", RequestID: "access-rotate", Now: func() time.Time { return rotateAt },
	}); err != nil {
		t.Fatal("could not rotate test credential")
	}
	authenticate := func(token string, now time.Time) (AgentAccessPrincipal, error) {
		t.Helper()
		id, verifier, err := identity.AgentTokenVerifier(token)
		if err != nil {
			return AgentAccessPrincipal{}, ErrInvalidAgentCredential
		}
		return AuthenticateAgentCredential(t.Context(), f.databaseURL, id, verifier, func() time.Time { return now })
	}
	oldPrincipal, err := authenticate(issued.Token, rotateAt.Add(time.Second))
	if err != nil || oldPrincipal.AgentID != created.Agent.ID || !oldPrincipal.ExpiresAt.Equal(rotateAt.Add(overlap)) {
		t.Fatalf("overlap principal=%+v err=%v", oldPrincipal, err)
	}
	if got, err := authenticate(issued.Token, rotateAt.Add(overlap)); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("exact retirement boundary was not rejected")
	}
	if got, err := authenticate(rotatedToken.Token, rotateAt.Add(time.Second)); err != nil || got.CredentialID != rotatedToken.ID {
		t.Fatalf("replacement token principal=%+v err=%v", got, err)
	}
	if _, err := RevokeAgentCredential(t.Context(), f.databaseURL, RevokeAgentCredentialInput{
		Proof: proof, AgentID: created.Agent.ID, CredentialID: rotatedToken.ID, ExpectedVersion: 2,
		IdempotencyKey: "access-revoke", RequestID: "access-revoke", Now: func() time.Time { return rotateAt.Add(3 * time.Second) },
	}); err != nil {
		t.Fatal("could not revoke test credential")
	}
	if got, err := authenticate(rotatedToken.Token, rotateAt.Add(4*time.Second)); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("revoked token remained valid")
	}
}

func TestIntegrationAgentAccessRejectsCredentialWithReplacedEpoch(t *testing.T) {
	f := newAgentFixture(t)
	issued, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue test token")
	}
	input := f.input("access-epoch-agent", "access-epoch-create")
	input.CredentialID = issued.ID
	input.CredentialVerifier = issued.Verifier
	if _, err := CreateAgent(t.Context(), f.databaseURL, input); err != nil {
		t.Fatal("could not persist test credential")
	}
	clock := func() time.Time { return f.base.Add(4 * time.Second) }
	if got, err := AuthenticateAgentCredential(t.Context(), f.databaseURL, issued.ID, issued.Verifier, clock); err != nil || got.CredentialID != issued.ID {
		t.Fatalf("valid pre-restore credential principal=%+v err=%v", got, err)
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET credential_epoch=gen_random_uuid() WHERE singleton"); err != nil {
		t.Fatal("could not replace installation epoch")
	}
	closeIntegrationConn(conn)
	if got, err := AuthenticateAgentCredential(t.Context(), f.databaseURL, issued.ID, issued.Verifier, clock); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("credential from prior installation epoch remained valid")
	}
}

func TestIntegrationAgentAccessRejectsDisabledAgentWithUnrevokedCredential(t *testing.T) {
	f := newAgentFixture(t)
	issued, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue test token")
	}
	input := f.input("access-disabled-agent", "access-disabled-create")
	input.CredentialID = issued.ID
	input.CredentialVerifier = issued.Verifier
	if _, err := CreateAgent(t.Context(), f.databaseURL, input); err != nil {
		t.Fatal("could not persist test credential")
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_agents SET enabled=false WHERE id=(SELECT agent_id FROM public.almanac_agent_credentials WHERE id=$1)", issued.ID); err != nil {
		t.Fatal("could not disable test agent")
	}
	var revoked bool
	if err := conn.QueryRow(t.Context(), "SELECT revoked_at IS NOT NULL FROM public.almanac_agent_credentials WHERE id=$1", issued.ID).Scan(&revoked); err != nil || revoked {
		t.Fatal("test credential was revoked while disabling agent")
	}
	closeIntegrationConn(conn)
	if got, err := AuthenticateAgentCredential(t.Context(), f.databaseURL, issued.ID, issued.Verifier, func() time.Time { return f.base.Add(4 * time.Second) }); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("disabled agent with an otherwise valid credential authenticated")
	}
}

func TestIntegrationAgentAccessRejectsIncompleteOnboardingAndZeroClock(t *testing.T) {
	f := newAgentFixture(t)
	issued, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue test token")
	}
	input := f.input("access-onboarding-agent", "access-onboarding-create")
	input.CredentialID = issued.ID
	input.CredentialVerifier = issued.Verifier
	if _, err := CreateAgent(t.Context(), f.databaseURL, input); err != nil {
		t.Fatal("could not persist test credential")
	}
	conn := integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET onboarding_completed_at=NULL WHERE singleton"); err != nil {
		t.Fatal("could not clear onboarding completion")
	}
	closeIntegrationConn(conn)
	validClock := func() time.Time { return f.base.Add(4 * time.Second) }
	if got, err := AuthenticateAgentCredential(t.Context(), f.databaseURL, issued.ID, issued.Verifier, validClock); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("credential authenticated before onboarding completed")
	}
	conn = integrationConnect(t, f.databaseURL)
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET onboarding_completed_at=$1 WHERE singleton", f.base.Add(3*time.Second)); err != nil {
		t.Fatal("could not restore onboarding completion")
	}
	closeIntegrationConn(conn)
	if got, err := AuthenticateAgentCredential(t.Context(), f.databaseURL, issued.ID, issued.Verifier, func() time.Time { return time.Time{} }); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("zero clock authenticated a credential")
	}
}

func TestIntegrationAgentAccessHonorsExplicitZeroRotationOverlap(t *testing.T) {
	f := newAgentFixture(t)
	oldToken, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue original token")
	}
	input := f.input("access-zero-overlap-agent", "access-zero-overlap-create")
	input.CredentialID = oldToken.ID
	input.CredentialVerifier = oldToken.Verifier
	created, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal("could not persist original credential")
	}
	newToken, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue replacement token")
	}
	zeroOverlap := time.Duration(0)
	rotateAt := f.base.Add(5 * time.Second)
	if _, err := RotateAgentCredential(t.Context(), f.databaseURL, RotateAgentCredentialInput{
		Proof: f.input("unused", "unused").Proof, AgentID: created.Agent.ID, ExpectedVersion: 1,
		Overlap: &zeroOverlap, CredentialID: newToken.ID, CredentialVerifier: newToken.Verifier,
		IdempotencyKey: "access-zero-overlap-rotate", RequestID: "access-zero-overlap-rotate",
		Now: func() time.Time { return rotateAt },
	}); err != nil {
		t.Fatal("could not rotate with explicit zero overlap")
	}
	clock := func() time.Time { return rotateAt }
	if got, err := AuthenticateAgentCredential(t.Context(), f.databaseURL, oldToken.ID, oldToken.Verifier, clock); !errors.Is(err, ErrInvalidAgentCredential) || got != (AgentAccessPrincipal{}) {
		t.Fatal("old credential remained valid with explicit zero overlap")
	}
	if got, err := AuthenticateAgentCredential(t.Context(), f.databaseURL, newToken.ID, newToken.Verifier, clock); err != nil || got.CredentialID != newToken.ID {
		t.Fatalf("replacement credential principal=%+v err=%v", got, err)
	}
}

func TestIntegrationAgentAccessSamplesClockAfterInstallationLockWait(t *testing.T) {
	f := newAgentFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	issued, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue test token")
	}
	input := f.input("access-lock-clock-agent", "access-lock-clock-create")
	input.CredentialID = issued.ID
	input.CredentialVerifier = issued.Verifier
	created, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal("could not persist test credential")
	}
	validAt := f.base.Add(4 * time.Second)
	expiresAt := created.Credential.ExpiresAt
	var clockNanos atomic.Int64
	clockNanos.Store(validAt.UnixNano())
	clock := func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() }
	lockConn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(lockConn)
	lockTx, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatal("could not begin installation lock transaction")
	}
	var singleton bool
	if err := lockTx.QueryRow(ctx, "SELECT singleton FROM public.almanac_installation WHERE singleton FOR UPDATE").Scan(&singleton); err != nil || !singleton {
		t.Fatal("could not lock installation state")
	}
	defer rollbackTransaction(lockTx)
	resultCh := make(chan resultAgentAccess, 1)
	started := make(chan struct{})
	workersDone := make(chan struct{}, 1)
	workerCount := 0
	defer func() {
		cancel()
		rollbackTransaction(lockTx)
		if workerCount == 0 {
			return
		}
		select {
		case <-workersDone:
		case <-time.After(3 * time.Second):
			t.Error("authentication worker did not stop after cancellation and lock release")
		}
	}()
	workerCount++
	go func() {
		defer func() { workersDone <- struct{}{} }()
		close(started)
		principal, err := AuthenticateAgentCredential(ctx, f.databaseURL, issued.ID, issued.Verifier, clock)
		resultCh <- resultAgentAccess{principal: principal, err: err}
	}()
	<-started
	if err := waitForAgentAccessLockWait(ctx, t, f, "SELECT credential_epoch, bootstrap_admin_id, onboarding_completed_at"); err != nil {
		t.Fatal("authentication did not wait for installation lock")
	}
	clockNanos.Store(expiresAt.UnixNano())
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal("could not release installation lock")
	}
	got := receiveAgentAccess(ctx, resultCh)
	if got.principal != (AgentAccessPrincipal{}) || !errors.Is(got.err, ErrInvalidAgentCredential) {
		t.Fatal("authentication used a pre-lock time sample at the exact expiry boundary")
	}
}

func TestIntegrationAgentAccessSerializesBehindCredentialRevocation(t *testing.T) {
	f := newAgentFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	issued, err := identity.NewAgentToken()
	if err != nil {
		t.Fatal("could not issue test token")
	}
	input := f.input("access-revoke-order-agent", "access-revoke-order-create")
	input.CredentialID = issued.ID
	input.CredentialVerifier = issued.Verifier
	created, err := CreateAgent(t.Context(), f.databaseURL, input)
	if err != nil {
		t.Fatal("could not persist test credential")
	}
	proof := f.input("unused", "unused").Proof
	lockConn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(lockConn)
	lockTx, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatal("could not begin agent lock transaction")
	}
	var lockedAgent string
	if err := lockTx.QueryRow(ctx, "SELECT id FROM public.almanac_agents WHERE id=$1 FOR UPDATE", created.Agent.ID).Scan(&lockedAgent); err != nil || lockedAgent != created.Agent.ID {
		t.Fatal("could not lock agent authority")
	}
	defer rollbackTransaction(lockTx)
	workersDone := make(chan struct{}, 2)
	workerCount := 0
	defer func() {
		cancel()
		rollbackTransaction(lockTx)
		for range workerCount {
			select {
			case <-workersDone:
			case <-time.After(3 * time.Second):
				t.Error("authentication or revocation worker did not stop after cancellation and lock release")
				return
			}
		}
	}()
	revokeDone := make(chan error, 1)
	workerCount++
	go func() {
		defer func() { workersDone <- struct{}{} }()
		_, err := RevokeAgentCredential(ctx, f.databaseURL, RevokeAgentCredentialInput{
			Proof: proof, AgentID: created.Agent.ID, CredentialID: issued.ID, ExpectedVersion: 1,
			IdempotencyKey: "access-order-revoke", RequestID: "access-order-revoke",
			Now: func() time.Time { return f.base.Add(4 * time.Second) },
		})
		revokeDone <- err
	}()
	if err := waitForAgentAccessLockWait(ctx, t, f, "SELECT id, name, display_name, enabled, version"); err != nil {
		t.Fatal("revocation did not wait for agent authority lock")
	}
	accessDone := make(chan resultAgentAccess, 1)
	workerCount++
	go func() {
		defer func() { workersDone <- struct{}{} }()
		principal, err := AuthenticateAgentCredential(ctx, f.databaseURL, issued.ID, issued.Verifier, func() time.Time { return f.base.Add(4 * time.Second) })
		accessDone <- resultAgentAccess{principal: principal, err: err}
	}()
	if err := waitForAgentAccessLockWait(ctx, t, f, "SELECT id, enabled FROM public.almanac_agents"); err != nil {
		t.Fatal("authentication did not wait for agent authority lock")
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal("could not release agent lock")
	}
	if err := receiveAgentAccessError(ctx, revokeDone); err != nil {
		t.Fatal("credential revocation failed")
	}
	access := receiveAgentAccess(ctx, accessDone)
	if access.principal != (AgentAccessPrincipal{}) || !errors.Is(access.err, ErrInvalidAgentCredential) {
		t.Fatal("authentication ordered after revocation succeeded")
	}
}

type resultAgentAccess struct {
	principal AgentAccessPrincipal
	err       error
}

func waitForAgentAccessLockWait(ctx context.Context, t *testing.T, f agentFixture, queryFragment string) error {
	t.Helper()
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := conn.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND state = 'active'
			  AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%'
		)`, queryFragment).Scan(&waiting)
		if err != nil {
			return errors.New("could not inspect database lock wait")
		}
		if waiting {
			return nil
		}
		select {
		case <-deadline.C:
			return errors.New("operation did not reach expected database lock wait")
		case <-ctx.Done():
			return errors.New("context ended before expected database lock wait")
		case <-ticker.C:
		}
	}
}

func receiveAgentAccessError(ctx context.Context, result <-chan error) error {
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return errors.New("revocation did not complete before context deadline")
	}
}

func receiveAgentAccess(ctx context.Context, result <-chan resultAgentAccess) resultAgentAccess {
	select {
	case got := <-result:
		return got
	case <-ctx.Done():
		return resultAgentAccess{err: errors.New("authentication did not complete before context deadline")}
	}
}
