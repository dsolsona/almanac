package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
)

func TestMaintenanceReusedHourGridGeometry(t *testing.T) {
	t.Parallel()
	// Exercise each accepted whole-second cadence at several physical-hour phases.
	// A reused partial hour must own at least two starts: with zero or one start,
	// its exclusion count is necessarily either zero or the whole population,
	// which evaluateMaintenanceMask reads canonically without ReusedHours.
	base := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	const sloID = "11111111-1111-4111-8111-111111111111"
	const sliID = "22222222-2222-4222-8222-222222222222"
	const buildID = "33333333-3333-4333-8333-333333333333"
	for seconds := 15; seconds <= 86400; seconds++ {
		cadence := time.Duration(seconds) * time.Second
		for _, phase := range []time.Duration{0, time.Hour, 23 * time.Hour} {
			hour := base.Add(phase)
			start, end := ceilSliBoundary(hour, cadence), ceilSliBoundary(hour.Add(time.Hour), cadence)
			owned := end.Sub(start) / cadence
			if cadence >= time.Hour && owned > 1 {
				t.Fatalf("coarse hour has partial population: cadence=%s hour=%s owned=%d", cadence, hour, owned)
			}
			if owned < 2 {
				continue
			}
			w := alert.WindowEvidence{Start: hour.Add(-time.Hour), End: hour.Add(3 * time.Hour), Snapshot: alert.SnapshotIdentity{SLOID: sloID, SLORevision: 1, SLIRevision: 1, SourceGeneration: 2, ExclusionRevision: 2}}
			w.Maintenance = &alert.MaintenanceEvidence{RequestedRevision: 2, ComputedRevision: 2, BuildID: buildID, ReusedHours: []alert.MaintenanceHourEvidence{{SLOID: sloID, SLIID: sliID, BuildID: buildID, SLORevision: 1, SLIRevision: 1, SourceGeneration: 1, ComputedRevision: 1, HourStart: hour, Proof: alert.MaintenanceProof{Kind: "indexed_absence", ComputedRevision: 1, RequestedRevision: 2, IndexedThrough: 2, Start: start, End: end}}}}
			if err := alert.ValidateMaintenanceEvidence(w); err != nil {
				t.Fatalf("legitimate partial-hour proof rejected: cadence=%s hour=%s proof=%s..%s: %v", cadence, hour, start, end, err)
			}
		}
	}
}
