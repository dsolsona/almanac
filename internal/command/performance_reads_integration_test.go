//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

type performancePhaseReport struct {
	Name        string                                    `json:"name"`
	Preparation map[string][]performancePreparationSample `json:"preparation"`
	Reads       []performanceReadReport                   `json:"reads"`
	Failures    []string                                  `json:"failures"`
}

// This runs ordinary authenticated API requests against the candidate executable.
// It is called separately for persisted-cache startup and blank-cache rebuilding;
// callers must never combine the two phases' setup or latency distributions.
func runPerformanceReadyPhase(ctx context.Context, fleet performanceFleet, name string, end time.Time) (performancePhaseReport, error) {
	report := performancePhaseReport{Name: name, Preparation: make(map[string][]performancePreparationSample)}
	run := func(operation performanceOperation) ([]performancePreparationSample, bool) {
		prepared, err := preparePerformanceOperation(ctx, fleet.readers, operation)
		report.Preparation[operation.Name] = prepared
		if err != nil {
			report.Failures = append(report.Failures, err.Error())
			return prepared, false
		}
		reads, err := samplePerformanceReads(ctx, fleet.readers, operation)
		report.Reads = append(report.Reads, reads)
		if err != nil {
			report.Failures = append(report.Failures, err.Error())
		}
		return prepared, true
	}
	run(performanceOperation{Name: "sli-list", Paths: []string{"/api/v1/slis?limit=50"}, Budget: 200 * time.Millisecond,
		Check: func(_ int, body []byte) error {
			var page api.SliPage
			if json.Unmarshal(body, &page) != nil || len(page.Items) != 50 || page.NextCursor == "" {
				return errors.New("SLI list did not return a bounded first page")
			}
			return nil
		}})
	run(performanceOperation{Name: "slo-list", Paths: []string{"/api/v1/slos/overview?limit=50"}, Budget: 200 * time.Millisecond,
		Check: func(_ int, body []byte) error {
			var page api.SloOperationalOverviewPage
			if json.Unmarshal(body, &page) != nil || len(page.Items) != 50 || page.NextCursor == "" ||
				page.GroupCounts.Healthy+page.GroupCounts.NeedsAttention+page.GroupCounts.Watch != 100 {
				return errors.New("SLO list did not preserve global counts and pagination")
			}
			for _, item := range page.Items {
				if item.Generation == nil || checkPerformanceGeneration(*item.Generation) != nil {
					return errors.New("SLO row has no valid generation provenance")
				}
			}
			return nil
		}})
	status := performanceOperation{Name: "slo-status", Budget: 200 * time.Millisecond}
	for _, id := range fleet.objectives {
		status.Paths = append(status.Paths, "/api/v1/slos/"+id+"/status")
	}
	status.Check = func(target int, body []byte) error {
		var value api.SloCurrentStatus
		if json.Unmarshal(body, &value) != nil || value.Slo.Id.String() != fleet.objectives[target] || checkPerformanceGeneration(value.Generation) != nil ||
			!value.Generation.EvaluatedThrough.Equal(value.Current.EvaluationAt) {
			return errors.New("current status identity or generation mismatch")
		}
		return nil
	}
	run(status)
	for _, days := range []int{7, 28, 90, 365} {
		operation := performanceOperation{Name: fmt.Sprintf("slo-history-%dd", days), Budget: 500 * time.Millisecond}
		start := end.Add(-time.Duration(days) * 24 * time.Hour)
		query := url.Values{"at": {end.UTC().Format(time.RFC3339Nano)}, "start": {start.UTC().Format(time.RFC3339Nano)}, "end": {end.UTC().Format(time.RFC3339Nano)}, "burn_window_seconds": {"3600"}}
		for _, id := range fleet.objectives {
			operation.Paths = append(operation.Paths, "/api/v1/slos/"+id+"/operational?"+query.Encode())
		}
		operation.Check = func(target int, body []byte) error {
			var value api.SloOperationalDetail
			if json.Unmarshal(body, &value) != nil || value.Objective.Id.String() != fleet.objectives[target] || value.Generation == nil ||
				checkPerformanceGeneration(*value.Generation) != nil || !value.Generation.EvaluatedThrough.Equal(end) ||
				len(value.AttainmentSeries.Samples) > 1500 || len(value.BudgetSeries.Samples) > 1500 || len(value.BurnSeries.Samples) > 1500 ||
				len(value.ComplianceSeries.Days) > days {
				return errors.New("history identity, provenance or point bound mismatch")
			}
			return nil
		}
		prepared, ready := run(operation)
		if !ready {
			continue
		}
		generations := make([]string, len(prepared))
		values := performanceOperation{Name: fmt.Sprintf("slo-values-%dd", days), Budget: 500 * time.Millisecond}
		for target, sample := range prepared {
			var detail api.SloOperationalDetail
			// The preparation helper has already validated the document. Retain
			// its exact generation for value requests even if currentness moves.
			if err := json.Unmarshal(sample.Body, &detail); err != nil || detail.Generation == nil {
				report.Failures = append(report.Failures, "prepared history document could not be pinned")
				continue
			}
			generations[target] = detail.Generation.GenerationId.String()
			values.Paths = append(values.Paths, "/api/v1/slos/"+fleet.objectives[target]+"/history/"+generations[target]+"/values?lane=budget&offset=0&limit=100")
		}
		if len(values.Paths) != len(fleet.objectives) {
			continue
		}
		values.Check = func(target int, body []byte) error {
			var page api.SloHistoryValuePage
			if json.Unmarshal(body, &page) != nil || checkPerformanceGeneration(page.Generation) != nil ||
				page.Generation.GenerationId.String() != generations[target] || page.Lane != api.SloHistoryValuePageLane("budget") ||
				page.Offset != 0 || len(page.Samples) != min(100, page.TotalValues) || len(page.Days) != 0 {
				return errors.New("exact-value page changed the pinned history generation or bounds")
			}
			return nil
		}
		run(values)
	}
	if len(report.Failures) > 0 {
		return report, fmt.Errorf("performance phase %s has %d failures", name, len(report.Failures))
	}
	return report, nil
}

func checkPerformanceGeneration(value api.DerivedGeneration) error {
	if !validCLIUUID(value.GenerationId.String()) || value.CalculationVersion == "" || value.EvaluatedThrough.IsZero() || value.PublishedAt.IsZero() {
		return errors.New("missing generation provenance")
	}
	for _, revision := range []string{value.DefinitionRevision, value.MeasurementRevision, value.SourceGeneration} {
		if parsed, err := strconv.ParseInt(revision, 10, 64); err != nil || parsed <= 0 {
			return errors.New("invalid generation revision")
		}
	}
	return nil
}
