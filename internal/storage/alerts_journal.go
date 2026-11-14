package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
)

type alertCommitResult struct {
	FormatVersion int
	EvaluationID  string
	Occurrence    *alert.Occurrence
	Version       int64
}

func alertReceipt(ctx context.Context, q *dbgen.Queries, claim alertClaim, digest [32]byte) (alertCommitResult, bool, error) {
	id, _ := canonicalUUID(claim.PolicyID)
	saved, err := q.FindAlertEvaluationReceipt(ctx, dbgen.FindAlertEvaluationReceiptParams{PolicyID: id, Revision: claim.Revision, ScheduledAt: timestamp(claim.ScheduledAt)})
	if errors.Is(err, pgx.ErrNoRows) {
		return alertCommitResult{}, false, nil
	}
	if err != nil {
		return alertCommitResult{}, false, err
	}
	if saved.ClaimEpoch != claim.Epoch || saved.ClaimToken != claim.Token || !bytes.Equal(saved.InputDigest, digest[:]) {
		return alertCommitResult{}, false, ErrAlertStale
	}
	var result alertCommitResult
	if err := json.Unmarshal(saved.Result, &result); err != nil {
		return alertCommitResult{}, false, err
	}
	if result.FormatVersion != 1 || result.EvaluationID != saved.ID.String() {
		return alertCommitResult{}, false, ErrAlertInvalid
	}
	return result, true, nil
}

// commitAlertEvaluation accepts backend-owned supplied evidence only. A later
// canonical adapter must produce it; this is not a public submission surface.
func (r *Runtime) commitAlertEvaluation(ctx context.Context, claim alertClaim, input alert.Input, limits alert.Limits, clock func() time.Time) (alertCommitResult, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "alert.commit")
	defer span.End()
	span.SetAttributes(attribute.String("alert.policy.id", claim.PolicyID))
	if !validAlertClaim(claim) || clock == nil || input.Policy.ID != claim.PolicyID || input.Policy.Revision != claim.Revision || input.Snapshot.Cutoff.After(claim.ScheduledAt) {
		return alertCommitResult{}, ErrAlertInvalid
	}
	encoded, err := marshalAlertData(input)
	if err != nil {
		return alertCommitResult{}, err
	}
	digest := sha256.Sum256(encoded)
	var result alertCommitResult
	err = r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
		if saved, found, err := alertReceipt(ctx, q, claim, digest); err != nil {
			return err
		} else if found {
			result = saved
			return nil
		}
		// Pure reduction happens before source/configuration locks, not during a
		// canonical scan. The transaction below only validates and publishes.
		evaluation, err := alert.Evaluate(input, limits)
		if err != nil {
			return err
		}
		id, _ := canonicalUUID(claim.PolicyID)
		binding, err := q.ReadAlertBinding(ctx, id)
		if err != nil {
			return err
		}
		measurement, err := q.LockAlertSli(ctx, binding.SliID)
		if err != nil {
			return err
		}
		definition, err := q.LockAlertSlo(ctx, binding.SloID)
		if err != nil {
			return err
		}
		now := normalizedAgentNow(clock)
		if now.IsZero() {
			return ErrAlertInvalid
		}
		generation, err := q.FenceAlertSourceGeneration(ctx, dbgen.FenceAlertSourceGenerationParams{SliID: binding.SliID, MeasurementRevision: binding.MeasurementRevision})
		if errors.Is(err, pgx.ErrNoRows) {
			// Generation rows intentionally start at one. The SLI SHARE lock
			// held through commit conflicts with first ingestion's UPDATE lock;
			// absence is fenced by that parent authority, never by coalesce alone.
			generation = 0
		} else if err != nil {
			return err
		}
		exclusion, err := q.FenceMaintenanceState(ctx, binding.SloID)
		if err != nil {
			return err
		}
		policy, err := q.LockAlertPolicy(ctx, id)
		if err != nil {
			return err
		}
		// A competing commit may have won while this transaction waited. Its
		// immutable receipt precedes version/generation rejection on replay.
		if saved, found, err := alertReceipt(ctx, q, claim, digest); err != nil {
			return err
		} else if found {
			result = saved
			return nil
		}
		if !policy.Enabled || policy.EvaluationRevision != claim.Revision || binding.EvaluationRevision != claim.Revision || measurement != binding.MeasurementRevision || definition != binding.DefinitionRevision {
			return ErrAlertStale
		}
		identity := input.Snapshot
		if identity.SLOID != binding.SloID.String() || identity.SLORevision != definition || identity.SLIRevision != measurement || identity.SourceGeneration != generation || identity.ExclusionRevision != exclusion || identity.CalculationVersion != binding.CalculationVersion || identity.CalculationVersion != operationalCalculationVersion {
			return ErrAlertStale
		}
		var conditions []alert.Condition
		if err := unmarshalAlertData(binding.Conditions, &conditions); err != nil {
			return err
		}
		if !slices.Equal(conditions, input.Policy.Conditions) {
			return ErrAlertStale
		}
		meaning, err := q.GetSlo(ctx, binding.SloID)
		if err != nil {
			return err
		}
		if sli.NormalizeDecimal(input.Objective.Target) != sli.NormalizeDecimal(meaning.Target) || input.Objective.Cadence != time.Duration(meaning.CadenceSeconds)*time.Second || !input.Objective.DefinitionValidFrom.Equal(meaning.EvaluationValidFrom.Time) || !input.Objective.MeasurementValidFrom.Equal(meaning.SliValidFrom.Time) {
			return ErrAlertStale
		}
		cursor, err := q.LockAlertCursor(ctx, dbgen.LockAlertCursorParams{PolicyID: id, Revision: claim.Revision})
		if err != nil {
			return err
		}
		now = normalizedAgentNow(clock)
		if !cursor.Schedulable || cursor.Epoch != claim.Epoch || cursor.ClaimToken != claim.Token || cursor.Version != claim.Version || !cursor.NextScheduledAt.Time.Equal(claim.ScheduledAt) || !cursor.ClaimUntil.Valid || !cursor.ClaimUntil.Time.After(now) {
			return ErrAlertStale
		}
		if cursor.LastCutoff.Valid && !identity.Cutoff.After(cursor.LastCutoff.Time) {
			return ErrAlertStale
		}
		if cursor.PendingCutoff.Valid && !cursor.PendingCutoff.Time.Equal(identity.Cutoff) {
			return ErrAlertStale
		}
		var stored alertStoredOccurrence
		if cursor.OpenOccurrenceID.Valid {
			row, err := q.LockAlertOccurrence(ctx, cursor.OpenOccurrenceID)
			if err != nil {
				return err
			}
			stored, err = decodeAlertOccurrence(row)
			if err != nil {
				return err
			}
		}
		now = normalizedAgentNow(clock)
		if !cursor.ClaimUntil.Time.After(now) {
			return ErrAlertStale
		}
		transition, err := alert.Reduce(stored.Occurrence, evaluation, now)
		if err != nil {
			return err
		}
		evaluationID := newAlertID()
		expectedVersion := stored.Version
		if transition.Next != nil {
			stored.Occurrence = transition.Next
			stored.Version++
			if transition.Action == alert.Create {
				stored.Occurrence.ID = newAlertID().String()
				stored.FiringEvaluationID = evaluationID
			}
			stored.LatestEvaluationID = evaluationID
			if transition.Action == alert.Create || transition.Action == alert.Resolve {
				stored.Sequence++
			}
		}
		result = alertCommitResult{FormatVersion: 1, EvaluationID: evaluationID.String(), Occurrence: stored.Occurrence, Version: stored.Version}
		receipt, err := json.Marshal(result)
		if err != nil {
			return err
		}
		evidence, err := marshalAlertData(struct {
			FormatVersion int
			Input         alert.Input
			Evaluation    alert.Evaluation
		}{1, input, evaluation})
		if err != nil {
			return err
		}
		if err := q.InsertAlertEvaluation(ctx, dbgen.InsertAlertEvaluationParams{ID: evaluationID, PolicyID: id, Revision: claim.Revision, ScheduledAt: timestamp(claim.ScheduledAt), EffectiveCutoff: timestamp(identity.Cutoff), Truth: string(evaluation.Truth), SourceGeneration: generation, ExclusionRevision: exclusion, ClaimEpoch: claim.Epoch, ClaimToken: claim.Token, InputDigest: digest[:], Evidence: evidence, Result: receipt, CommittedAt: timestamp(now)}); err != nil {
			return err
		}
		if stored.Occurrence != nil {
			if transition.Action == alert.Create {
				payload, err := json.Marshal(stored.Occurrence)
				if err != nil {
					return err
				}
				metadata, err := json.Marshal(struct {
					Name, Severity, SLOName, ServiceName, Target string
					RollingWindowSeconds                         int64
				}{policy.Name, policy.Severity, meaning.DisplayName, meaning.ServiceDisplayName, meaning.Target, meaning.RollingWindowSeconds})
				if err != nil {
					return err
				}
				occID, _ := canonicalUUID(stored.Occurrence.ID)
				if err := q.InsertAlertOccurrence(ctx, dbgen.InsertAlertOccurrenceParams{ID: occID, PolicyID: id, Revision: claim.Revision, State: string(stored.Occurrence.State), Version: stored.Version, TransitionSequence: stored.Sequence, FiringEvaluationID: stored.FiringEvaluationID, LatestEvaluationID: evaluationID, Payload: payload, PolicySnapshot: metadata, OpenedAt: timestamp(stored.Occurrence.FiringSnapshot.Cutoff), UpdatedAt: timestamp(now)}); err != nil {
					return err
				}
			} else if err := writeAlertOccurrence(ctx, q, stored, expectedVersion); err != nil {
				return err
			}
			if transition.Action == alert.Create || transition.Action == alert.Resolve {
				if err := insertAlertTransition(ctx, q, stored, transition, evaluationID, ""); err != nil {
					return err
				}
			}
			if evaluation.Truth == alert.Breaching {
				occurrenceID, _ := canonicalUUID(stored.Occurrence.ID)
				n, err := q.UpsertAlertBreachPin(ctx, dbgen.UpsertAlertBreachPinParams{OccurrenceID: occurrenceID, PolicyID: id, Revision: claim.Revision, EvaluationID: evaluationID})
				if err != nil {
					return err
				}
				if n != 1 {
					return ErrAlertStale
				}
			}
		}
		var openID pgtype.UUID
		if stored.Occurrence != nil && (stored.Occurrence.State == alert.Firing || stored.Occurrence.State == alert.Acknowledged) {
			openID, _ = canonicalUUID(stored.Occurrence.ID)
		}
		n, err := q.AdvanceAlertCursor(ctx, dbgen.AdvanceAlertCursorParams{PolicyID: id, Revision: claim.Revision, NextScheduledAt: timestamp(claim.ScheduledAt.Add(time.Minute)), LastCutoff: timestamp(identity.Cutoff), LastEvaluationID: evaluationID, OpenOccurrenceID: openID, Epoch: claim.Epoch, ClaimToken: claim.Token, Version: claim.Version, ExpectedBoundary: timestamp(claim.ScheduledAt)})
		if err != nil {
			return err
		}
		if n != 1 || !cursor.ClaimUntil.Time.After(normalizedAgentNow(clock)) {
			return ErrAlertStale
		}
		return nil
	})
	if err != nil {
		return alertCommitResult{}, err
	}
	return result, nil
}

type alertAcknowledgement struct {
	ID, ActorID, RequestID string
	ExpectedVersion        int64
	Now                    func() time.Time
}

// acknowledgeAlertTx is inside the future authenticated management transaction.
// Session/role, command digest/receipt and authority completion checks are its
// caller's responsibility, not bypassed through an exposed endpoint.
func acknowledgeAlertTx(ctx context.Context, tx pgx.Tx, input alertAcknowledgement) (alertStoredOccurrence, error) {
	id, idOK := canonicalUUID(input.ID)
	_, actorOK := canonicalUUID(input.ActorID)
	if !idOK || !actorOK || input.RequestID == "" || input.ExpectedVersion <= 0 || input.Now == nil {
		return alertStoredOccurrence{}, ErrAlertInvalid
	}
	q := dbgen.New(tx)
	row, err := q.LockAlertOccurrence(ctx, id)
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	if row.Version != input.ExpectedVersion {
		return alertStoredOccurrence{}, ErrAlertStale
	}
	now := normalizedAgentNow(input.Now)
	if now.IsZero() {
		return alertStoredOccurrence{}, ErrAlertInvalid
	}
	stored, err := decodeAlertOccurrence(row)
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	transition, err := alert.Acknowledge(*stored.Occurrence, input.ActorID, now)
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	if transition.Action == alert.Unchanged {
		return stored, nil
	}
	stored.Occurrence = transition.Next
	stored.Sequence++
	stored.Version++
	if err := writeAlertOccurrence(ctx, q, stored, input.ExpectedVersion); err != nil {
		return alertStoredOccurrence{}, err
	}
	if err := insertAlertTransition(ctx, q, stored, transition, pgtype.UUID{}, input.RequestID); err != nil {
		return alertStoredOccurrence{}, err
	}
	if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: input.ActorID, SubjectID: id, Action: "alert.acknowledged", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return alertStoredOccurrence{}, err
	}
	return stored, nil
}
