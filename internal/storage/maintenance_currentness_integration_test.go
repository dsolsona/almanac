//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationMaintenanceCurrentnessFencesAuthority(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	f.drain(t, "current authority source")
	id := mustPGUUID(f.ids[0])
	if err := f.runtime.refreshCurrentProjection(t.Context(), id, f.clock); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(f.conn)
	key := dbgen.GetPublishedCurrentProjectionParams{SloID: id, Now: timestamp(f.now), CalculationVersion: operationalCalculationVersion}
	before, err := q.GetPublishedCurrentProjection(t.Context(), key)
	if err != nil || !before.Current || !before.Usable {
		t.Fatalf("baseline projection: %+v %v", before, err)
	}
	change := f.input("authority-after-current")
	change.Start, change.End = f.at.Add(-30*time.Minute), f.at.Add(-20*time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, change); err != nil {
		t.Fatal(err)
	}
	after, err := q.GetPublishedCurrentProjection(t.Context(), key)
	if err != nil || after.Current || after.Usable {
		t.Fatalf("old exclusion authority remained current/usable: current=%v usable=%v %v", after.Current, after.Usable, err)
	}
}
