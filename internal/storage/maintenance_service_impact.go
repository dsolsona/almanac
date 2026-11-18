package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// The authority revision commits with membership and selector changes. The
// window revision and projection completion bind the impact page to its exact projection.
type maintenanceCursorSnapshot struct {
	AuthorityRevision  int64 `json:"authority_revision"`
	WindowRevision     int64 `json:"window_revision,omitzero"`
	ProjectionComplete bool  `json:"projection_complete,omitzero"`
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
	authority, err := q.ObserveServiceMaintenanceAuthority(ctx)
	if err != nil {
		return page, err
	}
	snapshot := maintenanceCursorSnapshot{AuthorityRevision: authority}
	if cursor.Time.IsZero() {
		cursor.Time = started
	} else if cursor.MaintenanceSnapshot == nil || *cursor.MaintenanceSnapshot != snapshot || cursor.Time.After(started) {
		return page, ErrMaintenanceSnapshotChanged
	}
	cursor.MaintenanceSnapshot = &snapshot
	page, err = readMaintenancePreview(ctx, q, kind, slos, services, limit, cursor)
	if err != nil {
		return MaintenanceImpactPage{}, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return MaintenanceImpactPage{}, err
	}
	return page, nil
}

func readMaintenancePreview(ctx context.Context, q *dbgen.Queries, kind MaintenanceScopeKind, slos, services []string, limit int, cursor agentCursor) (MaintenanceImpactPage, error) {
	var page MaintenanceImpactPage
	at := cursor.Time
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
		InventoryAt: timestamp(at), ScopeKind: string(kind), SloIds: sloIDs, ServiceIds: serviceIDs, AfterSlo: uuidOrZero(cursor.ID), PageLimit: int32(limit + 1),
	})
	if err != nil {
		return page, err
	}
	page = MaintenanceImpactPage{Total: count, InventoryAt: at, Ready: true}
	if len(rows) > limit {
		rows = rows[:limit]
		cursor.ID = rows[len(rows)-1].SloID.String()
		page.NextCursor, err = encodeAgentCursor(cursor)
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

func ReadMaintenanceImpact(ctx context.Context, databaseURL string, input MaintenanceImpactInput) (MaintenanceImpactPage, error) {
	id, ok := canonicalUUID(input.ID)
	if !ok || input.Revision < 0 || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return MaintenanceImpactPage{}, ErrMaintenanceInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithOptions(ctx, databaseURL, input.Proof.SessionVerifier, input.Now,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return MaintenanceImpactPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	const cursorKind = "maintenance-impact"
	limit, cursor, err := validateAgentPage(cursorKind, input.ID, input.Limit, input.Cursor)
	if err != nil {
		return MaintenanceImpactPage{}, ErrMaintenanceInvalid
	}
	revision := input.Revision
	if !cursor.Time.IsZero() {
		if cursor.MaintenanceSnapshot == nil || cursor.MaintenanceSnapshot.WindowRevision < 1 ||
			revision != 0 && revision != cursor.MaintenanceSnapshot.WindowRevision {
			return MaintenanceImpactPage{}, ErrMaintenanceInvalid
		}
		revision = cursor.MaintenanceSnapshot.WindowRevision
	}
	record, err := readMaintenanceRecord(ctx, q, id, revision)
	if err != nil {
		return MaintenanceImpactPage{}, err
	}
	at := started
	superseded, err := q.FindMaintenanceSupersededAt(ctx, dbgen.FindMaintenanceSupersededAtParams{ExclusionID: id, Revision: record.Revision})
	if err == nil {
		if superseded.Time.Before(at) {
			at = superseded.Time.UTC()
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return MaintenanceImpactPage{}, err
	}
	afterTime := time.Unix(0, 0).UTC()
	if !cursor.Time.IsZero() {
		if !validMaintenanceOverlayTime(cursor.SnapshotAt) {
			return MaintenanceImpactPage{}, ErrMaintenanceInvalid
		}
		if cursor.SnapshotAt.After(at) {
			return MaintenanceImpactPage{}, ErrMaintenanceSnapshotChanged
		}
		at, afterTime = cursor.SnapshotAt, cursor.Time
	}
	authority, err := q.ObserveServiceMaintenanceAuthority(ctx)
	if err != nil {
		return MaintenanceImpactPage{}, err
	}
	terminalEmpty := record.Tombstone || superseded.Valid && !superseded.Time.After(record.Start)
	snapshot := maintenanceCursorSnapshot{AuthorityRevision: authority, WindowRevision: record.Revision, ProjectionComplete: true}
	ready := true
	if isServiceMaintenanceScope(record.ScopeKind) && !terminalEmpty {
		snapshot.ProjectionComplete, err = q.ReadMaintenanceProjectionReady(ctx, dbgen.ReadMaintenanceProjectionReadyParams{ExclusionID: id, Revision: record.Revision})
		if err != nil {
			return MaintenanceImpactPage{}, err
		}
		// A superseded revision has no remaining projection to prepare. Its
		// recorded impact is reconstructed from the immutable membership journal.
		ready = !at.Before(record.Start) && (snapshot.ProjectionComplete || superseded.Valid)
	}
	if !cursor.Time.IsZero() && (cursor.MaintenanceSnapshot == nil || *cursor.MaintenanceSnapshot != snapshot) {
		return MaintenanceImpactPage{}, ErrMaintenanceSnapshotChanged
	}

	if terminalEmpty {
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return MaintenanceImpactPage{}, err
		}
		return MaintenanceImpactPage{InventoryAt: at, Ready: true, Items: []MaintenanceImpactMember{}}, nil
	}

	count, err := q.CountMaintenanceImpactMembers(ctx, dbgen.CountMaintenanceImpactMembersParams{
		ExclusionID: id, Revision: record.Revision, InventoryAt: timestamp(at),
	})
	if err != nil {
		return MaintenanceImpactPage{}, err
	}
	rows, err := q.ListMaintenanceImpactMembers(ctx, dbgen.ListMaintenanceImpactMembersParams{
		ExclusionID: id, Revision: record.Revision, InventoryAt: timestamp(at),
		AfterSlo: uuidOrZero(cursor.ID), AfterTime: timestamp(afterTime), PageLimit: int32(limit + 1),
	})
	if err != nil {
		return MaintenanceImpactPage{}, err
	}
	page := MaintenanceImpactPage{InventoryAt: at, Total: count, Ready: ready}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor, err = encodeAgentCursor(agentCursor{
			Version: 1, Kind: cursorKind, AgentID: input.ID, SnapshotAt: at, Time: last.StartsAt.Time.UTC(), ID: last.SloID.String(),
			MaintenanceSnapshot: &snapshot,
		})
		if err != nil {
			return MaintenanceImpactPage{}, err
		}
	}
	page.Items = make([]MaintenanceImpactMember, 0, len(rows))
	for _, row := range rows {
		page.Items = append(page.Items, MaintenanceImpactMember{
			SloID: row.SloID.String(), SloName: row.SloName, ServiceID: row.ServiceID.String(), ServiceName: row.ServiceName,
			Start: row.StartsAt.Time.UTC(), End: row.EndsAt.Time.UTC(),
		})
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return MaintenanceImpactPage{}, err
	}
	return page, nil
}
