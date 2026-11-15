package storage

import (
	"context"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type alertDisableInput struct {
	PolicyID, ActorID, Reason, RequestID string
	ExpectedVersion                      int64
	EffectiveAt                          time.Time
	Now                                  func() time.Time
}

// disableAlertPolicyTx participates in the authenticated management transaction;
// its caller owns session/role, receipt and completion authorization checks.
// This changes policy intent and records cancellation, never measured recovery.
func disableAlertPolicyTx(ctx context.Context, tx pgx.Tx, input alertDisableInput) (alertStoredOccurrence, error) {
	id, idOK := canonicalUUID(input.PolicyID)
	actor, actorOK := canonicalUUID(input.ActorID)
	if !idOK || !actorOK || input.ExpectedVersion <= 0 || strings.TrimSpace(input.Reason) == "" || input.RequestID == "" || input.Now == nil {
		return alertStoredOccurrence{}, ErrAlertInvalid
	}
	q := dbgen.New(tx)
	binding, err := q.ReadAlertBinding(ctx, id)
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	if _, err := q.LockAlertSli(ctx, binding.SliID); err != nil {
		return alertStoredOccurrence{}, err
	}
	if _, err := q.LockAlertSlo(ctx, binding.SloID); err != nil {
		return alertStoredOccurrence{}, err
	}
	policy, err := q.LockAlertPolicy(ctx, id)
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	if policy.Version != input.ExpectedVersion || policy.SloID != binding.SloID || policy.EvaluationRevision != binding.EvaluationRevision {
		return alertStoredOccurrence{}, ErrAlertStale
	}
	detected := normalizedAgentNow(input.Now)
	if detected.IsZero() || (!input.EffectiveAt.IsZero() && (input.EffectiveAt.After(detected) || input.EffectiveAt.Before(policy.CreatedAt.Time))) {
		return alertStoredOccurrence{}, ErrAlertInvalid
	}
	if !policy.Enabled {
		return alertStoredOccurrence{}, nil
	}
	cursor, err := q.LockAlertCursor(ctx, dbgen.LockAlertCursorParams{PolicyID: id, Revision: policy.EvaluationRevision})
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	var stored alertStoredOccurrence
	if cursor.OpenOccurrenceID.Valid {
		row, err := q.LockAlertOccurrence(ctx, cursor.OpenOccurrenceID)
		if err != nil {
			return alertStoredOccurrence{}, err
		}
		stored, err = decodeAlertOccurrence(row)
		if err != nil {
			return alertStoredOccurrence{}, err
		}
	}
	detected = normalizedAgentNow(input.Now)
	effective := input.EffectiveAt
	if effective.IsZero() {
		effective = detected
	}
	if detected.IsZero() || effective.After(detected) || effective.Before(policy.CreatedAt.Time) {
		return alertStoredOccurrence{}, ErrAlertInvalid
	}
	stored, err = cancelAlertRevision(ctx, q, policy, cursor, stored, actor, input.ActorID, input.Reason, input.RequestID, alert.PolicyDisabled, effective, detected)
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	n, err := q.DisableAlertPolicyIdentity(ctx, dbgen.DisableAlertPolicyIdentityParams{ID: id, Version: input.ExpectedVersion, UpdatedAt: timestamp(detected)})
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	if n != 1 {
		return alertStoredOccurrence{}, ErrAlertStale
	}
	if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: input.ActorID, SubjectID: id, Action: "alert-policy.disabled", OccurredAt: timestamp(detected), RequestID: input.RequestID}); err != nil {
		return alertStoredOccurrence{}, err
	}
	return stored, nil
}
