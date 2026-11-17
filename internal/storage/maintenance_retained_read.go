package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

// Reconcile all source-ready candidates together, before rendering releases the
// RR snapshot. A changed bucket does not hide independently compatible rows.
func (r *extremaReader) reconcileMaintenanceRows(ctx context.Context, base slo.WindowInput, parts []retainedReadPart, stored map[retainedReadPart]dbgen.ReadSloExtremaPartsRow) error {
	if r.maintenance == nil {
		return nil
	}
	terminal := map[retainedReadPart]bool{}
	if len(parts) > 0 {
		params := dbgen.ReadMaintenanceTerminalExtremaTargetsParams{SloID: r.row.ID, SliID: r.row.SliID, DefinitionRevision: r.row.DefinitionRevision, MeasurementRevision: r.row.MeasurementRevision, TargetRevision: r.maintenance.requestedRevision, SourceGeneration: r.maintenance.sourceGeneration}
		for _, part := range parts {
			params.Starts = append(params.Starts, timestamp(part.start))
			kind := "extrema_hour"
			if part.resolution == 86400 {
				kind = "extrema_day"
			}
			params.Kinds = append(params.Kinds, kind)
		}
		failed, err := r.q.ReadMaintenanceTerminalExtremaTargets(ctx, params)
		if err != nil {
			return err
		}
		for _, job := range failed {
			duration, resolution := time.Hour, int32(3600)
			if job.Kind == "extrema_day" {
				duration, resolution = 24*time.Hour, 86400
			}
			terminal[retainedReadPart{start: job.HourStart.Time.UTC(), end: job.HourStart.Time.UTC().Add(duration), resolution: resolution}] = true
		}
	}
	var inputs []maintenanceRetainedReuseInput
	var selected []retainedReadPart
	for _, part := range parts {
		row := stored[part]
		if !row.Ready {
			if terminal[part] {
				row.Failing = true
				stored[part] = row
			}
			continue
		}
		var lanes extremaLanes
		if !row.RetentionStart.Valid || unmarshalReceiptMetadata(row.Lanes, &lanes) != nil {
			return ErrDerivedFailed
		}
		input := base
		input.RetentionStart = row.RetentionStart.Time.UTC()
		inputs = append(inputs, maintenanceRetainedReuseInput{Base: input, Lanes: lanes, Start: part.start, End: part.end, Row: maintenanceRetainedRowAuthority{Scope: maintenancePreparationScope{SloID: r.row.ID, SliID: r.row.SliID, DefinitionRevision: r.row.DefinitionRevision, MeasurementRevision: r.row.MeasurementRevision}, SourceGeneration: row.SourceGeneration, RequestedRevision: row.ExclusionRevision}})
		selected = append(selected, part)
	}
	if len(inputs) == 0 {
		return nil
	}
	results, err := reconcileMaintenanceRetainedBatch(ctx, r.maintenance.tx, inputs, r.maintenance.requestedRevision, r.maintenance.maxRetainedProofs)
	if err != nil && !errors.Is(err, ErrDerivedPending) {
		return err
	}
	if len(results) != len(selected) {
		return ErrDerivedFailed
	}
	for i, result := range results {
		part := selected[i]
		row := stored[part]
		if result.NeedsCalculation {
			row.Ready = false
			if terminal[part] {
				row.Failing = true
			} else if len(result.Preparation) > 0 {
				r.maintenance.preparation = append(r.maintenance.preparation, result.Preparation...)
			} else {
				kind := "extrema_hour"
				if part.resolution == 86400 {
					kind = "extrema_day"
				}
				r.maintenance.preparation = append(r.maintenance.preparation, maintenanceEvaluationPreparation{Kind: kind, RequestedRevision: r.maintenance.requestedRevision, Hour: maintenanceHourKey{SloID: r.row.ID, SliID: r.row.SliID, DefinitionRevision: r.row.DefinitionRevision, MeasurementRevision: r.row.MeasurementRevision, SourceGeneration: r.maintenance.sourceGeneration, HourStart: part.start}})
			}
		} else {
			row.Lanes, err = marshalReceiptMetadata(result.Lanes)
			if err != nil {
				return err
			}
			// Only this detached response container gains requested authority.
			// Every mark retains its original computed/source/build identities.
			row.ExclusionRevision = r.maintenance.requestedRevision
		}
		stored[part] = row
	}
	return nil
}
