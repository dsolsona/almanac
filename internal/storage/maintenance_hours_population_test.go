package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

// ADR0052 bounds the calculator at one-second cadence. Public datasource
// configuration still has a 15-second minimum;
// this unit fixture deliberately does not alter that SQL/API constraint.
func TestMaintenanceHourOneSecondPopulationBound(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	key := maintenanceHourKey{HourStart: start, SourceGeneration: 1}
	raw := make([]dbgen.ReadMaintenanceHourRawRow, 3600)
	for i := range raw {
		raw[i] = dbgen.ReadMaintenanceHourRawRow{IntervalStart: timestamp(start.Add(time.Duration(i) * time.Second)), RecordedAt: timestamp(start.Add(time.Duration(i+1) * time.Second)), Outcome: "observed", Good: "990", Total: "1000", IngestionGeneration: 1}
	}
	source := dbgen.LockMaintenanceHourSourceRow{SourceGeneration: 1, Good: "3564000", Total: "3600000", ObservedIntervals: 3600, FirstIntervalStart: timestamp(start), LastIntervalEnd: timestamp(start.Add(time.Hour)), FirstObservationAt: timestamp(start), LastObservationAt: timestamp(start.Add(time.Hour))}
	mask := []dbgen.ReadMaintenanceHourFragmentsRow{{StartsAt: timestamp(start.Add(10 * time.Minute)), EndsAt: timestamp(start.Add(20 * time.Minute))}}
	got, err := partitionMaintenanceHour(key, 1, raw, mask, source)
	if err != nil || got.Eligible.Expected != 3000 || got.Eligible.Observed != 3000 || got.Eligible.Good.RatString() != "2970000" || got.Excluded.Expected != 600 || got.Excluded.Total.RatString() != "600000" {
		t.Fatalf("one-second domain: %+v %v", got, err)
	}
}
