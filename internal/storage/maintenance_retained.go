package storage

import (
	"math/big"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5/pgtype"
)

// Private replay inputs complement the existing eligible population in a mark.
// Readers must authenticate Scope against their persisted row/binding before
// using it. Pure replay validates internal identities and the coverage certificate.
type retainedMaintenanceInputs struct {
	UnavailableReason  string                         `json:"unavailable_reason,omitempty"`
	Version            int                            `json:"version"`
	Scope              maintenancePreparationScope    `json:"scope"`
	SourceGeneration   int64                          `json:"source_generation"`
	ComputedRevision   int64                          `json:"computed_revision"`
	RequestedRevision  int64                          `json:"requested_revision"`
	BuildID            string                         `json:"build_id,omitempty"`
	Proof              *maintenanceCompatibilityProof `json:"proof,omitempty"`
	ReusedHours        []maintenanceReusedHour        `json:"reused_hours,omitempty"`
	Coverage           *slo.WindowCoverage            `json:"coverage,omitempty"`
	ExcludedGood       *string                        `json:"excluded_good,omitempty"`
	ExcludedTotal      *string                        `json:"excluded_total,omitempty"`
	ExcludedObserved   int64                          `json:"excluded_observed"`
	ExcludedKnownEmpty int64                          `json:"excluded_known_empty"`
}

func validRetainedMaintenanceProof(p maintenanceCompatibilityProof, scope maintenancePreparationScope, computed, requested int64, start, end time.Time) bool {
	if p.SloID != scope.SloID.String() || p.ComputedRevision != computed || p.RequestedRevision != requested || !p.Start.Equal(start) || !p.End.Equal(end) || !start.Before(end) || p.IndexedThrough < 0 {
		return false
	}
	switch p.Kind {
	case "same_revision":
		return computed == requested && p.ComparisonID == ""
	case "indexed_absence":
		return computed < requested && p.IndexedThrough >= requested && p.ComparisonID == ""
	case "equal_union":
		return computed < requested && p.IndexedThrough >= requested && canonicalUUIDCheck(p.ComparisonID)
	default:
		return false
	}
}

func (m retainedMaintenanceInputs) validate(input slo.WindowInput) error {
	if m.Version != 1 || !m.Scope.SloID.Valid || !m.Scope.SliID.Valid || !canonicalUUIDCheck(m.Scope.SloID.String()) || !canonicalUUIDCheck(m.Scope.SliID.String()) || m.Scope.DefinitionRevision < 1 || m.Scope.MeasurementRevision < 1 || m.SourceGeneration < 0 || m.ComputedRevision < 0 || m.ComputedRevision > m.RequestedRevision || (m.BuildID == "" && m.ComputedRevision != 0) || (m.BuildID != "" && !canonicalUUIDCheck(m.BuildID)) || input.Cadence <= 0 || input.Cadence%time.Microsecond != 0 || input.Duration <= 0 || len(input.Observations) != 0 || len(input.Exclusions) != 0 {
		return ErrDerivedFailed
	}
	if m.UnavailableReason != "" {
		if m.UnavailableReason != "maintenance_history_unavailable" && m.UnavailableReason != "maintenance_preparation_failed" || m.Proof != nil || len(m.ReusedHours) != 0 || m.Coverage != nil || m.ExcludedGood != nil || m.ExcludedTotal != nil || m.ExcludedObserved != 0 || m.ExcludedKnownEmpty != 0 {
			return ErrDerivedFailed
		}
		return nil
	}
	if m.Proof == nil {
		if m.ComputedRevision != m.RequestedRevision {
			return ErrDerivedFailed
		}
	} else if !validRetainedMaintenanceProof(*m.Proof, m.Scope, m.ComputedRevision, m.RequestedRevision, input.At.Add(-input.Duration), input.At) {
		return ErrDerivedFailed
	}
	if m.ComputedRevision == 0 {
		if m.Coverage != nil || m.ExcludedGood != nil || m.ExcludedTotal != nil || m.ExcludedObserved != 0 || m.ExcludedKnownEmpty != 0 || len(m.ReusedHours) != 0 {
			return ErrDerivedFailed
		}
		return nil
	}
	if m.Coverage == nil {
		return ErrDerivedFailed
	}
	bounds := derivedWindowParams(pgtype.UUID{}, 1, input)
	seen := make(map[int64]bool, len(m.ReusedHours))
	for _, h := range m.ReusedHours {
		k := h.Key
		at := k.HourStart.UnixMicro()
		if !validMaintenanceHourKey(k) || !canonicalUUIDCheck(k.BuildID.String()) || k.SloID != m.Scope.SloID || k.SliID != m.Scope.SliID || k.DefinitionRevision != m.Scope.DefinitionRevision || k.MeasurementRevision != m.Scope.MeasurementRevision || k.SourceGeneration > m.SourceGeneration || h.ComputedRevision < 0 || h.ComputedRevision >= m.ComputedRevision || seen[at] || k.HourStart.Before(bounds.FullStart.Time) || !k.HourStart.Before(bounds.FullEnd.Time) || !validRetainedMaintenanceProof(h.Proof, m.Scope, h.ComputedRevision, m.ComputedRevision, ceilSliBoundary(k.HourStart, input.Cadence), ceilSliBoundary(k.HourStart.Add(time.Hour), input.Cadence)) {
			return ErrDerivedFailed
		}
		seen[at] = true
	}
	return nil
}

func unavailableMaintenanceWindow(input slo.WindowInput, reason string) (slo.WindowResult, error) {
	if input.At.IsZero() || input.Duration <= 0 || input.Cadence <= 0 || input.Duration%input.Cadence != 0 || reason != "maintenance_history_unavailable" && reason != "maintenance_preparation_failed" {
		return slo.WindowResult{}, ErrDerivedFailed
	}
	start := input.At.Add(-input.Duration)
	eligible := maxTime(start, input.ValidFrom, input.SliValidFrom)
	if eligible.After(input.At) {
		eligible = input.At
	}
	retention := maxTime(eligible, input.RetentionStart)
	if retention.After(input.At) {
		retention = input.At
	}
	return slo.WindowResult{RequestedStart: start, EffectiveStart: start, EligibleStart: eligible, RetentionStart: retention, End: input.At, State: "unavailable", Freshness: "unknown", Performance: "unknown", Reason: reason, Provisional: true}, nil
}

func retainedMaintenanceDecimal(value string) (*big.Rat, bool) {
	whole, fraction, point := strings.Cut(value, ".")
	if whole == "" || point && (fraction == "" || len(fraction) > 12) || strings.Trim(whole, "0123456789") != "" || strings.Trim(fraction, "0123456789") != "" {
		return nil, false
	}
	return new(big.Rat).SetString(value)
}

func (m retainedMaintenanceInputs) excludedTotals() (slo.ExclusionTotals, error) {
	totals := slo.ExclusionTotals{Observed: m.ExcludedObserved, KnownEmpty: m.ExcludedKnownEmpty}
	if m.ExcludedObserved < 0 || m.ExcludedKnownEmpty < 0 || (m.ExcludedGood == nil) != (m.ExcludedTotal == nil) {
		return totals, ErrDerivedFailed
	}
	if m.ExcludedGood != nil {
		var valid bool
		totals.Good, valid = retainedMaintenanceDecimal(*m.ExcludedGood)
		if !valid {
			return totals, ErrDerivedFailed
		}
		totals.Total, valid = retainedMaintenanceDecimal(*m.ExcludedTotal)
		if !valid {
			return totals, ErrDerivedFailed
		}
	}
	return totals, nil
}
