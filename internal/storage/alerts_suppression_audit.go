package storage

import (
	"context"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

type ListAlertMaintenanceSuppressionsInput struct {
	ReadAlertInput
	Revision int64
	Limit    int
	Cursor   string
}

type AlertMaintenanceSuppressionRecord struct {
	EvaluationID                     string
	Cutoff, ScheduledAt, CommittedAt time.Time
	Suppression                      alert.MaintenanceSuppression
}

type AlertMaintenanceSuppressionPage struct {
	Items      []AlertMaintenanceSuppressionRecord
	NextCursor string
}

func ListAlertMaintenanceSuppressions(ctx context.Context, databaseURL string, input ListAlertMaintenanceSuppressionsInput) (AlertMaintenanceSuppressionPage, error) {
	page := AlertMaintenanceSuppressionPage{Items: []AlertMaintenanceSuppressionRecord{}}
	id, valid := canonicalUUID(input.ID)
	if !valid || input.Revision < 1 || input.Limit < 1 || input.Limit > 200 {
		return page, ErrAlertInvalid
	}
	const kind = "alert-maintenance-suppressions"
	filter := input.ID + ":" + strconv.FormatInt(input.Revision, 10)
	cursor, err := decodeAlertReadCursor(input.Cursor, kind, filter)
	if err != nil {
		return page, err
	}
	err = alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		exists, err := q.AlertPolicyRevisionExists(ctx, dbgen.AlertPolicyRevisionExistsParams{PolicyID: id, Revision: input.Revision})
		if err != nil {
			return err
		}
		if !exists {
			return ErrAlertNotFound
		}
		const scanLimit = 500
		var after pgtype.Timestamptz
		if input.Cursor != "" {
			after = timestamp(time.UnixMicro(cursor.Time))
		}
		rows, err := q.ScanAlertMaintenanceSuppressions(ctx, dbgen.ScanAlertMaintenanceSuppressionsParams{
			PolicyID: id, Revision: input.Revision, AfterCutoff: after, ScanLimit: scanLimit + 1,
		})
		if err != nil {
			return err
		}
		consumed := 0
		for _, row := range rows[:min(len(rows), scanLimit)] {
			if row.Suppression != "" && len(page.Items) == input.Limit {
				break
			}
			consumed++
			if row.Suppression == "" {
				continue
			}
			item := AlertMaintenanceSuppressionRecord{
				EvaluationID: row.ID.String(), Cutoff: row.EffectiveCutoff.Time.UTC(), ScheduledAt: row.ScheduledAt.Time.UTC(), CommittedAt: row.CommittedAt.Time.UTC(),
			}
			if err := unmarshalAlertData([]byte(row.Suppression), &item.Suppression); err != nil {
				return err
			}
			coverage := item.Suppression
			if !canonicalUUIDCheck(coverage.WindowID) || coverage.Revision <= 0 || !coverage.OpeningSuppressed ||
				item.Cutoff.Before(coverage.Start) || !item.Cutoff.Before(coverage.End) {
				return ErrAlertInvalid
			}
			page.Items = append(page.Items, item)
		}
		if consumed < len(rows) {
			last := rows[consumed-1]
			page.NextCursor, err = encodeAlertReadCursor(alertReadCursor{
				Version: 1, Kind: kind, Filter: filter, ID: last.ID.String(), Time: last.EffectiveCutoff.Time.UnixMicro(),
			})
		}
		return err
	})
	if err != nil {
		return AlertMaintenanceSuppressionPage{}, err
	}
	return page, nil
}
