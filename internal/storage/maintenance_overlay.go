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
	if !input.Scoped && (!input.IncludeOverflow || input.Cursor != "") {
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
	kind := "slo-maintenance"
	if input.Scoped {
		kind = "scoped-slo-maintenance"
	}
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
	page := MaintenancePage{}
	rows, err := maintenanceOverlayRows(ctx, q, input, dbgen.ListScopedSloMaintenanceOverlapsParams{
		SloID: sloID, WindowStart: timestamp(input.Start), WindowEnd: timestamp(input.End),
		AfterTime: timestamp(after), AfterID: uuidOrZero(cursor.ID), PageLimit: int32(limit + 1),
	}, &page.Pending)
	if err != nil {
		return MaintenancePage{}, err
	}
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
			page.Overflow, err = maintenanceOverlayOverflow(ctx, q, input.Scoped, dbgen.SummarizeScopedSloMaintenanceOverflowParams{
				SloID: sloID, WindowStart: timestamp(input.Start), WindowEnd: timestamp(input.End),
				AfterTime: afterTime, AfterID: afterID,
			})
			if err != nil {
				return MaintenancePage{}, err
			}
		}
	}
	page.Items = make([]MaintenanceRecord, 0, len(rows))
	for _, row := range rows {
		record, err := readMaintenanceRecord(ctx, q, row.ExclusionID, row.Revision)
		if err != nil {
			return MaintenancePage{}, err
		}
		if input.Scoped {
			record.EffectiveStart, record.EffectiveEnd = row.StartsAt.Time.UTC(), row.EndsAt.Time.UTC()
		}
		page.Items = append(page.Items, record)
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return MaintenancePage{}, err
	}
	return page, nil
}

func maintenanceOverlayRows(ctx context.Context, q *dbgen.Queries, input ListSloMaintenanceInput, args dbgen.ListScopedSloMaintenanceOverlapsParams, pending *bool) ([]dbgen.ListScopedSloMaintenanceOverlapsRow, error) {
	if input.Scoped {
		var err error
		*pending, err = q.HasPendingServiceMaintenance(ctx, dbgen.HasPendingServiceMaintenanceParams{
			SloID: args.SloID, StartAt: args.WindowStart, EndAt: args.WindowEnd,
		})
		if err != nil || *pending {
			return nil, err
		}
		return q.ListScopedSloMaintenanceOverlaps(ctx, args)
	}
	rows, err := q.ListSloMaintenanceOverlaps(ctx, dbgen.ListSloMaintenanceOverlapsParams(args))
	if err != nil {
		return nil, err
	}
	out := make([]dbgen.ListScopedSloMaintenanceOverlapsRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, dbgen.ListScopedSloMaintenanceOverlapsRow{
			ExclusionID: row.ExclusionID, Revision: row.Revision, StartsAt: row.StartsAt,
		})
	}
	return out, nil
}

func maintenanceOverlayOverflow(ctx context.Context, q *dbgen.Queries, scoped bool, args dbgen.SummarizeScopedSloMaintenanceOverflowParams) (*MaintenanceOverflow, error) {
	if scoped {
		row, err := q.SummarizeScopedSloMaintenanceOverflow(ctx, args)
		if err != nil {
			return nil, err
		}
		return &MaintenanceOverflow{Start: row.StartsAt.Time.UTC(), End: row.EndsAt.Time.UTC(), Count: row.WindowCount}, nil
	}
	row, err := q.SummarizeSloMaintenanceOverflow(ctx, dbgen.SummarizeSloMaintenanceOverflowParams(args))
	if err != nil {
		return nil, err
	}
	return &MaintenanceOverflow{Start: row.StartsAt.Time.UTC(), End: row.EndsAt.Time.UTC(), Count: row.WindowCount}, nil
}

// Chart viewports can end between observation boundaries. Retain PostgreSQL's
// microsecond precision for both the query and the cursor's range identity.
func validMaintenanceOverlayTime(at time.Time) bool {
	_, offset := at.Zone()
	return !at.IsZero() && at.Year() >= 1970 && at.Year() <= 9999 && offset == 0 && at.Nanosecond()%1000 == 0
}
