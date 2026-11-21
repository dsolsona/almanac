package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

const sliChartMaxBuckets = 240

type SliMeasurementChart struct {
	BucketSeconds int                        `json:"bucket_seconds"`
	Buckets       []api.SliMeasurementBucket `json:"buckets"`
}

func ReadSliMeasurementChart(ctx context.Context, databaseURL string, runtime *Runtime, proof DatasourceProof, sliID, period string, now func() time.Time) (SliMeasurementChart, error) {
	var span time.Duration
	switch period {
	case "3h":
		span = 3 * time.Hour
	case "6h":
		span = 6 * time.Hour
	case "24h":
		span = 24 * time.Hour
	case "7d":
		span = 7 * 24 * time.Hour
	case "30d":
		span = 30 * 24 * time.Hour
	default:
		return SliMeasurementChart{}, ErrSliInvalid
	}
	id, valid := canonicalUUID(sliID)
	if !valid || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return SliMeasurementChart{}, ErrSliInvalid
	}
	conn, queries, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, runtime, proof.SessionVerifier, now, pgx.TxOptions{}, "administrator", "editor", "viewer")
	if err != nil {
		return SliMeasurementChart{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)

	definition, err := queries.GetSli(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliMeasurementChart{}, ErrSliNotFound
	}
	if err != nil {
		return SliMeasurementChart{}, operationError(ctx, "storage: SLI chart definition read failed", err)
	}
	current := started
	cadence := time.Duration(definition.PollingIntervalSeconds) * time.Second
	end := floorSliBoundary(current.Add(-time.Duration(definition.SettlingDelaySeconds)*time.Second), cadence)
	start := maxTime(end.Add(-(span/cadence)*cadence), ceilSliBoundary(current.Add(-sli.ObservationRetention), cadence), ceilSliBoundary(definition.EffectiveFrom.Time, cadence))
	width := max(1, (span/cadence+sliChartMaxBuckets-1)/sliChartMaxBuckets) * cadence
	result := SliMeasurementChart{BucketSeconds: int(width / time.Second), Buckets: []api.SliMeasurementBucket{}}
	if definition.ActivatedAt.Valid && end.After(start) {
		rows, err := queries.BucketSliMeasurements(ctx, dbgen.BucketSliMeasurementsParams{
			SliID: id, MeasurementRevision: definition.MeasurementRevision,
			RangeStart: timestamp(start), RangeEnd: timestamp(end), BucketSeconds: width.Seconds(),
		})
		if err != nil {
			return SliMeasurementChart{}, operationError(ctx, "storage: SLI chart history read failed", err)
		}
		first := end.Add(-((end.Sub(start) + width - 1) / width) * width)
		for at := first; at.Before(end); at = at.Add(width) {
			bucket := api.SliMeasurementBucket{Start: maxTime(start, at), End: at.Add(width)}
			bucket.Missing = int64(bucket.End.Sub(bucket.Start) / cadence)
			if len(rows) > 0 && rows[0].BucketStart.Time.Equal(at) {
				row := rows[0]
				rows = rows[1:]
				bucket.Observed, bucket.ZeroTraffic, bucket.KnownEmpty = row.Observed, row.ZeroTraffic, row.KnownEmpty
				bucket.Failed, bucket.Incomplete = row.Failed, row.Incomplete
				bucket.Missing -= row.Observed + row.ZeroTraffic + row.KnownEmpty + row.Failed + row.Incomplete
				if row.Good != "" {
					bucket.Good, bucket.Total = new(row.Good), new(row.Total)
				}
			}
			result.Buckets = append(result.Buckets, bucket)
		}
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return SliMeasurementChart{}, err
	}
	return result, nil
}
