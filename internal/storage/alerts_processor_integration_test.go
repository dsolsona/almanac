//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/jackc/pgx/v5"
)

func alertProcessorTestOptions(f alertTestFixture) alertProcessorOptions {
	return alertProcessorOptions{MaxPolicies: 10, Concurrency: 2, Lease: time.Minute, Horizon: 24 * time.Hour, PendingRetry: time.Second, FailureRetry: time.Second, MaxAttempts: 2, Limits: f.limits}
}

func TestIntegrationAlertProcessorLifecycle(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Processor burn")
	f.drain(t, "processor counts")
	options := alertProcessorTestOptions(f)
	first, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock)
	if err != nil || first.Evaluations != 1 || first.Pending != 0 {
		t.Fatalf("first turn: %+v %v", first, err)
	}
	second, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock)
	if err != nil || second.Claimed != 0 {
		t.Fatalf("same tick repeated work: %+v %v", second, err)
	}
	var state string
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT state,(SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1) FROM almanac_alert_occurrences WHERE policy_id=$1`, policy).Scan(&state, &count); err != nil || state != string(alert.Firing) || count != 1 {
		t.Fatalf("persisted lifecycle: state=%s evals=%d: %v", state, count, err)
	}
	// Stop ingestion and move the clock: no-data must preserve this occurrence.
	f.now = f.at.Add(10 * time.Minute)
	if _, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock); err != nil {
		t.Fatal(err)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT state FROM almanac_alert_occurrences WHERE policy_id=$1`, policy).Scan(&state); err != nil || state != string(alert.Firing) {
		t.Fatalf("outage resolved occurrence: %s %v", state, err)
	}
}

func TestIntegrationAlertProcessorPendingAndRetry(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Processor pending")
	options := alertProcessorTestOptions(f)
	first, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock)
	if err != nil || first.Pending != 1 || first.Evaluations != 0 {
		t.Fatalf("pending turn: %+v %v", first, err)
	}
	var next, pin time.Time
	var attempts int
	if err := f.conn.QueryRow(t.Context(), `SELECT next_scheduled_at,pending_cutoff,failure_attempts FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&next, &pin, &attempts); err != nil || !next.Equal(f.at) || !pin.Equal(f.at) || attempts != 0 {
		t.Fatalf("pending boundary lost: next=%s pin=%s failures=%d: %v", next, pin, attempts, err)
	}
	f.drain(t, "processor pending publication")
	f.now = f.at.Add(time.Second)
	second, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
	if err != nil || second.Evaluations != 1 || second.Pending != 0 {
		t.Fatalf("ready retry: %+v %v", second, err)
	}
}

func TestIntegrationAlertProcessorExhaustion(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Processor failure")
	f.drain(t, "processor failure")
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION reject_processor_eval() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture publication failure'; END$$; CREATE TRIGGER reject_processor_eval BEFORE INSERT ON almanac_alert_evaluations FOR EACH ROW EXECUTE FUNCTION reject_processor_eval()`); err != nil {
		t.Fatal(err)
	}
	options := alertProcessorTestOptions(f)
	for attempt := range 2 {
		f.now = f.at.Add(time.Duration(attempt) * time.Second)
		got, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
		if err == nil || got.Failed != 1 {
			t.Fatalf("failure hidden: %+v %v", got, err)
		}
	}
	var exhausted, heldClaim bool
	var attempts, evaluations int
	var next time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT exhausted,failure_attempts,next_scheduled_at,claim_token IS NOT NULL,(SELECT count(*) FROM almanac_alert_evaluations) FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&exhausted, &attempts, &next, &heldClaim, &evaluations); err != nil || !exhausted || attempts != 2 || !next.Equal(f.at) || heldClaim || evaluations != 0 {
		t.Fatalf("exhaustion lost work: exhausted=%v attempts=%d next=%s held=%v eval=%d: %v", exhausted, attempts, next, heldClaim, evaluations, err)
	}
	f.now = f.at.Add(3 * time.Second)
	got, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
	if err != nil || got.Claimed != 0 {
		t.Fatalf("exhausted work retried forever: %+v %v", got, err)
	}
}

func TestIntegrationAlertProcessorInvalidOptions(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Invalid processor")
	options := alertProcessorTestOptions(f)
	options.Concurrency = 0
	if _, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("unbounded invalid options: %v", err)
	}
}

func TestIntegrationAlertProcessorIndependentProgress(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	pending := f.policy(t, "Long pending")
	short := uuid.New().String()
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		return registerAlertPolicy(t.Context(), tx, alertRegistration{Policy: alert.PolicyRevision{ID: short, Revision: 1, Conditions: []alert.Condition{{ID: "short", Short: time.Minute, Long: 2 * time.Minute, Threshold: "1"}}}, SLOID: f.sloID, Name: "Short ready", Severity: "medium", FirstBoundary: f.at, Now: f.now, Limits: f.limits})
	})
	options := alertProcessorTestOptions(f)
	got, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock)
	if err != nil || got.Pending != 1 || got.Evaluations != 1 || got.Claimed != 2 {
		t.Fatalf("one pending policy stalled the other: %+v %v", got, err)
	}
	var next time.Time
	var active string
	if err := f.conn.QueryRow(t.Context(), `SELECT next_scheduled_at FROM almanac_alert_cursors WHERE policy_id=$1`, pending).Scan(&next); err != nil || !next.Equal(f.at) {
		t.Fatalf("pending advanced: %s %v", next, err)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT state FROM almanac_alert_occurrences WHERE policy_id=$1`, short).Scan(&active); err != nil || active != "firing" {
		t.Fatalf("ready policy did not fire: %s %v", active, err)
	}
}

func TestIntegrationAlertProcessorConcurrentTurns(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	for _, name := range []string{"A", "B", "C", "D"} {
		f.policy(t, "Concurrent "+name)
	}
	f.drain(t, "concurrent processor")
	options := alertProcessorTestOptions(f)
	type outcome struct {
		result alertProcessorResult
		err    error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock)
			done <- outcome{result, err}
		}()
	}
	var claimed, evaluations int
	for range 2 {
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		claimed += got.result.Claimed
		evaluations += got.result.Evaluations
	}
	if claimed != 4 || evaluations != 4 {
		t.Fatalf("concurrent duplicate/loss: claims=%d evaluations=%d", claimed, evaluations)
	}
	var persisted int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations`).Scan(&persisted); err != nil || persisted != 4 {
		t.Fatalf("persisted=%d %v", persisted, err)
	}
}

func TestIntegrationAlertProcessorCancellationRecovery(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Cancelled process")
	f.drain(t, "cancelled process")
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION hold_processor_eval() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(7302); RETURN NEW; END$$; CREATE TRIGGER hold_processor_eval BEFORE INSERT ON almanac_alert_evaluations FOR EACH ROW EXECUTE FUNCTION hold_processor_eval()`); err != nil {
		t.Fatal(err)
	}
	gate, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(gate)
	if _, err := gate.Exec(t.Context(), `SELECT pg_advisory_xact_lock(7302)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	options := alertProcessorTestOptions(f)
	done := make(chan error, 1)
	go func() { _, err := f.runtime.processAlertTick(ctx, f.at, options, f.clock); done <- err }()
	observer := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%INSERT INTO public.almanac_alert_evaluations%")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	if err := gate.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count, attempts int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations),failure_attempts FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&count, &attempts); err != nil || count != 0 || attempts != 0 {
		t.Fatalf("cancelled publication persisted: eval=%d attempts=%d %v", count, attempts, err)
	}
	f.now = f.at.Add(2 * time.Minute)
	got, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
	if err != nil || got.Evaluations != 1 {
		t.Fatalf("restart did not recover original work: %+v %v", got, err)
	}
}

func TestIntegrationAlertProcessorAuditedRetry(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Audited retry")
	f.drain(t, "audited retry")
	options := alertProcessorTestOptions(f)
	options.MaxAttempts = 1
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION reject_retry_eval() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture retry failure'; END$$; CREATE TRIGGER reject_retry_eval BEFORE INSERT ON almanac_alert_evaluations FOR EACH ROW EXECUTE FUNCTION reject_retry_eval()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock); err == nil {
		t.Fatal("fixture did not exhaust")
	}
	if _, err := f.conn.Exec(t.Context(), `DROP TRIGGER reject_retry_eval ON almanac_alert_evaluations; CREATE FUNCTION reject_retry_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='alert-policy.retried' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER reject_retry_audit BEFORE INSERT ON almanac_audit_events FOR EACH ROW EXECUTE FUNCTION reject_retry_audit()`); err != nil {
		t.Fatal(err)
	}
	input := alertRetryInput{PolicyID: policy, ActorID: f.f.userID, RequestID: "retry-command", ExpectedVersion: 1, Now: f.clock}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := retryAlertPolicyTx(t.Context(), tx, input); err == nil {
		t.Fatal("audit failure accepted")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var exhausted bool
	if err := f.conn.QueryRow(t.Context(), `SELECT exhausted FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&exhausted); err != nil || !exhausted {
		t.Fatalf("audit failure changed retry state: %v %v", exhausted, err)
	}
	if _, err := f.conn.Exec(t.Context(), `DROP TRIGGER reject_retry_audit ON almanac_audit_events`); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Second)
	alertTestTx(t, f.conn, func(tx pgx.Tx) error { return retryAlertPolicyTx(t.Context(), tx, input) })
	got, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
	if err != nil || got.Evaluations != 1 {
		t.Fatalf("audited retry did not recover: %+v %v", got, err)
	}
	var cutoff time.Time
	var audits int
	if err := f.conn.QueryRow(t.Context(), `SELECT effective_cutoff,(SELECT count(*) FROM almanac_audit_events WHERE subject_id=$1 AND action='alert-policy.retried' AND request_id='retry-command') FROM almanac_alert_evaluations WHERE policy_id=$1`, policy).Scan(&cutoff, &audits); err != nil || !cutoff.Equal(f.at) || audits != 1 {
		t.Fatalf("retry changed boundary or lost audit: %s count=%d: %v", cutoff, audits, err)
	}
}
