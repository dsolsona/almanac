package storage

import (
	"context"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func lockAlertRangeHead(ctx context.Context, q *dbgen.Queries, claim alertClaim, now time.Time) (dbgen.AlmanacAlertCursor, error) {
	if !validAlertClaim(claim) || now.IsZero() {
		return dbgen.AlmanacAlertCursor{}, ErrAlertInvalid
	}
	id, _ := canonicalUUID(claim.PolicyID)
	binding, err := q.ReadAlertBinding(ctx, id)
	if err != nil {
		return dbgen.AlmanacAlertCursor{}, err
	}
	measurement, err := q.LockAlertSli(ctx, binding.SliID)
	if err != nil {
		return dbgen.AlmanacAlertCursor{}, err
	}
	definition, err := q.LockAlertSlo(ctx, binding.SloID)
	if err != nil {
		return dbgen.AlmanacAlertCursor{}, err
	}
	policy, err := q.LockAlertPolicy(ctx, id)
	if err != nil {
		return dbgen.AlmanacAlertCursor{}, err
	}
	if !policy.Enabled || policy.EvaluationRevision != claim.Revision || binding.EvaluationRevision != claim.Revision || measurement != binding.MeasurementRevision || definition != binding.DefinitionRevision || binding.CalculationVersion != operationalCalculationVersion {
		return dbgen.AlmanacAlertCursor{}, ErrAlertStale
	}
	cursor, err := q.LockAlertCursor(ctx, dbgen.LockAlertCursorParams{PolicyID: id, Revision: claim.Revision})
	if err != nil {
		return cursor, err
	}
	if !cursor.Schedulable || cursor.Epoch != claim.Epoch || cursor.ClaimToken != claim.Token || cursor.Version != claim.Version || !cursor.NextScheduledAt.Time.Equal(claim.ScheduledAt) || !cursor.ClaimUntil.Valid || !cursor.ClaimUntil.Time.After(now) {
		return dbgen.AlmanacAlertCursor{}, ErrAlertStale
	}
	return cursor, nil
}

func advanceAlertRange(ctx context.Context, q *dbgen.Queries, cursor dbgen.AlmanacAlertCursor, claim alertClaim, end, now time.Time, kind, reason string) error {
	var reference pgtype.UUID
	if kind == "repeated_cutoff" {
		reference = cursor.LastEvaluationID
	}
	if err := q.InsertAlertRange(ctx, dbgen.InsertAlertRangeParams{ID: newAlertID(), PolicyID: cursor.PolicyID, Revision: cursor.Revision, StartsAt: timestamp(claim.ScheduledAt), EndsAt: timestamp(end), CadenceSeconds: 60, Kind: kind, Reason: reason, RecordedAt: timestamp(now), ReferenceEvaluationID: reference}); err != nil {
		return err
	}
	n, err := q.AdvanceAlertCursor(ctx, dbgen.AdvanceAlertCursorParams{PolicyID: cursor.PolicyID, Revision: cursor.Revision, NextScheduledAt: timestamp(end), LastCutoff: cursor.LastCutoff, LastEvaluationID: cursor.LastEvaluationID, OpenOccurrenceID: cursor.OpenOccurrenceID, Epoch: claim.Epoch, ClaimToken: claim.Token, Version: claim.Version, ExpectedBoundary: timestamp(claim.ScheduledAt)})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAlertStale
	}
	return nil
}

func (r *Runtime) recordAlertGap(ctx context.Context, claim alertClaim, clock func() time.Time, horizon time.Duration) (bool, error) {
	if clock == nil {
		return false, ErrAlertInvalid
	}
	var recorded bool
	err := r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
		cursor, err := lockAlertRangeHead(ctx, q, claim, normalizedAgentNow(clock))
		if err != nil {
			return err
		}
		now := normalizedAgentNow(clock)
		if now.IsZero() || !cursor.ClaimUntil.Time.After(now) {
			return ErrAlertStale
		}
		plan, err := alert.PlanCatchUp(alert.ScheduledGrid{Origin: cursor.OriginAt.Time, Cadence: time.Minute}, claim.ScheduledAt, now, horizon)
		if err != nil {
			return err
		}
		if plan.Skipped == nil {
			return nil
		}
		if err := advanceAlertRange(ctx, q, cursor, claim, plan.Next, now, "unreconstructed_gap", "catchup_horizon"); err != nil {
			return err
		}
		if !cursor.ClaimUntil.Time.After(normalizedAgentNow(clock)) {
			return ErrAlertStale
		}
		recorded = true
		return nil
	})
	return recorded, err
}

// Repeated cutoffs point to the immutable original evaluation. This records no
// new calculation/freshness claim and does not rewrite the occurrence's evidence.
func (r *Runtime) recordAlertRepeatedCutoff(ctx context.Context, claim alertClaim, cutoff time.Time, clock func() time.Time) error {
	if clock == nil {
		return ErrAlertInvalid
	}
	return r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
		cursor, err := lockAlertRangeHead(ctx, q, claim, normalizedAgentNow(clock))
		if err != nil {
			return err
		}
		if !cursor.LastCutoff.Valid || !cursor.LastCutoff.Time.Equal(cutoff) || !cursor.LastEvaluationID.Valid {
			return ErrAlertStale
		}
		now := normalizedAgentNow(clock)
		if now.IsZero() || !cursor.ClaimUntil.Time.After(now) {
			return ErrAlertStale
		}
		if err := advanceAlertRange(ctx, q, cursor, claim, claim.ScheduledAt.Add(time.Minute), now, "repeated_cutoff", "repeated_effective_cutoff"); err != nil {
			return err
		}
		if !cursor.ClaimUntil.Time.After(normalizedAgentNow(clock)) {
			return ErrAlertStale
		}
		return nil
	})
}
