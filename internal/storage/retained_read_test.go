package storage

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestRetainedDailyReusesTheProtectedGraphRead(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	day := api.SloDailyComplianceDay{DayStart: start, DayEnd: end, DefinitionRevision: "1", Status: "met"}
	reader := retainedSeriesReader{days: &api.SloDailyComplianceSeries{Days: []api.SloDailyComplianceDay{day}},
		daySummary: api.SloDailyComplianceSummary{MetDays: 1, EvaluableDays: 1}}
	// No query handle: a second database read cannot satisfy this cached day.
	series, summary, err := reader.daily(t.Context(), slo.WindowInput{Cadence: time.Minute}, start, end, end, day.SloId, "1", "0.99", 24*time.Hour, time.Minute)
	if err != nil || len(series.Days) != 1 || series.Days[0].Status != "met" || !series.Days[0].DayEnd.Equal(end) || summary.MetDays != 1 || summary.EvaluableDays != 1 {
		t.Fatal("daily compliance did not reuse the coherent graph result", err)
	}
}

func TestRetainedDenseYearKeepsExactExtremaAndDisclosesMissingCoverage(t *testing.T) {
	start := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(365 * 24 * time.Hour)
	var samples []api.SloOperationalSample
	for hour := range 365 * 24 {
		from := start.Add(time.Duration(hour) * time.Hour)
		for index, minute := range []int{1, 2, 60} {
			sample := api.SloOperationalSample{EvaluationAt: from.Add(time.Duration(minute) * time.Minute), State: "observed", Revision: "1",
				Good: new("99"), Total: new("100"), Quality: api.SloOperationalQuality{State: "complete", Freshness: "fresh"}}
			if index == 1 {
				sample.State, sample.Quality.State, sample.Good, sample.Total = "missing", "no_data", nil, nil
			} else if hour == 4000 && index == 0 {
				sample.Good = new("1")
			}
			samples = append(samples, sample)
		}
	}
	got, err := reduceRetainedDisplay(samples, start, end, "0.99", "attainment", 375)
	if err != nil || len(got) == 0 || len(got) > 1500 {
		t.Fatalf("dense year unavailable or unbounded: samples=%d err=%v", len(got), err)
	}
	peak := false
	for _, sample := range got {
		if sample.DisplayCoverage == nil || !slices.Contains(sample.DisplayCoverage.States, api.SloDisplayCoverageStates("missing")) ||
			!slices.Contains(sample.DisplayCoverage.QualityStates, api.SloDisplayCoverageQualityStates("no_data")) {
			t.Fatal("coarse bucket hid missing coverage")
		}
		if sample.Good != nil && *sample.Good == "1" {
			peak = sample.EvaluationAt.Equal(start.Add(4000*time.Hour+time.Minute)) && *sample.Total == "100" && sample.State == "observed" && sample.Quality.State == "complete"
		}
	}
	if !peak || !got[0].EvaluationAt.Equal(samples[0].EvaluationAt) || !got[len(got)-1].EvaluationAt.Equal(end) {
		t.Fatal("coarse graph changed an exact incident extremum or endpoint")
	}
	composed, err := reduceRetainedDisplay(got, start, end, "0.99", "attainment", 10)
	if err != nil || len(composed) > 40 {
		t.Fatalf("coarse composition is unavailable or unbounded: %d %v", len(composed), err)
	}
	for _, sample := range composed {
		if sample.DisplayCoverage == nil || !slices.Contains(sample.DisplayCoverage.States, api.SloDisplayCoverageStates("missing")) {
			t.Fatal("repeated reduction lost the original missing coverage")
		}
	}
	got[0].DisplayCoverage.States = nil
	if _, err := reduceRetainedDisplay(got, start, end, "0.99", "attainment", 10); !errors.Is(err, slo.ErrOperationalFacts) {
		t.Fatalf("malformed retained coverage accepted: %v", err)
	}
	fine, err := reduceRetainedDisplay(samples[4000*3:4001*3], start.Add(4000*time.Hour), start.Add(4001*time.Hour), "0.99", "attainment", 375)
	if err != nil || len(fine) != 3 || fine[1].State != "missing" || fine[0].DisplayCoverage != nil {
		t.Fatal("narrow range did not restore the individual gap")
	}
}

func TestRetainedDisplayIncludesPartialGridBucketsInItsBound(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)
	end := start.Add(375 * time.Hour)
	width := retainedDisplayWidth(start, end, 375)
	if width != 2*time.Hour {
		t.Fatalf("unaligned range creates more buckets than its limit: %s", width)
	}
}

func TestRetainedYearReadBoundsCoverageAndEdgeWork(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{0, time.Second, 15*time.Minute + 15*time.Second, 23*time.Hour + 59*time.Minute} {
		for _, duration := range []time.Duration{time.Minute, time.Hour, 7*24*time.Hour + 15*time.Minute, 28 * 24 * time.Hour, 90 * 24 * time.Hour, 365 * 24 * time.Hour} {
			start := base.Add(offset)
			end := start.Add(duration)
			parts, err := retainedReadParts(start, end)
			if err != nil || len(parts) > 674 || len(parts) == 0 || duration == 365*24*time.Hour && len(parts) > 413 {
				t.Fatalf("unbounded retained range: %d parts, %v", len(parts), err)
			}
			next, edges := start, 0
			for _, part := range parts {
				if !part.start.Equal(next) || !part.start.Before(part.end) {
					t.Fatal("retained read overlaps or loses a requested interval")
				}
				if part.resolution == 0 {
					edges++
					if part.end.Sub(part.start) > time.Hour {
						t.Fatal("raw edge exceeds one hour")
					}
				} else if part.end.Sub(part.start) != time.Duration(part.resolution)*time.Second || !part.start.Equal(part.start.Truncate(time.Duration(part.resolution)*time.Second)) {
					t.Fatal("retained tile is not on its stable UTC grid")
				}
				next = part.end
			}
			if !next.Equal(end) || edges > 2 {
				t.Fatal("retained read loses its end or requires unbounded raw edges")
			}
		}
	}
}

func TestExtremaCannotHideCorruptionAsEmptyHistory(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	reader := extremaReader{}
	if _, err := reader.decode(t.Context(), slo.WindowInput{Cadence: time.Minute}, retainedReadPart{start: start, end: start.Add(time.Hour), resolution: 3600},
		dbgen.ReadSloExtremaPartsRow{Lanes: []byte(`{}`), RetentionStart: timestamp(start)}); !errors.Is(err, ErrDerivedFailed) {
		t.Fatal("corrupt empty tile was displayed as absent history")
	}
}

func TestUnpreparedHistoryIsUnavailableRatherThanMissingMeasurements(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	reader := retainedSeriesReader{row: dbgen.GetSloRow{DefinitionRevision: 1}}
	part := retainedReadPart{start: start, end: start.Add(time.Hour), resolution: 3600}
	base := slo.WindowInput{Cadence: time.Minute}
	for _, failed := range []bool{false, true} {
		reader.failed = failed
		tile := reader.unpreparedTile(base, part)
		for _, lane := range [][]api.SloOperationalSample{tile.Attainment, tile.Budget, tile.Burn1h, tile.Burn6h} {
			if len(lane) != 1 || lane[0].Quality.State != "unavailable" || lane[0].Quality.MissingIntervals != 0 || lane[0].Good != nil || lane[0].Total != nil || lane[0].Attainment != nil || lane[0].RemainingFraction != nil || lane[0].Burn != nil {
				t.Fatalf("uncalculated history fabricated measurements: %+v", lane)
			}
		}
		day := reader.unpreparedDay(retainedReadPart{start: start, end: start.Add(24 * time.Hour)}, start, [16]byte{}, "1", "0.99", 24*time.Hour, time.Minute)
		if day.Status != "unknown" || day.Quality.State != "unavailable" || day.Performance.Attainment != nil || day.Performance.Good != nil {
			t.Fatalf("uncalculated compliance became a measured outcome: %+v", day)
		}
	}
}

func TestRetainedAttainmentMatchesCanonicalSingleInterval(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	for _, state := range []string{"observed", "complete_empty", "zero_traffic", "missing", "expired", "not_applicable"} {
		t.Run(state, func(t *testing.T) {
			base := slo.WindowInput{At: end, Now: end, ValidFrom: start, SliValidFrom: start, Cadence: time.Minute, Duration: time.Minute, Target: "0.99", FreshnessGrace: 2 * time.Minute}
			fact := slo.Observation{Start: start, End: end, Received: end, Outcome: "observed", Good: "99", Total: "100"}
			switch state {
			case "complete_empty":
				fact.Outcome = "complete_empty"
			case "zero_traffic":
				fact.Good, fact.Total = "0", "0"
			case "expired":
				base.Now = end.Add(366 * 24 * time.Hour)
			case "not_applicable":
				base.ValidFrom = end
			}
			base.RetentionStart = base.Now.Add(-365 * 24 * time.Hour)
			if state != "missing" {
				base.Observations = []slo.Observation{fact}
			}
			canonical, _, _, err := buildCanonicalAttainmentSamples(t.Context(), base, start, end, time.Minute, "1")
			if err != nil || len(canonical) != 1 {
				t.Fatal("canonical interval unavailable", err)
			}
			value, err := evaluateOperationalAt(t.Context(), base, end, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			actual := operationalAttainmentSample(value, start, end, "1")
			if !reflect.DeepEqual(actual, canonical[0]) {
				t.Fatalf("retained attainment differs from canonical %s: %+v / %+v", state, actual, canonical[0])
			}
		})
	}
}

func TestRetainedDisplayPreservesExtremaGapsAndYearBounds(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 17, 0, 0, time.UTC)
	end := start.Add(365 * 24 * time.Hour)
	var samples []api.SloOperationalSample
	for index, offset := range []time.Duration{0, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 60 * time.Minute, end.Sub(start)} {
		value := api.SloOperationalSample{EvaluationAt: start.Add(offset), Revision: "1", State: "observed", Good: new("99"), Total: new("100"),
			Quality: api.SloOperationalQuality{State: "complete", Freshness: "fresh"}}
		if index == 1 {
			value.Good = new("50")
		}
		if index == 2 {
			value.State, value.Quality.State, value.Good, value.Total = "missing", "no_data", nil, nil
		}
		samples = append(samples, value)
	}
	before := append([]api.SloOperationalSample(nil), samples...)
	selected, err := reduceRetainedDisplay(samples, start, end, "0.99", "budget", 375)
	if err != nil || len(selected) != len(samples) {
		t.Fatalf("display lost first/last, exact extremum or gap boundaries: %d, %v", len(selected), err)
	}
	for index, point := range selected {
		if !point.EvaluationAt.Equal(samples[index].EvaluationAt) || point.BucketStart.Before(start) || point.BucketEnd.After(end) ||
			point.EvaluationAt.Before(point.BucketStart) || point.EvaluationAt.After(point.BucketEnd) || !reflect.DeepEqual(point.Good, samples[index].Good) {
			t.Fatal("display changed exact selected values or overflowed the year range")
		}
	}
	if !reflect.DeepEqual(samples, before) {
		t.Fatal("display reducer mutated retained tiles")
	}
}

func TestRetainedBeforeValidityDoesNotBecomeMissingMeasurements(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	reader := retainedSeriesReader{row: dbgen.GetSloRow{Target: "0.99", DefinitionRevision: 1, RollingWindowSeconds: 86400}}
	base := slo.WindowInput{Now: start.Add(48 * time.Hour), ValidFrom: start.Add(24 * time.Hour), SliValidFrom: start.Add(-24 * time.Hour),
		RetentionStart: start.Add(-365 * 24 * time.Hour), Cadence: time.Minute, Target: "0.99", FreshnessGrace: 2 * time.Minute}
	tile, err := reader.beforeValidity(t.Context(), base, retainedReadPart{start: start, end: start.Add(time.Hour), resolution: 3600})
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range [][]api.SloOperationalSample{tile.Attainment, tile.Budget, tile.Burn1h, tile.Burn6h} {
		if len(lane) == 0 {
			t.Fatal("before-validity region is invisible")
		}
		for _, point := range lane {
			if point.Quality.State != "not_applicable" || point.Quality.ExpectedIntervals != 0 || point.Quality.MissingIntervals != 0 || point.Good != nil {
				t.Fatal("before-validity region invented missing or successful measurements")
			}
		}
	}
}

func TestRetainedExpiredRegionDoesNotWaitForDeletedTiles(t *testing.T) {
	start := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	reader := retainedSeriesReader{row: dbgen.GetSloRow{Target: "0.99", DefinitionRevision: 1, RollingWindowSeconds: 86400}}
	base := slo.WindowInput{Now: start.Add(366 * 24 * time.Hour), ValidFrom: start.Add(-24 * time.Hour), SliValidFrom: start.Add(-24 * time.Hour),
		RetentionStart: start.Add(24 * time.Hour), Cadence: time.Minute, Target: "0.99", FreshnessGrace: 2 * time.Minute}
	tile, err := reader.beforeValidity(t.Context(), base, retainedReadPart{start: start, end: start.Add(time.Hour), resolution: 3600})
	if err != nil || len(tile.Budget) == 0 {
		t.Fatalf("expired history waited for unavailable tiles: %v", err)
	}
	for _, point := range tile.Budget {
		if point.Good != nil || point.Total != nil || point.Quality.ExpiredIntervals == 0 {
			t.Fatal("expired history invented retained observations")
		}
	}
	point, err := reader.unobservedPoint(t.Context(), base, start)
	if err != nil || point.Current.Quality.ExpiredIntervals == 0 {
		t.Fatalf("exact starting endpoint waited for an expired point: %v", err)
	}
	base.RetentionStart = start.Add(30 * time.Second)
	point, err = reader.unobservedPoint(t.Context(), base, start.Add(time.Minute))
	if err != nil || point.Current.Quality.ExpiredIntervals == 0 || point.Current.Performance.Good != nil {
		t.Fatalf("partially expired cadence boundary waited for an unretained endpoint: %v", err)
	}
}
