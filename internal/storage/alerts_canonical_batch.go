package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

// The caller owns one repeatable-read snapshot for both reads. The ordinary
// canonical path stays unchanged. Only explicit source exhaustion needs the
// bounded batch again to retain usable windows beside terminal unavailability.
func readAlertCanonicalWindows(ctx context.Context, q *dbgen.Queries, sliID pgtype.UUID, revision int64, inputs []slo.WindowInput, identity alert.SnapshotIdentity, scheduled time.Time) ([]alert.WindowEvidence, error) {
	values, err := evaluateDerivedWindows(ctx, q, sliID, revision, inputs)
	failed := make([]bool, len(inputs))
	if errors.Is(err, ErrDerivedSourceFailed) {
		params, err := derivedWindowBatchParams(sliID, revision, inputs)
		if err != nil {
			return nil, err
		}
		counts, err := q.ReadRetainedWindowBatch(ctx, params)
		if err != nil {
			return nil, operationError(ctx, "storage: alert canonical failure evidence read failed", err)
		}
		if len(counts) != len(inputs) {
			return nil, slo.ErrOperationalFacts
		}
		values = make([]slo.WindowResult, len(inputs))
		for index, count := range counts {
			if count.Ordinal != int32(index+1) {
				return nil, slo.ErrOperationalFacts
			}
			if count.Exhausted {
				failed[index] = true
				continue
			}
			// Preserve canonical fact/error ordering. Any unfinished window
			// still returns pending and discards the entire policy's evidence.
			values[index], err = evaluateDerivedCounts(ctx, inputs[index], dbgen.ReadDerivedWindowTotalsRow{
				Good: count.Good, Total: count.Total, Observed: count.Observed, KnownEmpty: count.KnownEmpty,
				ZeroTraffic: count.ZeroTraffic, FirstObservation: count.FirstObservation,
				LastObservation: count.LastObservation, LatestReceipt: count.LatestReceipt,
				Prepared: count.Prepared, Exhausted: count.Exhausted,
			})
			if err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	result := make([]alert.WindowEvidence, len(inputs))
	for index, input := range inputs {
		if failed[index] {
			// Failed aggregates cannot supply trustworthy counts, even when a
			// stale aggregate row exists. Keep full bounds and explicit cause.
			result[index] = alert.WindowEvidence{Snapshot: identity, Start: identity.Cutoff.Add(-input.Duration), End: identity.Cutoff, Quality: alert.CalculationFailed, Reason: "calculation_failed"}
			continue
		}
		result[index], err = canonicalAlertWindow(identity, scheduled, input.FreshnessGrace, input.Duration, values[index])
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
