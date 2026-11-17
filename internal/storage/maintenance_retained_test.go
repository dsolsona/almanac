package storage

import (
	"bytes"
	"encoding/json/v2"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
)

func retainedMaintenanceFixture(t *testing.T) (extremaMark, slo.WindowInput, slo.WindowResult) {
	t.Helper()
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	input := slo.WindowInput{At: end, Now: end, Duration: time.Hour, Cadence: time.Minute, FreshnessGrace: time.Minute, Target: "0.999", ValidFrom: start, SliValidFrom: start, RetentionStart: start}
	raw := input
	for i := range 60 {
		at := start.Add(time.Duration(i) * time.Minute)
		raw.Observations = append(raw.Observations, slo.Observation{Start: at, End: at.Add(time.Minute), Received: at.Add(time.Minute), Outcome: "observed", Good: "990", Total: "1000"})
	}
	raw.Exclusions = []slo.ExclusionInterval{{Start: start.Add(20 * time.Minute), End: start.Add(30 * time.Minute)}}
	want, err := slo.EvaluateOperationalWindow(raw)
	if err != nil {
		t.Fatal(err)
	}
	sloID, _ := canonicalUUID("6f6805d3-a6f2-41c1-8188-c593af5245b3")
	sliID, _ := canonicalUUID("f92fc08f-5aae-4395-93da-1b1bb8b3f393")
	mark := extremaMark{At: end, Good: "49500", Total: "50000", Observed: 50, FirstObservation: new(start), LastObservation: new(end), LatestReceipt: new(end), State: "observed", Quality: "complete", Freshness: "fresh"}
	mark.Maintenance = &retainedMaintenanceInputs{Version: 1, Scope: maintenancePreparationScope{SloID: sloID, SliID: sliID, DefinitionRevision: 1, MeasurementRevision: 1}, SourceGeneration: 7, ComputedRevision: 1, RequestedRevision: 1, BuildID: "25023fee-32cf-4566-b13a-c22febd42b38", ExcludedGood: new("9900"), ExcludedTotal: new("10000"), ExcludedObserved: 10, Coverage: &slo.WindowCoverage{LatestEligibleEnd: end, Points: []slo.ExclusionCoveragePoint{{At: start, ExcludedBefore: 0}, {At: start.Add(time.Minute), ExcludedBefore: 0}, {At: end.Add(-time.Minute), ExcludedBefore: 10}, {At: end, ExcludedBefore: 10}}}}
	return mark, input, want
}

func TestMaintenanceRetainedReplay(t *testing.T) {
	t.Parallel()
	mark, input, want := retainedMaintenanceFixture(t)
	encoded, err := marshalReceiptMetadata(mark)
	if err != nil {
		t.Fatal(err)
	}
	var saved extremaMark
	if err := unmarshalReceiptMetadata(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	got, err := saved.result(t.Context(), input)
	if err != nil || !reflect.DeepEqual(got, want) || got.Expected != 50 || got.Excluded != 10 || got.Good == nil || *got.Good != "49500.000000000000" {
		t.Fatalf("covered retained replay diverges from raw: %+v %v want %+v", got, err, want)
	}
	before, _ := json.Marshal(saved)
	if _, err := saved.result(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(saved)
	if !bytes.Equal(before, after) {
		t.Fatal("replay mutated saved certificate")
	}
}

func TestMaintenanceRetainedCorruptCertificate(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"version", "missing_coverage", "rank", "revision", "build", "scope", "missing_excluded_total", "negative_excluded"} {
		t.Run(kind, func(t *testing.T) {
			mark, input, _ := retainedMaintenanceFixture(t)
			switch kind {
			case "version":
				mark.Maintenance.Version++
			case "missing_coverage":
				mark.Maintenance.Coverage = nil
			case "rank":
				mark.Maintenance.Coverage.Points[2].ExcludedBefore = 100
			case "revision":
				mark.Maintenance.ComputedRevision = 2
			case "build":
				mark.Maintenance.BuildID = "unknown"
			case "scope":
				mark.Maintenance.Scope.DefinitionRevision = 0
			case "missing_excluded_total":
				mark.Maintenance.ExcludedTotal = nil
			case "negative_excluded":
				mark.Maintenance.ExcludedObserved = -1
			}
			if _, err := mark.result(t.Context(), input); err == nil {
				t.Fatal("corrupt covered evidence became usable")
			}
		})
	}
}
