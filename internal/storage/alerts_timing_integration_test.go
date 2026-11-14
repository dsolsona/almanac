//go:build integration

package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationAlertDeferralExpiresWhileWaiting(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Deferred expiry")
	claim := f.claim(t, f.at)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := dbgen.New(tx).LockAlertCursor(t.Context(), dbgen.LockAlertCursorParams{PolicyID: mustPGUUID(claim.PolicyID), Revision: claim.Revision}); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(f.at.Add(time.Second).UnixNano())
	readClock := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	done := make(chan error, 1)
	go func() {
		done <- f.runtime.deferAlertWork(t.Context(), claim, readClock, time.Second, "aggregation_pending")
	}()
	observer := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%almanac_alert_cursors%")
	clock.Store(claim.Deadline.Add(time.Second).UnixNano())
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrAlertStale) {
		t.Fatalf("expired deferral changed cursor: %v", err)
	}
	reclaimed, err := f.runtime.claimAlertWork(t.Context(), readClock(), readClock(), time.Minute, 1)
	if err != nil || len(reclaimed) != 1 || !reclaimed[0].ScheduledAt.Equal(claim.ScheduledAt) {
		t.Fatalf("expired work was delayed or advanced: %+v %v", reclaimed, err)
	}
}

func TestIntegrationAlertDeferralRetryElapsesWhileWaiting(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Deferred retry wait")
	claim := f.claim(t, f.at)
	var clock atomic.Int64
	clock.Store(f.at.Add(time.Second).UnixNano())
	readClock := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	pin, err := f.runtime.pinAlertCutoff(t.Context(), claim, readClock)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := dbgen.New(tx).LockAlertCursor(t.Context(), dbgen.LockAlertCursorParams{PolicyID: mustPGUUID(claim.PolicyID), Revision: claim.Revision}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- f.runtime.deferAlertWork(t.Context(), claim, readClock, 10*time.Millisecond, "aggregate_pending")
	}()
	observer := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%almanac_alert_cursors%")
	clock.Store(f.at.Add(time.Second + 20*time.Millisecond).UnixNano())
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("pending retry elapsed during lock wait: %v", err)
	}
	var next, cutoff, available time.Time
	var attempts int
	var held bool
	var reason string
	if err := f.conn.QueryRow(t.Context(), `SELECT next_scheduled_at,pending_cutoff,available_at,failure_attempts,claim_token IS NOT NULL,waiting_reason FROM almanac_alert_cursors WHERE policy_id=$1`, claim.PolicyID).Scan(&next, &cutoff, &available, &attempts, &held, &reason); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(claim.ScheduledAt) || !cutoff.Equal(pin) || !available.Equal(f.at.Add(time.Second+30*time.Millisecond)) || attempts != 0 || held || reason != "aggregate_pending" {
		t.Fatalf("pending lost boundary or retry state: next=%s cutoff=%s available=%s attempts=%d held=%v reason=%s", next, cutoff, available, attempts, held, reason)
	}
}

func TestIntegrationAlertAcknowledgementDuringOccurrenceWait(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"recovery", "disable"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			policy := f.policy(t, "Occurrence wait")
			claim := f.claim(t, f.at)
			opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
			if err != nil {
				t.Fatal(err)
			}
			claim = f.claim(t, f.at.Add(time.Minute))
			input := f.input(t, claim, alert.Complete, false)
			tx, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			if _, err := dbgen.New(tx).LockAlertOccurrence(t.Context(), mustPGUUID(opened.Occurrence.ID)); err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64
			clock.Store(f.at.Add(time.Minute + time.Second).UnixNano())
			readClock := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			type outcome struct {
				occurrence *alert.Occurrence
				err        error
			}
			done := make(chan outcome, 1)
			go func() {
				if operation == "recovery" {
					result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, readClock)
					done <- outcome{result.Occurrence, err}
					return
				}
				var result alertStoredOccurrence
				err := f.runtime.alertTransaction(t.Context(), func(ctx context.Context, tx pgx.Tx, _ *dbgen.Queries) error {
					var err error
					result, err = disableAlertPolicyTx(ctx, tx, alertDisableInput{PolicyID: policy, ActorID: f.f.userID, Reason: "retired", RequestID: "wait-disable", ExpectedVersion: 1, EffectiveAt: claim.ScheduledAt, Now: readClock})
					return err
				})
				done <- outcome{result.Occurrence, err}
			}()
			observer := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(observer)
			waitForAgentQueryWait(t, observer, "%FROM public.almanac_alert_occurrences%FOR UPDATE%")
			ackAt := f.at.Add(time.Minute + 2*time.Second)
			if _, err := acknowledgeAlertTx(t.Context(), tx, alertAcknowledgement{ID: opened.Occurrence.ID, ExpectedVersion: opened.Version, ActorID: f.f.userID, RequestID: "wait-ack", Now: func() time.Time { return ackAt }}); err != nil {
				t.Fatal(err)
			}
			completedAt := ackAt.Add(time.Second)
			clock.Store(completedAt.UnixNano())
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err != nil {
				t.Fatalf("%s rejected acknowledgement that won the lock race: %v", operation, got.err)
			}
			want := alert.Resolved
			if operation == "disable" {
				want = alert.Disabled
			}
			if got.occurrence == nil || got.occurrence.State != want || got.occurrence.AcknowledgedBy != f.f.userID || !got.occurrence.AcknowledgedAt.Equal(ackAt) || !got.occurrence.UpdatedAt.Equal(completedAt) {
				t.Fatalf("lost acknowledgement or post-lock time: %+v", got.occurrence)
			}
		})
	}
}
