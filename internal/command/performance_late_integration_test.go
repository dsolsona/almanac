//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math/big"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

type performanceLateReport struct {
	SloID                        string    `json:"slo_id"`
	IntervalStart                time.Time `json:"interval_start"`
	Import, Maintenance, Restart time.Duration
	Before, After, Recovered     api.DerivedGeneration
	Reads                        []performancePreparationSample `json:"reads"`
	PinnedValuesPreserved        bool                           `json:"pinned_values_preserved"`
}

// Import one deliberately absent fixture interval, leaving production triggers
// and workers responsible for correction. This is not agent-ingestion evidence.
func performanceLateCorrection(t *testing.T, ctx context.Context, fleet performanceFleet) (performanceLateReport, error) {
	t.Helper()
	end := time.Date(fleet.historyEnd.Year(), fleet.historyEnd.Month(), fleet.historyEnd.Day(), 0, 0, 0, 0, time.UTC).Add(-24 * time.Hour)
	late := end.Add(-2*time.Hour + 17*time.Minute)
	id, sli := fleet.objectives[0], fleet.slis[0] // Fixture class 1 omits minute 17.
	report := performanceLateReport{SloID: id, IntervalStart: late}
	query := url.Values{"at": {end.Format(time.RFC3339)}, "start": {end.Add(-6 * time.Hour).Format(time.RFC3339)}, "end": {end.Format(time.RFC3339)}, "burn_window_seconds": {"3600"}}
	read := func(corrected bool) (api.SloOperationalDetail, error) {
		counts := func(at time.Time, duration time.Duration) (performanceOracleCounts, error) {
			want, err := performanceOracleWindow(1, at.Add(-duration), at)
			if err == nil && corrected && at.After(late) && !late.Before(at.Add(-duration)) {
				want.Good.Add(want.Good, big.NewRat(99999, 1000))
				want.Total.Add(want.Total, big.NewRat(100125, 1000))
				want.Observed++
				want.Missing--
			}
			return want, err
		}
		var detail api.SloOperationalDetail
		op := performanceOperation{Name: "late-correction", Paths: []string{"/api/v1/slos/" + id + "/operational?" + query.Encode()}, Budget: 500 * time.Millisecond}
		op.Check = func(_ int, body []byte) error {
			if json.Unmarshal(body, &detail) != nil || detail.Generation == nil || detail.Objective.Id.String() != id || !detail.Generation.EvaluatedThrough.Equal(end) || checkPerformanceGeneration(*detail.Generation) != nil {
				return errors.New("late correction lost historical identity/generation")
			}
			for _, item := range []struct {
				value    api.SloEvaluationPerformance
				quality  api.SloOperationalQuality
				duration time.Duration
			}{
				{detail.Current.Performance, detail.Current.Quality, 28 * 24 * time.Hour},
				{detail.Burn1h.Performance, detail.Burn1h.Quality, time.Hour},
				{detail.Burn6h.Performance, detail.Burn6h.Quality, 6 * time.Hour},
			} {
				want, err := counts(end, item.duration)
				if err != nil {
					return err
				}
				p := item.value
				if err := checkPerformanceOracleMetrics(p.Good, p.Total, p.Attainment, p.AllowedBudget, p.RemainingBudget, p.RemainingFraction, p.Burn, want, "budget"); err != nil {
					return err
				}
				if err := checkPerformanceOracleQuality(item.quality, want, item.duration); err != nil {
					return err
				}
			}
			for _, lane := range []struct {
				samples  []api.SloOperationalSample
				duration time.Duration
				metric   string
			}{{detail.AttainmentSeries.Samples, time.Minute, "attainment"}, {detail.BudgetSeries.Samples, 28 * 24 * time.Hour, "budget"}, {detail.BurnSeries.Samples, time.Hour, "burn"}} {
				found := false
				for _, sample := range lane.samples {
					want, err := counts(sample.EvaluationAt, lane.duration)
					if err != nil {
						return err
					}
					if err := checkPerformanceOracleSampleCounts(sample, want, lane.duration, lane.metric); err != nil {
						return err
					}
					found = found || sample.EvaluationAt.Equal(late.Add(time.Minute))
				}
				if !found {
					return errors.New("late correction omitted the affected graph point")
				}
			}
			return nil
		}
		samples, err := preparePerformanceOperation(ctx, fleet.readers, op)
		report.Reads = append(report.Reads, samples...)
		return detail, err
	}
	before, err := read(false)
	if err != nil {
		return report, err
	}
	report.Before = *before.Generation
	var pinned api.SloHistoryValuePage
	pin := performanceOperation{Name: "late-pinned-values", Paths: []string{"/api/v1/slos/" + id + "/history/" + before.Generation.GenerationId.String() + "/values?lane=budget&offset=0&limit=100"}, Budget: 500 * time.Millisecond}
	pin.Check = func(_ int, body []byte) error {
		var page api.SloHistoryValuePage
		if json.Unmarshal(body, &page) != nil || page.Generation.GenerationId != before.Generation.GenerationId || len(page.Samples) == 0 {
			return errors.New("late correction missing pinned exact values")
		}
		if pinned.TotalValues == 0 {
			pinned = page
		} else if !reflect.DeepEqual(page, pinned) {
			return errors.New("late correction changed immutable exact values")
		}
		return nil
	}
	if _, err := preparePerformanceOperation(ctx, fleet.readers, pin); err != nil {
		return report, err
	}
	imported := time.Now()
	importPerformanceGap(t, ctx, fleet, sli, late)
	report.Import = time.Since(imported)
	report.Maintenance, err = awaitPerformanceHistory(t, ctx, fleet)
	if err != nil {
		return report, err
	}
	after, err := read(true)
	if err != nil {
		return report, err
	}
	report.After = *after.Generation
	if report.After.GenerationId == report.Before.GenerationId || report.After.SourceGeneration == report.Before.SourceGeneration {
		return report, errors.New("late observation did not publish a new source generation")
	}
	if _, err := preparePerformanceOperation(ctx, fleet.readers, pin); err != nil {
		return report, err
	}
	report.Restart = fleet.fixture.backend.stop(t) + fleet.fixture.backend.start(t)
	recovered, err := read(true)
	if err != nil {
		return report, err
	}
	report.Recovered = *recovered.Generation
	if _, err := preparePerformanceOperation(ctx, fleet.readers, pin); err != nil {
		return report, err
	}
	report.PinnedValuesPreserved = true
	return report, nil
}

func importPerformanceGap(t *testing.T, ctx context.Context, fleet performanceFleet, sli string, at time.Time) {
	t.Helper()
	deployment := fleet.fixture.backend.deployment
	if deployment == nil || deployment.databaseURL != fleet.fixture.databaseURL {
		t.Fatal("late import requires owned capacity database")
	}
	deployment.evidence(t)
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db := testpostgres.Connect(t, fleet.fixture.databaseURL)
	defer closePerformanceObserver(db)
	tx, err := db.Begin(bounded)
	if err != nil {
		t.Fatal("begin late fixture import failed")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	var before int64
	if err := tx.QueryRow(bounded, `SELECT generation FROM public.almanac_sli_source_generations WHERE sli_id=$1 AND measurement_revision=1 FOR UPDATE`, sli).Scan(&before); err != nil {
		t.Fatal("lock late fixture source failed")
	}
	coverage := `INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,
 execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT $2,sli_id,measurement_revision,$2::timestamptz+interval '1 minute','observed',decode(repeat('cd',32),'hex'),'{}',
 gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,$2,CURRENT_TIMESTAMP
FROM public.almanac_sli_coverage WHERE sli_id=$1 AND measurement_revision=1 AND interval_start=$2::timestamptz-interval '1 minute'
ON CONFLICT DO NOTHING`
	values := `INSERT INTO public.almanac_sli_observation_values(interval_start,sli_id,measurement_revision,good_count,total_count)
VALUES($2,$1,1,99.999,100.125) ON CONFLICT DO NOTHING`
	var published int64
	for attempt := range 2 {
		for _, statement := range []string{coverage, values} {
			tag, err := tx.Exec(bounded, statement, sli, at)
			if err != nil || tag.RowsAffected() != int64(1-attempt) {
				t.Fatal("late fixture import lost insert/replay idempotency")
			}
		}
		var generation int64
		if err := tx.QueryRow(bounded, `SELECT generation FROM public.almanac_sli_source_generations WHERE sli_id=$1 AND measurement_revision=1`, sli).Scan(&generation); err != nil {
			t.Fatal("read late fixture source generation failed")
		}
		if attempt == 0 {
			if generation <= before {
				t.Fatal("late fixture import did not dirty source")
			}
			published = generation
		} else if generation != published {
			t.Fatal("duplicate late import advanced source generation")
		}
	}
	if err := tx.Commit(bounded); err != nil {
		t.Fatal("commit late fixture import failed")
	}
}
