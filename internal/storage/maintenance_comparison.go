package storage

import (
	"context"
	"math"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Complete builds and their coverage are immutable and retained. A direct pair
// is canonicalized by revision, never inferred by transitivity or a digest.
func prepareMaintenanceComparison(ctx context.Context, tx pgx.Tx, first, second pgtype.UUID, now time.Time) (dbgen.AlmanacMaintenanceComparison, error) {
	var empty dbgen.AlmanacMaintenanceComparison
	if !first.Valid || !second.Valid || now.IsZero() {
		return empty, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	a, err := q.ReadMaintenanceBuild(ctx, first)
	if err != nil {
		return empty, err
	}
	b, err := q.ReadMaintenanceBuild(ctx, second)
	if err != nil {
		return empty, err
	}
	if a.SloID != b.SloID {
		return empty, ErrMaintenanceSnapshotChanged
	}
	if a.Phase != "complete" || b.Phase != "complete" {
		return empty, ErrMaintenanceCompilePending
	}
	if a.TargetRevision > b.TargetRevision {
		first, second = second, first
	}
	return q.CreateMaintenanceComparison(ctx, dbgen.CreateMaintenanceComparisonParams{ID: maintenanceCompilerID(), FirstBuild: first, SecondBuild: second, CreatedAt: timestamp(now)})
}

// Each turn owns the row lock, so no cross-transaction lease or in-memory state
// is needed. At most limit rows per side are read; equal prefixes share one key.
// A crash/cancellation rolls the caller's transaction back without advancement.
func advanceMaintenanceComparison(ctx context.Context, tx pgx.Tx, id pgtype.UUID, limit int, now time.Time) (dbgen.AlmanacMaintenanceComparison, error) {
	var empty dbgen.AlmanacMaintenanceComparison
	if !id.Valid || limit < 1 || limit > 1000 || now.IsZero() {
		return empty, ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	c, err := q.LockMaintenanceComparison(ctx, id)
	if err != nil {
		return empty, err
	}
	if c.State != "pending" {
		return c, nil
	}
	a, err := q.ReadMaintenanceUnionPage(ctx, dbgen.ReadMaintenanceUnionPageParams{BuildID: c.FirstBuild, AfterStart: c.AfterStart, PageLimit: int32(limit)})
	if err != nil {
		return c, err
	}
	b, err := q.ReadMaintenanceUnionPage(ctx, dbgen.ReadMaintenanceUnionPageParams{BuildID: c.SecondBuild, AfterStart: c.AfterStart, PageLimit: int32(limit)})
	if err != nil {
		return c, err
	}
	if len(a) != len(b) {
		c.State = "different"
	} else {
		for i := range a {
			if !a[i].StartsAt.Time.Equal(b[i].StartsAt.Time) || !a[i].EndsAt.Time.Equal(b[i].EndsAt.Time) {
				c.State = "different"
				break
			}
		}
	}
	if c.State == "pending" {
		if c.Compared > math.MaxInt64-int64(len(a)) {
			return c, ErrMaintenanceInvalid
		}
		c.Compared += int64(len(a))
		if len(a) > 0 {
			c.AfterStart = a[len(a)-1].StartsAt
		}
		if len(a) < limit {
			c.State = "equal"
		}
	}
	c.UpdatedAt = timestamp(now)
	err = q.AdvanceMaintenanceComparison(ctx, dbgen.AdvanceMaintenanceComparisonParams{ID: c.ID, AfterStart: c.AfterStart, Compared: c.Compared, State: c.State, UpdatedAt: c.UpdatedAt})
	return c, err
}

func cancelMaintenanceComparison(ctx context.Context, tx pgx.Tx, id pgtype.UUID, now time.Time) error {
	if !id.Valid || now.IsZero() {
		return ErrMaintenanceInvalid
	}
	q := dbgen.New(tx)
	c, err := q.LockMaintenanceComparison(ctx, id)
	if err != nil {
		return err
	}
	if c.State != "pending" {
		return ErrMaintenanceConflict
	}
	return q.AdvanceMaintenanceComparison(ctx, dbgen.AdvanceMaintenanceComparisonParams{ID: id, AfterStart: c.AfterStart, Compared: c.Compared, State: "cancelled", UpdatedAt: timestamp(now)})
}
