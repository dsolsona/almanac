package storage

import (
	"context"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

// New artifacts are built for the authority captured by the caller's RR
// session. Historical reuse is authenticated separately by retained readers.
func buildMaintenanceExtremaHour(ctx context.Context, session *maintenanceConsumerSession, row dbgen.GetSloRow, base slo.WindowInput, start time.Time) (extremaLanes, error) {
	if session == nil || session.tx == nil || session.requestedRevision != row.ExclusionRevision || base.Cadence <= 0 || base.Cadence != time.Duration(row.CadenceSeconds)*time.Second {
		return extremaLanes{}, ErrMaintenanceInvalid
	}
	if err := ctx.Err(); err != nil {
		return extremaLanes{}, err
	}
	end := start.Add(time.Hour)
	// Expired intervals remain eligible: their exclusion geometry and unknown
	// event population must still pass through the shared calculator.
	if !end.After(maxTime(base.ValidFrom, base.SliValidFrom)) {
		return buildExtremaHour(ctx, dbgen.New(session.tx), row, base, start)
	}
	durations := retainedRepairDurations(base.Duration, base.Cadence)
	var inputs []slo.WindowInput
	var indices [4][]int
	for at := floorSliBoundary(start, base.Cadence).Add(base.Cadence); !at.After(end); at = at.Add(base.Cadence) {
		if len(inputs)+len(durations) > derivedWindowBatchLimit {
			return extremaLanes{}, ErrOperationalResource
		}
		for index, duration := range durations {
			lane := index
			if lane >= 2 && duration == 6*time.Hour {
				lane = 3
			}
			indices[lane] = append(indices[lane], len(inputs))
			input := base
			input.At, input.Duration = at, duration
			inputs = append(inputs, input)
		}
	}
	if len(inputs) == 0 {
		// Coarse cadences can leave an entire physical hour without endpoints.
		// The canonical helper constructs empty lanes without reading facts.
		return buildExtremaHour(ctx, dbgen.New(session.tx), row, base, start)
	}
	batch, err := session.evaluate(ctx, row, inputs, true, true)
	if err != nil {
		return extremaLanes{}, err
	}
	var lanes [4]*extremaLane
	for index, selected := range indices {
		if index == 2 && time.Hour%base.Cadence != 0 || index == 3 && 6*time.Hour%base.Cadence != 0 {
			continue
		}
		lane, err := makeMaintenanceExtremaLane(batch.input, batch.result, 0, selected, start, end, [4]string{"budget", "attainment", "burn", "burn"}[index], strconv.FormatInt(row.DefinitionRevision, 10))
		if err != nil {
			return extremaLanes{}, err
		}
		lanes[index] = &lane
	}
	return extremaLanes{Window: lanes[0], Attainment: lanes[1], Burn1h: lanes[2], Burn6h: lanes[3]}, nil
}

// Edge repair uses the same endpoint order and batch cap as canonical repair.
// Each public value retains the provenance for its own actual duration.
func buildMaintenanceRetainedRepairPoints(ctx context.Context, session *maintenanceConsumerSession, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, start, end, now time.Time) ([]retainedSloPoint, error) {
	if session == nil || session.tx == nil || session.requestedRevision != row.ExclusionRevision || row.CadenceSeconds <= 0 {
		return nil, ErrMaintenanceInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	base := slo.WindowInput{Now: now, Cadence: cadence, ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(), RetentionStart: now.Add(-sli.ObservationRetention), Target: sli.NormalizeDecimal(row.Target), FreshnessGrace: cadence + time.Duration(source.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds))}
	var inputs []slo.WindowInput
	durations := retainedRepairDurations(time.Duration(row.RollingWindowSeconds)*time.Second, cadence)
	for at := start.Add(cadence); !at.After(end); at = at.Add(cadence) {
		if len(inputs)+len(durations) > derivedWindowBatchLimit {
			return nil, ErrOperationalResource
		}
		for _, duration := range durations {
			input := base
			input.At, input.Duration = at, duration
			inputs = append(inputs, input)
		}
	}
	if len(inputs) == 0 {
		return []retainedSloPoint{}, nil
	}
	batch, err := session.evaluate(ctx, row, inputs, false, false)
	if err != nil {
		return nil, err
	}
	points := make([]retainedSloPoint, len(inputs)/len(durations))
	revision := strconv.FormatInt(row.DefinitionRevision, 10)
	for index, value := range batch.values {
		input := inputs[index]
		point := &points[index/len(durations)]
		provenance, err := batch.provenance(index)
		if err != nil {
			return nil, err
		}
		switch index % len(durations) {
		case 0:
			point.Current = operationalValue(value)
			point.Current.Quality = maintenancePublicQuality(value, point.Current.Quality)
			point.Current.CalculationProvenance = provenance
			applyOperationalDefinitionWindow(&point.Current, row.ActivatedAt.Time.UTC(), row.EvaluationValidFrom.Time.UTC(), row.HistoricalRequestedBy, row.HistoricalRequestedAt)
			point.Burn1h, point.Burn6h = unavailableBurn(3600, input.At, "cadence_mismatch"), unavailableBurn(21600, input.At, "cadence_mismatch")
		case 1:
			point.Attainment = operationalAttainmentSample(value, input.At.Add(-cadence), input.At, revision)
			point.Attainment.Quality = maintenancePublicQuality(value, point.Attainment.Quality)
			point.Attainment.CalculationProvenance = provenance
		default:
			burn := burnValue(value, int(input.Duration/time.Second))
			burn.Quality = maintenancePublicQuality(value, burn.Quality)
			burn.CalculationProvenance = provenance
			if input.Duration == time.Hour {
				point.Burn1h = burn
			} else {
				point.Burn6h = burn
			}
		}
	}
	return points, nil
}
