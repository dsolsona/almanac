package storage

import "github.com/dsolsona/almanac/internal/slo"

// Row authority authenticates private replay inputs against their persisted
// container. Currentness still requires the separate source/configuration fence.
type maintenanceRetainedRowAuthority struct {
	Scope             maintenancePreparationScope
	SourceGeneration  int64
	RequestedRevision int64
}

func (mark extremaMark) validateMaintenanceRowAuthority(input slo.WindowInput, row maintenanceRetainedRowAuthority) error {
	if !row.Scope.SloID.Valid || !row.Scope.SliID.Valid || !canonicalUUIDCheck(row.Scope.SloID.String()) || !canonicalUUIDCheck(row.Scope.SliID.String()) || row.Scope.DefinitionRevision < 1 || row.Scope.MeasurementRevision < 1 || row.SourceGeneration < 0 || row.RequestedRevision < 0 {
		return ErrDerivedFailed
	}
	if mark.Maintenance == nil {
		if row.RequestedRevision != 0 {
			// No interval exists before validity; absence of a mask claim is
			// intentional, but it must not hide any counts or observation bounds.
			if input.Cadence <= 0 || input.Duration <= 0 || mark.At.IsZero() ||
				ceilSliBoundary(maxTime(mark.At.Add(-input.Duration), input.ValidFrom, input.SliValidFrom), input.Cadence).Before(mark.At) ||
				mark.Good != "0" || mark.Total != "0" || mark.Observed != 0 || mark.KnownEmpty != 0 || mark.ZeroTraffic != 0 ||
				mark.FirstObservation != nil || mark.LastObservation != nil || mark.LatestReceipt != nil {
				return ErrDerivedFailed
			}
		}
		return nil
	}
	m := mark.Maintenance
	if m.Scope != row.Scope || m.SourceGeneration > row.SourceGeneration || m.RequestedRevision != row.RequestedRevision {
		return ErrDerivedFailed
	}
	input.At = mark.At
	if !mark.RetentionStart.IsZero() {
		input.RetentionStart = mark.RetentionStart
	}
	return m.validate(input)
}
