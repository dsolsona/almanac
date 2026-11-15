package storage

import (
	"math/big"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

func emptyMaintenancePopulation() maintenanceHourPopulation {
	return maintenanceHourPopulation{Good: new(big.Rat), Total: new(big.Rat)}
}
func partitionMaintenanceHour(k maintenanceHourKey, seconds int32, raw []dbgen.ReadMaintenanceHourRawRow, fragments []dbgen.ReadMaintenanceHourFragmentsRow, source dbgen.LockMaintenanceHourSourceRow) (maintenanceHourRecord, error) {
	result := maintenanceHourRecord{Key: k, Eligible: emptyMaintenancePopulation(), Excluded: emptyMaintenancePopulation()}
	if len(raw) > 3600 || len(fragments) > 3601 {
		return result, ErrOperationalResource
	}
	cadence := time.Duration(seconds) * time.Second
	aligned := func(at time.Time) bool { return validMaintenanceTime(at) && at.Unix()%int64(seconds) == 0 }
	for i, f := range fragments {
		if !f.StartsAt.Valid || !f.EndsAt.Valid || !aligned(f.StartsAt.Time) || !aligned(f.EndsAt.Time) || !f.StartsAt.Time.Before(f.EndsAt.Time) || i > 0 && !fragments[i-1].EndsAt.Time.Before(f.StartsAt.Time) {
			return result, ErrMaintenanceHourUnavailable
		}
	}
	// Merge two bounded ordered streams. A physical hour owns each whole interval
	// by its start, including an interval ending in the next physical hour.
	index := 0
	excluded := func(at time.Time) bool {
		for index < len(fragments) && !at.Before(fragments[index].EndsAt.Time) {
			index++
		}
		return index < len(fragments) && !at.Before(fragments[index].StartsAt.Time)
	}
	start := ceilSliBoundary(k.HourStart, cadence)
	end := k.HourStart.Add(time.Hour)
	for at := start; at.Before(end); at = at.Add(cadence) {
		if excluded(at) {
			result.Excluded.Expected++
		} else {
			result.Eligible.Expected++
		}
	}
	index = 0
	total := emptyMaintenancePopulation()
	for i, r := range raw {
		if !r.IntervalStart.Valid || !r.RecordedAt.Valid || !aligned(r.IntervalStart.Time) || r.IntervalStart.Time.Before(k.HourStart) || !r.IntervalStart.Time.Before(end) || i > 0 && !raw[i-1].IntervalStart.Time.Before(r.IntervalStart.Time) || r.IngestionGeneration > k.SourceGeneration {
			return result, ErrMaintenanceHourUnavailable
		}
		if err := addMaintenanceObservation(&total, r, cadence); err != nil {
			return result, err
		}
		population := &result.Eligible
		if excluded(r.IntervalStart.Time) {
			population = &result.Excluded
		}
		if err := addMaintenanceObservation(population, r, cadence); err != nil {
			return result, err
		}
	}
	if k.SourceGeneration == 0 {
		if len(raw) != 0 {
			return result, ErrMaintenanceHourStale
		}
	} else if len(raw) == 0 || !maintenancePopulationMatches(source, total, raw[0].IntervalStart, timestamp(raw[len(raw)-1].IntervalStart.Time.Add(cadence))) {
		return result, ErrMaintenanceHourUnavailable
	}
	if result.Eligible.Observed+result.Eligible.KnownEmpty > result.Eligible.Expected || result.Excluded.Observed+result.Excluded.KnownEmpty > result.Excluded.Expected {
		return result, ErrMaintenanceHourUnavailable
	}
	return result, nil
}
func addMaintenanceObservation(p *maintenanceHourPopulation, r dbgen.ReadMaintenanceHourRawRow, cadence time.Duration) error {
	switch r.Outcome {
	case "observed":
		g, gok := new(big.Rat).SetString(r.Good)
		v, vok := new(big.Rat).SetString(r.Total)
		if !gok || !vok || g.Sign() < 0 || g.Cmp(v) > 0 {
			return ErrMaintenanceHourUnavailable
		}
		p.Good.Add(p.Good, g)
		p.Total.Add(p.Total, v)
		p.Observed++
		if v.Sign() == 0 {
			p.ZeroTraffic++
		}
		if p.FirstObservation == nil {
			p.FirstObservation = new(r.IntervalStart.Time.UTC())
		}
		p.LastObservation = new(r.IntervalStart.Time.Add(cadence).UTC())
	case "complete_empty":
		p.KnownEmpty++
	case "failed", "incomplete":
		return nil
	default:
		return ErrMaintenanceHourUnavailable
	}
	if p.LatestReceipt == nil || p.LatestReceipt.Before(r.RecordedAt.Time) {
		p.LatestReceipt = new(r.RecordedAt.Time.UTC())
	}
	return nil
}
func maintenancePopulationMatches(h dbgen.LockMaintenanceHourSourceRow, p maintenanceHourPopulation, first, last pgtype.Timestamptz) bool {
	g, gok := new(big.Rat).SetString(h.Good)
	v, vok := new(big.Rat).SetString(h.Total)
	return gok && vok && g.Cmp(p.Good) == 0 && v.Cmp(p.Total) == 0 && h.ObservedIntervals == p.Observed && h.KnownEmptyIntervals == p.KnownEmpty && h.ZeroTrafficIntervals == p.ZeroTraffic && equalReceiptTime(h.FirstIntervalStart, first) && equalReceiptTime(h.LastIntervalEnd, last) && equalReceiptTime(h.FirstObservationAt, maintenanceHourTime(p.FirstObservation)) && equalReceiptTime(h.LastObservationAt, maintenanceHourTime(p.LastObservation))
}
func maintenanceHourTime(at *time.Time) pgtype.Timestamptz {
	if at == nil {
		return pgtype.Timestamptz{}
	}
	return timestamp(*at)
}
