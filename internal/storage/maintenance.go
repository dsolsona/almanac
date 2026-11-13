package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrMaintenanceInvalid         = errors.New("storage: invalid maintenance request")
	ErrMaintenanceNotFound        = errors.New("storage: maintenance resource not found")
	ErrMaintenanceConflict        = errors.New("storage: maintenance revision or replay conflict")
	ErrMaintenanceAlignment       = errors.New("storage: maintenance bounds are not aligned")
	ErrMaintenanceSnapshotChanged = errors.New("storage: maintenance snapshot changed")
)

type MaintenanceConflictError struct{ CurrentRevision int64 }

func (e *MaintenanceConflictError) Error() string { return ErrMaintenanceConflict.Error() }
func (e *MaintenanceConflictError) Unwrap() error { return ErrMaintenanceConflict }

type MaintenanceAlignmentError struct {
	SloIDs []string
	// Zero suggestions mean no common bounds can be represented; split the scope.
	SuggestedStart, SuggestedEnd time.Time
}

func (e *MaintenanceAlignmentError) Error() string { return ErrMaintenanceAlignment.Error() }
func (e *MaintenanceAlignmentError) Unwrap() error { return ErrMaintenanceAlignment }

type MaintenanceRecord struct {
	ID, Title, Reason, CreatorID, ActorID, Operation, RequestID, AuditID string
	Revision                                                             int64
	Start, End, CreatedAt, UpdatedAt                                     time.Time
	SloIDs                                                               []string
	Tombstone                                                            bool
}

type CreateMaintenanceInput struct {
	Proof                     DatasourceProof
	ID                        string
	Revision                  int64
	Title, Reason             string
	Start, End                time.Time
	SloIDs                    []string
	IdempotencyKey, RequestID string
	Now                       func() time.Time
}
type EditMaintenanceInput = CreateMaintenanceInput

type MaintenanceCommandInput struct {
	Proof                             DatasourceProof
	ID                                string
	Revision                          int64
	Reason, IdempotencyKey, RequestID string
	Now                               func() time.Time
}
type EndMaintenanceInput struct {
	Proof                             DatasourceProof
	ID                                string
	Revision                          int64
	End                               time.Time
	Reason, IdempotencyKey, RequestID string
	Now                               func() time.Time
}
type ReadMaintenanceInput struct {
	Proof    DatasourceProof
	ID       string
	Revision int64
	Now      func() time.Time
}
type ListMaintenanceInput struct {
	Proof  DatasourceProof
	Limit  int
	Cursor string
	Now    func() time.Time
}
type MaintenancePage struct {
	Items      []MaintenanceRecord
	NextCursor string
}

// These internal operations are not wired to HTTP/CLI or production calculators.
func CreateMaintenance(ctx context.Context, databaseURL string, input CreateMaintenanceInput) (MaintenanceRecord, error) {
	return mutateMaintenance(ctx, databaseURL, input, "create")
}
func EditMaintenance(ctx context.Context, databaseURL string, input EditMaintenanceInput) (MaintenanceRecord, error) {
	return mutateMaintenance(ctx, databaseURL, input, "edit")
}
func EndMaintenance(ctx context.Context, databaseURL string, input EndMaintenanceInput) (MaintenanceRecord, error) {
	return mutateMaintenance(ctx, databaseURL, CreateMaintenanceInput{Proof: input.Proof, ID: input.ID, Revision: input.Revision, End: input.End, Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, RequestID: input.RequestID, Now: input.Now}, "end")
}
func CancelMaintenance(ctx context.Context, databaseURL string, input MaintenanceCommandInput) (MaintenanceRecord, error) {
	return mutateMaintenance(ctx, databaseURL, maintenanceCommand(input), "cancel")
}
func WithdrawMaintenance(ctx context.Context, databaseURL string, input MaintenanceCommandInput) (MaintenanceRecord, error) {
	return mutateMaintenance(ctx, databaseURL, maintenanceCommand(input), "withdraw")
}
func maintenanceCommand(input MaintenanceCommandInput) CreateMaintenanceInput {
	return CreateMaintenanceInput{Proof: input.Proof, ID: input.ID, Revision: input.Revision, Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, RequestID: input.RequestID, Now: input.Now}
}

func validMaintenanceTime(at time.Time) bool {
	_, offset := at.Zone()
	return !at.IsZero() && at.Year() >= 1970 && at.Year() <= 9999 && offset == 0 && at.Nanosecond() == 0
}
func validMaintenanceText(text string, limit int) bool {
	return utf8.ValidString(text) && strings.TrimSpace(text) != "" && len(text) <= limit && !strings.ContainsRune(text, 0)
}
func maintenanceScope(ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, ErrMaintenanceInvalid
	}
	result := slices.Clone(ids)
	slices.Sort(result)
	for i, id := range result {
		if _, ok := canonicalUUID(id); !ok || (i > 0 && result[i-1] == id) {
			return nil, ErrMaintenanceInvalid
		}
	}
	return result, nil
}

func mutateMaintenance(ctx context.Context, databaseURL string, input CreateMaintenanceInput, op string) (MaintenanceRecord, error) {
	var empty MaintenanceRecord
	if input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) || !validPrintableASCII(input.IdempotencyKey, 1, 128) || !validMaintenanceText(input.RequestID, 512) || !validMaintenanceText(input.Reason, 4096) {
		return empty, ErrMaintenanceInvalid
	}
	id, validID := canonicalUUID(input.ID)
	if op == "create" {
		if input.ID != "" || input.Revision != 0 {
			return empty, ErrMaintenanceInvalid
		}
	} else if !validID || input.Revision < 1 || input.Revision == math.MaxInt64 {
		return empty, ErrMaintenanceInvalid
	}
	if op == "create" || op == "edit" {
		var err error
		input.SloIDs, err = maintenanceScope(input.SloIDs)
		if err != nil || !validMaintenanceText(input.Title, 512) || !validMaintenanceTime(input.Start) || !validMaintenanceTime(input.End) || !input.Start.Before(input.End) {
			return empty, ErrMaintenanceInvalid
		}
	} else if op == "end" && !validMaintenanceTime(input.End) {
		return empty, ErrMaintenanceInvalid
	}
	request := struct {
		ID            string
		Revision      int64
		Title, Reason string
		Start, End    time.Time
		SloIDs        []string
	}{input.ID, input.Revision, input.Title, input.Reason, input.Start, input.End, input.SloIDs}
	raw, err := json.Marshal(request, json.Deterministic(true))
	if err != nil {
		return empty, err
	}
	digest := sha256.Sum256(raw)
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor")
	if err != nil {
		return empty, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	operation := "maintenance." + op
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", user.ID.String()+":"+operation+":"+input.IdempotencyKey); err != nil {
		return empty, err
	}
	receiptKey := dbgen.FindAgentLifecycleReceiptParams{ActorID: user.ID, Operation: operation, IdempotencyKey: input.IdempotencyKey}
	receipt, err := q.FindAgentLifecycleReceipt(ctx, receiptKey)
	now := normalizedAgentNow(input.Now)
	if err == nil && now.Before(receipt.ExpiresAt.Time) {
		if !bytes.Equal(receipt.Digest, digest[:]) {
			return empty, ErrMaintenanceConflict
		}
		var saved struct {
			Version int
			Record  MaintenanceRecord
		}
		if json.Unmarshal(receipt.Result, &saved) != nil || saved.Version != 1 || saved.Record.ID == "" || saved.Record.Revision < 1 {
			return empty, errors.New("storage: corrupt maintenance receipt")
		}
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return empty, err
		}
		return saved.Record, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	if err == nil {
		if _, err := q.DeleteExpiredAgentLifecycleReceipt(ctx, dbgen.DeleteExpiredAgentLifecycleReceiptParams{ActorID: user.ID, Operation: operation, IdempotencyKey: input.IdempotencyKey, Now: timestamp(now)}); err != nil {
			return empty, err
		}
	}
	var old MaintenanceRecord
	if op != "create" {
		root, err := q.LockMaintenanceIdentity(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return empty, ErrMaintenanceNotFound
		}
		if err != nil {
			return empty, err
		}
		if root.CurrentRevision != input.Revision {
			return empty, &MaintenanceConflictError{CurrentRevision: root.CurrentRevision}
		}
		old, err = readMaintenanceRecord(ctx, q, id, root.CurrentRevision)
		if err != nil {
			return empty, err
		}
		if old.Tombstone {
			return empty, &MaintenanceConflictError{CurrentRevision: old.Revision}
		}
		if op != "edit" {
			input.Title, input.Start, input.SloIDs = old.Title, old.Start, slices.Clone(old.SloIDs)
			if op != "end" {
				input.End = old.End
			}
		}
	} else {
		id, _ = canonicalUUID(uuid.New().String())
	}
	affected := append(slices.Clone(old.SloIDs), input.SloIDs...)
	slices.Sort(affected)
	affected = slices.Compact(affected)
	// The write bound applies to removed and added scope together.
	if len(affected) > 100 {
		return empty, ErrMaintenanceInvalid
	}
	for _, value := range affected {
		sloID, _ := canonicalUUID(value)
		if _, err := q.LockMaintenanceSlo(ctx, sloID); errors.Is(err, pgx.ErrNoRows) {
			return empty, ErrMaintenanceNotFound
		} else if err != nil {
			return empty, err
		}
	}
	for _, value := range affected {
		sloID, _ := canonicalUUID(value)
		if _, err := q.LockMaintenanceState(ctx, sloID); err != nil {
			return empty, err
		}
	}
	now = normalizedAgentNow(input.Now)
	if err := maintenancePermission(op, user.Role, old, input, now); err != nil {
		return empty, err
	}
	if err := validateMaintenanceGrids(ctx, q, input.SloIDs, input.Start, input.End); err != nil {
		return empty, err
	}
	if op == "create" {
		if err := q.InsertMaintenanceIdentity(ctx, dbgen.InsertMaintenanceIdentityParams{ID: id, CreatorID: user.ID, CreatedAt: timestamp(now)}); err != nil {
			return empty, err
		}
	} else if err := q.AdvanceMaintenanceIdentity(ctx, dbgen.AdvanceMaintenanceIdentityParams{ID: id, CurrentRevision: input.Revision + 1, UpdatedAt: timestamp(now)}); err != nil {
		return empty, err
	}
	revision := input.Revision + 1
	auditID, err := q.InsertMaintenanceAudit(ctx, dbgen.InsertMaintenanceAuditParams{Actor: user.ID.String(), SubjectID: id, Action: operation, OccurredAt: timestamp(now), RequestID: input.RequestID})
	if err != nil {
		return empty, err
	}
	tombstone := op == "cancel" || op == "withdraw"
	if err := q.InsertMaintenanceRevision(ctx, dbgen.InsertMaintenanceRevisionParams{ExclusionID: id, Revision: revision, Title: input.Title, Reason: input.Reason, StartsAt: timestamp(input.Start), EndsAt: timestamp(input.End), Operation: op, Tombstone: tombstone, ActorID: user.ID, RecordedAt: timestamp(now), RequestID: input.RequestID, AuditID: auditID}); err != nil {
		return empty, err
	}
	for _, value := range input.SloIDs {
		sloID, _ := canonicalUUID(value)
		if err := q.InsertMaintenanceScope(ctx, dbgen.InsertMaintenanceScopeParams{ExclusionID: id, Revision: revision, SloID: sloID}); err != nil {
			return empty, err
		}
	}
	for _, value := range affected {
		sloID, _ := canonicalUUID(value)
		wasIncluded := slices.Contains(old.SloIDs, value) && !old.Tombstone
		included := slices.Contains(input.SloIDs, value) && !tombstone
		changed := wasIncluded != included || (included && (!old.Start.Equal(input.Start) || !old.End.Equal(input.End)))
		state, err := q.AdvanceMaintenanceState(ctx, dbgen.AdvanceMaintenanceStateParams{SloID: sloID, ChangedAt: timestamp(now)})
		if err != nil {
			return empty, err
		}
		if err := q.InsertMaintenanceChange(ctx, dbgen.InsertMaintenanceChangeParams{SloID: sloID, Revision: state, ExclusionID: id, ExclusionRevision: revision, Included: included, CoverageChanged: changed, RecordedAt: timestamp(now)}); err != nil {
			return empty, err
		}
		var start, end pgtype.Timestamptz
		if changed {
			a, b := input.Start, input.End
			if wasIncluded {
				a, b = old.Start, old.End
				if included {
					if input.Start.Before(a) {
						a = input.Start
					}
					if input.End.After(b) {
						b = input.End
					}
				}
			}
			start, end = timestamp(a), timestamp(b)
		}
		if err := q.EnqueueMaintenanceWork(ctx, dbgen.EnqueueMaintenanceWorkParams{SloID: sloID, DesiredRevision: state, AffectedStart: start, AffectedEnd: end, NextAttemptAt: timestamp(now)}); err != nil {
			return empty, err
		}
	}
	result, err := readMaintenanceRecord(ctx, q, id, revision)
	if err != nil {
		return empty, err
	}
	saved, err := json.Marshal(struct {
		Version int
		Record  MaintenanceRecord
	}{1, result})
	if err != nil {
		return empty, err
	}
	if err := q.InsertAgentLifecycleReceipt(ctx, dbgen.InsertAgentLifecycleReceiptParams{ActorID: user.ID, Operation: operation, IdempotencyKey: input.IdempotencyKey, Digest: digest[:], Result: saved, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(24 * time.Hour))}); err != nil {
		return empty, err
	}
	ended := normalizedAgentNow(input.Now)
	if err := maintenancePermission(op, user.Role, old, input, ended); err != nil {
		return empty, err
	}
	if err := finishAgentRead(ctx, tx, func() time.Time { return ended }, started, user, installation, session); err != nil {
		return empty, err
	}
	return result, nil
}

func maintenancePermission(op, role string, old MaintenanceRecord, input CreateMaintenanceInput, now time.Time) error {
	if now.IsZero() {
		return ErrMaintenanceInvalid
	}
	switch op {
	case "cancel":
		if !old.Start.After(now) {
			return ErrAgentForbidden
		}
	case "withdraw":
		if old.Start.After(now) {
			return ErrMaintenanceInvalid
		}
	case "end":
		if old.Start.After(now) || !old.End.After(now) || !input.End.After(now) || !input.End.After(old.Start) {
			return ErrAgentForbidden
		}
		return nil
	}
	if role != "administrator" && ((!old.Start.IsZero() && !old.Start.After(now)) || !input.Start.After(now)) {
		return ErrAgentForbidden
	}
	return nil
}

// Validate all effective definition grids touched by the proposed interval.
// Future semantic definition/import paths must call this under the same SLO lock.
func validateMaintenanceGrids(ctx context.Context, q *dbgen.Queries, ids []string, start, end time.Time) error {
	cadence := int64(1)
	for _, value := range ids {
		id, _ := canonicalUUID(value)
		grids, err := q.ListMaintenanceGrids(ctx, id)
		if err != nil {
			return err
		}
		if len(grids) == 0 {
			return ErrMaintenanceNotFound
		}
		for i, g := range grids {
			if i+1 < len(grids) && !grids[i+1].ActivatedAt.Time.After(start) {
				continue
			}
			if i > 0 && !g.ActivatedAt.Time.Before(end) {
				continue
			}
			if g.MeasurementKind != "event_counts" || g.ComponentMode != "interval_count" || g.CadenceSeconds <= 0 {
				return ErrMaintenanceInvalid
			}
			next := int64(g.CadenceSeconds)
			a, b := cadence, next
			for b != 0 {
				a, b = b, a%b
			}
			if cadence/a > math.MaxInt64/next {
				return &MaintenanceAlignmentError{SloIDs: slices.Clone(ids)}
			}
			cadence = cadence / a * next
		}
	}
	return maintenanceAlignment(cadence, start, end, ids)
}

func maintenanceAlignment(cadence int64, start, end time.Time, ids []string) error {
	if start.Unix()%cadence == 0 && end.Unix()%cadence == 0 {
		return nil
	}
	a := start.Unix() / cadence * cadence
	b := end.Unix() / cadence * cadence
	if b < end.Unix() {
		if b > math.MaxInt64-cadence {
			return &MaintenanceAlignmentError{SloIDs: slices.Clone(ids)}
		}
		b += cadence
	}
	suggestedStart, suggestedEnd := time.Unix(a, 0).UTC(), time.Unix(b, 0).UTC()
	if !validMaintenanceTime(suggestedStart) || !validMaintenanceTime(suggestedEnd) {
		return &MaintenanceAlignmentError{SloIDs: slices.Clone(ids)}
	}
	return &MaintenanceAlignmentError{SloIDs: slices.Clone(ids), SuggestedStart: suggestedStart, SuggestedEnd: suggestedEnd}
}

func readMaintenanceRecord(ctx context.Context, q *dbgen.Queries, id pgtype.UUID, revision int64) (MaintenanceRecord, error) {
	r, err := q.ReadMaintenanceRevision(ctx, dbgen.ReadMaintenanceRevisionParams{ID: id, Revision: revision})
	if errors.Is(err, pgx.ErrNoRows) {
		return MaintenanceRecord{}, ErrMaintenanceNotFound
	}
	if err != nil {
		return MaintenanceRecord{}, err
	}
	scope, err := q.ListMaintenanceScopes(ctx, dbgen.ListMaintenanceScopesParams{ExclusionID: id, Revision: r.Revision})
	if err != nil {
		return MaintenanceRecord{}, err
	}
	ids := make([]string, len(scope))
	for i, id := range scope {
		ids[i] = id.String()
	}
	return MaintenanceRecord{ID: id.String(), Revision: r.Revision, Title: r.Title, Reason: r.Reason, Start: r.StartsAt.Time.UTC(), End: r.EndsAt.Time.UTC(), SloIDs: ids, Tombstone: r.Tombstone, CreatorID: r.CreatorID.String(), ActorID: r.ActorID.String(), CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.RecordedAt.Time.UTC(), Operation: r.Operation, RequestID: r.RequestID, AuditID: r.AuditID.String()}, nil
}

func ReadMaintenance(ctx context.Context, databaseURL string, input ReadMaintenanceInput) (MaintenanceRecord, error) {
	id, ok := canonicalUUID(input.ID)
	if !ok || input.Revision < 0 || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return MaintenanceRecord{}, ErrMaintenanceInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor", "viewer")
	if err != nil {
		return MaintenanceRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	result, err := readMaintenanceRecord(ctx, q, id, input.Revision)
	if err != nil {
		return MaintenanceRecord{}, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return MaintenanceRecord{}, err
	}
	return result, nil
}

func ListMaintenance(ctx context.Context, databaseURL string, input ListMaintenanceInput) (MaintenancePage, error) {
	var page MaintenancePage
	if input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return page, ErrMaintenanceInvalid
	}
	limit, cursor, err := validateAgentPage("maintenance", "", input.Limit, input.Cursor)
	if err != nil {
		return page, ErrMaintenanceInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor", "viewer")
	if err != nil {
		return page, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	after := timestamp(cursor.Time)
	if cursor.Time.IsZero() {
		after = timestamp(time.Unix(0, 0))
	}
	rows, err := q.ListMaintenanceIdentities(ctx, dbgen.ListMaintenanceIdentitiesParams{AfterTime: after, AfterID: uuidOrZero(cursor.ID), PageLimit: int32(limit + 1)})
	if err != nil {
		return page, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: "maintenance", Time: last.CreatedAt.Time.UTC(), ID: last.ID.String()})
		if err != nil {
			return page, err
		}
	}
	page.Items = make([]MaintenanceRecord, 0, len(rows))
	for _, row := range rows {
		record, err := readMaintenanceRecord(ctx, q, row.ID, 0)
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
