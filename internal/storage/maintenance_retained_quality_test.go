package storage

import (
	"bytes"
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestMaintenanceRetainedFullyExcluded(t *testing.T) {
	t.Parallel()
	mark, input, _ := retainedMaintenanceFixture(t)
	start := input.At.Add(-input.Duration)
	mark.Good, mark.Total, mark.Observed = "0", "0", 0
	mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = nil, nil, nil
	mark.Maintenance.ExcludedGood, mark.Maintenance.ExcludedTotal = new("59400"), new("60000")
	mark.Maintenance.ExcludedObserved = 60
	mark.Maintenance.Coverage = &slo.WindowCoverage{LatestEligibleEnd: start, Points: []slo.ExclusionCoveragePoint{{At: start.Add(-time.Minute), ExcludedBefore: 0}, {At: start, ExcludedBefore: 0}, {At: input.At, ExcludedBefore: 60}}}
	got, err := mark.result(t.Context(), input)
	if err != nil || got.State != "excluded" || got.Performance != "unknown" || got.Expected != 0 || got.Excluded != 60 || got.Good != nil || got.Burn != nil || got.ExcludedTotal == nil || *got.ExcludedTotal != "60000.000000000000" {
		t.Fatalf("fully excluded replay invented eligible population: %+v %v", got, err)
	}
}

func TestMaintenanceRetainedUnknownExcludedEvents(t *testing.T) {
	t.Parallel()
	mark, input, _ := retainedMaintenanceFixture(t)
	mark.Maintenance.ExcludedObserved, mark.Maintenance.ExcludedKnownEmpty = 9, 1
	mark.Maintenance.ExcludedGood, mark.Maintenance.ExcludedTotal = new("8910"), new("9000")
	got, err := mark.result(t.Context(), input)
	if err != nil || got.State != "complete" || got.Expected != 50 || got.Excluded != 10 || got.ExcludedGood != nil || got.ExcludedTotal != nil || got.Good == nil || *got.Good != "49500.000000000000" {
		t.Fatalf("unknown excluded events became zero/partial eligible data: %+v %v", got, err)
	}
}

func TestMaintenanceRetainedReceiptQuarantine(t *testing.T) {
	t.Parallel()
	mark, input, _ := retainedMaintenanceFixture(t)
	uncertified, err := json.Marshal(mark)
	if err != nil {
		t.Fatal(err)
	}
	var saved extremaMark
	if err := unmarshalReceiptMetadata(uncertified, &saved); err != nil {
		t.Fatal(err)
	}
	got, err := saved.result(t.Context(), input)
	if err != nil || got.LatestReceipt != nil || got.State != "complete" || got.Expected != 50 || got.Excluded != 10 || got.Total == nil || *got.Total != "50000.000000000000" {
		t.Fatalf("unknown receipt damaged numeric replay: %+v %v", got, err)
	}
}

func TestMaintenanceRetainedLegacyMark(t *testing.T) {
	t.Parallel()
	mark, input, _ := retainedMaintenanceFixture(t)
	mark.Maintenance = nil
	mark.Good, mark.Total, mark.Observed = "59400", "60000", 60
	encoded, err := marshalReceiptMetadata(mark)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"maintenance"`)) {
		t.Fatal("legacy mark acquired maintenance metadata")
	}
	var saved extremaMark
	if err := unmarshalReceiptMetadata(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	got, err := saved.result(t.Context(), input)
	if err != nil || saved.Maintenance != nil || got.Expected != 60 || got.Excluded != 0 || got.Total == nil || *got.Total != "60000.000000000000" {
		t.Fatalf("legacy mark was relabelled: %+v %v", got, err)
	}
}

func TestMaintenanceRetainedTerminalGap(t *testing.T) {
	t.Parallel()
	mark, input, _ := retainedMaintenanceFixture(t)
	mark.Good, mark.Total, mark.Observed = "0", "0", 0
	mark.FirstObservation, mark.LastObservation, mark.LatestReceipt = nil, nil, nil
	m := mark.Maintenance
	m.UnavailableReason = "maintenance_history_unavailable"
	m.ComputedRevision, m.BuildID, m.Coverage = 0, "", nil
	m.ExcludedGood, m.ExcludedTotal, m.ExcludedObserved = nil, nil, 0
	got, err := mark.result(t.Context(), input)
	if err != nil || got.State != "unavailable" || got.Reason != m.UnavailableReason || got.Good != nil || got.Total != nil || got.Burn != nil || got.Performance != "unknown" {
		t.Fatalf("terminal gap became missing numeric evidence: %+v %v", got, err)
	}
}

func TestMaintenanceRetainedNilReusedBuild(t *testing.T) {
	t.Parallel()
	mark, input, _ := retainedMaintenanceFixture(t)
	m := mark.Maintenance
	m.ComputedRevision, m.RequestedRevision = 2, 2
	oldBuild, _ := canonicalUUID("cfd5bc95-7f07-497b-8abc-07c458112f31")
	start := input.At.Add(-input.Duration)
	m.ReusedHours = []maintenanceReusedHour{{Key: maintenanceHourKey{SloID: m.Scope.SloID, SliID: m.Scope.SliID, BuildID: oldBuild, DefinitionRevision: 1, MeasurementRevision: 1, SourceGeneration: 7, HourStart: start}, ComputedRevision: 1, Proof: maintenanceCompatibilityProof{SloID: m.Scope.SloID.String(), ComputedRevision: 1, RequestedRevision: 2, IndexedThrough: 2, Start: start, End: input.At, Kind: "indexed_absence"}}}
	if _, err := mark.result(t.Context(), input); err != nil {
		t.Fatalf("valid reused provenance rejected: %v", err)
	}
	m.ReusedHours[0].Key.BuildID = pgtype.UUID{Valid: true}
	if _, err := mark.result(t.Context(), input); err == nil {
		t.Fatal("nil reused build survived replay")
	}
}
