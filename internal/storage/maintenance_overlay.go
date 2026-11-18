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
	SloID      string
	Start, End time.Time
}

func ListSloMaintenance(ctx context.Context, databaseURL string, input ListSloMaintenanceInput) (MaintenancePage, error) {
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
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor", "viewer")
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
