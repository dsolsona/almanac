package storage

import (
	"context"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func liveSloEvaluationEnd(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, requested time.Time, settling int32) (time.Time, error) {
	end := sli.LatestDueEnd(requested, row.SliID.String(), int(row.CadenceSeconds), int(settling))
	// Already received intervals are usable early; the clock still exposes outages.
	ready, err := q.ReadSloReadyTailEnd(ctx, dbgen.ReadSloReadyTailEndParams{
		SliID: row.SliID, MeasurementRevision: row.MeasurementRevision, DueEnd: timestamp(end),
		CadenceSeconds: row.CadenceSeconds,
		RequestedEnd:   timestamp(floorSliBoundary(requested, time.Duration(row.CadenceSeconds)*time.Second)),
	})
	if err != nil {
		return time.Time{}, readTransactionError(ctx, "storage: current SLO measurement cutoff failed", err)
	}
	return ready.Time.UTC(), nil
}

func currentCacheDeadline(row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, at time.Time, qualities ...api.SloOperationalQuality) time.Time {
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	until := floorSliBoundary(at, cadence).Add(cadence)
	grace := cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))
	for _, quality := range qualities {
		if quality.Freshness != "fresh" || quality.LastObservationAt == nil {
			continue
		}
		// Freshness is inclusive; the first stale instant is one microsecond later.
		deadline := quality.LastObservationAt.Add(grace + time.Microsecond)
		if deadline.After(at) && deadline.Before(until) {
			until = deadline
		}
	}
	return until
}
