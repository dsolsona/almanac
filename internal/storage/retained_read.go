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
	openapi_types "github.com/oapi-codegen/runtime/types"
)

type retainedReadPart struct {
	start, end time.Time
	resolution int32
}

// A year uses at most 365 complete UTC days, 46 complete edge hours and two
// partial hours. Ranges through 28 days use at most 674 hourly/edge parts for
// finer detail. Storage work depends on the requested range, never fleet size.
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

type retainedSeriesReader struct {
	q            *dbgen.Queries
	row          dbgen.GetSloRow
	source       dbgen.GetSloOperationalContextRow
	now          time.Time
	series       *retainedSloTile
	first        *retainedSloPoint
	bytes        int64
	readySeconds int64
	totalSeconds int64
	pending      bool
	failed       bool
}

func (c *retainedSeriesReader) readParts(ctx context.Context, parts []retainedReadPart) ([]dbgen.ReadRetainedPartsRow, error) {
	if len(parts) == 0 {
		return []dbgen.ReadRetainedPartsRow{}, nil
	}
	params := dbgen.ReadRetainedPartsParams{SloID: c.row.ID, DefinitionRevision: c.row.DefinitionRevision,
		MeasurementRevision: c.row.MeasurementRevision, CalculationVersion: operationalCalculationVersion}
	for _, part := range parts {
		params.Starts, params.Ends = append(params.Starts, timestamp(part.start)), append(params.Ends, timestamp(part.end))
		params.Resolutions = append(params.Resolutions, part.resolution)
	}
	rows, err := c.q.ReadRetainedParts(ctx, params)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(parts) {
		return nil, slo.ErrOperationalFacts
	}
	for index, row := range rows {
		if row.PayloadBytes > 20<<20 {
			return nil, ErrOperationalResource
		}
		c.bytes += int64(len(row.Payload))
		if c.bytes > 20<<20 {
			return nil, ErrOperationalResource
		}
		part := parts[index]
		if row.Ordinal != int32(index+1) || !row.RangeStart.Time.Equal(part.start) || !row.RangeEnd.Time.Equal(part.end) || row.Resolution != part.resolution {
			return nil, slo.ErrOperationalFacts
		}
	}
	return rows, nil
}

func appendRetainedTile(to *retainedSloTile, from retainedSloTile) {
	to.Attainment = append(to.Attainment, from.Attainment...)
	to.Budget = append(to.Budget, from.Budget...)
	to.Burn1h = append(to.Burn1h, from.Burn1h...)
	to.Burn6h = append(to.Burn6h, from.Burn6h...)
}

func decodeRetainedTile(payload []byte, part retainedReadPart, revision string) (retainedSloTile, error) {
	var tile retainedSloTile
	if err := json.Unmarshal(payload, &tile); err != nil {
		return tile, ErrDerivedFailed
	}
	for _, lane := range [][]api.SloOperationalSample{tile.Attainment, tile.Budget, tile.Burn1h, tile.Burn6h} {
		if len(lane) == 0 {
			return tile, ErrDerivedFailed
		}
		if len(lane) > sloOperationalSeriesLimit {
			return tile, ErrOperationalRangeDense
		}
		for index, sample := range lane {
			if !sample.BucketStart.Equal(part.start) || !sample.BucketEnd.Equal(part.end) ||
				!sample.EvaluationAt.After(part.start) || sample.EvaluationAt.After(part.end) ||
				sample.Revision != revision || !sample.State.Valid() || !sample.Quality.State.Valid() || !sample.Quality.Freshness.Valid() ||
				index > 0 && !lane[index-1].EvaluationAt.Before(sample.EvaluationAt) {
				return tile, ErrDerivedFailed
			}
		}
	}
	return tile, nil
}

func (c *retainedSeriesReader) edge(ctx context.Context, base slo.WindowInput, part retainedReadPart) (retainedSloTile, error) {
	start, end := floorSliBoundary(part.start, base.Cadence), floorSliBoundary(part.end, base.Cadence)
	if !start.Before(end) {
		return retainedSloTile{}, nil
	}
	points, err := c.points(ctx, base, start, end)
	if err != nil {
		return retainedSloTile{}, err
	}
	return compactRetainedPoints(points, part.start, part.end, c.row.Target, strconv.FormatInt(c.row.DefinitionRevision, 10))
}

// Historical edges use the same durable endpoints as complete tiles. Rebuilding
// them from today's raw retention boundary would change previously known totals.
func (c *retainedSeriesReader) points(ctx context.Context, base slo.WindowInput, start, end time.Time) ([]retainedSloPoint, error) {
	first := start.Add(base.Cadence)
	eligible := maxTime(base.ValidFrom, base.SliValidFrom, ceilSliBoundary(base.RetentionStart, base.Cadence))
	var prefix []retainedSloPoint
	for !first.After(end) && !first.After(eligible) {
		point, err := c.unobservedPoint(ctx, base, first)
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, point)
		start, first = first, first.Add(base.Cadence)
	}
	if !start.Before(end) {
		return prefix, nil
	}
	if end.After(c.now.Truncate(time.Hour).Add(-time.Hour)) {
		points, err := buildRetainedRepairPoints(ctx, c.q, c.row, c.source, start, end, c.now)
		return append(prefix, points...), err
	}
	rows, err := c.q.ReadRetainedEdgePoints(ctx, dbgen.ReadRetainedEdgePointsParams{SloID: c.row.ID,
		DefinitionRevision: c.row.DefinitionRevision, MeasurementRevision: c.row.MeasurementRevision,
		CalculationVersion: operationalCalculationVersion, EvaluationAt: timestamp(start), EvaluationAt2: timestamp(end)})
	if err != nil {
		return nil, err
	}
	if len(rows) > 960 {
		return nil, ErrOperationalResource
	}
	expected := int(end.Sub(start) / base.Cadence)
	if len(rows) != expected {
		return nil, ErrDerivedPending
	}
	points := make([]retainedSloPoint, len(rows))
	for index, row := range rows {
		if row.PayloadBytes > 20<<20 {
			return nil, ErrOperationalResource
		}
		c.bytes += int64(len(row.Payload))
		if c.bytes > 20<<20 {
			return nil, ErrOperationalResource
		}
		if row.Exhausted && row.Repairing {
			c.failed = true
			return nil, ErrDerivedPending
		}
		if row.Repairing {
			return nil, ErrDerivedPending
		}
		if err := json.Unmarshal(row.Payload, &points[index]); err != nil ||
			!row.EvaluationAt.Time.Equal(start.Add(time.Duration(index+1)*base.Cadence)) ||
			!points[index].Current.EvaluationAt.Equal(row.EvaluationAt.Time) {
			return nil, ErrDerivedFailed
		}
	}
	return append(prefix, points...), nil
}

func (c *retainedSeriesReader) beforeValidity(ctx context.Context, base slo.WindowInput, part retainedReadPart) (retainedSloTile, error) {
	revision := strconv.FormatInt(c.row.DefinitionRevision, 10)
	points := []retainedSloPoint{}
	first, last := floorSliBoundary(part.start, base.Cadence).Add(base.Cadence), floorSliBoundary(part.end, base.Cadence)
	for at := first; !at.After(last); at = last {
		point, err := c.unobservedPoint(ctx, base, at)
		if err != nil {
			return retainedSloTile{}, err
		}
		points = append(points, point)
		if at.Equal(last) {
			break
		}
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
	point := retainedSloPoint{Current: operationalValue(rolling),
		Attainment: operationalAttainmentSample(interval, at.Add(-base.Cadence), at, strconv.FormatInt(c.row.DefinitionRevision, 10)),
		Burn1h:     unavailableBurn(3600, at, "cadence_mismatch"), Burn6h: unavailableBurn(21600, at, "cadence_mismatch")}
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

func (c *retainedSeriesReader) load(ctx context.Context, base slo.WindowInput, start, end time.Time) (retainedSloTile, error) {
	if c.series != nil {
		return *c.series, nil
	}
	parts, err := retainedReadParts(start, end)
	if err != nil {
		return retainedSloTile{}, err
	}
	rows, err := c.readParts(ctx, parts)
	if err != nil {
		return retainedSloTile{}, err
	}
	var combined retainedSloTile
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			return retainedSloTile{}, err
		}
		tile, err := c.loadPart(ctx, base, part, rows[index])
		c.totalSeconds += int64(part.end.Sub(part.start) / time.Second)
		if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed) {
			c.pending = true
			c.failed = c.failed || errors.Is(err, ErrDerivedSourceFailed)
			tile, err = c.unpreparedTile(base, part), nil
		} else if err == nil {
			c.readySeconds += int64(part.end.Sub(part.start) / time.Second)
		}
		if err != nil {
			return retainedSloTile{}, err
		}
		appendRetainedTile(&combined, tile)
	}
	c.series = &combined
	return combined, nil
}

func (c *retainedSeriesReader) unpreparedTile(base slo.WindowInput, part retainedReadPart) retainedSloTile {
	reason := "history_preparing"
	if c.failed {
		reason = "history_preparation_failed"
	}
	samples := unavailableOperationalSamples(part.start, part.end, base.Cadence, strconv.FormatInt(c.row.DefinitionRevision, 10), 1, reason)
	return retainedSloTile{Attainment: samples, Budget: samples, Burn1h: samples, Burn6h: samples}
}

func (c *retainedSeriesReader) loadPart(ctx context.Context, base slo.WindowInput, part retainedReadPart, stored dbgen.ReadRetainedPartsRow) (retainedSloTile, error) {
	switch {
	case !floorSliBoundary(part.end, base.Cadence).After(floorSliBoundary(part.start, base.Cadence)):
		return retainedSloTile{}, nil
	case !part.end.After(maxTime(base.ValidFrom, base.SliValidFrom, ceilSliBoundary(base.RetentionStart, base.Cadence))):
		return c.beforeValidity(ctx, base, part)
	case stored.Ready:
		return decodeRetainedTile(stored.Payload, part, strconv.FormatInt(c.row.DefinitionRevision, 10))
	case stored.Exhausted:
		c.failed = true
		return retainedSloTile{}, ErrDerivedPending
	case part.resolution == 0:
		return c.edge(ctx, base, part)
	case part.resolution == 3600 && part.end.After(c.now.Truncate(time.Hour).Add(-time.Hour)):
		// A boundary can arrive before the final hour tile is published.
		// This is a bounded exact read, not a request-triggered repair job.
		return c.edge(ctx, base, part)
	case part.resolution == 86400 && part.end.Equal(utcDayStart(c.now)):
		// The completed day can be composed from its already maintained hours
		// while its final daily reduction is being published.
		parts := make([]retainedReadPart, 24)
		for index := range parts {
			from := part.start.Add(time.Duration(index) * time.Hour)
			parts[index] = retainedReadPart{start: from, end: from.Add(time.Hour), resolution: 3600}
		}
		rows, err := c.readParts(ctx, parts)
		if err != nil {
			return retainedSloTile{}, err
		}
		var combined retainedSloTile
		for index, hour := range parts {
			tile, err := c.loadPart(ctx, base, hour, rows[index])
			if err != nil {
				return retainedSloTile{}, err
			}
			appendRetainedTile(&combined, tile)
		}
		return combined, nil
	default:
		return retainedSloTile{}, ErrDerivedPending
	}
}

// Re-reduce selected points only within display buckets. Stable-tile extrema
// survive this second selection because every contributing bucket's extrema
// participate; exact counts are never averaged from plotted values.
func reduceRetainedDisplay(samples []api.SloOperationalSample, start, end time.Time, target, metric string, buckets int) ([]api.SloOperationalSample, error) {
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
		selected, err := reduceRetainedLane(samples[first:last], from, to, target, metric)
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
	ticks := (end.Sub(start) + grid - 1) / grid
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
	// Budget/burn include the exact requested starting endpoint as well.
	if c.first == nil {
		points, err := c.points(ctx, base, start.Add(-cadence), start)
		if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed) {
			c.pending = true
			c.failed = c.failed || errors.Is(err, ErrDerivedSourceFailed)
			missing := c.unpreparedTile(base, retainedReadPart{start: start.Add(-cadence), end: start})
			samples = append(missing.Budget, samples...)
			return reduceRetainedDisplay(samples, start, end, c.row.Target, selectedMetric, min(maxBuckets, 375))
		}
		if err != nil || len(points) != 1 {
			if err == nil {
				err = slo.ErrOperationalFacts
			}
			return nil, err
		}
		c.first = &points[0]
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

func (c *retainedSeriesReader) daily(ctx context.Context, base slo.WindowInput, start, end, cutoff time.Time, id openapi_types.UUID, revision, target string, window, cadence time.Duration) (api.SloDailyComplianceSeries, api.SloDailyComplianceSummary, error) {
	series := api.SloDailyComplianceSeries{Kind: "daily_compliance", Days: []api.SloDailyComplianceDay{}}
	var summary api.SloDailyComplianceSummary
	last := utcDayStart(cutoff)
	if end.Before(last) {
		last = end
	}
	var parts []retainedReadPart
	for from := utcDayStart(start); !from.Add(24 * time.Hour).After(last); from = from.Add(24 * time.Hour) {
		parts = append(parts, retainedReadPart{start: from, end: from.Add(24 * time.Hour), resolution: 86400})
	}
	rows, err := c.readParts(ctx, parts)
	if err != nil {
		return series, summary, err
	}
	for index, part := range parts {
		var day api.SloDailyComplianceDay
		if !floorSliBoundary(part.end, cadence).After(maxTime(base.ValidFrom, base.SliValidFrom, ceilSliBoundary(base.RetentionStart, cadence))) {
			value, err := evaluateOperationalAt(ctx, base, floorSliBoundary(part.end, cadence), window)
			if err != nil {
				return series, summary, err
			}
			day = operationalComplianceDay(value, part.end, base.ValidFrom, id, revision, target, window, cadence)
		} else if rows[index].Ready {
			tile, err := decodeRetainedTile(rows[index].Payload, part, revision)
			if err != nil {
				return series, summary, err
			}
			if tile.Compliance == nil {
				// A partial day tile can have valid graph points without its
				// closing evaluation; that is unfinished compliance, not corruption.
				c.pending = true
				day = c.unpreparedDay(part, base.ValidFrom, id, revision, target, window, cadence)
			} else {
				day = *tile.Compliance
			}
		} else if !rows[index].Exhausted && part.end.Equal(utcDayStart(c.now)) {
			at := floorSliBoundary(part.end, cadence)
			points, err := c.points(ctx, base, at.Add(-cadence), at)
			if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed) {
				c.pending = true
				c.failed = c.failed || errors.Is(err, ErrDerivedSourceFailed)
				day = c.unpreparedDay(part, base.ValidFrom, id, revision, target, window, cadence)
			} else if err != nil || len(points) != 1 {
				if err == nil {
					err = slo.ErrOperationalFacts
				}
				return series, summary, err
			} else {
				day = operationalComplianceDay(retainedOperationalResult(points[0].Current), part.end, base.ValidFrom, id, revision, target, window, cadence)
			}
		} else {
			c.pending = true
			c.failed = c.failed || rows[index].Exhausted
			day = c.unpreparedDay(part, base.ValidFrom, id, revision, target, window, cadence)
		}
		if !day.DayStart.Equal(part.start) || !day.DayEnd.Equal(part.end) || day.DefinitionRevision != revision || day.SloId != id || !day.Status.Valid() {
			return series, summary, ErrDerivedFailed
		}
		series.Days = append(series.Days, day)
		switch day.Status {
		case "met":
			summary.MetDays++
		case "violated":
			summary.ViolatedDays++
		case "unknown":
			summary.UnknownDays++
		case "not_applicable":
			summary.NotApplicableDays++
		}
	}
	summary.EvaluableDays = summary.MetDays + summary.ViolatedDays
	return series, summary, nil
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
