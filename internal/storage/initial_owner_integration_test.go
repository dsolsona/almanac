//go:build integration

package storage

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const testPasswordVerifier = "$argon2id$v=19$m=19456,t=2,p=1$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZzAxMjM0NTY3ODlhYmNkZWY"

func TestIntegrationInitialOwnerProvisionIsAtomicAndIdempotent(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}

	state, err := ReadInitialOwnerState(t.Context(), databaseURL)
	if err != nil || state.Provisioned {
		t.Fatalf("initial state = %+v, %v; want unprovisioned", state, err)
	}

	input := InitialOwnerInput{Login: "Owner@Example", PasswordVerifier: testPasswordVerifier, Now: time.Date(2026, 10, 25, 12, 30, 0, 123456789, time.FixedZone("test", 3600)), RequestID: "request-001"}
	created, err := ProvisionInitialOwner(t.Context(), databaseURL, input)
	if err != nil || !created.Created || !created.State.Provisioned || created.State.AdministratorID == "" || created.State.OnboardingComplete {
		t.Fatalf("provision = %+v, %v", created, err)
	}

	conn := integrationConnect(t, databaseURL)
	var login, role, verifier string
	var enabled, mustChange bool
	var revision int64
	var createdAt, updatedAt time.Time
	err = conn.QueryRow(t.Context(), "SELECT login, password_verifier, role, enabled, must_change_password, credential_revision, created_at, updated_at FROM public.almanac_users WHERE id = $1", created.State.AdministratorID).Scan(&login, &verifier, &role, &enabled, &mustChange, &revision, &createdAt, &updatedAt)
	if err != nil {
		t.Fatal("read provisioned user failed")
	}
	wantTime := input.Now.UTC().Truncate(time.Microsecond)
	if login != input.Login || verifier != input.PasswordVerifier || role != "administrator" || !enabled || !mustChange || revision != 1 || !createdAt.Equal(wantTime) || !updatedAt.Equal(wantTime) {
		t.Fatalf("persisted owner = login %q role %q enabled %t must-change %t revision %d timestamps %v/%v", login, role, enabled, mustChange, revision, createdAt, updatedAt)
	}
	var auditCount int
	var actor, action, outcome, requestID, subjectID string
	var auditAt time.Time
	if err := conn.QueryRow(t.Context(), "SELECT count(*), min(actor), min(action), min(outcome), min(request_id), min(subject_id::text), min(occurred_at) FROM public.almanac_audit_events").Scan(&auditCount, &actor, &action, &outcome, &requestID, &subjectID, &auditAt); err != nil {
		t.Fatal("read bootstrap audit failed")
	}
	if auditCount != 1 || actor != "system" || action != "identity.bootstrap.provision" || outcome != "success" || requestID != input.RequestID || subjectID != created.State.AdministratorID || !auditAt.Equal(wantTime) {
		t.Fatalf("bootstrap audit count=%d actor=%q subject=%q action=%q outcome=%q request=%q time=%v", auditCount, actor, subjectID, action, outcome, requestID, auditAt)
	}
	var bootstrappedAt, onboardingAt pgtype.Timestamptz
	if err := conn.QueryRow(t.Context(), "SELECT bootstrapped_at, onboarding_completed_at FROM public.almanac_installation WHERE singleton").Scan(&bootstrappedAt, &onboardingAt); err != nil {
		t.Fatal("read installation bootstrap markers failed")
	}
	if !bootstrappedAt.Valid || !bootstrappedAt.Time.Equal(wantTime) || onboardingAt.Valid {
		t.Fatalf("installation markers bootstrapped=%v onboarding=%v", bootstrappedAt, onboardingAt)
	}
	for _, secret := range []string{input.Login, input.PasswordVerifier, databasePassword(databaseURL)} {
		if secret != "" && strings.Contains(strings.Join([]string{actor, action, outcome, requestID}, " "), secret) {
			t.Fatalf("bootstrap audit exposed input %q", secret)
		}
	}

	changed := input
	changed.Login = "different-owner"
	changed.PasswordVerifier = strings.Replace(testPasswordVerifier, "MDEy", "YWJj", 1)
	changed.Now = input.Now.Add(time.Hour)
	changed.RequestID = "different-request"
	repeated, err := ProvisionInitialOwner(t.Context(), databaseURL, changed)
	if err != nil || repeated.Created || repeated.State != created.State {
		t.Fatalf("changed repeat provision = %+v, %v; want persisted state without creation", repeated, err)
	}
	repeated, err = ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{})
	if err != nil || repeated.Created || repeated.State != created.State {
		t.Fatalf("empty repeat provision = %+v, %v; want persisted state without creation", repeated, err)
	}
	var afterVerifier string
	var afterRole string
	var afterRevision int64
	var afterUpdated time.Time
	if err := conn.QueryRow(t.Context(), "SELECT password_verifier, role, credential_revision, updated_at FROM public.almanac_users WHERE id = $1", created.State.AdministratorID).Scan(&afterVerifier, &afterRole, &afterRevision, &afterUpdated); err != nil {
		t.Fatal(err)
	}
	if afterVerifier != verifier || afterRole != role || afterRevision != revision || !afterUpdated.Equal(updatedAt) {
		t.Fatal("repeat provisioning changed persisted credentials or user state")
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events").Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count after repeat = %d, %v; want 1", auditCount, err)
	}
}

func TestIntegrationConcurrentInitialOwnerProvisionCreatesOneOwner(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	type result struct {
		value InitialOwnerResult
		err   error
	}
	results := make(chan result, 8)
	ready := make(chan struct{}, 8)
	for i := range 8 {
		go func() {
			ready <- struct{}{}
			<-start
			value, err := ProvisionInitialOwner(ctx, databaseURL, InitialOwnerInput{Login: "owner-" + string(rune('a'+i)), PasswordVerifier: testPasswordVerifier, Now: time.Date(2026, 10, 25, 12, 0, i, 0, time.UTC), RequestID: "request-" + string(rune('a'+i))})
			results <- result{value: value, err: err}
		}()
	}
	for range 8 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("concurrent provisioners did not reach coordinated start: %v", ctx.Err())
		}
	}
	close(start)
	var createdCount int
	var ownerID string
	for range 8 {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("concurrent provisioning failed: %v", got.err)
			}
			if got.value.Created {
				createdCount++
			}
			if ownerID == "" {
				ownerID = got.value.State.AdministratorID
			}
			if got.value.State.AdministratorID != ownerID {
				t.Fatal("concurrent provisioners returned different administrator IDs")
			}
		case <-ctx.Done():
			t.Fatalf("concurrent provisioning did not finish: %v", ctx.Err())
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent Created=true count = %d, want 1", createdCount)
	}
	conn := integrationConnect(t, databaseURL)
	var users, audits int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM public.almanac_users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM public.almanac_audit_events").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if users != 1 || audits != 1 {
		t.Fatalf("concurrent rows users=%d audits=%d, want 1 each", users, audits)
	}
}

func TestIntegrationInitialOwnerAuditFailureRollsBackAndCanRetry(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	_, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_initial_owner_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit insert blocked'; END $$;
CREATE TRIGGER fail_initial_owner_audit BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW EXECUTE FUNCTION public.fail_initial_owner_audit()`)
	if err != nil {
		t.Fatal("install test-owned audit failure trigger failed")
	}
	input := InitialOwnerInput{Login: "retry-owner", PasswordVerifier: testPasswordVerifier, Now: time.Now().UTC(), RequestID: "retry-request"}
	if _, err := ProvisionInitialOwner(t.Context(), databaseURL, input); err == nil || errors.Is(err, ErrInvalidInitialOwner) {
		t.Fatalf("provision with audit insert failure = %v, want safe persistence failure", err)
	} else if strings.Contains(err.Error(), input.Login) || strings.Contains(err.Error(), input.PasswordVerifier) || strings.Contains(err.Error(), databasePassword(databaseURL)) {
		t.Fatalf("persistence error disclosed input or database secret: %v", err)
	}
	var users, audits, markers int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_installation WHERE bootstrap_admin_id IS NOT NULL").Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if users != 0 || audits != 0 || markers != 0 {
		t.Fatalf("audit failure left partial state users=%d audits=%d markers=%d", users, audits, markers)
	}
	if _, err := conn.Exec(t.Context(), "DROP TRIGGER fail_initial_owner_audit ON public.almanac_audit_events; DROP FUNCTION public.fail_initial_owner_audit()"); err != nil {
		t.Fatal("remove test-owned audit failure trigger failed")
	}
	retried, err := ProvisionInitialOwner(t.Context(), databaseURL, input)
	if err != nil || !retried.Created {
		t.Fatalf("retry after rollback = %+v, %v", retried, err)
	}
}

func TestIntegrationInitialOwnerRejectsMalformedInputWithoutState(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	valid := InitialOwnerInput{Login: "owner", PasswordVerifier: testPasswordVerifier, Now: time.Now(), RequestID: "request"}
	cases := map[string]InitialOwnerInput{
		"empty login":          {PasswordVerifier: valid.PasswordVerifier, Now: valid.Now, RequestID: valid.RequestID},
		"oversized login":      {Login: strings.Repeat("x", 257), PasswordVerifier: valid.PasswordVerifier, Now: valid.Now, RequestID: valid.RequestID},
		"invalid utf8 login":   {Login: "owner\xff", PasswordVerifier: valid.PasswordVerifier, Now: valid.Now, RequestID: valid.RequestID},
		"malformed verifier":   {Login: valid.Login, PasswordVerifier: "synthetic-password-secret", Now: valid.Now, RequestID: valid.RequestID},
		"zero timestamp":       {Login: valid.Login, PasswordVerifier: valid.PasswordVerifier, RequestID: valid.RequestID},
		"empty request id":     {Login: valid.Login, PasswordVerifier: valid.PasswordVerifier, Now: valid.Now},
		"oversized request id": {Login: valid.Login, PasswordVerifier: valid.PasswordVerifier, Now: valid.Now, RequestID: strings.Repeat("r", 129)},
		"invalid utf8 request": {Login: valid.Login, PasswordVerifier: valid.PasswordVerifier, Now: valid.Now, RequestID: "req\xff"},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ProvisionInitialOwner(t.Context(), databaseURL, input)
			if !errors.Is(err, ErrInvalidInitialOwner) {
				t.Fatalf("provision error = %v, want ErrInvalidInitialOwner", err)
			}
			if strings.Contains(err.Error(), "synthetic-password-secret") || strings.Contains(err.Error(), databasePassword(databaseURL)) {
				t.Fatalf("validation error disclosed a secret: %v", err)
			}
		})
	}
	conn := integrationConnect(t, databaseURL)
	for _, table := range []string{"almanac_users", "almanac_audit_events"} {
		var count int
		query := "SELECT count(*) FROM public." + table
		if err := conn.QueryRow(t.Context(), query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count after invalid inputs = %d, %v; want zero", table, count, err)
		}
	}
	var markers int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_installation WHERE bootstrap_admin_id IS NOT NULL").Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("provisioning markers after invalid inputs = %d, %v; want zero", markers, err)
	}
}

func TestIntegrationInitialOwnerStorageConstraints(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	provisioned, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "CaseOwner", PasswordVerifier: testPasswordVerifier, Now: time.Now(), RequestID: "constraint-check"})
	if err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	for name, statement := range map[string]string{
		"case-insensitive login uniqueness": `INSERT INTO public.almanac_users (login, role, enabled, must_change_password, created_at, updated_at) VALUES ('caseowner', 'viewer', true, false, now(), now())`,
		"invalid role":                      `INSERT INTO public.almanac_users (login, role, enabled, must_change_password, created_at, updated_at) VALUES ('role-check', 'owner', true, false, now(), now())`,
		"empty login":                       `INSERT INTO public.almanac_users (login, role, enabled, must_change_password, created_at, updated_at) VALUES ('', 'viewer', true, false, now(), now())`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := conn.Exec(t.Context(), statement); err == nil {
				t.Fatal("database accepted invalid user row")
			}
		})
	}
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET bootstrap_admin_id = NULL WHERE singleton"); err == nil {
		t.Fatal("database accepted mismatched bootstrap marker columns")
	}
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_installation SET bootstrap_admin_id = NULL, bootstrapped_at = NULL, onboarding_completed_at = now() WHERE singleton"); err == nil {
		t.Fatal("database accepted onboarding completion without provisioning")
	}
	state, err := ReadInitialOwnerState(t.Context(), databaseURL)
	if err != nil || !state.Provisioned || state.AdministratorID != provisioned.State.AdministratorID || state.OnboardingComplete {
		t.Fatalf("constraint failures changed owner state: %+v, %v", state, err)
	}
}

func TestIntegrationInitialOwnerStateRequiresCurrentSchema(t *testing.T) {
	databaseURL := integrationDatabase(t)
	state, err := ReadInitialOwnerState(t.Context(), databaseURL)
	if err == nil || state != (InitialOwnerState{}) || strings.Contains(err.Error(), databasePassword(databaseURL)) {
		t.Fatalf("read on unupgraded database = %+v, %v; want redacted error", state, err)
	}
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	state, err = ReadInitialOwnerState(t.Context(), databaseURL)
	if err == nil || state != (InitialOwnerState{}) || strings.Contains(err.Error(), databasePassword(databaseURL)) {
		t.Fatalf("read on pending schema = %+v, %v; want redacted error", state, err)
	}
}

func TestIntegrationInitialOwnerLockWaitHonorsCancellationAndRetries(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	blocker := integrationConnect(t, databaseURL)
	blockerTx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blockerTx.Exec(t.Context(), "UPDATE public.almanac_installation SET created_at = created_at WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := ProvisionInitialOwner(ctx, databaseURL, InitialOwnerInput{Login: "cancel-owner", PasswordVerifier: testPasswordVerifier, Now: time.Now(), RequestID: "cancel-request"})
		result <- err
	}()
	observer := integrationConnect(t, databaseURL)
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		var count int
		err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%almanac_installation%'`).Scan(&count)
		if err != nil {
			t.Fatal("observe initial owner lock wait failed")
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		cancel()
		t.Fatal("provisioner never reached the singleton row lock wait")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("cancelled provision error = %v, want ErrCancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled row-lock wait did not return promptly")
	}
	if err := blockerTx.Rollback(t.Context()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatal("release initial owner row lock failed")
	}
	retried, err := ProvisionInitialOwner(t.Context(), databaseURL, InitialOwnerInput{Login: "cancel-owner", PasswordVerifier: testPasswordVerifier, Now: time.Now(), RequestID: "retry-after-cancel"})
	if err != nil || !retried.Created {
		t.Fatalf("provision after cancelled lock wait = %+v, %v", retried, err)
	}
}

func databasePassword(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return ""
	}
	password, _ := parsed.User.Password()
	return password
}
