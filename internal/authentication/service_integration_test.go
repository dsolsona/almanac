//go:build integration

package authentication

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

type integrationClock struct {
	mu    sync.Mutex
	value time.Time
}

func newIntegrationClock() *integrationClock {
	return &integrationClock{value: time.Now().UTC().Truncate(time.Microsecond)}
}
func (c *integrationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}
func (c *integrationClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = c.value.Add(d)
}

func newIntegrationService(t *testing.T) (string, *Service, *integrationClock) {
	t.Helper()
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade test database failed")
	}
	clock := newIntegrationClock()
	service, err := New(t.Context(), databaseURL, clock.Now)
	if err != nil {
		t.Fatal("construct authentication service failed")
	}
	return databaseURL, service, clock
}

func integrationProtectedFile(t *testing.T, password string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(password), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func integrationOwner(t *testing.T, service *Service, password string) (storage.InitialOwnerResult, string) {
	t.Helper()
	path := integrationProtectedFile(t, password, 0o600)
	owner, err := service.EnsureInitialOwner(t.Context(), "owner", path, "bootstrap-owner")
	if err != nil || !owner.Created {
		t.Fatalf("provision owner = %+v, %v", owner, err)
	}
	return owner, path
}

type controlledHasher struct {
	base        passwordWork
	afterVerify func(context.Context, string, string, bool) (bool, error)
}

func (h controlledHasher) Hash(ctx context.Context, password string) (string, error) {
	return h.base.Hash(ctx, password)
}
func (h controlledHasher) Verify(ctx context.Context, password, verifier string) (bool, error) {
	verified, err := h.base.Verify(ctx, password, verifier)
	if err != nil || h.afterVerify == nil {
		return verified, err
	}
	return h.afterVerify(ctx, password, verifier, verified)
}

func TestIntegrationBootstrapRestrictedLoginRotationAndLogout(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade test database failed")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	service, err := New(t.Context(), databaseURL, func() time.Time { now = now.Add(time.Microsecond); return now })
	if err != nil {
		t.Fatal("construct authentication service failed")
	}
	path := filepath.Join(t.TempDir(), "initial-password")
	initial := "Initial password phrase 124!"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := service.EnsureInitialOwner(t.Context(), "owner", path, "bootstrap-1")
	if err != nil || !first.Created || !first.State.Provisioned {
		t.Fatalf("bootstrap = %+v, %v", first, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	second, err := service.EnsureInitialOwner(t.Context(), "", "", "")
	if err != nil || second.Created || second.State.AdministratorID != first.State.AdministratorID {
		t.Fatalf("repeat bootstrap = %+v, %v", second, err)
	}
	source := netip.MustParseAddr("192.0.2.10")
	login, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: initial, Source: source, RequestID: "login-1"})
	if err != nil || login.Secret == "" || !login.Access.Restricted {
		t.Fatalf("initial login = %+v, %v", login.Access, err)
	}
	permanent := "  Permanent Password phrase 457! \n"
	rotated, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: login.Secret, CurrentPassword: initial, NewPassword: permanent, Source: source, RequestID: "rotate-1"})
	if err != nil || rotated.Secret == "" || rotated.Access.Restricted {
		t.Fatalf("initial rotation = %+v, %v", rotated.Access, err)
	}
	if _, err := service.Authenticate(t.Context(), login.Secret); err == nil {
		t.Fatal("restricted session remained usable after rotation")
	}
	if _, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: initial, Source: source, RequestID: "login-old"}); err == nil {
		t.Fatal("initial password remained usable")
	}
	access, err := service.Authenticate(t.Context(), rotated.Secret)
	if err != nil || access.Restricted || access.UserID != first.State.AdministratorID {
		t.Fatalf("normal session = %+v, %v", access, err)
	}
	if ok, err := service.Logout(t.Context(), rotated.Secret, "logout-1"); err != nil || !ok {
		t.Fatalf("logout = %t, %v", ok, err)
	}
	if _, err := service.Authenticate(t.Context(), rotated.Secret); err == nil {
		t.Fatal("logged out session authenticated")
	}
	if _, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: permanent, Source: source, RequestID: "login-new"}); err != nil {
		t.Fatalf("exact whitespace password did not verify: %v", err)
	}
	if err := identity.ValidateNewPassword(permanent); err != nil {
		t.Fatalf("test password does not meet policy: %v", err)
	}
}

func TestIntegrationCancelledLoginDoesNotReturnSession(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	service, err := New(t.Context(), databaseURL, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := service.Login(ctx, LoginInput{Login: "unknown", Password: "short", Source: netip.MustParseAddr("192.0.2.2"), RequestID: "cancel"})
	if err == nil || result.Secret != "" {
		t.Fatalf("cancelled login returned_secret=%t err=%v", result.Secret != "", err)
	}
}

func TestIntegrationConcurrentBootstrapAndRemovedPasswordFile(t *testing.T) {
	_, service, _ := newIntegrationService(t)
	password := "Concurrent bootstrap password 681!"
	path := integrationProtectedFile(t, password, 0o600)
	const callers = 5
	start := make(chan struct{})
	results := make(chan storage.InitialOwnerResult, callers)
	errorsOut := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			result, err := service.EnsureInitialOwner(t.Context(), "owner", path, "bootstrap-concurrent")
			results <- result
			errorsOut <- err
		})
	}
	close(start)
	var winner string
	created := 0
	for range callers {
		result, err := <-results, <-errorsOut
		if err != nil {
			t.Fatalf("concurrent bootstrap failed: %v", err)
		}
		if result.Created {
			created++
		}
		if winner == "" {
			winner = result.State.AdministratorID
		}
		if result.State.AdministratorID != winner {
			t.Fatal("concurrent bootstrap returned multiple owners")
		}
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("Created=true calls = %d, want 1", created)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	repeat, err := service.EnsureInitialOwner(t.Context(), "invalid login", "/removed/password", "")
	if err != nil || repeat.Created || repeat.State.AdministratorID != winner {
		t.Fatalf("repeat after file removal = %+v, %v", repeat, err)
	}
}

func TestIntegrationBootstrapRejectsMissingUnsafeWeakAndCommonCredentials(t *testing.T) {
	_, service, _ := newIntegrationService(t)
	for name, path := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "missing"),
		"unsafe":  integrationProtectedFile(t, "A sufficiently long password 441!", 0o640),
		"weak":    integrationProtectedFile(t, "too short", 0o600),
		"common":  integrationProtectedFile(t, "passwordpassword", 0o600),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.EnsureInitialOwner(t.Context(), "owner", path, "bad-bootstrap")
			if !errors.Is(err, ErrInitialCredentials) {
				t.Fatalf("bootstrap error = %v, want initial credentials", err)
			}
		})
	}
	state, err := storage.ReadInitialOwnerState(t.Context(), service.databaseURL)
	if err != nil || state.Provisioned {
		t.Fatalf("state after invalid bootstrap = %+v, %v", state, err)
	}
}

func TestIntegrationOrdinaryRotationAndFailedProofPreserveCredentials(t *testing.T) {
	databaseURL, service, _ := newIntegrationService(t)
	owner, _ := integrationOwner(t, service, "Ordinary rotation starting password 386!")
	initial := "passwordpassword"
	verifier, err := identity.NewPasswordHasher().Hash(t.Context(), initial)
	if err != nil {
		t.Fatal("hash existing common password fixture failed")
	}
	query := testpostgres.Connect(t, databaseURL)
	if _, err := query.Exec(t.Context(), "UPDATE public.almanac_users SET password_verifier=$1 WHERE id=$2", verifier, owner.State.AdministratorID); err != nil {
		t.Fatal("install existing common password fixture failed")
	}
	source := netip.MustParseAddr("192.0.2.31")
	pending, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: initial, Source: source, RequestID: "login-pending"})
	if err != nil {
		t.Fatal(err)
	}
	permanent := "Ordinary rotation permanent phrase 913!"
	firstRotation, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: pending.Secret, CurrentPassword: initial, NewPassword: permanent, Source: source, RequestID: "complete-onboarding"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: permanent, Source: source, RequestID: "login-normal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: second.Secret, CurrentPassword: "wrong password candidate", NewPassword: "Another permanent password 674!", Source: source, RequestID: "wrong-current"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong current password = %v", err)
	}
	unchanged, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: second.Secret, CurrentPassword: permanent, NewPassword: permanent, Source: source, RequestID: "unchanged"})
	if !errors.Is(err, ErrPasswordUnchanged) || unchanged.Secret != "" {
		t.Fatalf("equal password returned_secret=%t err=%v", unchanged.Secret != "", err)
	}
	common, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: second.Secret, CurrentPassword: permanent, NewPassword: "passwordpassword", Source: source, RequestID: "common"})
	if !errors.Is(err, identity.ErrCommonPassword) || common.Secret != "" {
		t.Fatalf("common password returned_secret=%t err=%v", common.Secret != "", err)
	}
	query = testpostgres.Connect(t, databaseURL)
	var revision int64
	var sessions int
	if err := query.QueryRow(t.Context(), "SELECT credential_revision FROM public.almanac_users WHERE id=$1", owner.State.AdministratorID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions WHERE user_id=$1", owner.State.AdministratorID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	var failedAttempts int
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_login_attempts WHERE state='failed'").Scan(&failedAttempts); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || sessions != 2 || failedAttempts != 1 {
		t.Fatalf("state after failed proofs and successful completion: revision=%d sessions=%d failed_attempts=%d", revision, sessions, failedAttempts)
	}
	finalPassword := "Ordinary rotation replacement phrase 799!"
	third, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: second.Secret, CurrentPassword: permanent, NewPassword: finalPassword, Source: source, RequestID: "rotate-ordinary"})
	if err != nil || third.Secret == "" {
		t.Fatalf("ordinary rotation = %+v, %v", third.Access, err)
	}
	if _, err := service.Authenticate(t.Context(), second.Secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old normal session authentication = %v", err)
	}
	if _, err := service.Authenticate(t.Context(), firstRotation.Secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("older session authentication = %v", err)
	}
	if _, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: permanent, Source: source, RequestID: "login-replaced"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old password login = %v", err)
	}
	if _, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: finalPassword, Source: source, RequestID: "login-final"}); err != nil {
		t.Fatalf("new password login = %v", err)
	}
}

func TestIntegrationCompletedProofLeaseExpiryDoesNotIssueSession(t *testing.T) {
	databaseURL, service, clock := newIntegrationService(t)
	password := "Lease expiry proof password 325!"
	integrationOwner(t, service, password)
	service.hasher = controlledHasher{base: service.hasher, afterVerify: func(_ context.Context, _, _ string, verified bool) (bool, error) {
		clock.Advance(time.Minute + time.Second)
		return verified, nil
	}}
	result, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: password, Source: netip.MustParseAddr("192.0.2.42"), RequestID: "expired-proof"})
	if !errors.Is(err, ErrAttemptExpired) || result.Secret != "" {
		t.Fatalf("expired proof returned_secret=%t err=%v", result.Secret != "", err)
	}
	query := testpostgres.Connect(t, databaseURL)
	var count int
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("sessions after expired proof=%d err=%v", count, err)
	}
}

func TestIntegrationCredentialEpochRaceDeniesLoginAndRotation(t *testing.T) {
	t.Run("login issuance", func(t *testing.T) {
		databaseURL, service, _ := newIntegrationService(t)
		password := "Epoch race login password 315!"
		owner, _ := integrationOwner(t, service, password)
		query := testpostgres.Connect(t, databaseURL)
		service.hasher = controlledHasher{base: service.hasher, afterVerify: func(_ context.Context, _, _ string, verified bool) (bool, error) {
			if !verified {
				return false, nil
			}
			if _, err := query.Exec(t.Context(), "UPDATE public.almanac_users SET credential_revision=credential_revision+1 WHERE id=$1", owner.State.AdministratorID); err != nil {
				return false, err
			}
			return verified, nil
		}}
		result, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: password, Source: netip.MustParseAddr("192.0.2.53"), RequestID: "epoch-login-race"})
		if !errors.Is(err, ErrUnauthenticated) || result.Secret != "" {
			t.Fatalf("login revision race returned_secret=%t err=%v", result.Secret != "", err)
		}
		var sessions int
		if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions WHERE user_id=$1", owner.State.AdministratorID).Scan(&sessions); err != nil || sessions != 0 {
			t.Fatalf("sessions after epoch race=%d err=%v", sessions, err)
		}
	})
	t.Run("rotation authority", func(t *testing.T) {
		databaseURL, service, _ := newIntegrationService(t)
		initial := "Epoch race rotation initial 385!"
		owner, _ := integrationOwner(t, service, initial)
		source := netip.MustParseAddr("192.0.2.54")
		pending, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: initial, Source: source, RequestID: "epoch-pending"})
		if err != nil {
			t.Fatal(err)
		}
		permanent := "Epoch race rotation permanent 747!"
		normal, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: pending.Secret, CurrentPassword: initial, NewPassword: permanent, Source: source, RequestID: "epoch-first-rotate"})
		if err != nil {
			t.Fatal(err)
		}
		query := testpostgres.Connect(t, databaseURL)
		service.hasher = controlledHasher{base: service.hasher, afterVerify: func(_ context.Context, _, _ string, verified bool) (bool, error) {
			if !verified {
				return false, nil
			}
			if _, err := query.Exec(t.Context(), "UPDATE public.almanac_installation SET credential_epoch=gen_random_uuid() WHERE singleton"); err != nil {
				return false, err
			}
			return verified, nil
		}}
		result, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: normal.Secret, CurrentPassword: permanent, NewPassword: "Epoch race replacement phrase 688!", Source: source, RequestID: "epoch-rotation-race"})
		if !errors.Is(err, ErrUnauthenticated) || result.Secret != "" {
			t.Fatalf("rotation epoch race returned_secret=%t err=%v", result.Secret != "", err)
		}
		var revision int64
		if err := query.QueryRow(t.Context(), "SELECT credential_revision FROM public.almanac_users WHERE id=$1", owner.State.AdministratorID).Scan(&revision); err != nil || revision != 2 {
			t.Fatalf("revision after denied race=%d err=%v", revision, err)
		}
	})
}

func TestIntegrationMalformedAndCancelledInputsReturnNoSecrets(t *testing.T) {
	_, service, _ := newIntegrationService(t)
	password := "Malformed input test password 491!"
	integrationOwner(t, service, password)
	secretSentinel := "session-secret-sentinel"
	result, err := service.Login(t.Context(), LoginInput{Login: strings.Repeat("x", 257), Password: password, Source: netip.MustParseAddr("192.0.2.61"), RequestID: "input"})
	if !errors.Is(err, ErrInvalidInput) || result.Secret != "" {
		t.Fatalf("oversized login returned_secret=%t err=%v", result.Secret != "", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = service.Login(canceled, LoginInput{Login: "owner", Password: password, Source: netip.MustParseAddr("192.0.2.62"), RequestID: "cancel"})
	if err == nil || result.Secret != "" {
		t.Fatalf("cancelled operation returned_secret=%t err=%v", result.Secret != "", err)
	}
	encoded, err := json.Marshal(SessionResult{Secret: secretSentinel})
	leaked := strings.Contains(string(encoded), secretSentinel)
	if err != nil || leaked {
		t.Fatalf("session JSON leaked_secret=%t marshal_error=%v", leaked, err)
	}
	if strings.Contains(ErrInitialCredentials.Error(), password) || strings.Contains(ErrUnauthenticated.Error(), secretSentinel) {
		t.Fatal("authentication error exposed synthetic credential")
	}
}

func TestIntegrationSharedRateLimitCoversLoginAndCurrentPasswordProof(t *testing.T) {
	databaseURL, service, clock := newIntegrationService(t)
	initial := "Admission control initial password 512!"
	owner, _ := integrationOwner(t, service, initial)
	source := netip.MustParseAddr("198.51.100.21")
	pending, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: initial, Source: source, RequestID: "admission-pending"})
	if err != nil {
		t.Fatal(err)
	}
	permanent := "Admission control permanent phrase 803!"
	normal, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: pending.Secret, CurrentPassword: initial, NewPassword: permanent, Source: source, RequestID: "admission-rotate"})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 5 {
		result, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: "incorrect candidate", Source: netip.AddrFrom4([4]byte{198, 51, 100, byte(attempt + 30)}), RequestID: fmt.Sprintf("admission-fail-%d", attempt)})
		if !errors.Is(err, ErrUnauthenticated) || result.Secret != "" {
			t.Fatalf("failed admission attempt %d returned_secret=%t err=%v", attempt, result.Secret != "", err)
		}
		clock.Advance(time.Second)
	}
	var latestFailure time.Time
	query := testpostgres.Connect(t, databaseURL)
	if err := query.QueryRow(t.Context(), `SELECT min(expires_at) FROM public.almanac_login_attempts
WHERE account_key=sha256(convert_to(lower('owner'), 'UTF8')) AND state='failed'`).Scan(&latestFailure); err != nil {
		t.Fatal(err)
	}
	blockedAt := clock.Now()
	_, err = service.Login(t.Context(), LoginInput{Login: "owner", Password: permanent, Source: netip.MustParseAddr("198.51.100.88"), RequestID: "admission-blocked-login"})
	limit, ok := errors.AsType[*RateLimitError](err)
	if !ok || limit.RetryAfter != latestFailure.Sub(blockedAt) || limit.RetryAfter <= 0 {
		t.Fatalf("blocked login error = %v; expected retry %s", err, latestFailure.Sub(blockedAt))
	}
	_, err = service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: normal.Secret, CurrentPassword: permanent, NewPassword: "Admission control replacement phrase 605!", Source: netip.MustParseAddr("198.51.100.89"), RequestID: "admission-blocked-rotation"})
	limit, ok = errors.AsType[*RateLimitError](err)
	if !ok || limit.RetryAfter != latestFailure.Sub(blockedAt) {
		t.Fatalf("blocked current-password proof error = %v", err)
	}
	var revision int64
	if err := query.QueryRow(t.Context(), "SELECT credential_revision FROM public.almanac_users WHERE id=$1", owner.State.AdministratorID).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("rate-limited mutation revision=%d err=%v", revision, err)
	}
}

func TestIntegrationAdministratorProofThrottlePreventsPasswordWork(t *testing.T) {
	_, service, _ := newIntegrationService(t)
	initial := "Administrator proof throttle initial 513!"
	integrationOwner(t, service, initial)
	source := netip.MustParseAddr("192.0.2.171")
	pending, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: initial, Source: source, RequestID: "proof-throttle-pending"})
	if err != nil {
		t.Fatal("create restricted session failed")
	}
	permanent := "Administrator proof throttle permanent 804!"
	normal, err := service.ChangePassword(t.Context(), ChangePasswordInput{
		SessionSecret: pending.Secret, CurrentPassword: initial, NewPassword: permanent,
		Source: source, RequestID: "proof-throttle-complete-onboarding",
	})
	if err != nil {
		t.Fatal("complete administrator onboarding failed")
	}
	var verified atomic.Int64
	service.hasher = controlledHasher{base: service.hasher, afterVerify: func(_ context.Context, _, _ string, result bool) (bool, error) {
		verified.Add(1)
		return result, nil
	}}
	for attempt := range 5 {
		failed, err := service.Login(t.Context(), LoginInput{
			Login: "owner", Password: "incorrect password",
			Source:    netip.AddrFrom4([4]byte{192, 0, 2, byte(attempt + 20)}),
			RequestID: fmt.Sprintf("proof-throttle-failure-%d", attempt),
		})
		if !errors.Is(err, ErrUnauthenticated) || failed.Secret != "" {
			t.Fatal("failed login did not complete normally before account throttle")
		}
	}
	if got := verified.Load(); got != 5 {
		t.Fatalf("password verification count before throttled proof = %d; want 5", got)
	}
	_, err = service.ProveAdministrator(t.Context(), AdministratorProofInput{
		SessionSecret: normal.Secret, CurrentPassword: permanent,
		Source: netip.MustParseAddr("192.0.2.171"), RequestID: "proof-throttle-denied",
	})
	limit, ok := errors.AsType[*RateLimitError](err)
	if !ok || limit.RetryAfter <= 0 {
		t.Fatal("administrator proof did not preserve shared account throttle retry")
	}
	if got := verified.Load(); got != 5 {
		t.Fatalf("rate-limited administrator proof performed password work; verify count = %d", got)
	}
}

func TestIntegrationSharedSourceRateLimitReturnsExactRetry(t *testing.T) {
	databaseURL, service, clock := newIntegrationService(t)
	source := netip.MustParseAddr("198.51.100.101")
	for attempt := range 20 {
		result, err := service.Login(t.Context(), LoginInput{Login: fmt.Sprintf("unknown-%d", attempt), Password: "wrong candidate", Source: source, RequestID: fmt.Sprintf("source-failure-%d", attempt)})
		if !errors.Is(err, ErrUnauthenticated) || result.Secret != "" {
			t.Fatalf("source failure %d returned_secret=%t err=%v", attempt, result.Secret != "", err)
		}
		clock.Advance(time.Second)
	}
	query := testpostgres.Connect(t, databaseURL)
	var earliestExpiry time.Time
	if err := query.QueryRow(t.Context(), `SELECT min(expires_at) FROM public.almanac_login_attempts
WHERE source_key=sha256(convert_to('198.51.100.101', 'UTF8')) AND state='failed'`).Scan(&earliestExpiry); err != nil {
		t.Fatal(err)
	}
	blockedAt := clock.Now()
	_, err := service.Login(t.Context(), LoginInput{Login: "source-limit-final", Password: "wrong candidate", Source: source, RequestID: "source-blocked"})
	limit, ok := errors.AsType[*RateLimitError](err)
	if !ok || limit.RetryAfter != earliestExpiry.Sub(blockedAt) || limit.RetryAfter <= 0 {
		t.Fatalf("source limit error=%v expected_retry=%s", err, earliestExpiry.Sub(blockedAt))
	}

	initial := "Source budget current proof password 214!"
	owner, _ := integrationOwner(t, service, initial)
	otherSource := netip.MustParseAddr("192.0.2.191")
	pending, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: initial, Source: otherSource, RequestID: "source-budget-owner-login"})
	if err != nil {
		t.Fatalf("unthrottled owner login failed: %v", err)
	}
	permanent := "Source budget permanent password 748!"
	normal, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: pending.Secret, CurrentPassword: initial, NewPassword: permanent, Source: otherSource, RequestID: "source-budget-onboard"})
	if err != nil {
		t.Fatalf("unthrottled onboarding rotation failed: %v", err)
	}
	var beforeVerifier string
	var beforeRevision int64
	var beforeSessions int
	var beforeOnboardingComplete bool
	if err := query.QueryRow(t.Context(), `SELECT u.password_verifier, u.credential_revision,
 (SELECT count(*) FROM public.almanac_sessions s WHERE s.user_id=u.id),
 i.onboarding_completed_at IS NOT NULL
FROM public.almanac_users u CROSS JOIN public.almanac_installation i WHERE u.id=$1`, owner.State.AdministratorID).Scan(&beforeVerifier, &beforeRevision, &beforeSessions, &beforeOnboardingComplete); err != nil {
		t.Fatal(err)
	}
	changeAt := clock.Now()
	result, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: normal.Secret, CurrentPassword: permanent, NewPassword: "Source budget replacement password 819!", Source: source, RequestID: "source-budget-blocked-change"})
	limit, ok = errors.AsType[*RateLimitError](err)
	if !ok || limit.RetryAfter != earliestExpiry.Sub(changeAt) || limit.RetryAfter <= 0 || result.Secret != "" {
		t.Fatalf("source-limited current proof returned_secret=%t err=%v expected_retry=%s", result.Secret != "", err, earliestExpiry.Sub(changeAt))
	}
	var afterVerifier string
	var afterRevision int64
	var afterSessions int
	var afterOnboardingComplete bool
	if err := query.QueryRow(t.Context(), `SELECT u.password_verifier, u.credential_revision,
 (SELECT count(*) FROM public.almanac_sessions s WHERE s.user_id=u.id),
 i.onboarding_completed_at IS NOT NULL
FROM public.almanac_users u CROSS JOIN public.almanac_installation i WHERE u.id=$1`, owner.State.AdministratorID).Scan(&afterVerifier, &afterRevision, &afterSessions, &afterOnboardingComplete); err != nil {
		t.Fatal(err)
	}
	if beforeVerifier != afterVerifier || beforeRevision != afterRevision || beforeSessions != afterSessions || beforeOnboardingComplete != afterOnboardingComplete {
		t.Fatalf("source-limited proof changed credential/onboarding state: verifier_same=%t revision_same=%t sessions_same=%t onboarding_same=%t", beforeVerifier == afterVerifier, beforeRevision == afterRevision, beforeSessions == afterSessions, beforeOnboardingComplete == afterOnboardingComplete)
	}
}

func TestIntegrationDatabaseFailurePrecedesBootstrapCredentialRead(t *testing.T) {
	databaseURL, service, _ := newIntegrationService(t)
	query := testpostgres.Connect(t, databaseURL)
	if _, err := query.Exec(t.Context(), "DROP TABLE public.almanac_installation CASCADE"); err != nil {
		t.Fatal(err)
	}
	_, err := service.EnsureInitialOwner(t.Context(), "", filepath.Join(t.TempDir(), "missing"), "")
	if err == nil || errors.Is(err, ErrInitialCredentials) {
		t.Fatalf("database failure = %v; want operational error before credential validation", err)
	}
}

func TestIntegrationUnusableIdentitySnapshotsNeverIssueSession(t *testing.T) {
	for _, mode := range []string{"unknown", "disabled", "null verifier", "unsupported verifier"} {
		t.Run(mode, func(t *testing.T) {
			databaseURL, service, _ := newIntegrationService(t)
			login := "owner"
			if mode != "unknown" {
				integrationOwner(t, service, "Snapshot proof password 817!")
			}
			query := testpostgres.Connect(t, databaseURL)
			switch mode {
			case "disabled":
				if _, err := query.Exec(t.Context(), "UPDATE public.almanac_users SET enabled=false"); err != nil {
					t.Fatal(err)
				}
			case "null verifier":
				if _, err := query.Exec(t.Context(), "UPDATE public.almanac_users SET password_verifier=NULL"); err != nil {
					t.Fatal(err)
				}
			case "unsupported verifier":
				if _, err := query.Exec(t.Context(), "UPDATE public.almanac_users SET password_verifier='$argon2id$unsupported'"); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.Login(t.Context(), LoginInput{Login: login, Password: "synthetic candidate", Source: netip.MustParseAddr("203.0.113.72"), RequestID: "snapshot-check"})
			if !errors.Is(err, ErrUnauthenticated) || result.Secret != "" {
				t.Fatalf("%s login returned_secret=%t err=%v", mode, result.Secret != "", err)
			}
			var sessions int
			if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions").Scan(&sessions); err != nil || sessions != 0 {
				t.Fatalf("%s session count=%d err=%v", mode, sessions, err)
			}
		})
	}
}
