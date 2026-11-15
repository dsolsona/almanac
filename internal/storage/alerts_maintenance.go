package storage

import (
	"context"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func readMaintenanceAlertWindows(ctx context.Context, tx pgx.Tx, claim alertClaim, input alert.Input, windows []slo.WindowInput, alertLimits alert.Limits, limits maintenanceWindowLimits) (alertPreparedEvidence, error) {
	out := alertPreparedEvidence{Cutoff: input.Snapshot.Cutoff, Snapshot: input.Snapshot}
	q := dbgen.New(tx)
	sloID, valid := canonicalUUID(input.Snapshot.SLOID)
	if !valid {
		return out, ErrAlertInvalid
	}
	row, err := q.GetSlo(ctx, sloID)
	if err != nil {
		return out, err
	}
	if _, err := planMaintenanceWindows(ctx, windows, limits); err != nil {
		return out, err
	}
	params, err := derivedWindowBatchParams(row.SliID, row.MeasurementRevision, windows)
	if err != nil {
		return out, err
	}
	sources, err := q.ReadAlertMaintenanceSourceFailures(ctx, dbgen.ReadAlertMaintenanceSourceFailuresParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision, Now: params.Now, Starts: params.FullStarts, Ends: params.FullEnds})
	if err != nil {
		return out, err
	}
	if len(sources) != len(windows) {
		return out, slo.ErrOperationalFacts
	}
	failed := make([]bool, len(windows))
	var usable []slo.WindowInput
	for i, source := range sources {
		if source.Ordinal != int32(i+1) {
			return out, slo.ErrOperationalFacts
		}
		failed[i] = source.Exhausted
		if !source.Exhausted {
			usable = append(usable, windows[i])
		}
	}
	in := maintenanceEvaluationInput{SloID: row.ID, SliID: row.SliID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, Limits: limits, Requests: []maintenanceMaskWindows{{RequestedRevision: input.Snapshot.ExclusionRevision, Windows: usable}}}
	policyID, valid := canonicalUUID(claim.PolicyID)
	if !valid {
		return out, ErrAlertInvalid
	}
	cursor, err := q.ReadAlertCursor(ctx, dbgen.ReadAlertCursorParams{PolicyID: policyID, Revision: claim.Revision})
	if err != nil {
		return out, err
	}
	if cursor.OpenOccurrenceID.Valid {
		stored, err := q.ReadAlertOccurrence(ctx, cursor.OpenOccurrenceID)
		if err != nil {
			return out, err
		}
		occurrence, err := decodeAlertOccurrence(stored)
		if err != nil {
			return out, err
		}
		if occurrence.Occurrence.PolicyID != claim.PolicyID || occurrence.Occurrence.Revision != claim.Revision {
			return out, ErrAlertStale
		}
		baseline := occurrence.Occurrence.LatestBreachingSnapshot.ExclusionRevision
		if baseline != input.Snapshot.ExclusionRevision {
			in.Requests = append(in.Requests, maintenanceMaskWindows{RequestedRevision: baseline, Windows: usable})
		}
	}
	out.MaintenanceInput = in
	var calculated maintenanceEvaluationResult
	if len(usable) > 0 {
		calculated, err = evaluateMaintenanceWindows(ctx, tx, in)
	} else {
		calculated.SourceGeneration = input.Snapshot.SourceGeneration
		for _, request := range in.Requests {
			calculated.Masks = append(calculated.Masks, maintenanceEvaluatedMask{RequestedRevision: request.RequestedRevision})
		}
	}
	out.Preparation = calculated.Preparation
	if err != nil {
		return out, err
	}
	if calculated.SourceGeneration != input.Snapshot.SourceGeneration || len(calculated.Masks) != len(in.Requests) {
		return out, ErrAlertStale
	}
	convert := func(mask maintenanceEvaluatedMask) ([]alert.WindowEvidence, error) {
		if len(mask.Windows) != len(usable) || len(mask.Unavailable) != len(usable) {
			return nil, alert.ErrEvidence
		}
		identity := input.Snapshot
		identity.ExclusionRevision = mask.RequestedRevision
		converted := make([]alert.WindowEvidence, len(windows))
		position := 0
		for i, window := range windows {
			if failed[i] {
				converted[i] = alert.WindowEvidence{Snapshot: identity, Start: identity.Cutoff.Add(-window.Duration), End: identity.Cutoff, Quality: alert.CalculationFailed, Reason: "calculation_failed"}
				continue
			}
			value := mask.Windows[position]
			provenancePosition := position
			position++
			if reason := mask.Unavailable[provenancePosition]; reason != "" {
				quality := alert.Missing
				switch reason {
				case "maintenance_history_unavailable":
				case "maintenance_preparation_failed":
					quality = alert.CalculationFailed
				default:
					return nil, alert.ErrEvidence
				}
				converted[i] = alert.WindowEvidence{Snapshot: identity, Start: identity.Cutoff.Add(-window.Duration), End: identity.Cutoff, Quality: quality, Reason: reason}
				continue
			}
			converted[i], err = canonicalAlertWindow(identity, claim.ScheduledAt, window.FreshnessGrace, window.Duration, value)
			if err != nil {
				return nil, err
			}
			converted[i].Maintenance, err = alertMaintenanceProvenance(mask, window, provenancePosition)
			if err != nil {
				return nil, err
			}
		}
		return converted, nil
	}
	input.Windows, err = convert(calculated.Masks[0])
	if err != nil {
		return out, err
	}
	// The pure kernel requires a recovery comparison only for a clear current
	// decision. Both populations were nevertheless admitted and read together.
	current, err := alert.Evaluate(input, alertLimits)
	if err != nil {
		return out, err
	}
	if current.Truth == alert.Clear && len(calculated.Masks) == 2 {
		baseline, err := convert(calculated.Masks[1])
		if err != nil {
			return out, err
		}
		input.RecoveryBaseline = &alert.RecoveryInput{ExclusionRevision: calculated.Masks[1].RequestedRevision, Windows: baseline}
	}
	if _, err := alert.Evaluate(input, alertLimits); err != nil {
		return out, err
	}
	out.Input, out.Calculations = input, calculated.Masks
	return out, nil
}

func alertMaintenanceProof(p maintenanceCompatibilityProof) alert.MaintenanceProof {
	return alert.MaintenanceProof{Kind: p.Kind, ComputedRevision: p.ComputedRevision, RequestedRevision: p.RequestedRevision, IndexedThrough: p.IndexedThrough, Start: p.Start, End: p.End, ComparisonID: p.ComparisonID}
}

// This conversion follows authenticated shared-calculator reads. It preserves
// actual identities rather than assigning the requested authority to old math.
func alertMaintenanceProvenance(mask maintenanceEvaluatedMask, window slo.WindowInput, position int) (*alert.MaintenanceEvidence, error) {
	if mask.Computed.Revision != mask.RequestedRevision && len(mask.Proofs) == 0 {
		// No eligible interval requires no mask at all. There is no numerical
		// population or compatibility claim to record for that warmup window.
		start := window.At.Add(-window.Duration)
		for _, valid := range []time.Time{window.ValidFrom, window.SliValidFrom} {
			if valid.After(start) {
				start = valid
			}
		}
		if !ceilSliBoundary(start, window.Cadence).Before(window.At) {
			return nil, nil
		}
		return nil, alert.ErrEvidence
	}
	m := &alert.MaintenanceEvidence{RequestedRevision: mask.RequestedRevision, ComputedRevision: mask.Computed.Revision}
	if mask.Computed.BuildID.Valid {
		m.BuildID = mask.Computed.BuildID.String()
	}
	if len(mask.Proofs) > 0 {
		if len(mask.Proofs) != len(mask.Windows) || position < 0 || position >= len(mask.Proofs) {
			return nil, alert.ErrEvidence
		}
		m.Proof = new(alertMaintenanceProof(mask.Proofs[position]))
	}
	bounds := derivedWindowParams(pgtype.UUID{}, 1, window)
	for _, h := range mask.ReusedHours {
		if h.Key.HourStart.Before(bounds.FullStart.Time) || !h.Key.HourStart.Before(bounds.FullEnd.Time) {
			continue
		}
		m.ReusedHours = append(m.ReusedHours, alert.MaintenanceHourEvidence{SLOID: h.Key.SloID.String(), SLIID: h.Key.SliID.String(), BuildID: h.Key.BuildID.String(), SLORevision: h.Key.DefinitionRevision, SLIRevision: h.Key.MeasurementRevision, SourceGeneration: h.Key.SourceGeneration, ComputedRevision: h.ComputedRevision, HourStart: h.Key.HourStart, Proof: alertMaintenanceProof(h.Proof)})
	}
	return m, nil
}
