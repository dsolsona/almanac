package storage

import (
	"context"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

type ListAlertTransitionsInput struct {
	ReadAlertInput
	Limit  int
	Cursor string
}
type AlertTransitionRecord struct {
	ID, From, To, Cause, ActorID, Reason, EvaluationID, RequestID string
	Sequence                                                      int64
	EffectiveAt, DetectedAt                                       time.Time
}
type AlertTransitionPage struct {
	Items      []AlertTransitionRecord
	NextCursor string
}

func ListAlertTransitions(ctx context.Context, databaseURL string, input ListAlertTransitionsInput) (AlertTransitionPage, error) {
	page := AlertTransitionPage{Items: []AlertTransitionRecord{}}
	if input.Limit < 1 || input.Limit > 200 || !canonicalUUIDCheck(input.ID) {
		return page, ErrAlertInvalid
	}
	cursor, err := decodeAlertReadCursor(input.Cursor, "alert-transitions", input.ID)
	if err != nil || cursor.Sequence < 0 {
		return page, ErrAlertInvalid
	}
	err = alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		if _, err := readAlertRecord(ctx, q, input.ID); err != nil {
			return err
		}
		id, _ := canonicalUUID(input.ID)
		rows, err := q.ListAlertTransitionHistory(ctx, dbgen.ListAlertTransitionHistoryParams{OccurrenceID: id, Sequence: cursor.Sequence, Limit: int32(input.Limit + 1)})
		if err != nil {
			return err
		}
		for _, row := range rows[:min(len(rows), input.Limit)] {
			record := AlertTransitionRecord{ID: row.ID.String(), From: row.FromState.String, To: row.ToState, Cause: row.Cause, Reason: row.Reason, RequestID: row.RequestID, Sequence: row.Sequence, EffectiveAt: row.EffectiveAt.Time.UTC(), DetectedAt: row.DetectedAt.Time.UTC()}
			if row.ActorID.Valid {
				record.ActorID = row.ActorID.String()
			}
			if row.EvaluationID.Valid {
				record.EvaluationID = row.EvaluationID.String()
			}
			page.Items = append(page.Items, record)
		}
		if len(rows) > input.Limit {
			last := rows[input.Limit-1]
			page.NextCursor, err = encodeAlertReadCursor(alertReadCursor{Version: 1, Kind: "alert-transitions", Filter: input.ID, ID: last.ID.String(), Sequence: last.Sequence})
		}
		return err
	})
	if err != nil {
		return AlertTransitionPage{}, err
	}
	return page, nil
}

type ListAlertRangesInput struct {
	ReadAlertInput
	Revision int64
	Limit    int
	Cursor   string
}
type AlertRangeRecord struct {
	ID, Kind, Reason, ActorID, RequestID, EvaluationID string
	Start, End, RecordedAt                             time.Time
	CadenceSeconds                                     int64
}
type AlertRangePage struct {
	Items      []AlertRangeRecord
	NextCursor string
}

func ListAlertHistoryRanges(ctx context.Context, databaseURL string, input ListAlertRangesInput) (AlertRangePage, error) {
	page := AlertRangePage{Items: []AlertRangeRecord{}}
	if input.Limit < 1 || input.Limit > 200 || input.Revision < 1 || !canonicalUUIDCheck(input.ID) {
		return page, ErrAlertInvalid
	}
	identity := input.ID + ":" + strconv.FormatInt(input.Revision, 10)
	cursor, err := decodeAlertReadCursor(input.Cursor, "alert-ranges", identity)
	if err != nil {
		return page, err
	}
	err = alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		id, _ := canonicalUUID(input.ID)
		exists, err := q.AlertPolicyRevisionExists(ctx, dbgen.AlertPolicyRevisionExistsParams{PolicyID: id, Revision: input.Revision})
		if err != nil {
			return err
		}
		if !exists {
			return ErrAlertNotFound
		}
		rows, err := q.ListAlertRangeHistory(ctx, dbgen.ListAlertRangeHistoryParams{PolicyID: id, Revision: input.Revision, HasCursor: input.Cursor != "", AfterStart: timestamp(time.UnixMicro(cursor.Time)), AfterID: uuidOrZero(cursor.ID), PageLimit: int32(input.Limit + 1)})
		if err != nil {
			return err
		}
		for _, row := range rows[:min(len(rows), input.Limit)] {
			record := AlertRangeRecord{ID: row.ID.String(), Kind: row.Kind, Reason: row.Reason, RequestID: row.RequestID, Start: row.StartsAt.Time.UTC(), End: row.EndsAt.Time.UTC(), RecordedAt: row.RecordedAt.Time.UTC(), CadenceSeconds: row.CadenceSeconds}
			if row.ActorID.Valid {
				record.ActorID = row.ActorID.String()
			}
			if row.ReferenceEvaluationID.Valid {
				record.EvaluationID = row.ReferenceEvaluationID.String()
			}
			page.Items = append(page.Items, record)
		}
		if len(rows) > input.Limit {
			last := rows[input.Limit-1]
			page.NextCursor, err = encodeAlertReadCursor(alertReadCursor{Version: 1, Kind: "alert-ranges", Filter: identity, ID: last.ID.String(), Time: last.StartsAt.Time.UnixMicro()})
		}
		return err
	})
	if err != nil {
		return AlertRangePage{}, err
	}
	return page, nil
}
