package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrAlertKeyConflict    = errors.New("storage: alert command key body conflict")
	ErrAlertReceiptExpired = errors.New("storage: alert command result expired")
	ErrAlertNotFound       = errors.New("storage: alert resource not found")
	ErrAlertNameConflict   = errors.New("storage: alert policy name already exists")
)

// These storage inputs remain unwired. Public activation/admission/replay
// defaults are not chosen by the management foundation.
type AlertCommandInput struct {
	Runtime                   *Runtime
	Proof                     DatasourceProof
	IdempotencyKey, RequestID string
	Now                       func() time.Time
	ReceiptLifetime           time.Duration
}

type CreateAlertPolicyInput struct {
	AlertCommandInput
	Name, SLOID, Severity string
	Conditions            []alert.Condition
	FirstBoundary         time.Time
	Limits                alert.Limits
}

type AcknowledgeAlertInput struct {
	AlertCommandInput
	ID              string
	ExpectedVersion int64
}

type DisableAlertPolicyInput struct {
	AlertCommandInput
	ID, Reason      string
	ExpectedVersion int64
	EffectiveAt     time.Time
}

type RetryAlertPolicyInput struct {
	AlertCommandInput
	ID              string
	ExpectedVersion int64
}

type AlertPolicyRecord struct {
	ID, Name, SLOID, Severity   string
	Version, EvaluationRevision int64
	Enabled                     bool
	Conditions                  []alert.Condition
	CreatedAt, UpdatedAt        time.Time
}

type AlertOriginalPolicy struct {
	Name, Severity, SLOName, ServiceName, Target string
	RollingWindowSeconds                         int64
}

type AlertRecord struct {
	Occurrence        alert.Occurrence
	Version, Sequence int64
	OriginalPolicy    AlertOriginalPolicy
}

type AlertCommandResult struct {
	FormatVersion        int
	Operation, RequestID string
	Policy               *AlertPolicyRecord
	Alert                *AlertRecord
}

func validAlertOperation(operation string) bool {
	switch operation {
	case "alert-policy.create", "alert.acknowledge", "alert-policy.disable", "alert-policy.retry":
		return true
	default:
		return false
	}
}

func executeAlertCommand(ctx context.Context, databaseURL string, input AlertCommandInput, operation string, request any, work func(context.Context, pgx.Tx, *dbgen.Queries, string) (AlertCommandResult, error)) (AlertCommandResult, error) {
	var empty AlertCommandResult
	if input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) || !validPrintableASCII(input.IdempotencyKey, 1, 128) || !validRequestID(input.RequestID) || input.ReceiptLifetime <= 0 || input.ReceiptLifetime%time.Microsecond != 0 || !validAlertOperation(operation) {
		return empty, ErrAlertInvalid
	}
	encoded, err := marshalAlertData(request)
	if err != nil {
		return empty, err
	}
	digest := sha256.Sum256(encoded)
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{}, "administrator", "editor")
	if err != nil {
		return empty, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", user.ID.String()+":"+operation+":"+input.IdempotencyKey); err != nil {
		return empty, err
	}
	receipt, err := q.FindAgentLifecycleReceipt(ctx, dbgen.FindAgentLifecycleReceiptParams{ActorID: user.ID, Operation: operation, IdempotencyKey: input.IdempotencyKey})
	now := normalizedAgentNow(input.Now)
	if now.IsZero() {
		return empty, ErrAlertInvalid
	}
	if err == nil {
		if !bytes.Equal(receipt.Digest, digest[:]) {
			return empty, ErrAlertKeyConflict
		}
		if !now.Before(receipt.ExpiresAt.Time) {
			return empty, ErrAlertReceiptExpired
		}
		var saved AlertCommandResult
		if err := unmarshalAlertData(receipt.Result, &saved); err != nil || saved.FormatVersion != 1 || saved.Operation != operation || saved.RequestID == "" {
			return empty, ErrAlertInvalid
		}
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return empty, err
		}
		return saved, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	result, err := work(ctx, tx, q, user.ID.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return empty, ErrAlertNotFound
		}
		if errors.Is(err, alert.ErrTransition) {
			return empty, ErrAlertStale
		}
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" && pgErr.ConstraintName == "almanac_alert_policies_name_key" {
			return empty, ErrAlertNameConflict
		}
		return empty, err
	}
	result.FormatVersion, result.Operation, result.RequestID = 1, operation, input.RequestID
	saved, err := marshalAlertData(result)
	if err != nil {
		return empty, err
	}
	now = normalizedAgentNow(input.Now)
	if now.IsZero() {
		return empty, ErrAlertInvalid
	}
	if err := q.InsertAgentLifecycleReceipt(ctx, dbgen.InsertAgentLifecycleReceiptParams{ActorID: user.ID, Operation: operation, IdempotencyKey: input.IdempotencyKey, Digest: digest[:], Result: saved, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(input.ReceiptLifetime))}); err != nil {
		return empty, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return empty, err
	}
	return result, nil
}

func CreateAlertPolicy(ctx context.Context, databaseURL string, input CreateAlertPolicyInput) (AlertCommandResult, error) {
	request := struct {
		Name, SLOID, Severity string
		Conditions            []alert.Condition
		FirstBoundary         time.Time
	}{input.Name, input.SLOID, input.Severity, input.Conditions, input.FirstBoundary}
	return executeAlertCommand(ctx, databaseURL, input.AlertCommandInput, "alert-policy.create", request, func(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, actor string) (AlertCommandResult, error) {
		id := newAlertID().String()
		if err := registerAlertPolicy(ctx, tx, alertRegistration{Policy: alert.PolicyRevision{ID: id, Revision: 1, Conditions: input.Conditions}, SLOID: input.SLOID, Name: input.Name, Severity: input.Severity, FirstBoundary: input.FirstBoundary, Now: normalizedAgentNow(input.Now), Limits: input.Limits}); err != nil {
			return AlertCommandResult{}, err
		}
		pgID, _ := canonicalUUID(id)
		if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: actor, SubjectID: pgID, Action: "alert-policy.created", OccurredAt: timestamp(normalizedAgentNow(input.Now)), RequestID: input.RequestID}); err != nil {
			return AlertCommandResult{}, err
		}
		record, err := readAlertPolicyRecord(ctx, q, id)
		return AlertCommandResult{Policy: &record}, err
	})
}

func AcknowledgeAlert(ctx context.Context, databaseURL string, input AcknowledgeAlertInput) (AlertCommandResult, error) {
	request := struct {
		ID      string
		Version int64
	}{input.ID, input.ExpectedVersion}
	return executeAlertCommand(ctx, databaseURL, input.AlertCommandInput, "alert.acknowledge", request, func(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, actor string) (AlertCommandResult, error) {
		if _, err := acknowledgeAlertTx(ctx, tx, alertAcknowledgement{ID: input.ID, ActorID: actor, RequestID: input.RequestID, ExpectedVersion: input.ExpectedVersion, Now: input.Now}); err != nil {
			return AlertCommandResult{}, err
		}
		record, err := readAlertRecord(ctx, q, input.ID)
		return AlertCommandResult{Alert: &record}, err
	})
}

func DisableAlertPolicy(ctx context.Context, databaseURL string, input DisableAlertPolicyInput) (AlertCommandResult, error) {
	request := struct {
		ID, Reason  string
		Version     int64
		EffectiveAt time.Time
	}{input.ID, input.Reason, input.ExpectedVersion, input.EffectiveAt}
	return executeAlertCommand(ctx, databaseURL, input.AlertCommandInput, "alert-policy.disable", request, func(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, actor string) (AlertCommandResult, error) {
		stored, err := disableAlertPolicyTx(ctx, tx, alertDisableInput{PolicyID: input.ID, ActorID: actor, RequestID: input.RequestID, Reason: input.Reason, ExpectedVersion: input.ExpectedVersion, EffectiveAt: input.EffectiveAt, Now: input.Now})
		if err != nil {
			return AlertCommandResult{}, err
		}
		policy, err := readAlertPolicyRecord(ctx, q, input.ID)
		if err != nil {
			return AlertCommandResult{}, err
		}
		result := AlertCommandResult{Policy: &policy}
		if stored.Occurrence != nil {
			record, err := readAlertRecord(ctx, q, stored.Occurrence.ID)
			if err != nil {
				return AlertCommandResult{}, err
			}
			result.Alert = &record
		}
		return result, nil
	})
}

func RetryAlertPolicy(ctx context.Context, databaseURL string, input RetryAlertPolicyInput) (AlertCommandResult, error) {
	request := struct {
		ID      string
		Version int64
	}{input.ID, input.ExpectedVersion}
	return executeAlertCommand(ctx, databaseURL, input.AlertCommandInput, "alert-policy.retry", request, func(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, actor string) (AlertCommandResult, error) {
		if err := retryAlertPolicyTx(ctx, tx, alertRetryInput{PolicyID: input.ID, ActorID: actor, RequestID: input.RequestID, ExpectedVersion: input.ExpectedVersion, Now: input.Now}); err != nil {
			return AlertCommandResult{}, err
		}
		policy, err := readAlertPolicyRecord(ctx, q, input.ID)
		return AlertCommandResult{Policy: &policy}, err
	})
}
