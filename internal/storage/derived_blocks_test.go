package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
)

func TestMissingBlocksKeepUnchangedRegionsOutOfRebuilds(t *testing.T) {
	got := missingBlockGroups([]bool{true, false, false, true, true, false, true, false, false})
	want := [][2]int{{1, 3}, {5, 6}, {7, 9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuild groups %v, want %v", got, want)
	}
}

func TestIndependentRollingBlocksMatchTheCanonicalExtremaAndGapGrid(t *testing.T) {
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	cadence, window := time.Minute, 6*time.Hour
	ticks := 3*24*60 + 17
	end := start.Add(time.Duration(ticks) * cadence)
	base := slo.WindowInput{Now: end, ValidFrom: start.Add(-window), SliValidFrom: start.Add(-window), RetentionStart: start.Add(-24 * time.Hour), Cadence: cadence, FreshnessGrace: 2 * cadence, Target: "0.99"}
	for index := -360; index < ticks; index++ {
		if index >= 217 && index < 247 {
			continue
		}
		at := start.Add(time.Duration(index) * cadence)
		fact := slo.Observation{Start: at, End: at.Add(cadence), Received: at.Add(cadence), Outcome: "observed", Good: "999.000000000001", Total: "1000.000000000001"}
		if index == 123 {
			fact.Good = "0"
		}
		if index == 1701 {
			fact.Good, fact.Total = "99999999999999999999999999.123456789012", "99999999999999999999999999.123456789012"
		}
		if index == 707 {
			fact.Outcome, fact.Good, fact.Total = "complete_empty", "", ""
		}
		if index == 708 {
			fact.Good, fact.Total = "0", "0"
		}
		base.Observations = append(base.Observations, fact)
	}
	const buckets = 64
	bounds := bucketBoundaries(start, end, ticks, buckets, cadence)
	for _, metric := range []string{"remaining_fraction", "burn"} {
		whole, err := buildOperationalSamples(t.Context(), base, start, end, window, cadence, "1", metric, buckets)
		if err != nil {
			t.Fatal(err)
		}
		var combined []api.SloOperationalSample
		for _, group := range [][2]int{{0, 3}, {3, 27}, {27, 28}, {28, buckets}} {
			part, err := buildRollingBuckets(t.Context(), base, bounds[group[0]:group[1]+1], window, cadence, "1", metric, group[0] == 0, buckets*4)
			if err != nil {
				t.Fatal(err)
			}
			combined = append(combined, part...)
		}
		if !reflect.DeepEqual(whole, combined) {
			t.Fatalf("%s block composition changed exact values, extrema or gap transitions: whole=%d parts=%d", metric, len(whole), len(combined))
		}
	}
	attainment, _, _, err := buildAttainmentSamples(t.Context(), base, start, end, cadence, "1")
	if err != nil {
		t.Fatal(err)
	}
	count := sampleBucketLimit(start, end, cadence)
	bounds = bucketBoundaries(start, end, ticks, count, cadence)
	var combined []api.SloOperationalSample
	for _, group := range [][2]int{{0, 3}, {3, 27}, {27, count}} {
		part, _, _, err := buildAttainmentSamples(t.Context(), base, bounds[group[0]], bounds[group[1]], cadence, "1", bounds[group[0]:group[1]+1])
		if err != nil {
			t.Fatal(err)
		}
		combined = append(combined, part...)
	}
	if !reflect.DeepEqual(attainment, combined) {
		t.Fatal("independent attainment blocks changed exact bucket counts")
	}
}
