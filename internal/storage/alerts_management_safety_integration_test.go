//go:build integration

package storage

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationAlertManagementAcknowledgementWaitTime(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Acknowledgement timing")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := dbgen.New(tx).LockAlertOccurrence(t.Context(), mustPGUUID(opened.Occurrence.ID)); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(f.at.Add(time.Second).UnixNano())
	input := AcknowledgeAlertInput{AlertCommandInput: alertManagementCommand(f, "wait-time-ack"), ID: opened.Occurrence.ID, ExpectedVersion: opened.Version}
	input.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	type outcome struct {
		result AlertCommandResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, input)
		done <- outcome{result, err}
	}()
	observer := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_alert_occurrences WHERE id=%FOR UPDATE%")
	completed := f.at.Add(2 * time.Second)
	clock.Store(completed.UnixNano())
	// Release an unchanged occurrence: a version conflict must not hide whether
	// acknowledgement samples time after this authority wait.
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.result.Alert == nil {
		t.Fatalf("acknowledgement: %+v %v", got.result, got.err)
	}
	occurrence := got.result.Alert.Occurrence
	if !occurrence.AcknowledgedAt.Equal(completed) || !occurrence.UpdatedAt.Equal(completed) {
		t.Fatalf("acknowledgement recorded pre-lock time: ack=%s updated=%s want=%s", occurrence.AcknowledgedAt, occurrence.UpdatedAt, completed)
	}
	var effective, detected, audited time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT t.effective_at,t.detected_at,a.occurred_at
FROM almanac_alert_transitions t JOIN almanac_audit_events a ON a.request_id=t.request_id
WHERE t.occurrence_id=$1 AND t.cause='acknowledged' AND a.action='alert.acknowledged'`, opened.Occurrence.ID).Scan(&effective, &detected, &audited); err != nil {
		t.Fatal(err)
	}
	if !effective.Equal(completed) || !detected.Equal(completed) || !audited.Equal(completed) {
		t.Fatalf("transition/audit lost completion time: %s/%s/%s", effective, detected, audited)
	}
	clock.Store(completed.Add(time.Second).UnixNano())
	replay, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, input)
	if err != nil || !replay.Alert.Occurrence.AcknowledgedAt.Equal(completed) || replay.Alert.Version != got.result.Alert.Version {
		t.Fatalf("replay replaced original acknowledgement time: %+v %v", replay, err)
	}
}

func TestIntegrationAlertManagementEvidenceDigest(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Evidence digest")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	// Append a deliberately damaged evaluation; do not update immutable history
	// or bypass its trigger. Its identity/cutoff fields agree, but input digest
	// still describes the original input. The active latest pointer may advance.
	corruptID := newAlertID()
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_alert_evaluations
(id,policy_id,revision,scheduled_at,effective_cutoff,truth,source_generation,exclusion_revision,claim_epoch,claim_token,input_digest,evidence,result,committed_at)
SELECT $2,policy_id,revision,scheduled_at+interval '1 minute',effective_cutoff+interval '1 minute',truth,source_generation,exclusion_revision,claim_epoch,claim_token,input_digest,
jsonb_set(jsonb_set(evidence,'{Input,Snapshot,Cutoff}',to_jsonb(effective_cutoff+interval '1 minute')),'{Evaluation,Snapshot,Cutoff}',to_jsonb(effective_cutoff+interval '1 minute')),result,committed_at+interval '1 minute'
FROM almanac_alert_evaluations WHERE id=$1`, opened.EvaluationID, corruptID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_alert_occurrences SET latest_evaluation_id=$2 WHERE id=$1`, opened.Occurrence.ID, corruptID); err != nil {
		t.Fatal(err)
	}
	input := ReadAlertEvidenceInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "digest-read").Proof, ID: opened.Occurrence.ID, Now: f.clock}, Selection: "latest"}
	if result, err := ReadAlertEvidence(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertInvalid) || result.ID != "" {
		t.Fatalf("corrupt evidence was exposed: %+v %v", result, err)
	}
	input.Selection = "firing"
	if result, err := ReadAlertEvidence(t.Context(), f.f.databaseURL, input); err != nil || result.ID != opened.EvaluationID {
		t.Fatalf("corruption replaced immutable firing evidence: %+v %v", result, err)
	}
}

func TestIntegrationAlertManagementRestrictedReplay(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	input := alertManagementCreate(f, "restricted-replay")
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sessions SET restricted=true WHERE verifier=$1`, input.Proof.SessionVerifier[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("restricted replay: %v", err)
	}
	lookup := ReadAlertCommandInput{Runtime: f.runtime, Proof: input.Proof, Operation: "alert-policy.create", IdempotencyKey: input.IdempotencyKey, Now: f.clock}
	if _, err := ReadAlertCommand(t.Context(), f.f.databaseURL, lookup); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("restricted receipt lookup: %v", err)
	}
}

func TestIntegrationAlertManagementReplayExpiresDuringWait(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"session", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			input := alertManagementCreate(f, "expiry-replay")
			if kind == "receipt" {
				input.ReceiptLifetime = 10 * time.Second
			}
			if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); err != nil {
				t.Fatal(err)
			}
			if kind == "session" {
				if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sessions SET idle_minutes=1,last_seen_at=$2 WHERE verifier=$1`, input.Proof.SessionVerifier[:], f.at.Add(-50*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			if _, err := dbgen.New(tx).FindAgentLifecycleReceipt(t.Context(), dbgen.FindAgentLifecycleReceiptParams{ActorID: mustPGUUID(f.f.userID), Operation: "alert-policy.create", IdempotencyKey: input.IdempotencyKey}); err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64
			clock.Store(f.at.UnixNano())
			input.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			done := make(chan error, 1)
			go func() { _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); done <- err }()
			observer := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(observer)
			waitForAgentQueryWait(t, observer, "%FROM public.almanac_management_receipts%FOR UPDATE%")
			clock.Store(f.at.Add(20 * time.Second).UnixNano())
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := ErrInvalidSession
			if kind == "receipt" {
				want = ErrAlertReceiptExpired
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("%s expiry replay=%v want=%v", kind, err, want)
			}
			var policies, receipts, audits int
			if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_policies),(SELECT count(*) FROM almanac_management_receipts WHERE operation='alert-policy.create'),(SELECT count(*) FROM almanac_audit_events WHERE action='alert-policy.created')`).Scan(&policies, &receipts, &audits); err != nil || policies != 1 || receipts != 1 || audits != 1 {
				t.Fatalf("expired replay changed effects: %d/%d/%d %v", policies, receipts, audits, err)
			}
		})
	}
}
