package storage

import (
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
)

// Select exact extrema before attaching replay inputs. Indices identify one
// duration/metric lane inside the calculator's original admitted window order.
func makeMaintenanceExtremaLane(in maintenanceEvaluationInput, result maintenanceEvaluationResult, maskIndex int, indices []int, start, end time.Time, metric, revision string) (extremaLane, error) {
	if len(indices) > derivedWindowBatchLimit {
		return extremaLane{}, ErrOperationalResource
	}
	if maskIndex < 0 || maskIndex >= len(in.Requests) || len(result.Masks) != len(in.Requests) || len(indices) == 0 || revision != strconv.FormatInt(in.DefinitionRevision, 10) {
		return extremaLane{}, ErrDerivedFailed
	}
	mask := result.Masks[maskIndex]
	windows := in.Requests[maskIndex].Windows
	if len(mask.Windows) != len(windows) || len(mask.Unavailable) != len(windows) {
		return extremaLane{}, ErrDerivedFailed
	}
	values := make([]slo.WindowResult, 0, len(indices))
	positions := make(map[int64]int, len(indices))
	var base slo.WindowInput
	for i, index := range indices {
		if index < 0 || index >= len(windows) {
			return extremaLane{}, ErrDerivedFailed
		}
		window := windows[index]
		if i == 0 {
			base = window
		} else if window.Target != base.Target || window.Duration != base.Duration || window.Cadence != base.Cadence || !window.At.After(values[i-1].End) {
			return extremaLane{}, ErrDerivedFailed
		}
		value := mask.Windows[index]
		if reason := mask.Unavailable[index]; reason != "" {
			var err error
			value, err = unavailableMaintenanceWindow(window, reason)
			if err != nil {
				return extremaLane{}, err
			}
		}
		if !value.End.Equal(window.At) || !value.RequestedStart.Equal(window.At.Add(-window.Duration)) {
			return extremaLane{}, ErrDerivedFailed
		}
		positions[window.At.UnixMicro()] = index
		values = append(values, value)
	}
	lane, err := makeExtremaSummaryLane(values, start, end, base.Target, metric, revision)
	if err != nil {
		return extremaLane{}, err
	}
	for i, mark := range lane.Marks {
		index, found := positions[mark.At.UnixMicro()]
		if !found {
			return extremaLane{}, ErrDerivedFailed
		}
		lane.Marks[i], err = mark.withMaintenanceInputs(in, result, maskIndex, index)
		if err != nil {
			return extremaLane{}, err
		}
	}
	return lane, nil
}
