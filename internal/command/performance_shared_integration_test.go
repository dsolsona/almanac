//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

type performanceSharedReport struct {
	IndependentSLOs int                            `json:"independent_slos"`
	FinalSLOs       int                            `json:"final_slos"`
	SharedSLI       string                         `json:"shared_sli"`
	Added           []api.Slo                      `json:"added_objectives"`
	Maintenance     time.Duration                  `json:"maintenance_ns"`
	Authentication  time.Duration                  `json:"management_authentication_ns"`
	Reads           []performancePreparationSample `json:"historical_reads"`
	Publication     []performanceCurrentnessSample `json:"publication"`
}

// Supplement the unchanged independent stress fleet after its measured stages.
// Distinct targets/windows must not share a calculated result just because their
// canonical SLI is shared. Only one day of new objective validity is bootstrapped.
func performanceSharedSource(t *testing.T, parent context.Context, fleet performanceFleet) (performanceSharedReport, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	f := fleet.fixture
	sli := fleet.slis[9] // Class0 has complete retained input for the exact oracle.
	report := performanceSharedReport{IndependentSLOs: len(fleet.objectives), FinalSLOs: len(fleet.objectives) + 2, SharedSLI: sli}
	report.Authentication = reauthenticatePerformanceManager(t, ctx, fleet, 0)
	end := time.Date(fleet.historyEnd.Year(), fleet.historyEnd.Month(), fleet.historyEnd.Day(), 0, 0, 0, 0, time.UTC).Add(-24 * time.Hour)
	start := end.Add(-2 * time.Hour)
	for index, shape := range []struct {
		target string
		window int
	}{{"0.99", 5400}, {"0.999", 4620}} {
		var objective api.Slo
		name := fmt.Sprintf("shared-performance-%d", index)
		performanceCreateCLI(t, f, fleet.agents[0].profile, "slos", name, map[string]any{
			"name": name, "display_name": name, "service_id": fleet.serviceID, "sli_id": sli, "target": shape.target, "rolling_window_seconds": shape.window, "evaluation_valid_from": end.Add(-24 * time.Hour).Format(time.RFC3339),
		}, &objective, "slo")
		if !validCLIUUID(objective.Id.String()) || objective.SliId.String() != sli || objective.Target != shape.target || objective.RollingWindowSeconds != int64(shape.window) {
			return report, errors.New("shared objective lost its source/target/window")
		}
		report.Added = append(report.Added, objective)
	}
	cohort := fleet
	cohort.slis = []string{sli}
	cohort.objectives = []string{report.Added[0].Id.String(), report.Added[1].Id.String()}
	var err error
	report.Maintenance, err = awaitPerformanceHistory(t, ctx, cohort)
	if err != nil {
		return report, err
	}
	var minimumSource int64
	read := func() error {
		for _, objective := range report.Added {
			query := url.Values{"at": {end.Format(time.RFC3339)}, "start": {start.Format(time.RFC3339)}, "end": {end.Format(time.RFC3339)}, "burn_window_seconds": {"3600"}}
			op := performanceOperation{Name: "shared-exact-history", Paths: []string{"/api/v1/slos/" + objective.Id.String() + "/operational?" + query.Encode()}, Budget: 500 * time.Millisecond}
			op.Check = func(_ int, body []byte) error {
				var value api.SloOperationalDetail
				if json.Unmarshal(body, &value) != nil || value.Objective.Id != objective.Id || value.Objective.Target != objective.Target || value.Objective.RollingWindowSeconds != objective.RollingWindowSeconds || value.Generation == nil || checkPerformanceGeneration(*value.Generation) != nil || !value.Generation.EvaluatedThrough.Equal(end) {
					return errors.New("shared history lost objective identity or generation")
				}
				generation, err := strconv.ParseInt(value.Generation.SourceGeneration, 10, 64)
				if err != nil || generation < minimumSource {
					return errors.New("shared history did not incorporate the published source generation")
				}
				window := time.Duration(objective.RollingWindowSeconds) * time.Second
				want, err := performanceOracleWindow(0, end.Add(-window), end)
				if err != nil {
					return err
				}
				p := value.Current.Performance
				if err := checkPerformanceOracleTargetMetrics(p.Good, p.Total, p.Attainment, p.AllowedBudget, p.RemainingBudget, p.RemainingFraction, p.Burn, want, "budget", objective.Target); err != nil {
					return err
				}
				if err := checkPerformanceOracleQuality(value.Current.Quality, want, window); err != nil {
					return err
				}
				state := api.SloEvaluationPerformanceState("met")
				if objective.Target == "0.999" {
					state = "violated"
				}
				if p.State != state || p.Provisional {
					return errors.New("shared targets reused another objective's performance")
				}
				for _, lane := range []struct {
					samples  []api.SloOperationalSample
					duration time.Duration
					metric   string
				}{
					{value.AttainmentSeries.Samples, time.Minute, "attainment"}, {value.BudgetSeries.Samples, window, "budget"}, {value.BurnSeries.Samples, time.Hour, "burn"},
				} {
					if len(lane.samples) < 2 {
						return errors.New("shared history omitted populated graph")
					}
					for _, sample := range lane.samples {
						want, err := performanceOracleWindow(0, sample.EvaluationAt.Add(-lane.duration), sample.EvaluationAt)
						if err != nil {
							return err
						}
						if err := checkPerformanceOracleQuality(sample.Quality, want, lane.duration); err != nil {
							return err
						}
						if err := checkPerformanceOracleTargetMetrics(sample.Good, sample.Total, sample.Attainment, sample.AllowedBudget, sample.RemainingBudget, sample.RemainingFraction, sample.Burn, want, lane.metric, objective.Target); err != nil {
							return err
						}
					}
				}
				return nil
			}
			samples, err := preparePerformanceOperation(ctx, fleet.readers, op)
			report.Reads = append(report.Reads, samples...)
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := read(); err != nil {
		return report, err
	}
	db := testpostgres.Connect(t, f.databaseURL)
	defer closePerformanceObserver(db)
	var original int64
	if err := db.QueryRow(ctx, `SELECT generation FROM public.almanac_sli_source_generations WHERE sli_id=$1 AND measurement_revision=1`, sli).Scan(&original); err != nil {
		return report, errors.New("read shared source baseline failed")
	}
	ticks := time.NewTicker(250 * time.Millisecond)
	defer ticks.Stop()
	var source int64
	var through, received time.Time
	completed := make(map[string]bool)
	for len(completed) < len(report.Added) {
		if source == 0 {
			var current int64
			bounded, stop := context.WithTimeout(ctx, 2*time.Second)
			err := db.QueryRow(bounded, `SELECT source.generation,c.interval_end,c.recorded_at FROM public.almanac_sli_source_generations source
JOIN LATERAL (SELECT interval_end,recorded_at FROM public.almanac_sli_coverage WHERE sli_id=source.sli_id AND measurement_revision=source.measurement_revision ORDER BY interval_start DESC LIMIT 1)c ON true
WHERE source.sli_id=$1 AND source.measurement_revision=1`, sli).Scan(&current, &through, &received)
			stop()
			if err != nil {
				return report, errors.New("observe new shared-source receipt failed")
			}
			if current > original {
				source = current
			}
		}
		if source > 0 {
			for index, objective := range report.Added {
				id := objective.Id.String()
				if completed[id] {
					continue
				}
				statusCode, _, body, err := performanceLoadRequest(ctx, fleet.readers[index], "/api/v1/slos/"+id+"/status")
				var status api.SloCurrentStatus
				if err != nil || statusCode != http.StatusOK || json.Unmarshal(body, &status) != nil || status.Slo.Id != objective.Id || status.Slo.SliId.String() != sli || status.Slo.Target != objective.Target || checkPerformanceGeneration(status.Generation) != nil || !status.Current.EvaluationAt.Equal(status.Generation.EvaluatedThrough) {
					return report, errors.New("shared-source status became unavailable or inconsistent")
				}
				generation, err := strconv.ParseInt(status.Generation.SourceGeneration, 10, 64)
				if err != nil {
					return report, errors.New("shared-source status has invalid generation")
				}
				if generation >= source && !status.Generation.EvaluatedThrough.Before(through) {
					visible := time.Now()
					lag := visible.Sub(received)
					if lag < 0 || lag > 10*time.Second {
						return report, errors.New("shared-source publication exceeded ten seconds")
					}
					report.Publication = append(report.Publication, performanceCurrentnessSample{Measurement: "supplemental-shared-source", Kind: "receipt", SloID: id, Through: through, Source: source, EarliestAt: received, VisibleAt: visible, UpperBound: lag})
					completed[id] = true
				}
			}
			if time.Since(received) > 10*time.Second && len(completed) < len(report.Added) {
				return report, errors.New("shared-source publication remained unavailable beyond ten seconds")
			}
		}
		if len(completed) == len(report.Added) {
			break
		}
		select {
		case <-ctx.Done():
			return report, errors.New("shared-source live receipt/publication observation expired")
		case <-ticks.C:
		}
	}
	minimumSource = source
	err = read()
	return report, err
}
