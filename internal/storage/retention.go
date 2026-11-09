package storage

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
)

type retentionPartition struct {
	RelationID    int64     `json:"relation_id"`
	Parent        string    `json:"parent"`
	Name          string    `json:"name"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	Bytes         int64     `json:"bytes"`
	EstimatedRows *int64    `json:"estimated_rows"`
}

type retentionImpact struct {
	Cutoff                time.Time            `json:"cutoff"`
	Partitions            []retentionPartition `json:"partitions"`
	UnsupportedPartitions []string             `json:"unsupported_partitions"`
	Bytes                 int64                `json:"bytes"`
	CalculatedRowsAtLeast map[string]int64     `json:"calculated_rows_at_least"`
	PreviewRowLimit       int                  `json:"preview_row_limit"`
}

// Preview reads catalog bounds and physical bytes without touching observations.
// Row estimates remain explicitly optional: unanalyzed partitions have no count.
func (r *Runtime) previewRetention(ctx context.Context, now time.Time) (impact retentionImpact, resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "retention.preview")
	defer func() { finishDerivedTrace(ctx, span, resultErr, "preview") }()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return impact, err
	}
	defer conn.Release()
	rows, err := dbgen.New(conn).ListCanonicalRetentionPartitions(bounded)
	if err != nil {
		return impact, err
	}
	return canonicalRetentionImpact(rows, now)
}

func canonicalRetentionImpact(rows []dbgen.ListCanonicalRetentionPartitionsRow, now time.Time) (retentionImpact, error) {
	impact := retentionImpact{Cutoff: now.UTC().Truncate(time.Microsecond).Add(-sli.ObservationRetention),
		Partitions: []retentionPartition{}, UnsupportedPartitions: []string{}}
	if now.IsZero() || len(rows) >= 1000 {
		return impact, ErrOperationalResource
	}
	for _, row := range rows {
		if !row.RangeStart.Valid || !row.RangeEnd.Valid || row.RelationID <= 0 || row.Bytes < 0 {
			impact.UnsupportedPartitions = append(impact.UnsupportedPartitions, row.PartitionName)
			continue
		}
		start, end := row.RangeStart.Time.UTC(), row.RangeEnd.Time.UTC()
		month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
		if row.ParentName != "almanac_sli_observations" ||
			!start.Equal(month) || !end.Equal(start.AddDate(0, 1, 0)) || row.PartitionName != row.ParentName+"_"+start.Format("200601") {
			impact.UnsupportedPartitions = append(impact.UnsupportedPartitions, row.PartitionName)
			continue
		}
		if end.After(impact.Cutoff) {
			continue
		}
		if row.Bytes > math.MaxInt64-impact.Bytes {
			return impact, ErrOperationalResource
		}
		part := retentionPartition{RelationID: row.RelationID, Parent: row.ParentName, Name: row.PartitionName, Start: start, End: end, Bytes: row.Bytes}
		if row.EstimatedRows >= 0 {
			part.EstimatedRows = new(row.EstimatedRows)
		}
		impact.Partitions = append(impact.Partitions, part)
		impact.Bytes += row.Bytes
	}
	slices.SortFunc(impact.Partitions, func(a, b retentionPartition) int {
		return a.Start.Compare(b.Start)
	})
	return impact, nil
}
