package storage

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

// Parts have already passed source readiness and requested-authority
// reconciliation in the caller's snapshot. The row keeps its original source
// generation; individual marks keep their actual computed identity and proof.
// Supply all 24 hours, including canonical pre-validity and empty coarse hours.
type maintenanceExtremaDayPart struct {
	Start, RetentionStart time.Time
	Lanes                 extremaLanes
	Row                   maintenanceRetainedRowAuthority
}

func composeMaintenanceExtremaDay(ctx context.Context, row dbgen.GetSloRow, base slo.WindowInput, end time.Time, parts []maintenanceExtremaDayPart) (extremaLanes, error) {
	if err := ctx.Err(); err != nil {
		return extremaLanes{}, err
	}
	if end.IsZero() || !end.Equal(end.UTC().Truncate(24*time.Hour)) || base.Cadence <= 0 || base.Cadence != time.Duration(row.CadenceSeconds)*time.Second || base.Duration <= 0 || base.Duration != time.Duration(row.RollingWindowSeconds)*time.Second || base.Target != sli.NormalizeDecimal(row.Target) || row.ExclusionRevision < 0 || len(parts) > 24 {
		return extremaLanes{}, ErrMaintenanceInvalid
	}
	if len(parts) != 24 {
		return extremaLanes{}, ErrDerivedPending
	}
	start := end.Add(-24 * time.Hour)
	scope := maintenancePreparationScope{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision}
	var samples [4][]api.SloOperationalSample
	var originals [4]map[time.Time]extremaMark
	for i := range originals {
		originals[i] = make(map[time.Time]extremaMark)
	}
	var last *extremaMark
	revision := strconv.FormatInt(row.DefinitionRevision, 10)
	for partIndex, part := range parts {
		if err := ctx.Err(); err != nil {
			return extremaLanes{}, err
		}
		hour := start.Add(time.Duration(partIndex) * time.Hour)
		hourEnd := hour.Add(time.Hour)
		if !part.Start.Equal(hour) || part.RetentionStart.IsZero() || part.Row.Scope != scope || part.Row.RequestedRevision != row.ExclusionRevision || part.Row.SourceGeneration < 0 {
			return extremaLanes{}, ErrDerivedFailed
		}
		input := base
		input.RetentionStart = part.RetentionStart
		first, final := floorSliBoundary(hour, base.Cadence).Add(base.Cadence), floorSliBoundary(hourEnd, base.Cadence)
		for laneIndex, lane := range part.Lanes.list() {
			duration := [4]time.Duration{base.Duration, base.Cadence, time.Hour, 6 * time.Hour}[laneIndex]
			if duration%base.Cadence != 0 {
				if lane != nil {
					return extremaLanes{}, ErrDerivedFailed
				}
				continue
			}
			if lane == nil {
				return extremaLanes{}, ErrDerivedPending
			}
			if len(lane.Marks) > sloOperationalSeriesLimit {
				return extremaLanes{}, ErrDerivedFailed
			}
			if first.After(final) {
				if len(lane.Marks) != 0 {
					return extremaLanes{}, ErrDerivedFailed
				}
			} else if len(lane.Marks) == 0 || !lane.Marks[0].At.Equal(first) || !lane.Marks[len(lane.Marks)-1].At.Equal(final) {
				return extremaLanes{}, ErrDerivedPending
			}
			window := input
			window.Duration = duration
			for _, mark := range lane.Marks {
				if !mark.At.Equal(floorSliBoundary(mark.At, base.Cadence)) {
					return extremaLanes{}, ErrDerivedFailed
				}
				if err := mark.validateMaintenanceRowAuthority(window, part.Row); err != nil {
					return extremaLanes{}, err
				}
			}
		}
		if compliance := part.Lanes.Compliance; compliance != nil {
			if !compliance.DayEnd.Equal(hourEnd) || !compliance.Mark.At.Equal(final) {
				return extremaLanes{}, ErrDerivedFailed
			}
			if err := compliance.Mark.validateMaintenanceRowAuthority(input, part.Row); err != nil {
				return extremaLanes{}, err
			}
			if _, err := (extremaLane{Marks: []extremaMark{compliance.Mark}}).samples(ctx, input, hour, hourEnd, revision, "budget"); err != nil {
				return extremaLanes{}, err
			}
		}
		tile, err := part.Lanes.tile(ctx, input, hour, hourEnd, revision)
		if err != nil {
			return extremaLanes{}, err
		}
		for laneIndex, values := range [4][]api.SloOperationalSample{tile.Budget, tile.Attainment, tile.Burn1h, tile.Burn6h} {
			samples[laneIndex] = append(samples[laneIndex], values...)
			if lane := part.Lanes.list()[laneIndex]; lane != nil {
				for _, mark := range lane.Marks {
					if mark.RetentionStart.IsZero() {
						mark.RetentionStart = part.RetentionStart
					}
					originals[laneIndex][mark.At.UTC()] = mark
					if laneIndex == 0 && (last == nil || mark.At.After(last.At)) {
						last = new(mark)
					}
				}
			}
		}
	}
	lanes, err := makeExtremaLanes(samples, start, end, base.Target, base.Cadence)
	if err != nil {
		return extremaLanes{}, err
	}
	for laneIndex, lane := range lanes.list() {
		if lane == nil {
			continue
		}
		for i, selected := range lane.Marks {
			mark, ok := originals[laneIndex][selected.At.UTC()]
			if !ok {
				return extremaLanes{}, ErrDerivedFailed
			}
			mark.Labels = slices.Clone(selected.Labels)
			lane.Marks[i] = mark
		}
	}
	if last == nil || !last.At.Equal(floorSliBoundary(end, base.Cadence)) {
		return extremaLanes{}, ErrDerivedPending
	}
	last.Labels = slices.Clone(last.Labels)
	lanes.Compliance = &extremaCompliance{DayEnd: end, Mark: *last}
	// Own only selected output evidence, including compliance: hourly inputs
	// may be reused or released independently after composition.
	return cloneMaintenanceRetainedLanes(lanes), nil
}
