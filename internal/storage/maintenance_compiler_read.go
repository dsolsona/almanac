package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type maintenanceCoverage struct {
	BuildID, SloID       string
	Revision             int64
	ExcludedMicroseconds int64
	// This is a configuration endpoint, not evidence of an observed interval.
	// It may precede the request when a containing union started earlier.
	LatestEligibleEnd time.Time
}

// The future calculator caller must share its repeatable-read source transaction.
// Two predecessor seeks replace scanning a fragmented population for duration.
func readMaintenanceCoverage(ctx context.Context, q *dbgen.Queries, id pgtype.UUID, start, end time.Time) (maintenanceCoverage, error) {
	var empty maintenanceCoverage
	if !id.Valid || !validMaintenanceTime(start) || !validMaintenanceTime(end) || !start.Before(end) {
		return empty, ErrMaintenanceInvalid
	}
	b, err := q.ReadMaintenanceBuild(ctx, id)
	if err != nil {
		return empty, err
	}
	if b.Phase == "unavailable" {
		return empty, ErrMaintenanceCompileUnavailable
	}
	if b.Phase != "complete" {
		return empty, ErrMaintenanceCompilePending
	}
	prefix := func(at time.Time) (int64, dbgen.AlmanacMaintenanceCoverage, error) {
		row, err := q.ReadMaintenanceCoveragePredecessor(ctx, dbgen.ReadMaintenanceCoveragePredecessorParams{BuildID: id, At: timestamp(at)})
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, row, nil
		}
		if err != nil {
			return 0, row, err
		}
		value := row.CumulativeUs
		if at.Before(row.EndsAt.Time) {
			value -= row.EndsAt.Time.UnixMicro() - at.UnixMicro()
		}
		return value, row, nil
	}
	a, _, err := prefix(start)
	if err != nil {
		return empty, err
	}
	z, last, err := prefix(end)
	if err != nil {
		return empty, err
	}
	if z < a || z-a > end.UnixMicro()-start.UnixMicro() {
		return empty, ErrMaintenanceCompileUnavailable
	}
	result := maintenanceCoverage{BuildID: id.String(), SloID: b.SloID.String(), Revision: b.TargetRevision, ExcludedMicroseconds: z - a, LatestEligibleEnd: end}
	if last.StartsAt.Valid && !last.EndsAt.Time.Before(end) {
		result.LatestEligibleEnd = last.StartsAt.Time.UTC()
	}
	return result, nil
}
