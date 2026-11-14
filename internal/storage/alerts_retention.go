package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type alertRetentionOptions struct {
	Batch                        int
	CompactBefore, HistoryBefore time.Time
}
type alertRetentionResult struct{ Scanned, CompactedEvaluations, DeletedEvaluations, DeletedOccurrences, DeletedRanges int }

// maintainAlertEvidence is unwired bounded maintenance. Each stage owns a
// durable keyset over raw rows, before eligibility/pin filters, so a retained
// prefix cannot starve later policies or cause an unbounded eligibility scan.
func (r *Runtime) maintainAlertEvidence(ctx context.Context, options alertRetentionOptions, clock func() time.Time) (alertRetentionResult, error) {
	var total alertRetentionResult
	if clock == nil || options.Batch < 1 || options.Batch > 200 {
		return total, ErrAlertInvalid
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() || options.CompactBefore.IsZero() || options.HistoryBefore.IsZero() || options.CompactBefore.After(now.Add(-24*time.Hour)) || options.HistoryBefore.After(now.Add(-365*24*time.Hour)) || options.HistoryBefore.After(options.CompactBefore) {
		return total, ErrAlertInvalid
	}
	for _, kind := range []string{"occurrence", "range", "evaluation"} {
		var stage alertRetentionResult
		err := r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
			work, err := q.LockAlertRetentionWork(ctx, kind)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			switch kind {
			case "occurrence":
				stage, err = retainAlertOccurrences(ctx, q, work, options)
			case "range":
				stage, err = retainAlertRanges(ctx, q, work, options)
			case "evaluation":
				stage, err = retainAlertEvaluations(ctx, q, work, options, now)
			}
			if err != nil {
				return err
			}
			if ended := normalizedAgentNow(clock); ended.IsZero() || ended.Before(now) {
				return ErrAlertInvalid
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total.Scanned += stage.Scanned
		total.CompactedEvaluations += stage.CompactedEvaluations
		total.DeletedEvaluations += stage.DeletedEvaluations
		total.DeletedOccurrences += stage.DeletedOccurrences
		total.DeletedRanges += stage.DeletedRanges
	}
	return total, nil
}

func alertRetentionAdvance(ctx context.Context, q *dbgen.Queries, work dbgen.AlmanacAlertRetentionWork, policy pgtype.UUID, revision int64, at pgtype.Timestamptz, id pgtype.UUID, reset bool) error {
	if reset {
		policy, id = uuidOrZero(""), uuidOrZero("")
		revision = 0
		at = pgtype.Timestamptz{InfinityModifier: pgtype.NegativeInfinity, Valid: true}
	}
	n, err := q.AdvanceAlertRetentionWork(ctx, dbgen.AdvanceAlertRetentionWorkParams{Kind: work.Kind, AfterPolicy: policy, AfterRevision: revision, AfterTime: at, AfterID: id, Version: work.Version})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAlertStale
	}
	return nil
}

func retainAlertOccurrences(ctx context.Context, q *dbgen.Queries, work dbgen.AlmanacAlertRetentionWork, options alertRetentionOptions) (alertRetentionResult, error) {
	var result alertRetentionResult
	rows, err := q.ScanAlertTerminalOccurrences(ctx, dbgen.ScanAlertTerminalOccurrencesParams{AfterTime: work.AfterTime, AfterID: work.AfterID, BatchLimit: int32(options.Batch)})
	if err != nil {
		return result, err
	}
	result.Scanned = len(rows)
	var ids []pgtype.UUID
	for _, row := range rows {
		if row.TerminalAt.Valid && row.TerminalAt.Time.Before(options.HistoryBefore) && row.UpdatedAt.Time.Before(options.HistoryBefore) {
			ids = append(ids, row.ID)
		}
	}
	if len(ids) > 0 {
		if _, err := q.DeleteRetainedAlertTransitions(ctx, ids); err != nil {
			return result, err
		}
		if _, err := q.DeleteRetainedAlertBreachPins(ctx, ids); err != nil {
			return result, err
		}
		n, err := q.DeleteRetainedAlertOccurrences(ctx, ids)
		if err != nil {
			return result, err
		}
		if int(n) != len(ids) {
			return result, ErrAlertStale
		}
		result.DeletedOccurrences = int(n)
	}
	if len(rows) == 0 {
		return result, alertRetentionAdvance(ctx, q, work, pgtype.UUID{}, 0, pgtype.Timestamptz{}, pgtype.UUID{}, true)
	}
	last := rows[len(rows)-1]
	return result, alertRetentionAdvance(ctx, q, work, uuidOrZero(""), 0, last.UpdatedAt, last.ID, false)
}

func retainAlertRanges(ctx context.Context, q *dbgen.Queries, work dbgen.AlmanacAlertRetentionWork, options alertRetentionOptions) (alertRetentionResult, error) {
	var result alertRetentionResult
	rows, err := q.ScanAlertRetentionRanges(ctx, dbgen.ScanAlertRetentionRangesParams{AfterTime: work.AfterTime, AfterID: work.AfterID, BatchLimit: int32(options.Batch)})
	if err != nil {
		return result, err
	}
	result.Scanned = len(rows)
	var ids []pgtype.UUID
	for _, row := range rows {
		if row.RecordedAt.Time.Before(options.HistoryBefore) {
			ids = append(ids, row.ID)
		}
	}
	if len(ids) > 0 {
		n, err := q.DeleteRetainedAlertRanges(ctx, ids)
		if err != nil {
			return result, err
		}
		if int(n) != len(ids) {
			return result, ErrAlertStale
		}
		result.DeletedRanges = int(n)
	}
	if len(rows) == 0 {
		return result, alertRetentionAdvance(ctx, q, work, pgtype.UUID{}, 0, pgtype.Timestamptz{}, pgtype.UUID{}, true)
	}
	last := rows[len(rows)-1]
	return result, alertRetentionAdvance(ctx, q, work, uuidOrZero(""), 0, last.RecordedAt, last.ID, false)
}

func retainAlertEvaluations(ctx context.Context, q *dbgen.Queries, work dbgen.AlmanacAlertRetentionWork, options alertRetentionOptions, now time.Time) (alertRetentionResult, error) {
	var result alertRetentionResult
	rows, err := q.ScanAlertRetainedEvaluations(ctx, dbgen.ScanAlertRetainedEvaluationsParams{AfterPolicy: work.AfterPolicy, AfterRevision: work.AfterRevision, AfterTime: work.AfterTime, AfterID: work.AfterID, BatchLimit: int32(options.Batch)})
	if err != nil {
		return result, err
	}
	result.Scanned = len(rows)
	entries := make([]alert.RetentionEntry, 0, len(rows))
	var expired []pgtype.UUID
	for _, row := range rows {
		var saved struct {
			FormatVersion int
			Evaluation    alert.Evaluation
		}
		if err := unmarshalAlertData(row.Evidence, &saved); err != nil {
			return result, err
		}
		if saved.FormatVersion != 1 || saved.Evaluation.PolicyID != row.PolicyID.String() || saved.Evaluation.Revision != row.Revision || !saved.Evaluation.Snapshot.Cutoff.Equal(row.EffectiveCutoff.Time) || string(saved.Evaluation.Truth) != row.Truth || !row.Pinned.Valid {
			return result, ErrAlertInvalid
		}
		pinned := row.Pinned.Bool
		if !pinned && row.ScheduledAt.Time.Before(options.HistoryBefore) && row.CommittedAt.Time.Before(options.HistoryBefore) {
			expired = append(expired, row.ID)
			pinned = true
		}
		entries = append(entries, alert.RetentionEntry{ID: row.ID.String(), Evaluation: saved.Evaluation, ScheduledAt: row.ScheduledAt.Time.UTC(), CommittedAt: row.CommittedAt.Time.UTC(), Pinned: pinned})
	}
	runs, err := alert.PlanCompaction(entries, options.CompactBefore)
	if err != nil {
		return result, err
	}
	if len(expired) > 0 {
		n, err := q.DeleteRetainedAlertEvaluations(ctx, expired)
		if err != nil {
			return result, err
		}
		if int(n) != len(expired) {
			return result, ErrAlertStale
		}
		result.DeletedEvaluations = int(n)
	}
	for _, run := range runs {
		var policy pgtype.UUID
		var revision int64
		for _, entry := range entries {
			if entry.ID == run.EvaluationIDs[0] {
				policy, _ = canonicalUUID(entry.Evaluation.PolicyID)
				revision = entry.Evaluation.Revision
				break
			}
		}
		if revision == 0 {
			return result, ErrAlertInvalid
		}
		overlap, err := q.AlertRangesOverlap(ctx, dbgen.AlertRangesOverlapParams{PolicyID: policy, Revision: revision, StartsAt: timestamp(run.Start), EndsAt: timestamp(run.End)})
		if err != nil {
			return result, err
		}
		if overlap {
			continue
		}
		summary, err := marshalAlertData(struct {
			FormatVersion int
			First, Last   alert.SnapshotIdentity
			Truth         alert.Truth
			Conditions    []alert.ConditionCompactionSummary
		}{1, run.First, run.Last, run.Truth, run.Conditions})
		if err != nil {
			return result, err
		}
		if err := q.InsertAlertCompactionRange(ctx, dbgen.InsertAlertCompactionRangeParams{ID: newAlertID(), PolicyID: policy, Revision: revision, StartsAt: timestamp(run.Start), EndsAt: timestamp(run.End), RecordedAt: timestamp(now), Summary: summary}); err != nil {
			return result, err
		}
		ids := make([]pgtype.UUID, 0, len(run.EvaluationIDs))
		for _, id := range run.EvaluationIDs {
			value, valid := canonicalUUID(id)
			if !valid {
				return result, ErrAlertInvalid
			}
			ids = append(ids, value)
		}
		n, err := q.DeleteRetainedAlertEvaluations(ctx, ids)
		if err != nil {
			return result, err
		}
		if int(n) != len(ids) {
			return result, ErrAlertStale
		}
		result.CompactedEvaluations += int(n)
	}
	if len(rows) == 0 {
		return result, alertRetentionAdvance(ctx, q, work, pgtype.UUID{}, 0, pgtype.Timestamptz{}, pgtype.UUID{}, true)
	}
	last := rows[len(rows)-1]
	return result, alertRetentionAdvance(ctx, q, work, last.PolicyID, last.Revision, last.ScheduledAt, last.ID, false)
}
