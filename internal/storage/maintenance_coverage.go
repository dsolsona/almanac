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

// readMaintenanceWindowCoverage is unwired. The caller owns the RR source-fact
// transaction and final source/definition publication fences. Historical mask R
// need not be the current authority. No numeric population is read or invented.
// Three queries at most: identity, distinct ends, remaining distinct anchors.
func readMaintenanceWindowCoverage(ctx context.Context, tx pgx.Tx, sloID, buildID pgtype.UUID, revision int64, inputs []slo.WindowInput, totals []slo.WindowTotals) ([]slo.WindowCoverage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tx == nil || !sloID.Valid || !buildID.Valid || revision < 0 || len(inputs) != len(totals) || len(inputs) > derivedWindowBatchLimit {
		return nil, ErrMaintenanceInvalid
	}
	result := make([]slo.WindowCoverage, len(inputs))
	if len(inputs) == 0 {
		return result, nil
	}
	// Validate anchor geometry before SQL; latest=At is a provisional endpoint.
	for i, input := range inputs {
		if _, err := slo.RequiredCoverageAnchors(input, totals[i], input.At); err != nil {
			return nil, err
		}
	}
	q := dbgen.New(tx)
	identity, err := q.ReadMaintenanceCoverageIdentity(ctx, buildID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMaintenanceNotFound
	}
	if err != nil {
		return nil, err
	}
	if identity.Isolation != "repeatable read" || identity.SloID != sloID || identity.TargetRevision != revision {
		return nil, ErrMaintenanceSnapshotChanged
	}
	if identity.Phase == "unavailable" || identity.Phase == "cancelled" {
		return nil, ErrMaintenanceCompileUnavailable
	}
	if identity.Phase != "complete" {
		return nil, ErrMaintenanceCompilePending
	}
	cadence := time.Duration(identity.CadenceSeconds) * time.Second
	if cadence <= 0 {
		return nil, ErrMaintenanceInvalid
	}
	ends := make([]pgtype.Timestamptz, 0, len(inputs))
	seen := make(map[int64]bool, len(inputs))
	for _, input := range inputs {
		if input.Cadence != cadence {
			return nil, ErrMaintenanceInvalid
		}
		key := input.At.UnixMicro()
		if !seen[key] {
			ends = append(ends, timestamp(input.At))
			seen[key] = true
		}
	}
	rows, err := q.ReadMaintenanceCoverageRanks(ctx, dbgen.ReadMaintenanceCoverageRanksParams{BuildID: buildID, Anchors: ends})
	if err != nil {
		return nil, err
	}
	ranks := make(map[int64]uint64, 9*len(inputs))
	latest := make(map[int64]time.Time, len(ends))
	consume := func(rows []dbgen.ReadMaintenanceCoverageRanksRow) error {
		for _, row := range rows {
			at := row.At.Time.UTC()
			value := row.CumulativeUs
			end := at
			if row.StartsAt.Valid {
				a, z := row.StartsAt.Time.UTC(), row.EndsAt.Time.UTC()
				if !row.EndsAt.Valid || !validMaintenanceTime(a) || !validMaintenanceTime(z) || !a.Before(z) || !a.Before(at) || a.UnixMicro()%cadence.Microseconds() != 0 || z.UnixMicro()%cadence.Microseconds() != 0 || value < z.UnixMicro()-a.UnixMicro() {
					return ErrMaintenanceCompileUnavailable
				}
				if !z.Before(at) {
					end = a
					value -= z.UnixMicro() - at.UnixMicro()
				}
			} else if row.EndsAt.Valid || value != 0 {
				return ErrMaintenanceCompileUnavailable
			}
			if value < 0 || value%cadence.Microseconds() != 0 {
				return ErrMaintenanceCompileUnavailable
			}
			ranks[at.UnixMicro()] = uint64(value / cadence.Microseconds())
			latest[at.UnixMicro()] = end
		}
		return nil
	}
	if err := consume(rows); err != nil {
		return nil, err
	}
	anchors := make([]pgtype.Timestamptz, 0, 9*len(inputs))
	seen = make(map[int64]bool, 9*len(inputs))
	for i, input := range inputs {
		end, ok := latest[input.At.UnixMicro()]
		if !ok {
			return nil, ErrMaintenanceCompileUnavailable
		}
		required, err := slo.RequiredCoverageAnchors(input, totals[i], end)
		if err != nil {
			return nil, err
		}
		result[i].LatestEligibleEnd = end
		for _, at := range required {
			key := at.UnixMicro()
			result[i].Points = append(result[i].Points, slo.ExclusionCoveragePoint{At: at})
			if _, ok := ranks[key]; !ok && !seen[key] {
				anchors = append(anchors, timestamp(at))
				seen[key] = true
			}
		}
	}
	if len(anchors) > 0 {
		rows, err = q.ReadMaintenanceCoverageRanks(ctx, dbgen.ReadMaintenanceCoverageRanksParams{BuildID: buildID, Anchors: anchors})
		if err != nil {
			return nil, err
		}
		if err := consume(rows); err != nil {
			return nil, err
		}
	}
	for i := range result {
		for j := range result[i].Points {
			point := &result[i].Points[j]
			rank, ok := ranks[point.At.UnixMicro()]
			if !ok {
				return nil, ErrMaintenanceCompileUnavailable
			}
			point.ExcludedBefore = rank
		}
	}
	return result, nil
}
