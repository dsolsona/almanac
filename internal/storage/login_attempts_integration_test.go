//go:build integration

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationLoginAdmissionEnforcesAccountAndSourceLimits(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 123456789, time.UTC)
	for attempt := range 5 {
		admission := beginFailedLogin(t, databaseURL, "Shared@Example", netip.AddrFrom4([4]byte{192, 0, 2, byte(attempt + 1)}), now.Add(time.Duration(attempt)*time.Second))
		if attempt == 0 && !admission.ExpiresAt.Equal(now.Truncate(time.Microsecond).Add(time.Minute)) {
			t.Fatalf("reservation expiry = %v; want normalized time plus one minute", admission.ExpiresAt)
		}
	}
	accountDenied, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "shared@example", Source: netip.MustParseAddr("192.0.2.99"), Now: now.Add(5 * time.Second),
	})
	if err != nil || accountDenied.Allowed || accountDenied.AttemptID != "" || !accountDenied.ExpiresAt.IsZero() || accountDenied.RetryAfter != 15*time.Minute-5*time.Second {
		t.Fatalf("sixth account attempt = %+v, %v; want account denial with 14m55s hint", accountDenied, err)
	}
	if countLoginAttempts(t, databaseURL) != 5 {
		t.Fatal("account denial inserted or removed an attempt")
	}
	againDenied, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "shared@example", Source: netip.MustParseAddr("192.0.2.98"), Now: now.Add(6 * time.Second),
	})
	if err != nil || againDenied.Allowed || againDenied.RetryAfter != 15*time.Minute-6*time.Second || countLoginAttempts(t, databaseURL) != 5 {
		t.Fatalf("repeated account denial = %+v, %v; want non-extending 14m54s hint and no row", againDenied, err)
	}

	source := netip.MustParseAddr("198.51.100.44")
	for attempt := range 20 {
		beginFailedLogin(t, databaseURL, "source-account-"+string(rune('a'+attempt)), source, now.Add(time.Duration(attempt)*time.Second))
	}
	sourceDenied, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "source-account-new", Source: source, Now: now.Add(20 * time.Second),
	})
	if err != nil || sourceDenied.Allowed || sourceDenied.RetryAfter != 15*time.Minute-20*time.Second {
		t.Fatalf("twenty-first source attempt = %+v, %v; want source denial with 14m40s hint", sourceDenied, err)
	}
	if countLoginAttempts(t, databaseURL) != 25 {
		t.Fatal("source denial inserted or removed an attempt")
	}

	otherAccount, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "different-account", Source: netip.MustParseAddr("203.0.113.14"), Now: now,
	})
	if err != nil || !otherAccount.Allowed {
		t.Fatalf("different account and source admission = %+v, %v; want allowed", otherAccount, err)
	}
	if countLoginAttempts(t, databaseURL) != 26 {
		t.Fatal("allowed independent attempt was not inserted")
	}
}

func TestIntegrationLoginAdmissionCanonicalKeysSecretsAndCompletions(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	earlierFailure := beginFailedLogin(t, databaseURL, "Case@Example", netip.MustParseAddr("192.0.2.77"), now.Add(-time.Minute))
	first, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "Case@Example", Source: netip.MustParseAddr("::ffff:192.0.2.77"), Now: now,
	})
	if err != nil || !first.Allowed {
		t.Fatalf("first mapped admission = %+v, %v", first, err)
	}
	second, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "case@example", Source: netip.MustParseAddr("192.0.2.77"), Now: now,
	})
	if err != nil || !second.Allowed {
		t.Fatalf("case/unmapped admission = %+v, %v", second, err)
	}
	if first.AttemptID != strings.ToLower(first.AttemptID) {
		t.Fatalf("attempt ID is not canonical lowercase UUID: %q", first.AttemptID)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var accountKey, sourceKey []byte
	if err := conn.QueryRow(t.Context(), "SELECT account_key, source_key FROM public.almanac_login_attempts WHERE id=$1", first.AttemptID).Scan(&accountKey, &sourceKey); err != nil {
		t.Fatal("read login digests failed")
	}
	var expectedAccount, expectedSource []byte
	if err := conn.QueryRow(t.Context(), "SELECT sha256(convert_to(lower('Case@Example'), 'UTF8')), sha256(convert_to('192.0.2.77', 'UTF8'))").Scan(&expectedAccount, &expectedSource); err != nil {
		t.Fatal("calculate expected digests failed")
	}
	if len(accountKey) != sha256.Size || len(sourceKey) != sha256.Size || string(accountKey) != string(expectedAccount) || string(sourceKey) != string(expectedSource) {
		t.Fatal("stored login attempt keys are not the canonical PostgreSQL-derived digests")
	}
	var secondAccountKey, secondSourceKey []byte
	if err := conn.QueryRow(t.Context(), "SELECT account_key, source_key FROM public.almanac_login_attempts WHERE id=$1", second.AttemptID).Scan(&secondAccountKey, &secondSourceKey); err != nil {
		t.Fatal("read second canonical-key row failed")
	}
	if string(accountKey) != string(secondAccountKey) || string(sourceKey) != string(secondSourceKey) {
		t.Fatal("case-equivalent login and mapped/unmapped IPv4 did not share canonical keys")
	}
	var matchingUsers int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_users WHERE lower(login)=lower('Case@Example')").Scan(&matchingUsers); err != nil || matchingUsers != 0 {
		t.Fatalf("canonical login unexpectedly matched %d users; expected admission without user lookup", matchingUsers)
	}
	var rawMatches int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_login_attempts
WHERE position(convert_to('Case@Example', 'UTF8') in account_key) > 0
   OR position(convert_to('192.0.2.77', 'UTF8') in source_key) > 0`).Scan(&rawMatches); err != nil || rawMatches != 0 {
		t.Fatalf("raw input appears in persisted keys: matches=%d error=%v", rawMatches, err)
	}
	if finished, err := FinishLoginAttempt(t.Context(), databaseURL, first.AttemptID, true, now); err != nil || !finished {
		t.Fatalf("successful finish = %t, %v", finished, err)
	}
	if finished, err := FinishLoginAttempt(t.Context(), databaseURL, first.AttemptID, false, now); err != nil || finished {
		t.Fatalf("duplicate successful finish = %t, %v; want false", finished, err)
	}
	if countLoginAttempts(t, databaseURL) != 2 {
		t.Fatal("successful finish did not delete only its own reservation and preserve the earlier failure")
	}
	var earlierState string
	var earlierExpiry time.Time
	if err := conn.QueryRow(t.Context(), "SELECT state, expires_at FROM public.almanac_login_attempts WHERE id=$1", earlierFailure.AttemptID).Scan(&earlierState, &earlierExpiry); err != nil || earlierState != "failed" || !earlierExpiry.Equal(now.Add(14*time.Minute)) {
		t.Fatalf("successful completion changed earlier failure state=%q expiry=%v error=%v", earlierState, earlierExpiry, err)
	}
	if finished, err := FinishLoginAttempt(t.Context(), databaseURL, second.AttemptID, false, now.Add(time.Second)); err != nil || !finished {
		t.Fatalf("failed finish = %t, %v", finished, err)
	}
	if finished, err := FinishLoginAttempt(t.Context(), databaseURL, second.AttemptID, false, now.Add(2*time.Second)); err != nil || finished {
		t.Fatalf("duplicate failed finish = %t, %v; want false", finished, err)
	}
	var state string
	var expires time.Time
	if err := conn.QueryRow(t.Context(), "SELECT state, expires_at FROM public.almanac_login_attempts WHERE id=$1", second.AttemptID).Scan(&state, &expires); err != nil {
		t.Fatal("read failed attempt state failed")
	}
	if state != "failed" || !expires.Equal(now.Add(15*time.Minute+time.Second)) {
		t.Fatalf("failed attempt state=%q expires=%v; want failed and completion+15m", state, expires)
	}
	if countLoginAttempts(t, databaseURL) != 2 {
		t.Fatal("failed completion added an event rather than consuming its reservation")
	}
}

func TestIntegrationLoginAdmissionExpiryBoundariesAndRetryDimensions(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for attempt := range 5 {
		beginFailedLogin(t, databaseURL, "boundary-account", netip.AddrFrom4([4]byte{198, 51, 100, byte(attempt + 1)}), start.Add(time.Duration(attempt)*time.Second))
	}
	blocked, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "boundary-account", Source: netip.MustParseAddr("198.51.100.99"), Now: start.Add(5 * time.Second),
	})
	if err != nil || blocked.RetryAfter != 15*time.Minute-5*time.Second {
		t.Fatalf("account retry hint = %+v, %v; want 14m55s", blocked, err)
	}
	boundary := start.Add(15 * time.Minute)
	expiriesBefore := loginAttemptExpirySnapshot(t, databaseURL)
	justBefore, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "boundary-account", Source: netip.MustParseAddr("198.51.100.99"), Now: boundary.Add(-time.Microsecond),
	})
	if err != nil || justBefore.Allowed || justBefore.RetryAfter != time.Microsecond || countLoginAttempts(t, databaseURL) != 5 {
		t.Fatalf("attempt just before expiry = %+v, %v; want denied for 1us without extending rows", justBefore, err)
	}
	if expiriesAfter := loginAttemptExpirySnapshot(t, databaseURL); expiriesAfter != expiriesBefore {
		t.Fatalf("denied attempt changed stored expiries: before=%s after=%s", expiriesBefore, expiriesAfter)
	}
	atBoundary, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "boundary-account", Source: netip.MustParseAddr("198.51.100.99"), Now: boundary,
	})
	if err != nil || !atBoundary.Allowed {
		t.Fatalf("account attempt at exact expiry = %+v, %v; want allowed", atBoundary, err)
	}
	if finished, err := FinishLoginAttempt(t.Context(), databaseURL, atBoundary.AttemptID, true, boundary.Add(time.Minute)); err != nil || finished {
		t.Fatalf("finish at exact reservation expiry = %t, %v; want false", finished, err)
	}
	if finished, err := FinishLoginAttempt(t.Context(), databaseURL, atBoundary.AttemptID, true, boundary.Add(-time.Microsecond)); err != nil || finished {
		t.Fatalf("finish before reservation start = %t, %v; want false", finished, err)
	}
	if finished, err := FinishLoginAttempt(t.Context(), databaseURL, atBoundary.AttemptID, true, boundary); err != nil || !finished {
		t.Fatalf("finish at reservation start = %t, %v; want true", finished, err)
	}

	// A single input can be limited by both dimensions; the hint is the later
	// expiry needed to make each counter strictly less than its threshold.
	bothNow := start.Add(time.Second)
	conn := integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_login_attempts
(account_key, source_key, started_at, expires_at, state)

SELECT sha256(convert_to(lower('both-dimension'),'UTF8')), sha256(convert_to('192.0.2.201','UTF8')), $1::timestamptz, $2::timestamptz, 'failed' FROM generate_series(1,5)
UNION ALL
SELECT sha256(convert_to('other-dimension-' || n::text,'UTF8')), sha256(convert_to('192.0.2.201','UTF8')), $1::timestamptz, $3::timestamptz, 'failed' FROM generate_series(1,15) AS n`, start, bothNow.Add(2*time.Minute), bothNow.Add(time.Minute)); err != nil {
		t.Fatalf("seed account retry dimension failed: %v", err)
	}
	closeIntegrationConn(conn)
	combined, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "both-dimension", Source: netip.MustParseAddr("192.0.2.201"), Now: bothNow,
	})
	if err != nil || combined.Allowed || combined.RetryAfter != 2*time.Minute {
		t.Fatalf("combined retry hint = %+v, %v; want max dimension delay 2m", combined, err)
	}
}

func TestIntegrationLoginAdmissionConcurrentReservationsRespectBothLimits(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	run := func(login string, sourceFor func(int) netip.Addr, count int, sameLogin bool) (allowed int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		var firstErr error
		for i := range count {
			wg.Go(func() {
				<-start
				attemptLogin := login + string(rune('a'+i))
				if sameLogin {
					attemptLogin = login
				}
				result, err := BeginLoginAttempt(ctx, databaseURL, BeginLoginAttemptInput{
					Login: attemptLogin, Source: sourceFor(i), Now: now,
				})
				mu.Lock()
				defer mu.Unlock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				if result.Allowed {
					allowed++
				}
			})
		}
		close(start)
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatalf("concurrent admission group exceeded its 15s bound: %v", ctx.Err())
		}
		if firstErr != nil {
			t.Fatalf("concurrent admission failed: %v", firstErr)
		}
		return allowed
	}
	accountAllowed := run("parallel-account-", func(i int) netip.Addr {
		return netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)})
	}, 24, true)
	if accountAllowed != loginAccountLimit {
		t.Fatalf("concurrent account reservations = %d; want %d", accountAllowed, loginAccountLimit)
	}
	sourceAllowed := run("parallel-source-", func(int) netip.Addr { return netip.MustParseAddr("203.0.113.90") }, 24, false)
	if sourceAllowed != loginSourceLimit {
		t.Fatalf("concurrent source reservations = %d; want %d", sourceAllowed, loginSourceLimit)
	}
}

func TestIntegrationLoginAdmissionUsesFreshSnapshotAfterAdvisoryWait(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	setDisposableDatabaseRepeatableRead(t, databaseURL)
	const login = "repeatable-read-account"
	baseTime := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for attempt := range 4 {
		beginFailedLogin(t, databaseURL, login,
			netip.AddrFrom4([4]byte{198, 51, 100, byte(attempt + 20)}), baseTime)
	}

	keyConn := integrationConnect(t, databaseURL)
	keys, err := dbgen.New(keyConn).LoginAttemptKeys(t.Context(), dbgen.LoginAttemptKeysParams{
		Login: login, Source: "198.51.100.250",
	})
	if err != nil {
		t.Fatal("read repeatable-read account key failed")
	}
	closeIntegrationConn(keyConn)
	accountLockKey := int32(binary.BigEndian.Uint32(keys.AccountKey[:4]))
	blocker := integrationConnect(t, databaseURL)
	blockerTx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal("begin repeatable-read blocker failed")
	}
	if _, err := blockerTx.Exec(t.Context(), "SELECT pg_advisory_xact_lock($1, $2)", loginAccountLockNamespace, accountLockKey); err != nil {
		t.Fatal("acquire repeatable-read test lock failed")
	}
	var blockerPID int32
	if err := blocker.QueryRow(t.Context(), "SELECT pg_backend_pid()::integer").Scan(&blockerPID); err != nil {
		t.Fatal("read repeatable-read blocker PID failed")
	}

	type result struct {
		admission LoginAdmission
		err       error
	}
	const contenders = 4
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	start := make(chan struct{})
	ready := make(chan struct{}, contenders)
	results := make(chan result, contenders)
	var workers sync.WaitGroup
	for attempt := range contenders {
		workers.Go(func() {
			ready <- struct{}{}
			<-start
			admission, err := BeginLoginAttempt(ctx, databaseURL, BeginLoginAttemptInput{
				Login: login, Source: netip.AddrFrom4([4]byte{203, 0, 113, byte(attempt + 1)}), Now: baseTime,
			})
			results <- result{admission: admission, err: err}
		})
	}
	for range contenders {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("contenders did not become ready within bound: %v", ctx.Err())
		}
	}
	close(start)
	observer := integrationConnect(t, databaseURL)
	waitForLoginAdvisoryWaiters(t, ctx, observer, loginAccountLockNamespace, accountLockKey, blockerPID, contenders)
	if err := blockerTx.Rollback(t.Context()); err != nil {
		t.Fatal("release repeatable-read test lock failed")
	}
	closeIntegrationConn(blocker)
	closeIntegrationConn(observer)
	var allowed int
	for range contenders {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("queued repeatable-read admission failed: %v", got.err)
			}
			if got.admission.Allowed {
				allowed++
			}
		case <-ctx.Done():
			t.Fatalf("queued admissions did not finish within bound: %v", ctx.Err())
		}
	}
	workers.Wait()
	if allowed != 1 || countLoginAttempts(t, databaseURL) != loginAccountLimit {
		t.Fatalf("after four queued admissions on repeatable-read default allowed=%d total=%d; want one allowed and five total", allowed, countLoginAttempts(t, databaseURL))
	}
}

func setDisposableDatabaseRepeatableRead(t *testing.T, databaseURL string) {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path == "" {
		t.Fatal("parse disposable database URL failed")
	}
	databaseName := strings.TrimPrefix(parsed.Path, "/")
	admin := integrationConnect(t, os.Getenv("ALMANAC_TEST_DATABASE_URL"))
	if _, err := admin.Exec(t.Context(), "ALTER DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" SET default_transaction_isolation = 'repeatable read'"); err != nil {
		t.Fatal("set disposable database isolation default failed")
	}
	closeIntegrationConn(admin)
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var isolation string
	if err := conn.QueryRow(t.Context(), "SHOW default_transaction_isolation").Scan(&isolation); err != nil || isolation != "repeatable read" {
		t.Fatalf("disposable database default isolation=%q error=%v; want repeatable read", isolation, err)
	}
}

func waitForLoginAdvisoryWaiters(t *testing.T, ctx context.Context, observer *pgx.Conn, namespace, key, blockerPID int32, want int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		count := loginAdvisoryWaiterCount(t, ctx, observer, namespace, key, blockerPID)
		if count >= want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("observed %d/%d waiters for advisory lock (%#x,%#x), blocker PID %d: %v", count, want, namespace, key, blockerPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForNoLoginAdvisoryWaiters(t *testing.T, ctx context.Context, observer *pgx.Conn, namespace, key, blockerPID int32) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		count := loginAdvisoryWaiterCount(t, ctx, observer, namespace, key, blockerPID)
		if count == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%d waiters remain after cancellation for advisory lock (%#x,%#x), blocker PID %d: %v", count, namespace, key, blockerPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func loginAdvisoryWaiterCount(t *testing.T, ctx context.Context, observer *pgx.Conn, namespace, key, blockerPID int32) int {
	t.Helper()
	var count int
	err := observer.QueryRow(ctx, `SELECT count(*)
FROM pg_locks AS waiting
WHERE waiting.locktype='advisory'
  AND waiting.database=(SELECT oid FROM pg_database WHERE datname=current_database())
  AND waiting.classid::bigint=($1::bigint & 4294967295)
  AND waiting.objid::bigint=($2::bigint & 4294967295)
  AND waiting.objsubid=2
  AND NOT waiting.granted
  AND $3 = ANY(pg_blocking_pids(waiting.pid))`, namespace, key, blockerPID).Scan(&count)
	if err != nil {
		t.Fatalf("observe exact advisory-lock wait failed: %v", err)
	}
	return count
}

func backendPID(t *testing.T, conn *pgx.Conn) int32 {
	t.Helper()
	var pid int32
	if err := conn.QueryRow(t.Context(), "SELECT pg_backend_pid()::integer").Scan(&pid); err != nil {
		t.Fatal("read PostgreSQL backend PID failed")
	}
	return pid
}

func TestIntegrationLoginAdmissionPruningBoundAndSkipLocked(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	conn := integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_login_attempts
(account_key, source_key, started_at, expires_at, state)
SELECT sha256(convert_to('expired-account-' || n::text,'UTF8')), sha256(convert_to('expired-source-' || n::text,'UTF8')),
       $1::timestamptz - interval '2 minutes', $1::timestamptz - interval '1 minute', 'failed'
FROM generate_series(1,150) AS n`, now); err != nil {
		t.Fatalf("seed expired attempts failed: %v", err)
	}
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_login_attempts SET started_at=$1::timestamptz - interval '3 minutes', expires_at=$1::timestamptz - interval '2 minutes' WHERE account_key=sha256(convert_to('expired-account-1','UTF8'))", now); err != nil {
		t.Fatal("set skip-locked row earliest expiry failed")
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_login_attempts
(account_key, source_key, started_at, expires_at, state)
VALUES (sha256(convert_to('live-account','UTF8')), sha256(convert_to('live-source','UTF8')), $1::timestamptz, $1::timestamptz + interval '1 minute', 'pending')`, now); err != nil {
		t.Fatalf("seed live attempt failed: %v", err)
	}
	var lockedID string
	if err := conn.QueryRow(t.Context(), `SELECT id::text FROM public.almanac_login_attempts
WHERE account_key=sha256(convert_to('expired-account-1','UTF8'))`).Scan(&lockedID); err != nil {
		t.Fatal("select skip-locked target failed")
	}
	closeIntegrationConn(conn)
	blocker := integrationConnect(t, databaseURL)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal("begin cleanup blocker failed")
	}
	if _, err := tx.Exec(t.Context(), "SELECT id FROM public.almanac_login_attempts WHERE id=$1 FOR UPDATE", lockedID); err != nil {
		t.Fatal("lock expired cleanup target failed")
	}
	if _, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
		Login: "prune-trigger", Source: netip.MustParseAddr("192.0.2.241"), Now: now,
	}); err != nil {
		t.Fatal("admission with bounded cleanup failed")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal("release cleanup blocker failed")
	}
	closeIntegrationConn(blocker)
	conn = integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var expired, live int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_login_attempts WHERE expires_at <= $1", now).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_login_attempts WHERE expires_at > $1", now).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if expired < 50 || expired > 51 || live != 2 {
		t.Fatalf("after bounded pruning expired=%d live=%d; want 50..51 expired (one locked) and two live", expired, live)
	}
	var lockedRemains bool
	if err := conn.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM public.almanac_login_attempts WHERE id=$1)", lockedID).Scan(&lockedRemains); err != nil || !lockedRemains {
		t.Fatalf("locked expired row remains=%t error=%v; want skipped", lockedRemains, err)
	}
}

func TestIntegrationLoginAdmissionLockCancellationAndFaultRollback(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	input := BeginLoginAttemptInput{Login: "lock-cancel", Source: netip.MustParseAddr("192.0.2.250"), Now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	keysConn := integrationConnect(t, databaseURL)
	keys, err := dbgen.New(keysConn).LoginAttemptKeys(t.Context(), dbgen.LoginAttemptKeysParams{Login: input.Login, Source: input.Source.String()})
	if err != nil {
		t.Fatal(err)
	}
	closeIntegrationConn(keysConn)
	lockKey := int32(binary.BigEndian.Uint32(keys.AccountKey[:4]))
	blocker := integrationConnect(t, databaseURL)
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "SELECT pg_advisory_xact_lock($1, $2)", loginAccountLockNamespace, lockKey); err != nil {
		t.Fatal("acquire test account advisory lock failed")
	}
	blockerPID := backendPID(t, blocker)
	ctx, cancel := context.WithCancel(t.Context())
	type beginResult struct {
		admission LoginAdmission
		err       error
	}
	result := make(chan beginResult, 1)
	go func() {
		admission, err := BeginLoginAttempt(ctx, databaseURL, input)
		result <- beginResult{admission: admission, err: err}
	}()
	observer := integrationConnect(t, databaseURL)
	observeCtx, stopObserving := context.WithTimeout(t.Context(), 5*time.Second)
	waitForLoginAdvisoryWaiters(t, observeCtx, observer, loginAccountLockNamespace, lockKey, blockerPID, 1)
	stopObserving()
	lockWaitStarted := time.Now()
	cancel()
	select {
	case got := <-result:
		if got.err == nil || errors.Is(got.err, ErrInvalidLoginAttemptInput) || strings.Contains(got.err.Error(), input.Login) {
			t.Fatalf("cancelled advisory wait error = %v; want safe operational failure", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled advisory waiter did not return within 2s")
	}
	if elapsed := time.Since(lockWaitStarted); elapsed >= 2*time.Second {
		t.Fatalf("cancelled advisory wait took %s; want bounded return under 2s", elapsed)
	}
	cleanupCtx, stopCleanup := context.WithTimeout(t.Context(), 3*time.Second)
	waitForNoLoginAdvisoryWaiters(t, cleanupCtx, observer, loginAccountLockNamespace, lockKey, blockerPID)
	stopCleanup()
	closeIntegrationConn(observer)
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal("release test account lock failed")
	}
	closeIntegrationConn(blocker)
	admission, err := BeginLoginAttempt(t.Context(), databaseURL, input)
	if err != nil || !admission.Allowed {
		t.Fatalf("retry after lock cancellation = %+v, %v", admission, err)
	}

	conn := integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_login_attempt_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'insert blocked'; END $$;
CREATE TRIGGER fail_login_attempt_insert BEFORE INSERT ON public.almanac_login_attempts FOR EACH ROW EXECUTE FUNCTION public.fail_login_attempt_insert()`); err != nil {
		t.Fatal("install test-only insert fault failed")
	}
	closeIntegrationConn(conn)
	faultInput := BeginLoginAttemptInput{Login: "fault-insert", Source: netip.MustParseAddr("192.0.2.251"), Now: input.Now}
	if _, err := BeginLoginAttempt(t.Context(), databaseURL, faultInput); err == nil || strings.Contains(err.Error(), faultInput.Login) {
		t.Fatalf("faulted insert error = %v; want safe operational failure", err)
	}
	conn = integrationConnect(t, databaseURL)
	var rows int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_login_attempts WHERE account_key=sha256(convert_to(lower($1),'UTF8'))", faultInput.Login).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("faulted insert left rows=%d error=%v", rows, err)
	}
	if _, err := conn.Exec(t.Context(), "DROP TRIGGER fail_login_attempt_insert ON public.almanac_login_attempts; DROP FUNCTION public.fail_login_attempt_insert()"); err != nil {
		t.Fatal("remove test-only insert fault failed")
	}
	if admission, err := BeginLoginAttempt(t.Context(), databaseURL, faultInput); err != nil || !admission.Allowed {
		t.Fatalf("retry after rolled back insert = %+v, %v", admission, err)
	}
	closeIntegrationConn(conn)

	conn = integrationConnect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION public.fail_login_attempt_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'update blocked'; END $$;
CREATE TRIGGER fail_login_attempt_update BEFORE UPDATE ON public.almanac_login_attempts FOR EACH ROW EXECUTE FUNCTION public.fail_login_attempt_update()`); err != nil {
		t.Fatal("install test-only update fault failed")
	}
	closeIntegrationConn(conn)
	if _, err := FinishLoginAttempt(t.Context(), databaseURL, admission.AttemptID, false, input.Now.Add(time.Second)); err == nil {
		t.Fatal("faulted completion update unexpectedly succeeded")
	}
	conn = integrationConnect(t, databaseURL)
	var state string
	if err := conn.QueryRow(t.Context(), "SELECT state FROM public.almanac_login_attempts WHERE id=$1", admission.AttemptID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("faulted completion left state=%q error=%v; want pending", state, err)
	}
	if _, err := conn.Exec(t.Context(), "DROP TRIGGER fail_login_attempt_update ON public.almanac_login_attempts; DROP FUNCTION public.fail_login_attempt_update()"); err != nil {
		t.Fatal("remove test-only update fault failed")
	}
	closeIntegrationConn(conn)
	if finished, err := FinishLoginAttempt(t.Context(), databaseURL, admission.AttemptID, false, input.Now.Add(time.Second)); err != nil || !finished {
		t.Fatalf("retry after rolled back completion = %t, %v", finished, err)
	}
}

func TestIntegrationLoginAdmissionSchemaCompatibilityAndMigrationPreservation(t *testing.T) {
	t.Run("schema 3 preserves users installation sessions and audits", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		migrations, err := LoadMigrations(embeddedMigrations)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:3]); err != nil {
			t.Fatal(err)
		}
		conn := integrationConnect(t, databaseURL)
		now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
		var originalInstallationID, originalEpoch string
		if err := conn.QueryRow(t.Context(), "SELECT installation_id::text, credential_epoch::text FROM public.almanac_installation WHERE singleton").Scan(&originalInstallationID, &originalEpoch); err != nil {
			t.Fatal("read schema 3 installation identity failed")
		}
		var userID, sessionID string
		if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_users (login, password_verifier, role, enabled, must_change_password, created_at, updated_at)
VALUES ('preserved-user', $1, 'editor', true, false, $2, $2) RETURNING id::text`, testPasswordVerifier, now).Scan(&userID); err != nil {
			t.Fatal("seed schema 3 user failed")
		}
		if err := conn.QueryRow(t.Context(), `INSERT INTO public.almanac_sessions (user_id, verifier, credential_revision, credential_epoch, restricted, created_at, last_seen_at)
SELECT $1, decode(repeat('01',32),'hex'), 1, credential_epoch, false, $2, $2 FROM public.almanac_installation WHERE singleton RETURNING id::text`, userID, now).Scan(&sessionID); err != nil {
			t.Fatal("seed schema 3 session failed")
		}
		if _, err := conn.Exec(t.Context(), "INSERT INTO public.almanac_audit_events (actor, subject_id, action, outcome, occurred_at, request_id) VALUES ('system', $1, 'test.preserved', 'success', $2, 'schema3')", userID, now); err != nil {
			t.Fatal("seed schema 3 audit failed")
		}
		closeIntegrationConn(conn)
		if result, err := Upgrade(t.Context(), databaseURL); err != nil || result.Applied != latestTestSchemaVersion(t)-3 || result.DatabaseSchemaVersion != latestTestSchemaVersion(t) {
			t.Fatalf("schema 3 to 13 = %+v, %v", result, err)
		}
		conn = integrationConnect(t, databaseURL)
		defer closeIntegrationConn(conn)
		var users, sessions, audits, attempts int
		var savedUser, savedSession string
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_users").Scan(&users); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_sessions").Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE request_id='schema3'").Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_login_attempts").Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(t.Context(), "SELECT id::text FROM public.almanac_users WHERE login='preserved-user'").Scan(&savedUser); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(t.Context(), "SELECT id::text FROM public.almanac_sessions WHERE user_id=$1", userID).Scan(&savedSession); err != nil {
			t.Fatal(err)
		}
		var installationCount int
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_installation WHERE singleton").Scan(&installationCount); err != nil {
			t.Fatal(err)
		}
		var savedInstallationID, savedEpoch string
		if err := conn.QueryRow(t.Context(), "SELECT installation_id::text, credential_epoch::text FROM public.almanac_installation WHERE singleton").Scan(&savedInstallationID, &savedEpoch); err != nil {
			t.Fatal(err)
		}
		if users != 1 || sessions != 1 || audits != 1 || attempts != 0 || installationCount != 1 || savedUser != userID || savedSession != sessionID {
			t.Fatalf("schema 3 data changed users=%d sessions=%d audits=%d attempts=%d installation=%d IDs=%q/%q", users, sessions, audits, attempts, installationCount, savedUser, savedSession)
		}
		if savedInstallationID != originalInstallationID || savedEpoch != originalEpoch {
			t.Fatal("schema 3 upgrade changed installation identity or credential epoch")
		}
		var indexes, checks, notNullColumns int
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND tablename='almanac_login_attempts'").Scan(&indexes); err != nil {
			t.Fatal("inspect login-attempt indexes failed")
		}
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM pg_constraint WHERE conrelid='public.almanac_login_attempts'::regclass AND contype='c'").Scan(&checks); err != nil {
			t.Fatal("inspect login-attempt constraints failed")
		}
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='almanac_login_attempts' AND is_nullable='NO'").Scan(&notNullColumns); err != nil {
			t.Fatal("inspect login-attempt nullability failed")
		}
		if indexes != 4 || checks != 4 || notNullColumns != 6 {
			t.Fatalf("login-attempt schema indexes=%d checks=%d non-null columns=%d; want 4, 4 and 6", indexes, checks, notNullColumns)
		}
	})
	for _, test := range []struct {
		name string
		prep func(*testing.T, string)
	}{
		{name: "missing", prep: func(*testing.T, string) {}},
		{name: "pending", prep: func(t *testing.T, databaseURL string) {
			migrations, err := LoadMigrations(embeddedMigrations)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:3]); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "newer", prep: func(t *testing.T, databaseURL string) {
			if _, err := Upgrade(t.Context(), databaseURL); err != nil {
				t.Fatal(err)
			}
			conn := integrationConnect(t, databaseURL)
			version, filename := integrationMigrationMarker(t, 1, "future")
			if _, err := conn.Exec(t.Context(), "INSERT INTO public.almanac_schema_migrations (version, filename, checksum) VALUES ($1, $2, decode(repeat('ab', 32), 'hex'))", version, filename); err != nil {
				t.Fatal(err)
			}
			closeIntegrationConn(conn)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			databaseURL := integrationDatabase(t)
			test.prep(t, databaseURL)
			_, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{
				Login: "schema-check", Source: netip.MustParseAddr("192.0.2.252"), Now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
			})
			if err == nil || strings.Contains(err.Error(), databasePassword(databaseURL)) || strings.Contains(err.Error(), "schema-check") {
				t.Fatalf("begin on %s schema = %v; want safe fail closed", test.name, err)
			}
			if finished, finishErr := FinishLoginAttempt(t.Context(), databaseURL, "a0b1c2d3-e4f5-4678-9012-3456789abcde", true, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)); finished || finishErr == nil || strings.Contains(finishErr.Error(), databasePassword(databaseURL)) {
				t.Fatalf("finish on %s schema = %t, %v; want safe fail closed", test.name, finished, finishErr)
			}
			conn := integrationConnect(t, databaseURL)
			defer closeIntegrationConn(conn)
			var exists bool
			if err := conn.QueryRow(t.Context(), "SELECT to_regclass('public.almanac_login_attempts') IS NOT NULL").Scan(&exists); err != nil {
				t.Fatal(err)
			}
			wantExists := test.name == "newer"
			if exists != wantExists {
				t.Fatalf("login table exists=%t; want %t for %s schema", exists, wantExists, test.name)
			}
		})
	}
	if _, err := BeginLoginAttempt(t.Context(), "postgres://private:private@127.0.0.1:1/absent?sslmode=disable", BeginLoginAttemptInput{
		Login: "outage-check", Source: netip.MustParseAddr("192.0.2.253"), Now: time.Now(),
	}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("begin during database outage = %v; want safe failure", err)
	}
	if finished, err := FinishLoginAttempt(t.Context(), "postgres://private:private@127.0.0.1:1/absent?sslmode=disable", "a0b1c2d3-e4f5-4678-9012-3456789abcde", true, time.Now()); finished || err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("finish during database outage = %t, %v; want safe failure", finished, err)
	}
}

func beginFailedLogin(t *testing.T, databaseURL, login string, source netip.Addr, now time.Time) LoginAdmission {
	t.Helper()
	admission, err := BeginLoginAttempt(t.Context(), databaseURL, BeginLoginAttemptInput{Login: login, Source: source, Now: now})
	if err != nil || !admission.Allowed {
		t.Fatalf("begin failure attempt for %q from %s = %+v, %v", login, source, admission, err)
	}
	finished, err := FinishLoginAttempt(t.Context(), databaseURL, admission.AttemptID, false, now)
	if err != nil || !finished {
		t.Fatalf("finish failure attempt for %q = %t, %v", login, finished, err)
	}
	return admission
}

func countLoginAttempts(t *testing.T, databaseURL string) int {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var count int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_login_attempts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func loginAttemptExpirySnapshot(t *testing.T, databaseURL string) string {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	var snapshot string
	if err := conn.QueryRow(t.Context(), "SELECT coalesce(json_agg(expires_at ORDER BY expires_at)::text, '[]') FROM public.almanac_login_attempts").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
