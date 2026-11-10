package storage

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

type retainedReadPart struct {
	start, end time.Time
	resolution int32
}

func retainedReadParts(start, end time.Time) ([]retainedReadPart, error) {
	if !start.Before(end) || end.Sub(start) > 365*24*time.Hour {
		return nil, ErrOperationalRange
	}
	var parts []retainedReadPart
	hourly := end.Sub(start) <= 28*24*time.Hour
	for start.Before(end) {
		part := retainedReadPart{start: start, end: end}
		if !hourly && start.Equal(utcDayStart(start)) && !start.Add(24*time.Hour).After(end) {
			part.end, part.resolution = start.Add(24*time.Hour), 86400
		} else if start.Equal(start.Truncate(time.Hour)) && !start.Add(time.Hour).After(end) {
			part.end, part.resolution = start.Add(time.Hour), 3600
		} else if nextHour := start.Truncate(time.Hour).Add(time.Hour); nextHour.Before(end) {
			part.end = nextHour
		}
		parts = append(parts, part)
		start = part.end
	}
	return parts, nil
}

// All database reads finish in extremaReader.prepare before display assembly.
type retainedSeriesReader struct {
	q            *dbgen.Queries
	row          dbgen.GetSloRow
	source       dbgen.GetSloOperationalContextRow
	now          time.Time
	series       *retainedSloTile
	first        *retainedSloPoint
	days         *api.SloDailyComplianceSeries
	daySummary   api.SloDailyComplianceSummary
	readySeconds int64
	totalSeconds int64
	pending      bool
	failed       bool
}

func appendRetainedTile(to *retainedSloTile, from retainedSloTile) {
	to.Attainment = append(to.Attainment, from.Attainment...)
	to.Budget = append(to.Budget, from.Budget...)
	to.Burn1h = append(to.Burn1h, from.Burn1h...)
	to.Burn6h = append(to.Burn6h, from.Burn6h...)
}

func (c *retainedSeriesReader) beforeValidity(ctx context.Context, base slo.WindowInput, part retainedReadPart) (retainedSloTile, error) {
	revision := strconv.FormatInt(c.row.DefinitionRevision, 10)
	points := []retainedSloPoint{}
	first, last := floorSliBoundary(part.start, base.Cadence).Add(base.Cadence), floorSliBoundary(part.end, base.Cadence)
	valid := ceilSliBoundary(maxTime(base.ValidFrom, base.SliValidFrom), base.Cadence)
	endpoints := []time.Time{first, last, valid, valid.Add(base.Cadence)}
	slices.SortFunc(endpoints, time.Time.Compare)
	for _, at := range slices.Compact(endpoints) {
		if at.Before(first) || at.After(last) {
			continue
		}
		point, err := c.unobservedPoint(ctx, base, at)
		if err != nil {
			return retainedSloTile{}, err
		}
		points = append(points, point)
	}
	return compactRetainedPoints(points, part.start, part.end, c.row.Target, revision)
}

func (c *retainedSeriesReader) unobservedPoint(ctx context.Context, base slo.WindowInput, at time.Time) (retainedSloPoint, error) {
	rolling, err := evaluateOperationalAt(ctx, base, at, time.Duration(c.row.RollingWindowSeconds)*time.Second)
	if err != nil {
		return retainedSloPoint{}, err
	}
	interval, err := evaluateOperationalAt(ctx, base, at, base.Cadence)
	if err != nil {
		return retainedSloPoint{}, err
	}
	point := retainedSloPoint{Current: operationalValue(rolling), Attainment: operationalAttainmentSample(interval, at.Add(-base.Cadence), at, strconv.FormatInt(c.row.DefinitionRevision, 10)), Burn1h: unavailableBurn(3600, at, "cadence_mismatch"), Burn6h: unavailableBurn(21600, at, "cadence_mismatch")}
	for _, duration := range []time.Duration{time.Hour, 6 * time.Hour} {
		if duration%base.Cadence != 0 {
			continue
		}
		burn, err := evaluateOperationalAt(ctx, base, at, duration)
		if err != nil {
			return retainedSloPoint{}, err
		}
		if duration == time.Hour {
			point.Burn1h = burnValue(burn, 3600)
		} else {
			point.Burn6h = burnValue(burn, 21600)
		}
	}
	return point, nil
}

func (c *retainedSeriesReader) load(ctx context.Context, _ slo.WindowInput, _, _ time.Time) (retainedSloTile, error) {
	if err := ctx.Err(); err != nil {
		return retainedSloTile{}, err
	}
	if c.series == nil {
		return retainedSloTile{}, slo.ErrOperationalFacts
	}
	return *c.series, nil
}

func (c *retainedSeriesReader) unpreparedTile(base slo.WindowInput, part retainedReadPart) retainedSloTile {
	reason := "history_preparing"
	if c.failed {
		reason = "history_preparation_failed"
	}
	samples := unavailableOperationalSamples(part.start, part.end, base.Cadence, strconv.FormatInt(c.row.DefinitionRevision, 10), 1, reason)
	return retainedSloTile{Attainment: samples, Budget: samples, Burn1h: samples, Burn6h: samples}
}

// Re-reduce exact extrema only within their display buckets.
func reduceRetainedDisplay(samples []api.SloOperationalSample, start, end time.Time, target, metric string, buckets int) ([]api.SloOperationalSample, error) {
	result, err := reduceRetainedDisplayLimit(samples, start, end, target, metric, buckets, sloOperationalSeriesLimit)
	if errors.Is(err, ErrOperationalRangeDense) && buckets > 0 {
		return reduceRetainedDisplayLimit(samples, start, end, target, metric, buckets, 4)
	}
	return result, err
}

func reduceRetainedDisplayLimit(samples []api.SloOperationalSample, start, end time.Time, target, metric string, buckets, perBucketLimit int) ([]api.SloOperationalSample, error) {
	if len(samples) == 0 {
		return []api.SloOperationalSample{}, nil
	}
	if buckets <= 0 {
		return nil, ErrOperationalRangeDense
	}
	result := make([]api.SloOperationalSample, 0, min(len(samples), sloOperationalSeriesLimit))
	width := retainedDisplayWidth(start, end, buckets)
	for first := 0; first < len(samples); {
		bucketStart := samples[first].EvaluationAt.Add(-time.Microsecond).Truncate(width)
		if samples[first].EvaluationAt.Equal(start) {
			bucketStart = start.Truncate(width)
		}
		from, to := maxTime(start, bucketStart), bucketStart.Add(width)
		if end.Before(to) {
			to = end
		}
		last := first + 1
		for last < len(samples) && !samples[last].EvaluationAt.After(to) {
			last++
		}
		selected, err := reduceRetainedLaneLimit(samples[first:last], from, to, target, metric, perBucketLimit)
		if err != nil {
			return nil, err
		}
		result = append(result, selected...)
		if len(result) > sloOperationalSeriesLimit {
			return nil, ErrOperationalRangeDense
		}
		first = last
	}
	return result, nil
}

func retainedDisplayWidth(start, end time.Time, buckets int) time.Duration {
	grid := time.Hour
	if end.Sub(start) > 28*24*time.Hour {
		grid = 24 * time.Hour
	}
	ticks := (end.Sub(start.Truncate(grid)) + grid - 1) / grid
	return max(1, (ticks+time.Duration(buckets)-1)/time.Duration(buckets)) * grid
}

func (c *retainedSeriesReader) rolling(ctx context.Context, base slo.WindowInput, start, end time.Time, duration, cadence time.Duration, revision, metric string, maxBuckets int) ([]api.SloOperationalSample, error) {
	tile, err := c.load(ctx, base, start, end)
	if err != nil {
		return nil, err
	}
	samples, selectedMetric := tile.Budget, "budget"
	if metric == "burn" {
		samples, selectedMetric = tile.Burn1h, "burn"
		if duration == 6*time.Hour {
			samples = tile.Burn6h
		}
	}
	if c.first == nil {
		missing := c.unpreparedTile(base, retainedReadPart{start: start.Add(-cadence), end: start})
		samples = append(missing.Budget, samples...)
		return reduceRetainedDisplay(samples, start, end, c.row.Target, selectedMetric, min(maxBuckets, 375))
	}
	first := c.first.Current
	if metric == "burn" {
		burn := c.first.Burn1h
		if duration == 6*time.Hour {
			burn = c.first.Burn6h
		}
		first.Quality, first.Performance = burn.Quality, burn.Performance
	}
	samples = append([]api.SloOperationalSample{retainedWindowSample(start, first.Quality, first.Performance, revision)}, samples...)
	return reduceRetainedDisplay(samples, start, end, c.row.Target, selectedMetric, min(maxBuckets, 375))
}

func (c *retainedSeriesReader) attainment(ctx context.Context, base slo.WindowInput, start, end time.Time, cadence time.Duration, revision string, _ ...[]time.Time) ([]api.SloOperationalSample, time.Duration, bool, error) {
	tile, err := c.load(ctx, base, start, end)
	if err != nil {
		return nil, 0, false, err
	}
	samples, err := reduceRetainedDisplay(tile.Attainment, start, end, c.row.Target, "attainment", min(sampleBucketLimit(start, end, cadence), 375))
	resolution := retainedDisplayWidth(start, end, min(sampleBucketLimit(start, end, cadence), 375))
	return samples, max(cadence, resolution), true, err
}

func (c *retainedSeriesReader) daily(ctx context.Context, _ slo.WindowInput, _, _, _ time.Time, _ openapi_types.UUID, _, _ string, _, _ time.Duration) (api.SloDailyComplianceSeries, api.SloDailyComplianceSummary, error) {
	if err := ctx.Err(); err != nil {
		return api.SloDailyComplianceSeries{}, api.SloDailyComplianceSummary{}, err
	}
	if c.days == nil {
		return api.SloDailyComplianceSeries{}, api.SloDailyComplianceSummary{}, slo.ErrOperationalFacts
	}
	return *c.days, c.daySummary, nil
}

func (c *retainedSeriesReader) unpreparedDay(part retainedReadPart, validFrom time.Time, id openapi_types.UUID, revision, target string, window, cadence time.Duration) api.SloDailyComplianceDay {
	reason := "history_preparing"
	if c.failed {
		reason = "history_preparation_failed"
	}
	day := operationalComplianceDay(slo.WindowResult{End: floorSliBoundary(part.end, cadence), State: "unavailable", Freshness: "unknown", Performance: "unknown"}, part.end, validFrom, id, revision, target, window, cadence)
	day.Status, day.Explanation = "unknown", reason
	day.Quality.Reason, day.Performance.Reason = &reason, &reason
	return day
}
