//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Nonmutating inspection coexists with protected reads while authority writers
// remain fenced. The original exclusive-lock failures are retained privately.
func TestIntegrationInspectSessionConcurrentReadAuthority(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	other := insertDiagnosticActor(t, f, "independent-inspection-user")
	runtime, err := OpenRuntime(t.Context(), f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	ctx := runtime.Bind(t.Context())
	now := f.base.Add(time.Minute)
	clock := func() time.Time { return now }
	observer := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(observer)
	userID, ok := canonicalUUID(f.userID)
	if !ok {
		t.Fatal("fixture user is not a canonical UUID")
	}

	for _, kind := range []string{"protected_read_user_conflict", "session_row_conflict"} {
		t.Run(kind, func(t *testing.T) {
			var holder *readConnection
			var tx pgx.Tx
			var err error
			queryName := "LockSessionUser"
			if kind == "protected_read_user_conflict" {
				holder, _, tx, _, _, _, _, err = beginPooledAgentRead(ctx, f.databaseURL, runtime, f.verifier, clock, pgx.TxOptions{}, "administrator")
			} else {
				queryName = "LockSession"
				holder, err = acquireReadConnection(ctx, f.databaseURL, runtime)
				if err == nil {
					tx, err = holder.Begin(ctx)
				}
				if err == nil {
					_, err = dbgen.New(tx).LockAgentSessionRead(ctx, f.verifier[:])
				}
			}
			if holder != nil {
				defer closeInitialOwnerConnection(holder)
			}
			if tx != nil {
				defer rollbackTransaction(tx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if access, err := InspectSession(ctx, f.databaseURL, other, now); err != nil || access.UserID == f.userID {
				t.Fatalf("separate-user inspection blocked or selected the wrong user: %v", err)
			}
			t.Log("separate-user inspection succeeds while the reader remains open")

			type outcome struct {
				access SessionAccess
				err    error
			}
			result := make(chan outcome, 1)
			began := time.Now()
			go func() {
				access, err := InspectSession(ctx, f.databaseURL, f.verifier, now)
				result <- outcome{access, err}
			}()
			var inspected outcome
			var waiter int32
			var waitEvent string
			observedBlocker := false
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			waiting := true
			for waiting {
				select {
				case inspected = <-result:
					waiting = false
				case <-ticker.C:
					if observedBlocker {
						continue
					}
					err := observer.QueryRow(t.Context(), `SELECT pid,wait_event FROM pg_stat_activity
WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE $1 AND $2::integer=ANY(pg_blocking_pids(pid)) LIMIT 1`,
						"-- name: "+queryName+" :one%", int32(holder.PgConn().PID())).Scan(&waiter, &waitEvent)
					if err != nil && !errors.Is(err, pgx.ErrNoRows) {
						t.Fatal(err)
					}
					observedBlocker = err == nil
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
			}
			t.Logf("inspection query=%s elapsed=%s blocker_observed=%t waiter_pid=%d holder_pid=%d wait_event=%s error=%v",
				queryName, time.Since(began), observedBlocker, waiter, holder.PgConn().PID(), waitEvent, inspected.err)
			if inspected.err != nil && (!observedBlocker || !errors.Is(inspected.err, ErrCancelled)) {
				t.Errorf("inspection failure was not attributed to the held authority lock: %v", inspected.err)
			}

			// Independently retain the PostgreSQL error hidden by operationError.
			probe, err := observer.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(probe)
			if err := setTransactionBounds(ctx, probe); err != nil {
				t.Fatal(err)
			}
			if queryName == "LockSessionUser" {
				_, err = dbgen.New(probe).LockSessionUser(t.Context(), userID)
			} else {
				_, err = dbgen.New(probe).LockSession(t.Context(), f.verifier[:])
			}
			pgErr, isPostgres := errors.AsType[*pgconn.PgError](err)
			if !isPostgres || pgErr.Code != "55P03" {
				t.Fatalf("independent exclusive query did not report lock_not_available: %v", err)
			}
			t.Logf("independent query=%s SQLSTATE=%s", queryName, pgErr.Code)
			rollbackTransaction(probe)
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			access, err := InspectSession(ctx, f.databaseURL, f.verifier, now)
			if err != nil || access.Role != "administrator" || !access.LastSeenAt.Equal(f.base.Add(time.Second)) {
				t.Fatalf("inspection after reader release failed or touched idle time: %+v, %v", access, err)
			}
			if inspected.err != nil {
				t.Errorf("nonmutating inspection must coexist with an authorized read: %v", inspected.err)
			} else if inspected.access.UserID != f.userID || inspected.access.Role != "administrator" || !inspected.access.LastSeenAt.Equal(f.base.Add(time.Second)) {
				t.Errorf("concurrent inspection returned incorrect authority or touched idle time: %+v", inspected.access)
			}
		})
	}

	t.Run("current_authority_and_no_idle_touch", func(t *testing.T) {
		check := func(at time.Time, wantRole string, invalid bool) {
			t.Helper()
			access, err := InspectSession(ctx, f.databaseURL, f.verifier, at)
			if invalid {
				if !errors.Is(err, ErrInvalidSession) {
					t.Fatalf("invalid authority was not rejected: %v", err)
				}
			} else if err != nil || access.Role != wantRole || access.Restricted || !access.LastSeenAt.Equal(f.base.Add(time.Second)) {
				t.Fatalf("inspection authority or idle time differs: %+v, %v", access, err)
			}
		}
		check(now, "administrator", false)
		check(f.base.Add(time.Second).Add(8*time.Hour), "", true)
		// Keep idle validity later than the absolute boundary to test it separately.
		if _, err := observer.Exec(t.Context(), "UPDATE public.almanac_sessions SET last_seen_at=$1 WHERE verifier=$2", f.base.Add(11*time.Hour), f.verifier[:]); err != nil {
			t.Fatal(err)
		}
		check(f.base.Add(time.Second).Add(12*time.Hour), "", true)
		if _, err := observer.Exec(t.Context(), "UPDATE public.almanac_sessions SET last_seen_at=$1 WHERE verifier=$2", f.base.Add(time.Second), f.verifier[:]); err != nil {
			t.Fatal(err)
		}
		for _, mutation := range []struct{ name, change, restore, role string }{
			{"role", "UPDATE public.almanac_users SET role='viewer' WHERE id=$1", "UPDATE public.almanac_users SET role='administrator' WHERE id=$1", "viewer"},
			{"disabled", "UPDATE public.almanac_users SET enabled=false WHERE id=$1", "UPDATE public.almanac_users SET enabled=true WHERE id=$1", ""},
			{"revision", "UPDATE public.almanac_users SET credential_revision=credential_revision+1 WHERE id=$1", "UPDATE public.almanac_users SET credential_revision=credential_revision-1 WHERE id=$1", ""},
			{"epoch", "UPDATE public.almanac_installation SET credential_epoch=gen_random_uuid() WHERE singleton AND credential_epoch=$1", "UPDATE public.almanac_installation SET credential_epoch=$1 WHERE singleton", ""},
		} {
			id := f.userID
			if mutation.name == "epoch" {
				id = f.epoch
			}
			if _, err := observer.Exec(t.Context(), mutation.change, id); err != nil {
				t.Fatal(err)
			}
			check(now, mutation.role, mutation.role == "")
			if _, err := observer.Exec(t.Context(), mutation.restore, id); err != nil {
				t.Fatal(err)
			}
			t.Logf("inspection respects current %s", mutation.name)
		}
		var saved time.Time
		if err := observer.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", f.verifier[:]).Scan(&saved); err != nil || !saved.Equal(f.base.Add(time.Second)) {
			t.Fatalf("inspection extended persisted idle lifetime: %v, %v", saved, err)
		}
		if revoked, err := RevokeSession(ctx, f.databaseURL, f.verifier, now, "inspection-revocation"); err != nil || !revoked {
			t.Fatalf("revoke fixture session: %v", err)
		}
		check(now, "", true)
		t.Log("idle/absolute expiry, revocation and persisted no-touch checks passed")
	})

	for _, change := range []struct{ name, sql string }{
		{"disable", "UPDATE public.almanac_users SET enabled=false WHERE id=$1"},
		{"credential_revision", "UPDATE public.almanac_users SET credential_revision=credential_revision+1 WHERE id=$1"},
		{"installation_epoch", "UPDATE public.almanac_installation SET credential_epoch=gen_random_uuid() WHERE singleton AND credential_epoch=$1"},
		{"session_revoke", "DELETE FROM public.almanac_sessions WHERE verifier=$1"},
	} {
		t.Run("committed_writer_"+change.name, func(t *testing.T) {
			f := newAgentFixture(t)
			runtime, err := OpenRuntime(t.Context(), f.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			writer := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(writer)
			observer := integrationConnect(t, f.databaseURL)
			defer closeIntegrationConn(observer)
			tx, err := writer.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			var argument any = f.userID
			if change.name == "installation_epoch" {
				argument = f.epoch
			} else if change.name == "session_revoke" {
				argument = f.verifier[:]
			}
			if changed, err := tx.Exec(t.Context(), change.sql, argument); err != nil || changed.RowsAffected() != 1 {
				t.Fatalf("stage authority mutation: rows=%d error=%v", changed.RowsAffected(), err)
			}
			result := make(chan error, 1)
			go func() {
				_, err := InspectSession(runtime.Bind(t.Context()), f.databaseURL, f.verifier, f.base.Add(time.Minute))
				result <- err
			}()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			var waiter int32
			var blockedQuery string
		waiting:
			for {
				select {
				case err := <-result:
					t.Fatalf("inspection returned before the authority writer committed: %v", err)
				case <-ticker.C:
					err := observer.QueryRow(t.Context(), `SELECT pid,split_part(query,E'\n',1) FROM pg_stat_activity
WHERE datname=current_database() AND wait_event_type='Lock'
 AND $1::integer=ANY(pg_blocking_pids(pid)) LIMIT 1`, int32(writer.PgConn().PID())).Scan(&waiter, &blockedQuery)
					if err == nil {
						break waiting
					}
					if !errors.Is(err, pgx.ErrNoRows) {
						t.Fatal(err)
					}
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrInvalidSession) {
					t.Fatalf("inspection did not reject committed %s: %v", change.name, err)
				}
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			t.Logf("writer=%s holder_pid=%d waiter_pid=%d query=%s; committed change rejected", change.name, writer.PgConn().PID(), waiter, blockedQuery)
		})
	}
}
