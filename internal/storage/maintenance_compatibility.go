package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type maintenanceCompatibilityProgress struct {
	Scanned        int
	IndexedThrough int64
	State          string
}
type maintenanceCompatibilityProof struct {
	SloID                                               string
	ComputedRevision, RequestedRevision, IndexedThrough int64
	Start, End                                          time.Time
	Kind, ComparisonID                                  string
}

// These unwired primitives use caller admission and transaction lifetime. A
// capture follows SLO -> exclusion authority -> input guard -> build -> index
// authority. A catch-up turn locks only build then index authority and never
// acquires an earlier writer lock. Pins survive crashes and incomplete turns.
func prepareMaintenanceCompatibility(ctx context.Context, tx pgx.Tx, id pgtype.UUID, now time.Time) (dbgen.AlmanacMaintenanceBuild, error) {
	b, err := captureMaintenanceBuild(ctx, tx, id, nil, now)
	if err != nil {
		return b, err
	}
	if b.Phase == "cancelled" {
		return b, ErrMaintenanceCompileStale
	}
	q := dbgen.New(tx)
	if err := q.EnsureMaintenanceCompatibility(ctx, id); err != nil {
		return b, err
	}
	_, err = q.LockMaintenanceCompatibility(ctx, id)
	return b, err
}

func advanceMaintenanceCompatibility(ctx context.Context, tx pgx.Tx, id pgtype.UUID, limit int) (maintenanceCompatibilityProgress, error) {
	var result maintenanceCompatibilityProgress
	if !id.Valid || limit < 1 || limit > 1000 {
		return result, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	b, err := q.LockMaintenanceBuild(ctx, id)
	if err != nil {
		return result, err
	}
	if b.Phase == "cancelled" {
		return result, ErrMaintenanceCompileStale
	}
	a, err := q.LockMaintenanceCompatibility(ctx, b.SloID)
	if err != nil {
		return result, err
	}
	result.IndexedThrough = a.IndexedThrough
	result.State = "pending"
	if a.IndexedThrough >= b.TargetRevision {
		result.State = "complete"
		return result, nil
	}
	rows, err := q.ReadMaintenanceCompilerEvents(ctx, dbgen.ReadMaintenanceCompilerEventsParams{SloID: b.SloID, AfterRevision: a.IndexedThrough, TargetRevision: b.TargetRevision, PageLimit: int32(limit)})
	if err != nil {
		return result, err
	}
	result.Scanned = len(rows)
	next := a.IndexedThrough
	for _, row := range rows {
		if row.Revision != next+1 || !row.StartsAt.Valid || !row.EndsAt.Valid || !row.Tombstone.Valid || row.Included && (!row.Scoped || row.Tombstone.Bool) {
			result.State = "unavailable"
			break
		}
		next = row.Revision
	}
	if len(rows) < limit && next < b.TargetRevision {
		result.State = "unavailable"
	}
	if result.State == "unavailable" {
		return result, q.MarkMaintenanceCompatibilityUnavailable(ctx, dbgen.MarkMaintenanceCompatibilityUnavailableParams{SloID: b.SloID, Unavailable: true})
	}
	// Only this validated contiguous catch-up path may clear a historical gap.
	if err := q.MarkMaintenanceCompatibilityUnavailable(ctx, dbgen.MarkMaintenanceCompatibilityUnavailableParams{SloID: b.SloID, Unavailable: false}); err != nil {
		return result, err
	}
	for _, row := range rows {
		ok, err := q.AnalyzeMaintenanceCompatibilityEvent(ctx, dbgen.AnalyzeMaintenanceCompatibilityEventParams{SloID: b.SloID, Revision: row.Revision})
		if err != nil {
			return result, err
		}
		if !ok {
			result.State = "unavailable"
			return result, q.MarkMaintenanceCompatibilityUnavailable(ctx, dbgen.MarkMaintenanceCompatibilityUnavailableParams{SloID: b.SloID, Unavailable: true})
		}
		result.IndexedThrough = row.Revision
	}
	if result.IndexedThrough >= b.TargetRevision {
		result.State = "complete"
	}
	return result, nil
}

// A proof preserves computed and requested revisions; it never relabels stored
// evidence. Final source/definition/current-authority publication fences remain
// the caller's responsibility. Every read participates in its source RR snapshot.
func readMaintenanceCompatibility(ctx context.Context, tx pgx.Tx, id pgtype.UUID, computed, requested int64, start, end time.Time) (maintenanceCompatibilityProof, error) {
	p := maintenanceCompatibilityProof{SloID: id.String(), ComputedRevision: computed, RequestedRevision: requested, Start: start, End: end}
	if !id.Valid || computed < 0 || requested < computed || !validMaintenanceTime(start) || !validMaintenanceTime(end) || !start.Before(end) {
		return p, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	a, err := q.ReadMaintenanceCompatibilityAuthority(ctx, id)
	if err != nil {
		return p, err
	}
	if a.Isolation != "repeatable read" || requested > a.CurrentRevision {
		return p, ErrMaintenanceSnapshotChanged
	}
	p.IndexedThrough = a.IndexedThrough
	// Identical immutable mask identity needs no intervening-history proof.
	if computed == requested {
		p.Kind = "same_revision"
		return p, nil
	}
	if a.IndexedThrough < requested {
		if a.Unavailable {
			return p, ErrMaintenanceCompileUnavailable
		}
		return p, ErrMaintenanceCompilePending
	}
	changed, err := q.HasMaintenanceChangedInput(ctx, dbgen.HasMaintenanceChangedInputParams{SloID: id, ScopeOrdinal: a.ScopeOrdinal, AfterRevision: computed, ThroughRevision: requested, WindowStart: timestamp(start), WindowEnd: timestamp(end)})
	if err != nil {
		return p, err
	}
	p.Kind = "indexed_absence"
	if !changed {
		return p, nil
	}
	p.Kind = "changed"
	comparison, err := q.ReadMaintenanceEqualComparison(ctx, dbgen.ReadMaintenanceEqualComparisonParams{SloID: id, FirstRevision: computed, SecondRevision: requested})
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.Kind = "equal_union"
	p.ComparisonID = comparison.String()
	return p, nil
}
