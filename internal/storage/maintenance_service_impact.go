package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type MaintenanceScopePreviewInput struct {
	Proof              DatasourceProof
	ScopeKind          MaintenanceScopeKind
	SloIDs, ServiceIDs []string
	Limit              int
	Cursor             string
	Now                func() time.Time
}

type MaintenanceImpactInput struct {
	Proof    DatasourceProof
	ID       string
	Revision int64
	Limit    int
	Cursor   string
	Now      func() time.Time
}

type MaintenanceImpactMember struct {
	SloID, SloName, ServiceID, ServiceName string
	Start, End                             time.Time
}

type MaintenanceImpactPage struct {
	Items       []MaintenanceImpactMember
	Total       int64
	NextCursor  string
	InventoryAt time.Time
	Ready       bool
}

func PreviewMaintenanceScope(ctx context.Context, databaseURL string, input MaintenanceScopePreviewInput) (MaintenanceImpactPage, error) {
	var page MaintenanceImpactPage
	kind, slos, services, err := normalizeMaintenanceSelector(input.ScopeKind, input.SloIDs, input.ServiceIDs)
	if err != nil || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return page, ErrMaintenanceInvalid
	}
	const cursorKind = "maintenance-preview"
	digest := sha256.Sum256([]byte(string(kind) + ":" + strings.Join(slos, ",") + ":" + strings.Join(services, ",")))
	cursorScope := hex.EncodeToString(digest[:])
	limit, cursor, err := validateAgentPage(cursorKind, cursorScope, input.Limit, input.Cursor)
	if err != nil {
		return page, ErrMaintenanceInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return page, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	at := started
	if !cursor.Time.IsZero() {
		if cursor.Time.After(started) {
			return page, ErrMaintenanceInvalid
		}
		at = cursor.Time
	}
	page, err = readMaintenancePreview(ctx, q, kind, slos, services, at, uuidOrZero(cursor.ID), limit, cursorKind, cursorScope)
	if err != nil {
		return MaintenanceImpactPage{}, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return MaintenanceImpactPage{}, err
	}
	return page, nil
}

func readMaintenancePreview(ctx context.Context, q *dbgen.Queries, kind MaintenanceScopeKind, slos, services []string, at time.Time, after pgtype.UUID, limit int, cursorKind, cursorScope string) (MaintenanceImpactPage, error) {
	var page MaintenanceImpactPage
	sloIDs, serviceIDs := maintenanceSelectorUUIDs(slos), maintenanceSelectorUUIDs(services)
	if kind == MaintenanceScopeServices {
		count, err := q.CountMaintenancePreviewServices(ctx, dbgen.CountMaintenancePreviewServicesParams{ServiceIds: serviceIDs, InventoryAt: timestamp(at)})
		if err != nil {
			return page, err
		}
		if count != int64(len(serviceIDs)) {
			return page, ErrMaintenanceNotFound
		}
	}
	count, err := q.CountMaintenancePreviewMembers(ctx, dbgen.CountMaintenancePreviewMembersParams{
		InventoryAt: timestamp(at), ScopeKind: string(kind), SloIds: sloIDs, ServiceIds: serviceIDs,
	})
	if err != nil {
		return page, err
	}
	if kind == MaintenanceScopeSlos && count != int64(len(sloIDs)) {
		return page, ErrMaintenanceNotFound
	}
	rows, err := q.ListMaintenancePreviewMembers(ctx, dbgen.ListMaintenancePreviewMembersParams{
		InventoryAt: timestamp(at), ScopeKind: string(kind), SloIds: sloIDs, ServiceIds: serviceIDs, AfterSlo: after, PageLimit: int32(limit + 1),
	})
	if err != nil {
		return page, err
	}
	page = MaintenanceImpactPage{Total: count, InventoryAt: at, Ready: true}
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: cursorKind, AgentID: cursorScope, Time: at, ID: rows[len(rows)-1].SloID.String()})
		if err != nil {
			return page, err
		}
	}
	page.Items = make([]MaintenanceImpactMember, 0, len(rows))
	for _, row := range rows {
		page.Items = append(page.Items, MaintenanceImpactMember{
			SloID: row.SloID.String(), SloName: row.SloName, ServiceID: row.ServiceID.String(), ServiceName: row.ServiceName,
		})
	}
	return page, nil
}

func maintenanceSelectorUUIDs(ids []string) []pgtype.UUID {
	result := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		result[i], _ = canonicalUUID(id)
	}
	return result
}
