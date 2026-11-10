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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type extremaReader struct {
	retainedSeriesReader
	liveEndpoints int
	rowsRead      int
}

// Preload the shared display assembly before it releases the protected snapshot.
func (r *extremaReader) prepare(ctx context.Context, base slo.WindowInput, bound operationalBounds) error {
	plan, err := planExtremaRead(base, bound)
	if err != nil {
		return err
	}
	return r.preparePlan(ctx, base, bound, plan)
}

func (r *extremaReader) preparePlan(ctx context.Context, base slo.WindowInput, bound operationalBounds, plan extremaReadPlan) error {
	var err error
	parts := make([]retainedReadPart, 0, len(plan.stored))
	valid := maxTime(base.ValidFrom, base.SliValidFrom)
	unobservedThrough := maxTime(valid, ceilSliBoundary(base.RetentionStart, base.Cadence))
	for _, part := range plan.stored {
		if part.end.After(valid) {
			parts = append(parts, part)
		}
	}
	stored := make(map[retainedReadPart]dbgen.ReadSloExtremaPartsRow, len(parts))
	if len(parts) > 0 {
		graph := make(map[retainedReadPart]bool, len(plan.parts))
		for _, part := range plan.parts {
			graph[part] = true
		}
		coarseFrom := valid
		if !plan.partialDay.IsZero() {
			coarseFrom = plan.partialDay.Add(24 * time.Hour)
		}
		params := dbgen.ReadSloExtremaPartsParams{SloID: r.row.ID, DefinitionRevision: r.row.DefinitionRevision, MeasurementRevision: r.row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, CoarseFrom: timestamp(coarseFrom)}
		for _, part := range parts {
			params.Starts = append(params.Starts, timestamp(part.start))
			params.Ends = append(params.Ends, timestamp(part.end))
			params.Resolutions = append(params.Resolutions, part.resolution)
			params.Graph = append(params.Graph, graph[part])
		}
		rows, err := r.q.ReadSloExtremaParts(ctx, params)
		if err != nil {
			return err
		}
		if len(rows) != len(parts) {
			return ErrDerivedFailed
		}
		for index, row := range rows {
			part := parts[index]
			if row.Ordinal != int32(index+1) || !row.RangeStart.Valid || !row.RangeStart.Time.Equal(part.start) ||
				!row.RangeEnd.Valid || !row.RangeEnd.Time.Equal(part.end) || row.Resolution != part.resolution {
				return ErrDerivedFailed
			}
			if row.PayloadBytes > 20<<20 {
				return ErrOperationalResource
			}
			stored[part] = row
		}
		r.rowsRead = len(rows)
	}
	r.totalSeconds = int64(bound.end.Sub(bound.start) / time.Second)
	r.series = new(retainedSloTile)
	var partial retainedSloTile
	for _, part := range plan.parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The first partial day displays only endpoints produced after activation.
		if !plan.partialDay.IsZero() && !part.start.Before(plan.partialDay) && !part.end.After(ceilSliBoundary(valid, base.Cadence)) {
			r.readySeconds += int64(part.end.Sub(part.start) / time.Second)
			continue
		}
		var tile retainedSloTile
		row := stored[part]
		switch {
		case !floorSliBoundary(part.end, base.Cadence).After(floorSliBoundary(part.start, base.Cadence)):
		case row.Ready:
			tile, err = r.decode(ctx, base, part, row)
		case !floorSliBoundary(part.end, base.Cadence).After(unobservedThrough):
			tile, err = r.beforeValidity(ctx, base, part)
		case row.Failing:
			r.failed = true
			err = ErrDerivedPending
		case part.resolution == 0 || part.resolution == 3600 && part.end.After(r.now.Truncate(time.Hour).Add(-time.Hour)):
			tile, err = r.live(ctx, base, part)
		default:
			err = ErrDerivedPending
		}
		if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed) {
			r.pending = true
			r.failed = r.failed || errors.Is(err, ErrDerivedSourceFailed)
			tile, err = r.unpreparedTile(base, part), nil
		} else if err == nil {
			r.readySeconds += int64(part.end.Sub(part.start) / time.Second)
		}
		if err != nil {
			return err
		}
		// Preserve the retained reader's pre-activation points on live edges.
		if part.start.Before(valid) && part.end.After(valid) && (row.Ready || !plan.partialDay.IsZero()) {
			lanes := [4]*[]api.SloOperationalSample{&tile.Budget, &tile.Attainment, &tile.Burn1h, &tile.Burn6h}
			for _, lane := range lanes {
				first := 0
				for first < len(*lane) && !(*lane)[first].EvaluationAt.After(ceilSliBoundary(valid, base.Cadence)) {
					first++
				}
				*lane = (*lane)[first:]
			}
			tile, err = compactRetainedTile(tile, part.start, part.end, base.Target)
			if err != nil {
				return err
			}
		}
		if !plan.partialDay.IsZero() && !part.start.Before(plan.partialDay) && !part.end.After(plan.partialDay.Add(24*time.Hour)) {
			appendRetainedTile(&partial, tile)
			if part.end.Equal(plan.partialDay.Add(24 * time.Hour)) {
				tile, err = compactRetainedTile(partial, plan.partialDay, part.end, base.Target)
				if err != nil {
					return err
				}
				appendRetainedTile(r.series, tile)
			}
		} else {
			appendRetainedTile(r.series, tile)
		}
	}
	first, err := r.livePoints(ctx, base, bound.start.Add(-base.Cadence), bound.start)
	if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed) {
		r.pending = true
		r.failed = r.failed || errors.Is(err, ErrDerivedSourceFailed)
	} else if err != nil {
		return err
	} else if len(first) != 1 {
		return slo.ErrOperationalFacts
	} else {
		r.first = &first[0]
	}
	days := api.SloDailyComplianceSeries{Kind: "daily_compliance", Days: []api.SloDailyComplianceDay{}}
	revision := strconv.FormatInt(r.row.DefinitionRevision, 10)
	for _, part := range plan.days {
		var day api.SloDailyComplianceDay
		row := stored[part]
		if !floorSliBoundary(part.end, base.Cadence).After(valid) || !row.Ready && !floorSliBoundary(part.end, base.Cadence).After(unobservedThrough) {
			value, err := evaluateOperationalAt(ctx, base, floorSliBoundary(part.end, base.Cadence), base.Duration)
			if err != nil {
				return err
			}
			day = operationalComplianceDay(value, part.end, base.ValidFrom, r.row.ID.Bytes, revision, base.Target, base.Duration, base.Cadence)
		} else if row.Ready {
			var lanes extremaLanes
			if json.Unmarshal(row.Lanes, &lanes) != nil || lanes.Compliance == nil || !lanes.Compliance.DayEnd.Equal(part.end) ||
				!lanes.Compliance.Mark.At.Equal(floorSliBoundary(part.end, base.Cadence)) || !row.RetentionStart.Valid {
				return ErrDerivedFailed
			}
			input := base
			input.RetentionStart = row.RetentionStart.Time.UTC()
			value, err := lanes.Compliance.Mark.result(ctx, input)
			if err != nil {
				return err
			}
			day = operationalComplianceDay(value, part.end, base.ValidFrom, r.row.ID.Bytes, revision, base.Target, base.Duration, base.Cadence)
		} else {
			r.pending = true
			r.failed = r.failed || row.Failing
			day = r.unpreparedDay(part, base.ValidFrom, r.row.ID.Bytes, revision, base.Target, base.Duration, base.Cadence)
		}
		days.Days = append(days.Days, day)
		switch day.Status {
		case "met":
			r.daySummary.MetDays++
		case "violated":
			r.daySummary.ViolatedDays++
		case "unknown":
			r.daySummary.UnknownDays++
		case "not_applicable":
			r.daySummary.NotApplicableDays++
		}
	}
	r.daySummary.EvaluableDays = r.daySummary.MetDays + r.daySummary.ViolatedDays
	r.days = &days
	trace.SpanFromContext(ctx).SetAttributes(attribute.Int("almanac.extrema.rows", r.rowsRead), attribute.Int("almanac.extrema.live_endpoints", r.liveEndpoints))
	return nil
}

func (r *extremaReader) decode(ctx context.Context, base slo.WindowInput, part retainedReadPart, row dbgen.ReadSloExtremaPartsRow) (retainedSloTile, error) {
	var lanes extremaLanes
	if json.Unmarshal(row.Lanes, &lanes) != nil || !row.RetentionStart.Valid || lanes.Window == nil || lanes.Attainment == nil ||
		(time.Hour%base.Cadence == 0) != (lanes.Burn1h != nil) || (6*time.Hour%base.Cadence == 0) != (lanes.Burn6h != nil) {
		return retainedSloTile{}, ErrDerivedFailed
	}
	if floorSliBoundary(part.end, base.Cadence).After(floorSliBoundary(part.start, base.Cadence)) && len(lanes.Window.Marks) == 0 {
		return retainedSloTile{}, ErrDerivedFailed
	}
	base.RetentionStart = row.RetentionStart.Time.UTC()
	return lanes.tile(ctx, base, part.start, part.end, strconv.FormatInt(r.row.DefinitionRevision, 10))
}

func (r *extremaReader) livePoints(ctx context.Context, base slo.WindowInput, start, end time.Time) ([]retainedSloPoint, error) {
	if !start.Before(end) {
		return []retainedSloPoint{}, nil
	}
	r.liveEndpoints += int(end.Sub(start) / base.Cadence)
	if r.liveEndpoints > 7200 {
		return nil, ErrOperationalResource
	}
	var points []retainedSloPoint
	for start.Before(end) {
		through := start.Add(retainedRepairSpan(base.Duration, base.Cadence))
		if end.Before(through) {
			through = end
		}
		values, err := buildRetainedRepairPoints(ctx, r.q, r.row, r.source, start, through, r.now)
		if err != nil {
			return nil, err
		}
		points = append(points, values...)
		start = through
	}
	return points, nil
}

func (r *extremaReader) live(ctx context.Context, base slo.WindowInput, part retainedReadPart) (retainedSloTile, error) {
	points, err := r.livePoints(ctx, base, floorSliBoundary(part.start, base.Cadence), floorSliBoundary(part.end, base.Cadence))
	if err != nil {
		return retainedSloTile{}, err
	}
	return compactRetainedPoints(points, part.start, part.end, r.row.Target, strconv.FormatInt(r.row.DefinitionRevision, 10))
}

type extremaReadPlan struct {
	parts      []retainedReadPart
	days       []retainedReadPart
	stored     []retainedReadPart
	partialDay time.Time
}

func planExtremaRead(input slo.WindowInput, bound operationalBounds) (extremaReadPlan, error) {
	parts, err := retainedReadParts(bound.start, bound.end)
	if err != nil || input.Cadence <= 0 {
		return extremaReadPlan{}, ErrOperationalRange
	}
	valid := maxTime(input.ValidFrom, input.SliValidFrom)
	plan := extremaReadPlan{}
	var graph []retainedReadPart
	for _, part := range parts {
		if part.resolution == 86400 && part.start.Before(valid) && floorSliBoundary(part.end, input.Cadence).After(ceilSliBoundary(valid, input.Cadence)) {
			// Hourly marks preserve the first eligible endpoint even when the
			// daily reducer discarded transitions before activation.
			for start := part.start; start.Before(part.end); start = start.Add(time.Hour) {
				graph = append(graph, retainedReadPart{start: start, end: start.Add(time.Hour), resolution: 3600})
			}
			plan.partialDay = part.start
		} else {
			graph = append(graph, part)
		}
	}
	parts = graph
	plan.parts = parts
	last := utcDayStart(bound.effectiveAt)
	if bound.end.Before(last) {
		last = bound.end
	}
	for from := utcDayStart(bound.start); !from.Add(24 * time.Hour).After(last); from = from.Add(24 * time.Hour) {
		plan.days = append(plan.days, retainedReadPart{start: from, end: from.Add(24 * time.Hour), resolution: 86400})
	}
	unique := make(map[retainedReadPart]bool)
	for _, day := range plan.days {
		if day.end.After(valid) {
			unique[day] = true
		}
	}
	for _, part := range parts {
		if part.resolution != 0 && part.end.After(valid) {
			unique[part] = true
		}
	}
	if bound.end.Sub(bound.start) > 28*24*time.Hour {
		// A few boundary hours are cheaper to evaluate live than a year read
		// exceeding its row budget; the complete days remain durable inputs.
		for index, part := range parts {
			if len(unique) <= 400 {
				break
			}
			if part.resolution == 3600 && unique[part] {
				delete(unique, part)
				parts[index].resolution = 0
			}
		}
	}
	seen := make(map[retainedReadPart]bool)
	for _, group := range [][]retainedReadPart{parts, plan.days} {
		for _, part := range group {
			if part.resolution != 0 && part.end.After(valid) && !seen[part] {
				plan.stored = append(plan.stored, part)
				seen[part] = true
			}
		}
	}
	live := 1
	for _, part := range parts {
		if part.resolution == 0 {
			live += int(floorSliBoundary(part.end, input.Cadence).Sub(floorSliBoundary(part.start, input.Cadence)) / input.Cadence)
		}
	}
	if live > 7200 {
		return extremaReadPlan{}, ErrOperationalResource
	}
	return plan, nil
}
