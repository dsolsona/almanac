//go:build integration

package storage

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
)

func alertManagementCommand(f alertTestFixture, key string) AlertCommandInput {
	var verifier [32]byte
	verifier[0] = 98
	return AlertCommandInput{Runtime: f.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, IdempotencyKey: key, RequestID: key, Now: f.clock, ReceiptLifetime: time.Hour}
}

func alertManagementCreate(f alertTestFixture, key string) CreateAlertPolicyInput {
	return CreateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, key), Name: "Managed burn", SLOID: f.sloID, Severity: "high", Conditions: []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "1"}}, FirstBoundary: f.at, Limits: f.limits}
}

func TestIntegrationAlertManagementCreateReplay(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	input := alertManagementCreate(f, "managed-create")
	first, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil || first.Policy == nil || first.Policy.Version != 1 {
		t.Fatalf("create: %+v %v", first, err)
	}
	replay, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil || replay.Policy.ID != first.Policy.ID || replay.RequestID != first.RequestID {
		t.Fatalf("replay changed result: %+v %v", replay, err)
	}
	input.Severity = "low"
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertKeyConflict) {
		t.Fatalf("key reused with different body: %v", err)
	}
	var policies, audits int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_policies),(SELECT count(*) FROM almanac_audit_events WHERE action='alert-policy.created')`).Scan(&policies, &audits); err != nil || policies != 1 || audits != 1 {
		t.Fatalf("duplicate effects: policies=%d audits=%d %v", policies, audits, err)
	}
	lookup, err := ReadAlertCommand(t.Context(), f.f.databaseURL, ReadAlertCommandInput{Runtime: f.runtime, Proof: input.Proof, Operation: "alert-policy.create", IdempotencyKey: input.IdempotencyKey, Now: f.clock})
	if err != nil || !lookup.Found || lookup.Expired || lookup.Result.Policy.ID != first.Policy.ID {
		t.Fatalf("reconciliation: %+v %v", lookup, err)
	}
}

func TestIntegrationAlertManagementAcknowledgementReplay(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "managed-ack-create"))
	if err != nil {
		t.Fatal(err)
	}
	f.drain(t, "management ack")
	claim := f.claim(t, f.at)
	prepared, err := f.runtime.readAlertEvidence(t.Context(), claim, time.Time{}, f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, prepared.Input, f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Second)
	input := AcknowledgeAlertInput{AlertCommandInput: alertManagementCommand(f, "managed-ack"), ID: opened.Occurrence.ID, ExpectedVersion: opened.Version}
	ack, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, input)
	if err != nil || ack.Alert == nil || ack.Alert.Occurrence.State != alert.Acknowledged || ack.Alert.Occurrence.AcknowledgedBy != f.f.userID {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	// Recovery happens later; replay must retain the original ack result.
	f.now = f.at.Add(time.Minute)
	claim = f.claim(t, f.now)
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, false), f.limits, f.clock); err != nil {
		t.Fatal(err)
	}
	replay, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, input)
	if err != nil || replay.Alert.Occurrence.State != alert.Acknowledged || replay.Alert.Version != ack.Alert.Version {
		t.Fatalf("replay became current state: %+v %v", replay, err)
	}
	firing, err := ReadAlertEvidence(t.Context(), f.f.databaseURL, ReadAlertEvidenceInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: opened.Occurrence.ID, Now: f.clock}, Selection: "firing"})
	if err != nil || firing.Evaluation.Truth != alert.Breaching || firing.Input.Windows[0].Good != "4950" || firing.ID != opened.EvaluationID {
		t.Fatalf("original firing evidence changed: %+v %v", firing, err)
	}
	latest, err := ReadAlertEvidence(t.Context(), f.f.databaseURL, ReadAlertEvidenceInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: opened.Occurrence.ID, Now: f.clock}, Selection: "latest"})
	if err != nil || latest.Evaluation.Truth != alert.Clear || latest.ID == firing.ID {
		t.Fatalf("latest evidence missing: %+v %v", latest, err)
	}
	current, err := ReadAlert(t.Context(), f.f.databaseURL, ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: opened.Occurrence.ID, Now: f.clock})
	if err != nil || current.Occurrence.State != alert.Resolved || current.OriginalPolicy.Name != "Managed burn" || current.Occurrence.FiringSnapshot.SLOID != f.sloID {
		t.Fatalf("details lost original meaning or SLO link: %+v %v", current, err)
	}
	input.IdempotencyKey, input.RequestID = "fresh-stale-ack", "fresh-stale-ack"
	if _, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("fresh stale acknowledgement accepted: %v", err)
	}
	input.IdempotencyKey, input.RequestID, input.ExpectedVersion = "terminal-ack", "terminal-ack", current.Version
	if _, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("terminal acknowledgement lacks conflict result: %v", err)
	}
	read, err := ReadAlertPolicy(t.Context(), f.f.databaseURL, ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: created.Policy.ID, Now: f.clock})
	if err != nil || read.ID != created.Policy.ID {
		t.Fatalf("policy read: %+v %v", read, err)
	}
}

func TestIntegrationAlertManagementPermissionsAndExpiry(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	input := alertManagementCreate(f, "managed-permissions")
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	setIntegrationUserRole(t, f.f, "viewer")
	if _, err := ReadAlertPolicy(t.Context(), f.f.databaseURL, ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: created.Policy.ID, Now: f.clock}); err != nil {
		t.Fatalf("viewer cannot read: %v", err)
	}
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("viewer replay mutated: %v", err)
	}
	setIntegrationUserRole(t, f.f, "administrator")
	f.now = f.at.Add(time.Hour)
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertReceiptExpired) {
		t.Fatalf("expired key reexecuted: %v", err)
	}
	lookup, err := ReadAlertCommand(t.Context(), f.f.databaseURL, ReadAlertCommandInput{Runtime: f.runtime, Proof: input.Proof, Operation: "alert-policy.create", IdempotencyKey: input.IdempotencyKey, Now: f.clock})
	if err != nil || !lookup.Found || !lookup.Expired || lookup.Result != nil {
		t.Fatalf("expiry reconciliation: %+v %v", lookup, err)
	}
	lookupInput := ReadAlertCommandInput{Runtime: f.runtime, Proof: input.Proof, Operation: "alert-policy.create", IdempotencyKey: "unknown", Now: f.clock}
	lookup, err = ReadAlertCommand(t.Context(), f.f.databaseURL, lookupInput)
	if err != nil || lookup.Found || lookup.Result != nil {
		t.Fatalf("unknown command invented result: %+v %v", lookup, err)
	}
	if _, err := RevokeSession(t.Context(), f.f.databaseURL, input.Proof.SessionVerifier, f.now, "revoke-management"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAlertCommand(t.Context(), f.f.databaseURL, lookupInput); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked session read command: %v", err)
	}
}

func TestIntegrationAlertManagementConcurrentReplay(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	input := alertManagementCreate(f, "concurrent-managed")
	type outcome struct {
		result AlertCommandResult
		err    error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
			done <- outcome{result, err}
		}()
	}
	var id string
	for range 2 {
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		if id != "" && id != got.result.Policy.ID {
			t.Fatal("same key created two policies")
		}
		id = got.result.Policy.ID
	}
	var policies, audits, receipts int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_policies),(SELECT count(*) FROM almanac_audit_events WHERE action='alert-policy.created'),(SELECT count(*) FROM almanac_management_receipts WHERE operation='alert-policy.create')`).Scan(&policies, &audits, &receipts); err != nil || policies != 1 || audits != 1 || receipts != 1 {
		t.Fatalf("duplicate effects: %d/%d/%d %v", policies, audits, receipts, err)
	}
}

func TestIntegrationAlertManagementAtomicity(t *testing.T) {
	for _, failure := range []string{"audit", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			statement := `CREATE FUNCTION reject_managed_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='alert-policy.created' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER reject_managed BEFORE INSERT ON almanac_audit_events FOR EACH ROW EXECUTE FUNCTION reject_managed_audit()`
			if failure == "receipt" {
				statement = `CREATE FUNCTION reject_managed_receipt() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.operation='alert-policy.create' THEN RAISE EXCEPTION 'fixture receipt failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER reject_managed BEFORE INSERT ON almanac_management_receipts FOR EACH ROW EXECUTE FUNCTION reject_managed_receipt()`
			}
			if _, err := f.conn.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "rollback-managed")); err == nil {
				t.Fatal("failed transaction accepted")
			}
			var policies, audits, receipts int
			if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_policies),(SELECT count(*) FROM almanac_audit_events WHERE action='alert-policy.created'),(SELECT count(*) FROM almanac_management_receipts WHERE operation='alert-policy.create')`).Scan(&policies, &audits, &receipts); err != nil || policies != 0 || audits != 0 || receipts != 0 {
				t.Fatalf("partial effects: %d/%d/%d %v", policies, audits, receipts, err)
			}
		})
	}
}

func TestIntegrationAlertManagementExpiryDuringWait(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	input := alertManagementCreate(f, "expiry-managed")
	input.FirstBoundary = f.at.Add(time.Minute)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sessions SET idle_minutes=1,last_seen_at=$2 WHERE verifier=$1`, input.Proof.SessionVerifier[:], f.at.Add(-50*time.Second)); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `SELECT id FROM almanac_slis WHERE id=$1 FOR UPDATE`, f.sliID); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(f.at.UnixNano())
	input.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	done := make(chan error, 1)
	go func() { _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); done <- err }()
	observer := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_slis WHERE id=%FOR SHARE%")
	clock.Store(f.at.Add(20 * time.Second).UnixNano())
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session mutation: %v", err)
	}
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_policies`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expiry left policy: %d %v", count, err)
	}
}

func TestIntegrationAlertManagementActorIsolation(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	input := alertManagementCreate(f, "actor-managed")
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	var otherID string
	if err := f.conn.QueryRow(t.Context(), `INSERT INTO almanac_users(login,password_verifier,role,enabled,must_change_password,created_at,updated_at) VALUES('other-alert-admin',$1,'administrator',true,false,$2,$2) RETURNING id::text`, testPasswordVerifier, f.at).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 97
	if _, err := IssueSession(t.Context(), f.f.databaseURL, IssueSessionInput{UserID: otherID, ExpectedCredentialEpoch: f.f.epoch, ExpectedCredentialRevision: 1, Verifier: verifier, Now: f.at, RequestID: "other-alert-session"}); err != nil {
		t.Fatal(err)
	}
	lookup, err := ReadAlertCommand(t.Context(), f.f.databaseURL, ReadAlertCommandInput{Runtime: f.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, Operation: "alert-policy.create", IdempotencyKey: input.IdempotencyKey, Now: f.clock})
	if err != nil || lookup.Found || lookup.Result != nil {
		t.Fatalf("another actor saw command: %+v %v", lookup, err)
	}
}

func TestIntegrationAlertManagementDisableReplay(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "disable-create"))
	if err != nil {
		t.Fatal(err)
	}
	f.drain(t, "managed disable")
	if _, err := f.runtime.processAlertTick(t.Context(), f.at, alertProcessorTestOptions(f), f.clock); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(2 * time.Second)
	input := DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "disable-command"), ID: created.Policy.ID, ExpectedVersion: 1, EffectiveAt: f.now, Reason: "Retire policy"}
	first, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil || first.Policy.Enabled || first.Policy.Version != 2 || first.Alert == nil || first.Alert.Occurrence.State != alert.Disabled {
		t.Fatalf("disable: %+v %v", first, err)
	}
	replay, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil || replay.Policy.Version != 2 || replay.Alert.Occurrence.ID != first.Alert.Occurrence.ID {
		t.Fatalf("disable replay: %+v %v", replay, err)
	}
	input.IdempotencyKey, input.RequestID = "stale-disable", "stale-disable"
	if _, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("fresh stale disable: %v", err)
	}
}

func TestIntegrationAlertManagementRetryReplay(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "retry-create"))
	if err != nil {
		t.Fatal(err)
	}
	f.drain(t, "managed retry")
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION reject_managed_eval() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture managed failure'; END$$; CREATE TRIGGER reject_managed_eval BEFORE INSERT ON almanac_alert_evaluations FOR EACH ROW EXECUTE FUNCTION reject_managed_eval()`); err != nil {
		t.Fatal(err)
	}
	options := alertProcessorTestOptions(f)
	options.MaxAttempts = 1
	if _, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock); err == nil {
		t.Fatal("fixture did not fail")
	}
	if _, err := f.conn.Exec(t.Context(), `DROP TRIGGER reject_managed_eval ON almanac_alert_evaluations`); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Second)
	input := RetryAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "retry-command"), ID: created.Policy.ID, ExpectedVersion: 1}
	first, err := RetryAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock); err != nil {
		t.Fatal(err)
	}
	replay, err := RetryAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil || replay.Policy.ID != first.Policy.ID {
		t.Fatalf("retry replay rejected after progress: %+v %v", replay, err)
	}
	var audits, evaluations int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_audit_events WHERE action='alert-policy.retried'),(SELECT count(*) FROM almanac_alert_evaluations)`).Scan(&audits, &evaluations); err != nil || audits != 1 || evaluations != 1 {
		t.Fatalf("retry duplicated effects: %d/%d %v", audits, evaluations, err)
	}
}

func TestIntegrationAlertManagementConflictErrors(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	input := alertManagementCreate(f, "conflict-first")
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey, input.RequestID = "conflict-second", "conflict-second"
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertNameConflict) {
		t.Fatalf("duplicate name lacks conflict result: %v", err)
	}
	missing := AcknowledgeAlertInput{AlertCommandInput: alertManagementCommand(f, "missing-ack"), ID: "00000000-0000-0000-0000-000000000001", ExpectedVersion: 1}
	if _, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, missing); !errors.Is(err, ErrAlertNotFound) {
		t.Fatalf("missing occurrence lacks notfound result: %v", err)
	}
}
