package storage

import (
	"context"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	if !idOK || !actorOK || input.ExpectedVersion <= 0 || strings.TrimSpace(input.Reason) == "" || input.RequestID == "" || input.Now == nil || input.EffectiveAt.IsZero() {
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
	if policy.Version != input.ExpectedVersion {
		return alertStoredOccurrence{}, ErrAlertStale
	}
	detected := normalizedAgentNow(input.Now)
	if detected.IsZero() || input.EffectiveAt.After(detected) || input.EffectiveAt.Before(policy.CreatedAt.Time) {
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
		transition, err := alert.ApplyAdministrativeChange(stored.Occurrence, alert.PolicyDisabled, input.ActorID, input.Reason, input.EffectiveAt, detected)
		if err != nil {
			return alertStoredOccurrence{}, err
		}
		previous := stored.Version
		stored.Occurrence = transition.Next
		stored.Version++
		stored.Sequence++
		if err := writeAlertOccurrence(ctx, q, stored, previous); err != nil {
			return alertStoredOccurrence{}, err
		}
		if err := insertAlertTransition(ctx, q, stored, transition, pgtype.UUID{}, input.RequestID); err != nil {
			return alertStoredOccurrence{}, err
		}
	}
	// Include the scheduled boundary at effective time. Never cancel later work
	// merely because application of an already authorized boundary was delayed.
	end := floorSliBoundary(input.EffectiveAt, time.Minute).Add(time.Minute)
	next := cursor.NextScheduledAt
	if cursor.Schedulable && cursor.NextScheduledAt.Time.Before(end) {
		if err := q.InsertAlertRange(ctx, dbgen.InsertAlertRangeParams{ID: newAlertID(), PolicyID: id, Revision: policy.EvaluationRevision, StartsAt: cursor.NextScheduledAt, EndsAt: timestamp(end), CadenceSeconds: 60, Kind: "administrative_cancellation", Reason: input.Reason, RecordedAt: timestamp(detected), ReferenceEvaluationID: cursor.LastEvaluationID, ActorID: actor, RequestID: input.RequestID}); err != nil {
			return alertStoredOccurrence{}, err
		}
		next = timestamp(end)
	}
	n, err := q.CancelAlertCursor(ctx, dbgen.CancelAlertCursorParams{PolicyID: id, Revision: policy.EvaluationRevision, NextScheduledAt: next, Version: cursor.Version})
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	if n != 1 {
		return alertStoredOccurrence{}, ErrAlertStale
	}
	n, err = q.DisableAlertPolicyIdentity(ctx, dbgen.DisableAlertPolicyIdentityParams{ID: id, Version: input.ExpectedVersion, UpdatedAt: timestamp(detected)})
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
