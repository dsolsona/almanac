package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type ReadAlertInput struct {
	Runtime *Runtime
	Proof   DatasourceProof
	ID      string
	Now     func() time.Time
}

type ReadAlertCommandInput struct {
	Runtime                   *Runtime
	Proof                     DatasourceProof
	Operation, IdempotencyKey string
	Now                       func() time.Time
}

type AlertCommandLookup struct {
	Found, Expired bool
	Result         *AlertCommandResult
}

func readAlertPolicyRecord(ctx context.Context, q *dbgen.Queries, id string) (AlertPolicyRecord, error) {
	pgID, valid := canonicalUUID(id)
	if !valid {
		return AlertPolicyRecord{}, ErrAlertInvalid
	}
	row, err := q.ReadAlertPolicy(ctx, pgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AlertPolicyRecord{}, ErrAlertNotFound
	}
	if err != nil {
		return AlertPolicyRecord{}, err
	}
	var conditions []alert.Condition
	if err := unmarshalAlertData(row.Conditions, &conditions); err != nil {
		return AlertPolicyRecord{}, err
	}
	return AlertPolicyRecord{ID: row.ID.String(), Name: row.Name, SLOID: row.SloID.String(), Severity: row.Severity, Version: row.Version, EvaluationRevision: row.EvaluationRevision, Enabled: row.Enabled, Conditions: conditions, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()}, nil
}

func readAlertRecord(ctx context.Context, q *dbgen.Queries, id string) (AlertRecord, error) {
	pgID, valid := canonicalUUID(id)
	if !valid {
		return AlertRecord{}, ErrAlertInvalid
	}
	row, err := q.ReadAlertOccurrence(ctx, pgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AlertRecord{}, ErrAlertNotFound
	}
	if err != nil {
		return AlertRecord{}, err
	}
	stored, err := decodeAlertOccurrence(row)
	if err != nil {
		return AlertRecord{}, err
	}
	var original AlertOriginalPolicy
	if err := unmarshalAlertData(row.PolicySnapshot, &original); err != nil {
		return AlertRecord{}, err
	}
	return AlertRecord{Occurrence: *stored.Occurrence, Version: stored.Version, Sequence: stored.Sequence, OriginalPolicy: original}, nil
}

func alertReadTransaction(ctx context.Context, databaseURL string, runtime *Runtime, proof DatasourceProof, clock func() time.Time, read func(*dbgen.Queries, string) error) error {
	if clock == nil || proof.SessionVerifier == ([32]byte{}) {
		return ErrAlertInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, runtime, proof.SessionVerifier, clock, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if err := read(q, user.ID.String()); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAlertNotFound
		}
		return err
	}
	return finishAgentRead(ctx, tx, clock, started, user, installation, session)
}

func ReadAlertPolicy(ctx context.Context, databaseURL string, input ReadAlertInput) (AlertPolicyRecord, error) {
	var result AlertPolicyRecord
	err := alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		var err error
		result, err = readAlertPolicyRecord(ctx, q, input.ID)
		return err
	})
	if err != nil {
		return AlertPolicyRecord{}, err
	}
	return result, nil
}

func ReadAlert(ctx context.Context, databaseURL string, input ReadAlertInput) (AlertRecord, error) {
	var result AlertRecord
	err := alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		var err error
		result, err = readAlertRecord(ctx, q, input.ID)
		return err
	})
	if err != nil {
		return AlertRecord{}, err
	}
	return result, nil
}

func ReadAlertCommand(ctx context.Context, databaseURL string, input ReadAlertCommandInput) (AlertCommandLookup, error) {
	var result AlertCommandLookup
	if !validAlertOperation(input.Operation) || !validPrintableASCII(input.IdempotencyKey, 1, 128) {
		return result, ErrAlertInvalid
	}
	err := alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, actor string) error {
		actorID, _ := canonicalUUID(actor)
		row, err := q.LookupAlertCommand(ctx, dbgen.LookupAlertCommandParams{ActorID: actorID, Operation: input.Operation, IdempotencyKey: input.IdempotencyKey})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result.Found = true
		if !normalizedAgentNow(input.Now).Before(row.ExpiresAt.Time) {
			result.Expired = true
			return nil
		}
		var saved AlertCommandResult
		if err := unmarshalAlertData(row.Result, &saved); err != nil || saved.FormatVersion != 1 || saved.Operation != input.Operation || saved.RequestID == "" {
			return ErrAlertInvalid
		}
		result.Result = &saved
		return nil
	})
	if err != nil {
		return AlertCommandLookup{}, err
	}
	return result, nil
}
