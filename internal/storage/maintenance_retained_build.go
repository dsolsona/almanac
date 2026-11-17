package storage

import (
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
)

// Attach replay inputs only after a reducer selected this actual endpoint.
// The source result is authenticated by the shared RR calculator; this method
// copies its facts/provenance and never reads expired observations again.
func (mark extremaMark) withMaintenanceInputs(in maintenanceEvaluationInput, result maintenanceEvaluationResult, maskIndex, windowIndex int) (extremaMark, error) {
	if maskIndex < 0 || maskIndex >= len(in.Requests) || len(result.Masks) != len(in.Requests) || windowIndex < 0 || windowIndex >= len(in.Requests[maskIndex].Windows) {
		return extremaMark{}, ErrDerivedFailed
	}
	mask := result.Masks[maskIndex]
	windows := in.Requests[maskIndex].Windows
	window := windows[windowIndex]
	if len(mask.Windows) != len(windows) || len(mask.Unavailable) != len(windows) || mask.RequestedRevision != in.Requests[maskIndex].RequestedRevision || !window.At.Equal(mark.At) {
		return extremaMark{}, ErrDerivedFailed
	}
	mark.Labels = slices.Clone(mark.Labels)
	if reason := mask.Unavailable[windowIndex]; reason != "" {
		value, err := unavailableMaintenanceWindow(window, reason)
		if err != nil {
			return extremaMark{}, err
		}
		m := &retainedMaintenanceInputs{Version: 1, Scope: maintenancePreparationScope{SloID: in.SloID, SliID: in.SliID, DefinitionRevision: in.DefinitionRevision, MeasurementRevision: in.MeasurementRevision}, SourceGeneration: result.SourceGeneration, RequestedRevision: mask.RequestedRevision, ComputedRevision: mask.Computed.Revision, UnavailableReason: reason}
		if mask.Computed.BuildID.Valid {
			m.BuildID = mask.Computed.BuildID.String()
		}
		if err := m.validate(window); err != nil {
			return extremaMark{}, err
		}
		mark.Maintenance, mark.Good, mark.Total = m, "0", "0"
		mark.Observed, mark.KnownEmpty, mark.ZeroTraffic = 0, 0, 0
		mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = nil, nil, nil
		sample := operationalSample(value, window.At.Add(-window.Duration), window.At, "")
		mark.State, mark.Quality, mark.Freshness = sample.State, sample.Quality.State, sample.Quality.Freshness
		return mark, nil
	}
	value := mask.Windows[windowIndex].WithValues()
	if !value.End.Equal(mark.At) || !value.RequestedStart.Equal(mark.At.Add(-window.Duration)) {
		return extremaMark{}, ErrDerivedFailed
	}
	if value.Expected == 0 && value.Excluded == 0 && value.Performance == "not_applicable" {
		// There was no eligible time and therefore no mask population to label.
		mark.Maintenance = nil
		return mark, nil
	}
	m := &retainedMaintenanceInputs{Version: 1, Scope: maintenancePreparationScope{SloID: in.SloID, SliID: in.SliID, DefinitionRevision: in.DefinitionRevision, MeasurementRevision: in.MeasurementRevision}, SourceGeneration: result.SourceGeneration, ComputedRevision: mask.Computed.Revision, RequestedRevision: mask.RequestedRevision}
	if mask.Computed.BuildID.Valid {
		m.BuildID = mask.Computed.BuildID.String()
	}
	if len(mask.Proofs) > 0 {
		if len(mask.Proofs) != len(windows) {
			return extremaMark{}, ErrDerivedFailed
		}
		m.Proof = new(mask.Proofs[windowIndex])
	}
	if mask.Computed.Revision > 0 {
		if !in.RetainFacts || len(mask.Facts) != len(windows) || mask.Facts[windowIndex].Coverage == nil {
			return extremaMark{}, ErrDerivedFailed
		}
		facts := ownMaintenanceWindowFacts(mask.Facts[windowIndex].Totals, *mask.Facts[windowIndex].Coverage)
		m.Coverage = facts.Coverage
		m.ExcludedObserved, m.ExcludedKnownEmpty = facts.Totals.Excluded.Observed, facts.Totals.Excluded.KnownEmpty
		if facts.Totals.Excluded.Good != nil {
			m.ExcludedGood = new(sli.NormalizeDecimal(facts.Totals.Excluded.Good.FloatString(12)))
		}
		if facts.Totals.Excluded.Total != nil {
			m.ExcludedTotal = new(sli.NormalizeDecimal(facts.Totals.Excluded.Total.FloatString(12)))
		}
		bounds := derivedWindowParams(in.SliID, in.MeasurementRevision, window)
		for _, h := range mask.ReusedHours {
			if !h.Key.HourStart.Before(bounds.FullStart.Time) && h.Key.HourStart.Before(bounds.FullEnd.Time) {
				m.ReusedHours = append(m.ReusedHours, h)
			}
		}
	}
	if err := m.validate(window); err != nil {
		return extremaMark{}, err
	}
	mark.Maintenance = m
	mark.Good, mark.Total = "0", "0"
	if value.Good != nil {
		mark.Good = *value.Good
	}
	if value.Total != nil {
		mark.Total = *value.Total
	}
	mark.Observed, mark.KnownEmpty, mark.ZeroTraffic = value.Observed, value.KnownEmpty, value.ZeroTraffic
	cloneTime := func(v *time.Time) *time.Time {
		if v == nil {
			return nil
		}
		return new(*v)
	}
	mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = cloneTime(value.FirstObservation), cloneTime(value.LastObservation), cloneTime(value.LatestReceipt)
	return mark, nil
}
