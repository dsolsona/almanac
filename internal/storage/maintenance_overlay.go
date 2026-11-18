package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type ListSloMaintenanceInput struct {
	ListMaintenanceInput
	SloID           string
	Start, End      time.Time
	IncludeOverflow bool
}

func ListSloMaintenance(ctx context.Context, databaseURL string, input ListSloMaintenanceInput) (MaintenancePage, error) {
	if !input.IncludeOverflow || input.Cursor != "" {
		return listSloMaintenance(ctx, databaseURL, input, pgx.TxOptions{})
	}
	var page MaintenancePage
	err := retryOperationalRead(ctx, func() error {
		var err error
		page, err = listSloMaintenance(ctx, databaseURL, input, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		return err
	})
	return page, err
}

func listSloMaintenance(ctx context.Context, databaseURL string, input ListSloMaintenanceInput, options pgx.TxOptions) (MaintenancePage, error) {
	sloID, ok := canonicalUUID(input.SloID)
	if !ok || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) ||
		!validMaintenanceOverlayTime(input.Start) || !validMaintenanceOverlayTime(input.End) || !input.Start.Before(input.End) {
		return MaintenancePage{}, ErrMaintenanceInvalid
	}
	const kind = "slo-maintenance"
	scope := input.SloID + "/" + input.Start.UTC().Format(time.RFC3339Nano) + "/" + input.End.UTC().Format(time.RFC3339Nano)
	limit, cursor, err := validateAgentPage(kind, scope, input.Limit, input.Cursor)
	if err != nil {
		return MaintenancePage{}, ErrMaintenanceInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, options, "administrator", "editor", "viewer")
	if err != nil {
		return MaintenancePage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if _, err := q.GetSlo(ctx, sloID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MaintenancePage{}, ErrMaintenanceNotFound
		}
		return MaintenancePage{}, err
	}

	after := cursor.Time
	if after.IsZero() {
		after = time.Unix(-1, 0).UTC()
	}
	rows, err := q.ListSloMaintenanceOverlaps(ctx, dbgen.ListSloMaintenanceOverlapsParams{
		SloID: sloID, WindowStart: timestamp(input.Start), WindowEnd: timestamp(input.End),
		AfterTime: timestamp(after), AfterID: uuidOrZero(cursor.ID), PageLimit: int32(limit + 1),
	})
	if err != nil {
		return MaintenancePage{}, err
	}
	page := MaintenancePage{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: kind, AgentID: scope, Time: last.StartsAt.Time.UTC(), ID: last.ExclusionID.String()})
		if err != nil {
			return MaintenancePage{}, err
		}
		if input.IncludeOverflow && input.Cursor == "" {
			// Keep a normal list page. The cluster replaces its last band and
			// therefore includes that record as well as the later pages.
			afterTime, afterID := timestamp(after), uuidOrZero(cursor.ID)
			if limit > 1 {
				previous := rows[limit-2]
				afterTime, afterID = previous.StartsAt, previous.ExclusionID
			}
			overflow, err := q.SummarizeSloMaintenanceOverflow(ctx, dbgen.SummarizeSloMaintenanceOverflowParams{
				SloID: sloID, WindowStart: timestamp(input.Start), WindowEnd: timestamp(input.End),
				AfterTime: afterTime, AfterID: afterID,
			})
			if err != nil {
				return MaintenancePage{}, err
			}
			page.Overflow = &MaintenanceOverflow{Start: overflow.StartsAt.Time.UTC(), End: overflow.EndsAt.Time.UTC(), Count: overflow.WindowCount}
		}
	}
	page.Items = make([]MaintenanceRecord, 0, len(rows))
	for _, row := range rows {
		record, err := readMaintenanceRecord(ctx, q, row.ExclusionID, row.Revision)
		if err != nil {
			return MaintenancePage{}, err
		}
		page.Items = append(page.Items, record)
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return MaintenancePage{}, err
	}
	return page, nil
}

// Chart viewports can end between observation boundaries. Retain PostgreSQL's
// microsecond precision for both the query and the cursor's range identity.
func validMaintenanceOverlayTime(at time.Time) bool {
	_, offset := at.Zone()
	return !at.IsZero() && at.Year() >= 1970 && at.Year() <= 9999 && offset == 0 && at.Nanosecond()%1000 == 0
}
