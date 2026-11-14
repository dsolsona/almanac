package storage

import (
	"context"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type alertRetryInput struct {
	PolicyID, ActorID, RequestID string
	ExpectedVersion              int64
	Now                          func() time.Time
}

// retryAlertPolicyTx is an unwired command seam. Its management caller owns
// session/role checks, replay receipt and final authority checks in this same
// transaction. Retry does not alter policy meaning, evidence or pending cutoff.
func retryAlertPolicyTx(ctx context.Context, tx pgx.Tx, input alertRetryInput) error {
	id, valid := canonicalUUID(input.PolicyID)
	if !valid || !canonicalUUIDCheck(input.ActorID) || strings.TrimSpace(input.RequestID) == "" || input.ExpectedVersion <= 0 || input.Now == nil {
		return ErrAlertInvalid
	}
	q := dbgen.New(tx)
	binding, err := q.ReadAlertBinding(ctx, id)
	if err != nil {
		return err
	}
	if _, err := q.LockAlertSli(ctx, binding.SliID); err != nil {
		return err
	}
	if _, err := q.LockAlertSlo(ctx, binding.SloID); err != nil {
		return err
	}
	policy, err := q.LockAlertPolicy(ctx, id)
	if err != nil {
		return err
	}
	if !policy.Enabled || policy.Version != input.ExpectedVersion {
		return ErrAlertStale
	}
	cursor, err := q.LockAlertCursor(ctx, dbgen.LockAlertCursorParams{PolicyID: id, Revision: policy.EvaluationRevision})
	if err != nil {
		return err
	}
	now := normalizedAgentNow(input.Now)
	if now.IsZero() {
		return ErrAlertInvalid
	}
	if !cursor.Schedulable || !cursor.Exhausted {
		return ErrAlertStale
	}
	n, err := q.RetryAlertCursor(ctx, dbgen.RetryAlertCursorParams{PolicyID: id, Revision: policy.EvaluationRevision, AvailableAt: timestamp(now), Version: cursor.Version})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAlertStale
	}
	return q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: input.ActorID, SubjectID: id, Action: "alert-policy.retried", OccurredAt: timestamp(now), RequestID: input.RequestID})
}
