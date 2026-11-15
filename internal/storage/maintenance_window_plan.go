package storage

import (
	"context"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5/pgtype"
)

// Budgets are supplied by the admitted caller, never inferred from queue depth.
// This unwired planner does not choose production throughput defaults.
type maintenanceWindowLimits struct {
	MaxSourceHours, MaxRawRows, MaxFragments int
}

type maintenanceWindowRange struct{ Start, End time.Time }
type maintenancePlannedWindow struct {
	Input              slo.WindowInput
	FullStart, FullEnd time.Time
	Edges              []maintenanceWindowRange
}
type maintenanceWindowPlan struct {
	Windows     []maintenancePlannedWindow
	SourceHours []time.Time
	Edges       []maintenanceWindowRange
	Limits      maintenanceWindowLimits
}

func planMaintenanceWindows(ctx context.Context, inputs []slo.WindowInput, limits maintenanceWindowLimits) (maintenanceWindowPlan, error) {
	var empty maintenanceWindowPlan
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if len(inputs) == 0 {
		return empty, nil
	}
	if limits.MaxSourceHours < 0 || limits.MaxRawRows <= 0 || limits.MaxFragments <= 0 {
		return empty, ErrOperationalResource
	}
	if len(inputs) > derivedWindowBatchLimit {
		return empty, ErrOperationalResource
	}
	for _, input := range inputs {
		if input.Cadence < time.Duration(datasource.MinimumPollingInterval)*time.Second || input.Cadence > time.Duration(datasource.MaximumPollingInterval)*time.Second || input.Cadence%time.Second != 0 || !validMaintenanceTime(input.At.UTC()) || input.Now.IsZero() || !representedMaintenanceWindowTime(input.Now) || !floorSliBoundary(input.At, input.Cadence).Equal(input.At) || len(input.Observations) > 0 || len(input.Exclusions) > 0 {
			return empty, slo.ErrInvalidCalculation
		}
		for _, at := range []time.Time{input.ValidFrom, input.SliValidFrom, input.RetentionStart} {
			if !at.IsZero() && !representedMaintenanceWindowTime(at) {
				return empty, slo.ErrInvalidCalculation
			}
		}
	}
	// Reuse the canonical batch geometry/clock/grid validation. These placeholder
	// identities are not queried or returned as a source identity.
	if _, err := derivedWindowBatchParams(pgtype.UUID{}, 1, inputs); err != nil {
		return empty, err
	}
	plan := maintenanceWindowPlan{Limits: limits, Windows: make([]maintenancePlannedWindow, len(inputs))}
	full := make([]maintenanceWindowRange, 0, len(inputs))
	for i, input := range inputs {
		p := derivedWindowParams(pgtype.UUID{}, 1, input)
		window := maintenancePlannedWindow{Input: input, FullStart: p.FullStart.Time, FullEnd: p.FullEnd.Time}
		if window.FullStart.Before(window.FullEnd) {
			full = append(full, maintenanceWindowRange{window.FullStart, window.FullEnd})
		}
		for j := range p.EdgeStarts {
			edge := maintenanceWindowRange{p.EdgeStarts[j].Time, p.EdgeEnds[j].Time}
			window.Edges = append(window.Edges, edge)
			plan.Edges = append(plan.Edges, edge)
		}
		plan.Windows[i] = window
	}
	full = mergeMaintenanceWindowRanges(full)
	plan.Edges = mergeMaintenanceWindowRanges(plan.Edges)
	var hours int
	for _, span := range full {
		// A union of many admitted windows can exceed a signed Duration. Exact
		// microsecond differences keep the pre-allocation admission check honest.
		count := (uint64(span.End.UnixMicro()) - uint64(span.Start.UnixMicro())) / uint64(time.Hour.Microseconds())
		if count > uint64(limits.MaxSourceHours-hours) {
			return empty, ErrOperationalResource
		}
		hours += int(count)
	}
	plan.SourceHours = make([]time.Time, 0, hours)
	for _, span := range full {
		for at := span.Start; at.Before(span.End); at = at.Add(time.Hour) {
			if len(plan.SourceHours)%64 == 0 {
				if err := ctx.Err(); err != nil {
					return empty, err
				}
			}
			plan.SourceHours = append(plan.SourceHours, at)
		}
	}
	return plan, nil
}

func mergeMaintenanceWindowRanges(spans []maintenanceWindowRange) []maintenanceWindowRange {
	if len(spans) < 2 {
		return spans
	}
	slices.SortFunc(spans, func(a, b maintenanceWindowRange) int {
		if order := a.Start.Compare(b.Start); order != 0 {
			return order
		}
		return a.End.Compare(b.End)
	})
	merged := spans[:0]
	for _, span := range spans {
		if len(merged) == 0 || span.Start.After(merged[len(merged)-1].End) {
			merged = append(merged, span)
			continue
		}
		last := &merged[len(merged)-1]
		if span.End.After(last.End) {
			last.End = span.End
		}
	}
	return merged
}

func representedMaintenanceWindowTime(at time.Time) bool {
	return at.Nanosecond()%1000 == 0 && time.UnixMicro(at.UnixMicro()).Equal(at)
}
