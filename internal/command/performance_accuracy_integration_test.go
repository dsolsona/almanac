//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

type performanceAccuracyReport struct {
	Start        time.Time                 `json:"start"`
	End          time.Time                 `json:"end"`
	Classes      int                       `json:"classes"`
	Histories    int                       `json:"histories"`
	GraphSamples int                       `json:"graph_samples"`
	ExactPages   int                       `json:"exact_pages"`
	ExactSamples int                       `json:"exact_samples"`
	Cases        []performanceAccuracyCase `json:"cases"`
}

type performanceAccuracyCase struct {
	SloID         string                  `json:"slo_id"`
	Class         int                     `json:"class"`
	BurnSeconds   int                     `json:"burn_seconds"`
	ExpectedGood  string                  `json:"expected_good_exact_ratio"`
	ExpectedTotal string                  `json:"expected_total_exact_ratio"`
	ActualCurrent api.SloOperationalValue `json:"actual_current"`
}

func checkPerformanceOracleQuality(q api.SloOperationalQuality, want performanceOracleCounts, duration time.Duration) error {
	if q.ObservedIntervals != want.Observed || q.KnownEmptyIntervals != want.Empty || q.ZeroTrafficIntervals != want.Zero || q.MissingIntervals != want.Missing || q.ExpectedIntervals != int64(duration/time.Minute) || q.ExpiredIntervals != 0 {
		return errors.New("oracle interval qualification mismatch")
	}
	if !performanceOracleRounded(q.Coverage, big.NewRat(want.Observed+want.Empty, int64(duration/time.Minute))) {
		return errors.New("oracle collection coverage mismatch")
	}
	return nil
}

func checkPerformanceOracleMetrics(good, total, attainment, allowed, remaining, fraction, burn *string, want performanceOracleCounts, metric string) error {
	if err := checkPerformanceOracleCounts(good, total, want); err != nil {
		return err
	}
	if want.Total.Sign() == 0 {
		if attainment != nil || burn != nil || fraction != nil {
			return errors.New("oracle zero traffic/missing interval became a measured fraction")
		}
		return nil
	}
	ratio := new(big.Rat).Quo(want.Good, want.Total)
	if !performanceOracleRounded(attainment, ratio) {
		return errors.New("oracle attainment mismatch")
	}
	if metric == "attainment" {
		return nil
	}
	budget := new(big.Rat).Quo(want.Total, big.NewRat(200, 1)) // Fixture target 99.5%.
	bad := new(big.Rat).Sub(want.Total, want.Good)
	left := new(big.Rat).Sub(budget, bad)
	if !performanceOracleRounded(allowed, budget) || !performanceOracleRounded(remaining, left) || !performanceOracleRounded(fraction, new(big.Rat).Quo(left, budget)) || !performanceOracleRounded(burn, new(big.Rat).Quo(bad, budget)) {
		return errors.New("oracle error-budget/burn mismatch")
	}
	return nil
}

func checkPerformanceOracleSample(sample api.SloOperationalSample, class int, duration time.Duration, metric string) error {
	want, err := performanceOracleWindow(class, sample.EvaluationAt.Add(-duration), sample.EvaluationAt)
	if err != nil {
		return err
	}
	if err := checkPerformanceOracleQuality(sample.Quality, want, duration); err != nil {
		return err
	}
	state := api.SloOperationalSampleState("observed")
	switch {
	case want.Observed == 0 && want.Empty > 0:
		state = "known_empty"
	case want.Observed == 0:
		state = "missing"
	case want.Total.Sign() == 0:
		state = "zero_traffic"
	case want.Missing > 0 || want.Empty > 0:
		state = "partial"
	}
	if sample.State != state {
		return errors.New("oracle graph mislabels observation qualification")
	}
	return checkPerformanceOracleMetrics(sample.Good, sample.Total, sample.Attainment, sample.AllowedBudget, sample.RemainingBudget, sample.RemainingFraction, sample.Burn, want, metric)
}

func checkPerformanceAccuracyDetail(value api.SloOperationalDetail, id string, class int, start, end time.Time, burn time.Duration) error {
	window := performanceOracleRollingWindow(class)
	if value.Objective.Id.String() != id || value.Objective.Target != "0.995" || value.Generation == nil || checkPerformanceGeneration(*value.Generation) != nil || !value.Generation.EvaluatedThrough.Equal(end) || value.BurnSeries.BurnWindowSeconds == nil || int(*value.BurnSeries.BurnWindowSeconds) != int(burn/time.Second) {
		return errors.New("oracle history identity/generation/target mismatch")
	}
	for _, lane := range []struct {
		samples  []api.SloOperationalSample
		duration time.Duration
		metric   string
	}{{value.AttainmentSeries.Samples, time.Minute, "attainment"}, {value.BudgetSeries.Samples, window, "budget"}, {value.BurnSeries.Samples, burn, "burn"}} {
		if len(lane.samples) < 2 || lane.samples[0].EvaluationAt.After(start.Add(time.Hour)) || !lane.samples[len(lane.samples)-1].EvaluationAt.Equal(end) {
			return errors.New("oracle expected populated history across the complete requested range")
		}
		for _, sample := range lane.samples {
			if sample.EvaluationAt.Before(start) || sample.EvaluationAt.After(end) {
				return errors.New("oracle sample outside requested range")
			}
			if err := checkPerformanceOracleSample(sample, class, lane.duration, lane.metric); err != nil {
				return fmt.Errorf("%s at%s: %w", lane.metric, sample.EvaluationAt.Format(time.RFC3339), err)
			}
		}
	}
	// A graph containing only correct normal points must still fail if the
	// incident extremum or either side of a known gap was omitted.
	attainment := value.AttainmentSeries.Samples
	var low, high *big.Rat
	points := make(map[time.Time]bool, len(attainment))
	for _, point := range attainment {
		points[point.EvaluationAt] = true
		if point.Good == nil || point.Total == nil {
			continue
		}
		g, _ := new(big.Rat).SetString(*point.Good)
		t, _ := new(big.Rat).SetString(*point.Total)
		if t.Sign() == 0 {
			continue
		}
		ratio := g.Quo(g, t)
		if low == nil || ratio.Cmp(low) < 0 {
			low = ratio
		}
		if high == nil || ratio.Cmp(high) > 0 {
			high = ratio
		}
	}
	minimum, _ := performanceOracleWindow(class, start.Add(3*time.Hour+time.Minute), start.Add(3*time.Hour+2*time.Minute))
	if class == 5 {
		minimum, _ = performanceOracleWindow(class, start.Add(31*time.Minute), start.Add(32*time.Minute))
		for day := start; day.Before(end); day = day.Add(24 * time.Hour) {
			if !points[day.Add(32*time.Minute)] {
				return errors.New("oracle graph omitted a one-minute outage")
			}
		}
	}
	maximum, _ := performanceOracleWindow(class, start.Add(12*time.Hour+time.Minute), start.Add(12*time.Hour+2*time.Minute))
	if low == nil || high == nil || low.Cmp(new(big.Rat).Quo(minimum.Good, minimum.Total)) != 0 || high.Cmp(new(big.Rat).Quo(maximum.Good, maximum.Total)) != 0 {
		return errors.New("oracle graph omitted exact attainment extrema")
	}
	if class >= 1 && class <= 3 {
		minute := time.Minute
		if class == 1 {
			minute = 18 * time.Minute
		}
		for hour := start; hour.Before(end); hour = hour.Add(time.Hour) {
			for _, at := range []time.Time{hour.Add(minute - time.Minute), hour.Add(minute), hour.Add(minute + time.Minute)} {
				if at.After(start) && !points[at] {
					return errors.New("oracle graph omitted a qualification transition boundary")
				}
			}
		}
	}
	want, err := performanceOracleWindow(class, end.Add(-window), end)
	if err != nil {
		return err
	}
	p := value.Current.Performance
	if err := checkPerformanceOracleMetrics(p.Good, p.Total, p.Attainment, p.AllowedBudget, p.RemainingBudget, p.RemainingFraction, p.Burn, want, "budget"); err != nil {
		return err
	}
	if err := checkPerformanceOracleQuality(value.Current.Quality, want, window); err != nil {
		return err
	}
	provisional := class == 1 || class == 2
	state := api.SloEvaluationPerformanceState("met")
	if provisional {
		state = "unknown"
	}
	if p.Provisional != provisional || p.State != state || provisional && (p.ObservedState == nil || *p.ObservedState != "met") {
		return errors.New("oracle provisional/current performance mismatch")
	}
	for _, item := range []struct {
		value    api.SloBurnValue
		duration time.Duration
	}{{value.Burn1h, time.Hour}, {value.Burn6h, 6 * time.Hour}} {
		burn, duration := item.value, item.duration
		if int(burn.WindowSeconds) != int(duration/time.Second) || !burn.End.Equal(end) || !burn.Start.Equal(end.Add(-duration)) {
			return errors.New("oracle burn bounds mismatch")
		}
		counts, err := performanceOracleWindow(class, end.Add(-duration), end)
		if err != nil {
			return err
		}
		p := burn.Performance
		if err := checkPerformanceOracleMetrics(p.Good, p.Total, p.Attainment, p.AllowedBudget, p.RemainingBudget, p.RemainingFraction, p.Burn, counts, "burn"); err != nil {
			return err
		}
		if err := checkPerformanceOracleQuality(burn.Quality, counts, duration); err != nil {
			return err
		}
	}
	if len(value.ComplianceSeries.Days) != 7 {
		return errors.New("oracle missing expected daily compliance")
	}
	for index, day := range value.ComplianceSeries.Days {
		expectedEnd := start.Add(time.Duration(index+1) * 24 * time.Hour)
		if day.SloId.String() != id || !day.EffectiveAt.Equal(expectedEnd) || !day.RequestedAt.Equal(expectedEnd) || !day.DayEnd.Equal(expectedEnd) || !day.DayStart.Equal(expectedEnd.Add(-24*time.Hour)) {
			return errors.New("oracle daily boundary/identity mismatch")
		}
		counts, err := performanceOracleWindow(class, day.EffectiveAt.Add(-window), day.EffectiveAt)
		if err != nil {
			return err
		}
		p := day.Performance
		if err := checkPerformanceOracleMetrics(p.Good, p.Total, p.Attainment, p.AllowedBudget, p.RemainingBudget, p.RemainingFraction, p.Burn, counts, "budget"); err != nil {
			return err
		}
		status := api.SloDailyComplianceDayStatus("met")
		if class == 1 || class == 2 {
			status = "unknown"
		}
		if day.Status != status {
			return errors.New("oracle daily compliance qualification mismatch")
		}
	}
	if class == 1 || class == 2 {
		if value.ComplianceSummary.UnknownDays != 7 || value.ComplianceSummary.EvaluableDays != 0 {
			return errors.New("oracle partial days counted as qualified success")
		}
	} else if value.ComplianceSummary.MetDays != 7 || value.ComplianceSummary.EvaluableDays != 7 {
		return errors.New("oracle qualified compliance summary mismatch")
	}
	summary := value.ComplianceSummary
	if summary.MetDays+summary.ViolatedDays+summary.UnknownDays+summary.NotApplicableDays != 7 || summary.EvaluableDays != summary.MetDays+summary.ViolatedDays {
		return errors.New("oracle daily summary denominator mismatch")
	}
	return nil
}

func runPerformanceAccuracy(ctx context.Context, fleet performanceFleet) (performanceAccuracyReport, error) {
	end := time.Date(fleet.historyEnd.Year(), fleet.historyEnd.Month(), fleet.historyEnd.Day(), 0, 0, 0, 0, time.UTC).Add(-24 * time.Hour)
	start := end.Add(-7 * 24 * time.Hour)
	report := performanceAccuracyReport{Start: start, End: end, Classes: 10, Cases: make([]performanceAccuracyCase, 20)}
	for burnIndex, burn := range []time.Duration{time.Hour, 6 * time.Hour} {
		query := url.Values{"at": {end.Format(time.RFC3339)}, "start": {start.Format(time.RFC3339)}, "end": {end.Format(time.RFC3339)}, "burn_window_seconds": {fmt.Sprint(int(burn / time.Second))}}
		op := performanceOperation{Name: fmt.Sprintf("accuracy-%dh", int(burn/time.Hour)), Budget: 500 * time.Millisecond}
		for _, id := range fleet.objectives[:10] {
			op.Paths = append(op.Paths, "/api/v1/slos/"+id+"/operational?"+query.Encode())
		}
		op.Check = func(index int, body []byte) error {
			var value api.SloOperationalDetail
			if err := json.Unmarshal(body, &value); err != nil {
				return err
			}
			class := (index + 1) % 10
			expected, err := performanceOracleWindow(class, end.Add(-performanceOracleRollingWindow(class)), end)
			if err != nil {
				return err
			}
			report.Cases[burnIndex*10+index] = performanceAccuracyCase{SloID: fleet.objectives[index], Class: class, BurnSeconds: int(burn / time.Second), ExpectedGood: expected.Good.RatString(), ExpectedTotal: expected.Total.RatString(), ActualCurrent: value.Current}
			return checkPerformanceAccuracyDetail(value, fleet.objectives[index], (index+1)%10, start, end, burn)
		}
		samples, err := preparePerformanceOperation(ctx, fleet.readers, op)
		if err != nil {
			return report, err
		}
		for index, sample := range samples {
			var detail api.SloOperationalDetail
			if err := json.Unmarshal(sample.Body, &detail); err != nil {
				return report, err
			}
			report.Histories++
			report.GraphSamples += len(detail.AttainmentSeries.Samples) + len(detail.BudgetSeries.Samples) + len(detail.BurnSeries.Samples)
			values := performanceOperation{Name: "accuracy-exact-values", Budget: 500 * time.Millisecond, Paths: []string{"/api/v1/slos/" + fleet.objectives[index] + "/history/" + detail.Generation.GenerationId.String() + "/values?lane=budget&offset=0&limit=100"}}
			values.Check = func(_ int, body []byte) error {
				var page api.SloHistoryValuePage
				if err := json.Unmarshal(body, &page); err != nil {
					return err
				}
				if page.Generation.GenerationId != detail.Generation.GenerationId || page.TotalValues != len(detail.BudgetSeries.Samples) || page.TotalValues == 0 || page.Offset != 0 || page.Lane != "budget" || len(page.Samples) != min(100, page.TotalValues) {
					return errors.New("oracle exact-value page lost pinned populated history")
				}
				for i, point := range page.Samples {
					if !point.EvaluationAt.Equal(detail.BudgetSeries.Samples[i].EvaluationAt) {
						return errors.New("oracle exact table differs from graph evaluation times")
					}
					if err := checkPerformanceOracleSample(point, (index+1)%10, performanceOracleRollingWindow((index+1)%10), "budget"); err != nil {
						return err
					}
				}
				return nil
			}
			pages, err := preparePerformanceOperation(ctx, fleet.readers, values)
			if err != nil {
				return report, err
			}
			report.ExactPages++
			var page api.SloHistoryValuePage
			if err := json.Unmarshal(pages[0].Body, &page); err != nil {
				return report, err
			}
			report.ExactSamples += len(page.Samples)
		}
	}
	return report, nil
}
