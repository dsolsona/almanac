package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type maintenanceEvidence struct {
	ID         string
	Revision   int64
	Reason     string
	Start, End time.Time
}
type sloExclusionSnapshot struct {
	SloID      string
	Revision   int64
	Start, End time.Time
	Intervals  []slo.ExclusionInterval
	Evidence   []maintenanceEvidence
}

// The caller must use the same repeatable-read transaction as source facts and
// definition reads. This internal foundation is not used by production calculators.
func readSloExclusionSnapshot(ctx context.Context, q *dbgen.Queries, id pgtype.UUID, start, end time.Time) (sloExclusionSnapshot, error) {
	revision, err := q.ReadMaintenanceState(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sloExclusionSnapshot{}, ErrMaintenanceNotFound
	}
	if err != nil {
		return sloExclusionSnapshot{}, err
	}
	return readSloExclusionSnapshotAt(ctx, q, id, revision, start, end)
}

func readSloExclusionSnapshotAt(ctx context.Context, q *dbgen.Queries, id pgtype.UUID, revision int64, start, end time.Time) (sloExclusionSnapshot, error) {
	result := sloExclusionSnapshot{SloID: id.String(), Revision: revision, Start: start, End: end}
	if revision < 0 || !validMaintenanceTime(start) || !validMaintenanceTime(end) || !start.Before(end) {
		return result, ErrMaintenanceInvalid
	}
	current, err := q.ReadMaintenanceState(ctx, id)
	if err != nil {
		return result, err
	}
	if revision > current {
		return result, ErrMaintenanceSnapshotChanged
	}
	rows, err := q.ReadMaintenanceSnapshot(ctx, dbgen.ReadMaintenanceSnapshotParams{SloID: id, Revision: revision, WindowStart: timestamp(start), WindowEnd: timestamp(end)})
	if err != nil {
		return result, err
	}
	for _, row := range rows {
		a, b := row.StartsAt.Time.UTC(), row.EndsAt.Time.UTC()
		result.Evidence = append(result.Evidence, maintenanceEvidence{ID: row.ExclusionID.String(), Revision: row.Revision, Reason: row.Reason, Start: a, End: b})
		// SQL orders by start/end. Adjacent and overlapping declarations form one mask.
		n := len(result.Intervals)
		if n > 0 && !a.After(result.Intervals[n-1].End) {
			if b.After(result.Intervals[n-1].End) {
				result.Intervals[n-1].End = b
			}
		} else {
			result.Intervals = append(result.Intervals, slo.ExclusionInterval{Start: a, End: b})
		}
	}
	return result, nil
}

// Keep the FOR SHARE lock until the evidence/publication transaction commits.
// A stale repeatable-read transaction receives either a mismatch or PostgreSQL's
// serialization failure; neither may be converted into a successful publication.
func fenceSloExclusionSnapshot(ctx context.Context, q *dbgen.Queries, id pgtype.UUID, expectedRevision int64) error {
	revision, err := q.FenceMaintenanceState(ctx, id)
	if err != nil {
		return err
	}
	if revision != expectedRevision {
		return ErrMaintenanceSnapshotChanged
	}
	return nil
}
