//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationAlertProcessorLockBackpressure(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Lock budget")
	f.drain(t, "lock budget source")
	// Exercise the real DerivedTraffic lock_timeout without changing its value.
	// Only evaluation insertion takes this fixture lock; deferral can commit.
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION wait_alert_publication() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(7814); RETURN NEW; END$$;
CREATE TRIGGER wait_alert_publication BEFORE INSERT ON almanac_alert_evaluations FOR EACH ROW EXECUTE FUNCTION wait_alert_publication()`); err != nil {
		t.Fatal(err)
	}
	gate, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(gate)
	if _, err := gate.Exec(t.Context(), `SELECT pg_advisory_xact_lock(7814)`); err != nil {
		t.Fatal(err)
	}
	options := alertProcessorTestOptions(f)
	options.MaxAttempts = 1
	got, processErr := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock)
	cursor, err := dbgen.New(gate).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(policy), Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if failure, ok := errors.AsType[*pgconn.PgError](processErr); ok {
		t.Logf("actual PostgreSQL error=%s failure_attempts=%d exhausted=%v", failure.Code, cursor.FailureAttempts, cursor.Exhausted)
	}
	if processErr != nil || got.Pending != 1 || got.Failed != 0 || cursor.FailureAttempts != 0 || cursor.Exhausted || cursor.ClaimToken.Valid {
		t.Fatalf("lock backpressure consumed failure budget: result=%+v attempts=%d exhausted=%v held=%v err=%v", got, cursor.FailureAttempts, cursor.Exhausted, cursor.ClaimToken.Valid, processErr)
	}
	if !cursor.NextScheduledAt.Time.Equal(f.at) || !cursor.PendingCutoff.Valid || !cursor.PendingCutoff.Time.Equal(f.at) || cursor.LastEvaluationID.Valid || cursor.OpenOccurrenceID.Valid || cursor.WaitingReason != "database_backpressure" || !cursor.AvailableAt.Time.Equal(f.at.Add(options.PendingRetry)) {
		t.Fatalf("deferred work lost identity/retry reason: %+v", cursor)
	}
	if err := gate.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(options.PendingRetry)
	retried, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
	if err != nil || retried.Evaluations != 1 || retried.Failed != 0 {
		t.Fatalf("backpressure retry failed: %+v %v", retried, err)
	}
	var cutoff time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT effective_cutoff FROM almanac_alert_evaluations WHERE policy_id=$1`, policy).Scan(&cutoff); err != nil || !cutoff.Equal(f.at) {
		t.Fatalf("retry changed original cutoff: %s %v", cutoff, err)
	}
}

func TestIntegrationAlertProcessorAdmissionBackpressure(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Admission budget")
	f.drain(t, "admission budget source")
	claim := f.claim(t, f.at)
	if _, err := f.runtime.pinAlertCutoff(t.Context(), claim, f.clock); err != nil {
		t.Fatal(err)
	}
	var held []*pgxpool.Conn
	release := func() {
		for _, conn := range held {
			conn.Release()
		}
		held = nil
	}
	defer release()
	pool := f.runtime.pools[DerivedTraffic]
	for range pool.Config().MaxConns {
		conn, err := f.runtime.Acquire(t.Context(), DerivedTraffic)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	before := pool.Stat().CanceledAcquireCount()
	options := alertProcessorTestOptions(f)
	options.MaxAttempts = 1
	type outcome struct {
		kind string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		kind, err := f.runtime.processAlertClaim(t.Context(), claim, options, f.clock)
		done <- outcome{kind, err}
	}()
	// Wait for the actual bounded acquisition to fail, then restore capacity so
	// the processor can persist its outcome. This is no sleep-based release.
	bounded, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for pool.Stat().CanceledAcquireCount() == before {
		select {
		case <-ticker.C:
		case <-bounded.Done():
			t.Fatal("processor did not reach bounded admission refusal")
		}
	}
	release()
	got := <-done
	cursor, err := dbgen.New(f.conn).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(policy), Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.err != nil || got.kind != "pending" || cursor.FailureAttempts != 0 || cursor.Exhausted || cursor.ClaimToken.Valid {
		t.Fatalf("admission refusal consumed failure budget: kind=%s attempts=%d exhausted=%v held=%v err=%v", got.kind, cursor.FailureAttempts, cursor.Exhausted, cursor.ClaimToken.Valid, got.err)
	}
	if !cursor.NextScheduledAt.Time.Equal(f.at) || !cursor.PendingCutoff.Valid || !cursor.PendingCutoff.Time.Equal(f.at) || cursor.LastEvaluationID.Valid || cursor.OpenOccurrenceID.Valid || cursor.WaitingReason != "database_backpressure" {
		t.Fatalf("admission deferral lost original work: %+v", cursor)
	}
}

func TestIntegrationAlertProcessorUnavailableDeferralAdmission(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Deferral admission")
	f.drain(t, "deferral admission source")
	claim := f.claim(t, f.at)
	if _, err := f.runtime.pinAlertCutoff(t.Context(), claim, f.clock); err != nil {
		t.Fatal(err)
	}
	var held []*pgxpool.Conn
	release := func() {
		for _, conn := range held {
			conn.Release()
		}
		held = nil
	}
	defer release()
	for range f.runtime.pools[DerivedTraffic].Config().MaxConns {
		conn, err := f.runtime.Acquire(t.Context(), DerivedTraffic)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	options := alertProcessorTestOptions(f)
	options.MaxAttempts = 1
	kind, err := f.runtime.processAlertClaim(t.Context(), claim, options, f.clock)
	if kind != "pending" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unpersisted deferral hidden or counted as failure: %s %v", kind, err)
	}
	cursor, err := dbgen.New(f.conn).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(policy), Revision: 1})
	if err != nil || cursor.FailureAttempts != 0 || cursor.Exhausted || !cursor.ClaimToken.Valid || !cursor.NextScheduledAt.Time.Equal(f.at) || !cursor.PendingCutoff.Valid || !cursor.PendingCutoff.Time.Equal(f.at) || cursor.LastEvaluationID.Valid {
		t.Fatalf("unadmitted deferral changed durable work: %+v %v", cursor, err)
	}
	release()
	f.now = claim.Deadline.Add(time.Microsecond)
	got, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
	if err != nil || got.Evaluations != 1 {
		t.Fatalf("expired claim did not recover: %+v %v", got, err)
	}
}
