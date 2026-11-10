package storage

import (
	"context"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

type retainedSloPoint struct {
	Current    api.SloOperationalValue  `json:"current"`
	Burn1h     api.SloBurnValue         `json:"burn_1h"`
	Burn6h     api.SloBurnValue         `json:"burn_6h"`
	Attainment api.SloOperationalSample `json:"attainment"`
}

// Attainment reports interval counts, without rolling-window receipt provenance.
func operationalAttainmentSample(value slo.WindowResult, start, end time.Time, revision string) api.SloOperationalSample {
	sample := operationalSample(value, start, end, revision)
	sample.AllowedBudget, sample.RemainingBudget, sample.RemainingFraction, sample.Burn, sample.ReceivedAt = nil, nil, nil, nil, nil
	sample.Quality.Reason = nil
	if value.Observed == 0 {
		sample.Quality.LatestReceiptAt = nil
	}
	return sample
}

func retainedRepairDurations(window, cadence time.Duration) []time.Duration {
	durations := []time.Duration{window, cadence}
	if time.Hour%cadence == 0 {
		durations = append(durations, time.Hour)
	}
	if 6*time.Hour%cadence == 0 {
		durations = append(durations, 6*time.Hour)
	}
	return durations
}

func retainedRepairSpan(window, cadence time.Duration) time.Duration {
	points := max(1, min(derivedWindowBatchLimit/len(retainedRepairDurations(window, cadence)), int(24*time.Hour/cadence)))
	return time.Duration(points) * cadence
}

func buildRetainedRepairPoints(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, source dbgen.GetSloOperationalContextRow, start, end, now time.Time) ([]retainedSloPoint, error) {
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
	values, err := evaluateDerivedWindows(ctx, q, row.SliID, row.MeasurementRevision, inputs)
	if err != nil {
		return nil, err
	}
	points := make([]retainedSloPoint, len(inputs)/len(durations))
	revision := strconv.FormatInt(row.DefinitionRevision, 10)
	for index, value := range values {
		input := inputs[index]
		point := &points[index/len(durations)]
		switch index % len(durations) {
		case 0:
			point.Current = operationalValue(value)
			applyOperationalDefinitionWindow(&point.Current, row.ActivatedAt.Time.UTC(), row.EvaluationValidFrom.Time.UTC(), row.HistoricalRequestedBy, row.HistoricalRequestedAt)
			point.Burn1h, point.Burn6h = unavailableBurn(3600, input.At, "cadence_mismatch"), unavailableBurn(21600, input.At, "cadence_mismatch")
		case 1:
			point.Attainment = operationalAttainmentSample(value, input.At.Add(-cadence), input.At, revision)
		default:
			if input.Duration == time.Hour {
				point.Burn1h = burnValue(value, 3600)
			} else {
				point.Burn6h = burnValue(value, 21600)
			}
		}
	}
	return points, nil
}
