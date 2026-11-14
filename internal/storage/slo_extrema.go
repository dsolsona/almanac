package storage

import (
	"context"
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

type extremaLanes struct {
	Window     *extremaLane       `json:"window"`
	Attainment *extremaLane       `json:"attainment"`
	Burn1h     *extremaLane       `json:"burn_1h"`
	Burn6h     *extremaLane       `json:"burn_6h"`
	Compliance *extremaCompliance `json:"compliance,omitempty"`
}

type extremaCompliance struct {
	DayEnd time.Time   `json:"day_end"`
	Mark   extremaMark `json:"mark"`
}

func extremaWindowInput(row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, now, retention time.Time) slo.WindowInput {
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	return slo.WindowInput{Now: now, ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(),
		RetentionStart: retention, Duration: time.Duration(row.RollingWindowSeconds) * time.Second, Cadence: cadence,
		Target: sli.NormalizeDecimal(row.Target), FreshnessGrace: cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))}
}

func (lanes extremaLanes) list() [4]*extremaLane {
	return [4]*extremaLane{lanes.Window, lanes.Attainment, lanes.Burn1h, lanes.Burn6h}
}

func (lanes extremaLanes) tile(ctx context.Context, input slo.WindowInput, start, end time.Time, revision string) (retainedSloTile, error) {
	var values [4][]api.SloOperationalSample
	for index, lane := range lanes.list() {
		if lane == nil {
			continue
		}
		base := input
		base.Duration = [4]time.Duration{input.Duration, input.Cadence, time.Hour, 6 * time.Hour}[index]
		var err error
		values[index], err = lane.samples(ctx, base, start, end, revision, [4]string{"budget", "attainment", "burn", "burn"}[index])
		if err != nil {
			return retainedSloTile{}, err
		}
	}
	return retainedSloTile{Budget: values[0], Attainment: values[1], Burn1h: values[2], Burn6h: values[3]}, nil
}

func makeExtremaLanes(values [4][]api.SloOperationalSample, start, end time.Time, target string, cadence time.Duration) (extremaLanes, error) {
	var lanes [4]*extremaLane
	for index, samples := range values {
		if index == 2 && time.Hour%cadence != 0 || index == 3 && 6*time.Hour%cadence != 0 {
			continue
		}
		metric := [4]string{"budget", "attainment", "burn", "burn"}[index]
		lane, err := makeExtremaLane(samples, start, end, target, metric)
		if err != nil {
			return extremaLanes{}, err
		}
		lanes[index] = &lane
	}
	return extremaLanes{Window: lanes[0], Attainment: lanes[1], Burn1h: lanes[2], Burn6h: lanes[3]}, nil
}

type extremaMark struct {
	RetentionStart   time.Time                                 `json:"retention_start,omitzero"`
	At               time.Time                                 `json:"at"`
	Labels           []api.SloOperationalSampleRetainedExtrema `json:"labels"`
	Good             string                                    `json:"good"`
	Total            string                                    `json:"total"`
	Observed         int64                                     `json:"observed"`
	KnownEmpty       int64                                     `json:"known_empty"`
	ZeroTraffic      int64                                     `json:"zero_traffic"`
	FirstObservation *time.Time                                `json:"first_observation_at,omitempty"`
	LastObservation  *time.Time                                `json:"last_observation_at,omitempty"`
	LatestReceipt    *time.Time                                `json:"latest_receipt_at,omitempty"`
	State            api.SloOperationalSampleState             `json:"state"`
	Quality          api.SloOperationalQualityState            `json:"quality"`
	Freshness        api.SloOperationalQualityFreshness        `json:"freshness"`
}

type extremaLane struct {
	Marks     []extremaMark                         `json:"marks"`
	States    []api.SloDisplayCoverageStates        `json:"states"`
	Quality   []api.SloDisplayCoverageQualityStates `json:"quality"`
	Freshness []api.SloDisplayCoverageFreshness     `json:"freshness"`
	Coarse    bool                                  `json:"coarse,omitzero"`
}

func makeExtremaLane(samples []api.SloOperationalSample, start, end time.Time, target, metric string) (extremaLane, error) {
	selected, err := reduceRetainedLaneLimit(samples, start, end, target, metric, sloOperationalSeriesLimit)
	if err != nil {
		return extremaLane{}, err
	}
	return extremaLaneFromSelected(samples, selected)
}

func makeExtremaSummaryLane(values []slo.WindowResult, start, end time.Time, target, metric, revision string) (extremaLane, error) {
	samples := make([]api.SloOperationalSample, len(values))
	indices := make(map[time.Time]int, len(values))
	for index, value := range values {
		samples[index] = operationalSample(value, start, end, revision)
		indices[value.End] = index
	}
	selected, err := reduceRetainedLaneWithKey(samples, start, end, target, sloOperationalSeriesLimit, func(index int, _ api.SloOperationalSample) (*big.Rat, error) {
		burn := values[index].ExactBurn
		if burn == nil {
			return nil, nil
		}
		switch metric {
		case "budget", "attainment":
			return new(big.Rat).Neg(burn), nil
		case "burn":
			return burn, nil
		default:
			return nil, slo.ErrOperationalFacts
		}
	})
	if err != nil {
		return extremaLane{}, err
	}
	for index, sample := range selected {
		formatted := operationalSample(values[indices[sample.EvaluationAt]].WithValues(), start, end, revision)
		formatted.RetainedExtrema, formatted.DisplayCoverage = sample.RetainedExtrema, sample.DisplayCoverage
		selected[index] = formatted
	}
	return extremaLaneFromSelected(samples, selected)
}

func extremaLaneFromSelected(samples, selected []api.SloOperationalSample) (extremaLane, error) {
	presence, err := retainedDisplayCoverage(samples)
	if err != nil {
		return extremaLane{}, err
	}
	lane := extremaLane{States: presence.States, Quality: presence.QualityStates, Freshness: presence.Freshness}
	for _, sample := range selected {
		mark := extremaMark{At: sample.EvaluationAt, Labels: sample.RetainedExtrema,
			Good: "0", Total: "0", Observed: sample.Quality.ObservedIntervals,
			KnownEmpty: sample.Quality.KnownEmptyIntervals, ZeroTraffic: sample.Quality.ZeroTrafficIntervals,
			FirstObservation: sample.Quality.FirstObservationAt, LastObservation: sample.Quality.LastObservationAt,
			LatestReceipt: sample.Quality.LatestReceiptAt, State: sample.State, Quality: sample.Quality.State,
			Freshness: sample.Quality.Freshness}
		if sample.Good != nil {
			mark.Good = *sample.Good
		}
		if sample.Total != nil {
			mark.Total = *sample.Total
		}
		lane.Coarse = lane.Coarse || sample.DisplayCoverage != nil
		lane.Marks = append(lane.Marks, mark)
	}
	return lane, nil
}

func (lane extremaLane) samples(ctx context.Context, input slo.WindowInput, start, end time.Time, revision, metric string) ([]api.SloOperationalSample, error) {
	if len(lane.Marks) > sloOperationalSeriesLimit {
		return nil, ErrDerivedFailed
	}
	samples := make([]api.SloOperationalSample, 0, len(lane.Marks))
	for index, mark := range lane.Marks {
		if !mark.At.After(start) || mark.At.After(end) ||
			index > 0 && !lane.Marks[index-1].At.Before(mark.At) {
			return nil, ErrDerivedFailed
		}
		value, err := mark.result(ctx, input)
		if err != nil {
			return nil, err
		}
		sample := operationalSample(value, start, end, revision)
		if metric == "attainment" {
			sample = operationalAttainmentSample(value, start, end, revision)
		} else if metric == "burn" {
			burn := burnValue(value, int(input.Duration/time.Second))
			sample = retainedWindowSample(mark.At, burn.Quality, burn.Performance, revision)
			sample.BucketStart, sample.BucketEnd = start, end
		}
		if sample.State != mark.State || sample.Quality.State != mark.Quality || sample.Quality.Freshness != mark.Freshness {
			return nil, ErrDerivedFailed
		}
		for _, label := range mark.Labels {
			if !label.Valid() {
				return nil, ErrDerivedFailed
			}
		}
		sample.RetainedExtrema = mark.Labels
		if lane.Coarse {
			sample.DisplayCoverage = &api.SloDisplayCoverage{States: lane.States, QualityStates: lane.Quality, Freshness: lane.Freshness}
		}
		samples = append(samples, sample)
	}
	if _, err := retainedDisplayCoverage(samples); err != nil {
		return nil, err
	}
	return samples, nil
}

func (mark extremaMark) result(ctx context.Context, input slo.WindowInput) (slo.WindowResult, error) {
	good, goodOK := new(big.Rat).SetString(mark.Good)
	total, totalOK := new(big.Rat).SetString(mark.Total)
	if !goodOK || !totalOK {
		return slo.WindowResult{}, ErrDerivedFailed
	}
	input.At = mark.At
	if !mark.RetentionStart.IsZero() {
		input.RetentionStart = mark.RetentionStart
	}
	return slo.EvaluateOperationalTotals(ctx, input, slo.WindowTotals{Good: good, Total: total, Observed: mark.Observed,
		KnownEmpty: mark.KnownEmpty, ZeroTraffic: mark.ZeroTraffic, FirstObservation: mark.FirstObservation,
		LastObservation: mark.LastObservation, LatestReceipt: mark.LatestReceipt})
}
