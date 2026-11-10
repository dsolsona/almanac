package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

type extremaBudgetCache struct {
	ClosedThrough time.Time                  `json:"closed_through"`
	PublishedAt   time.Time                  `json:"published_at"`
	RangeStart    time.Time                  `json:"range_start"`
	LastStart     time.Time                  `json:"last_start"`
	Closed        []api.SloOperationalSample `json:"closed"`
}

func attachExtremaBudget(ctx context.Context, q *dbgen.Queries, item *api.SloOperationalOverviewItem, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, now time.Time) ([]byte, error) {
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	at := item.Current.EvaluationAt
	start, closed := floorSliBoundary(at.Add(-7*24*time.Hour), cadence), at.Truncate(time.Hour)
	base := extremaWindowInput(row, source, now, now.Add(-365*24*time.Hour))
	eligible := maxTime(start.Truncate(time.Hour), maxTime(base.ValidFrom, base.SliValidFrom).Truncate(time.Hour))
	state, err := q.ReadSloBudgetExtremaState(ctx, dbgen.ReadSloBudgetExtremaStateParams{SloID: row.ID,
		DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
		CalculationVersion: operationalCalculationVersion, RangeStart: timestamp(start.Truncate(time.Hour)), EligibleStart: timestamp(eligible), ClosedThrough: timestamp(closed)})
	if err != nil {
		return nil, err
	}
	history := api.SloOperationalOverviewItemBudgetHistoryState("ready")
	if state.ReadyHours < max(0, int64(closed.Sub(eligible)/time.Hour)) {
		history = "preparing"
		if state.Failing {
			history = "unavailable"
		}
	}
	item.BudgetHistoryState = &history
	var previous []api.SloOperationalSample
	var cache extremaBudgetCache
	cacheHit := json.Unmarshal(state.Cache, &cache) == nil && json.Unmarshal(state.Samples, &previous) == nil &&
		len(previous) > 0 && len(previous) <= 120 && len(cache.Closed) <= 120 &&
		cache.RangeStart.Before(at) && !cache.RangeStart.Before(at.Add(-8*24*time.Hour)) &&
		cache.ClosedThrough.Equal(closed) && cache.PublishedAt.Equal(state.PublishedAt.Time)
	if cacheHit {
		start = cache.RangeStart
	}
	width := retainedDisplayWidth(start, at, 30)
	lastStart := maxTime(start, at.Add(-time.Microsecond).Truncate(width))
	var samples []api.SloOperationalSample
	if cacheHit {
		for _, sample := range previous {
			if sample.BucketStart.Before(lastStart) {
				samples = append(samples, sample)
			}
		}
		if cache.LastStart.Equal(lastStart) {
			samples = append(samples, cache.Closed...)
		} else {
			cache.Closed = nil
		}
	} else {
		bound := operationalBounds{start: start, end: at, effectiveAt: at}
		plan, err := planExtremaRead(base, bound)
		if err != nil {
			return nil, err
		}
		plan.days, plan.stored = nil, nil
		closedParts := plan.parts[:0]
		for _, part := range plan.parts {
			if part.end.After(closed) {
				continue
			}
			closedParts = append(closedParts, part)
			if part.resolution != 0 {
				plan.stored = append(plan.stored, part)
			}
		}
		plan.parts = closedParts
		reader := &extremaReader{retainedSeriesReader: retainedSeriesReader{q: q, row: row, source: source, now: now}}
		if err := reader.preparePlan(ctx, base, bound, plan); err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			history = "unavailable"
			item.BudgetSparkline = []api.SloOperationalSample{}
			return json.Marshal(extremaBudgetCache{ClosedThrough: closed, PublishedAt: state.PublishedAt.Time, RangeStart: start})
		}
		revision := strconv.FormatInt(row.DefinitionRevision, 10)
		if reader.first != nil {
			first := reader.first.Current
			samples = append(samples, retainedWindowSample(start, first.Quality, first.Performance, revision))
		} else {
			history = "preparing"
			samples = append(samples, reader.unpreparedTile(base, retainedReadPart{start: start.Add(-cadence), end: start}).Budget...)
		}
		for _, sample := range reader.series.Budget {
			if !sample.EvaluationAt.After(closed) {
				samples = append(samples, sample)
			}
		}
		cache = extremaBudgetCache{ClosedThrough: closed, PublishedAt: state.PublishedAt.Time, RangeStart: start, LastStart: lastStart}
		for _, sample := range samples {
			if sample.EvaluationAt.After(lastStart) {
				cache.Closed = append(cache.Closed, sample)
			}
		}
		if len(cache.Closed) > 0 {
			selected, reduceErr := reduceRetainedLaneLimit(cache.Closed, lastStart, closed, row.Target, "budget", 120)
			if reduceErr != nil {
				return nil, reduceErr
			}
			cache.Closed = selected
		}
	}
	current := retainedWindowSample(at, item.Current.Quality, item.Current.Performance, strconv.FormatInt(row.DefinitionRevision, 10))
	if !item.Current.Window.End.Equal(at) {
		base.At = at
		values, evalErr := evaluateDerivedWindows(ctx, q, row.SliID, row.MeasurementRevision, []slo.WindowInput{base})
		switch {
		case evalErr == nil:
			current = operationalSample(values[0], at, at, strconv.FormatInt(row.DefinitionRevision, 10))
		case errors.Is(evalErr, ErrDerivedPending), errors.Is(evalErr, ErrDerivedSourceFailed):
			history = "preparing"
			reason := "history_preparing"
			if errors.Is(evalErr, ErrDerivedSourceFailed) {
				history, reason = "unavailable", "history_preparation_failed"
			}
			gap := unavailableOperationalSamples(at.Add(-cadence), at, cadence, strconv.FormatInt(row.DefinitionRevision, 10), 1, reason)
			current = gap[len(gap)-1]
		default:
			return nil, evalErr
		}
	}
	if len(samples) > 0 && samples[len(samples)-1].EvaluationAt.Equal(at) {
		samples = samples[:len(samples)-1]
	}
	samples = append(samples, current)
	item.BudgetSparkline, err = reduceRetainedDisplay(samples, start, at, row.Target, "budget", 30)
	if err == nil && len(item.BudgetSparkline) > 120 {
		item.BudgetSparkline, err = reduceRetainedDisplayLimit(samples, start, at, row.Target, "budget", 30, 4)
	}
	if err != nil {
		return nil, err
	}
	item.BudgetHistoryGeneration = item.Generation
	cache.LastStart = lastStart
	return json.Marshal(cache)
}
