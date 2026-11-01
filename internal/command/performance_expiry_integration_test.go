//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/url"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func performanceRetentionExpiry(ctx context.Context, fleet performanceFleet) (performancePhaseReport, error) {
	report := performancePhaseReport{Name: "retention-expiry", Preparation: make(map[string][]performancePreparationSample)}
	cutoff := time.Now().UTC().Truncate(time.Minute).Add(-400 * 24 * time.Hour)
	start, end := cutoff.Add(-48*time.Hour), cutoff.Add(24*time.Hour)
	query := url.Values{"at": {end.Format(time.RFC3339Nano)}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}, "burn_window_seconds": {"3600"}}
	operation := performanceOperation{Name: "retention-expiry", Budget: 500 * time.Millisecond}
	for _, id := range fleet.objectives {
		operation.Paths = append(operation.Paths, "/api/v1/slos/"+id+"/operational?"+query.Encode())
	}
	operation.Check = func(target int, body []byte) error {
		var detail api.SloOperationalDetail
		if json.Unmarshal(body, &detail) != nil || detail.Objective.Id.String() != fleet.objectives[target] || detail.Generation == nil ||
			checkPerformanceGeneration(*detail.Generation) != nil || detail.Generation.RetentionStart == nil ||
			!detail.Generation.RetentionStart.After(start) || detail.Current.Quality.ExpiredIntervals <= 0 ||
			len(detail.AttainmentSeries.Samples) > 1500 || len(detail.BudgetSeries.Samples) > 1500 || len(detail.BurnSeries.Samples) > 1500 {
			return errors.New("400-day boundary did not disclose expired input coverage")
		}
		return nil
	}
	prepared, err := preparePerformanceOperation(ctx, fleet.readers, operation)
	report.Preparation[operation.Name] = prepared
	if err != nil {
		report.Failures = append(report.Failures, err.Error())
		return report, err
	}
	reads, err := samplePerformanceReads(ctx, fleet.readers, operation)
	report.Reads = append(report.Reads, reads)
	if err != nil {
		report.Failures = append(report.Failures, err.Error())
	}
	return report, err
}
