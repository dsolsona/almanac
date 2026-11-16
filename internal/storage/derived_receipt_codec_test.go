package storage

import (
	"bytes"
	"encoding/json/v2"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func TestReceiptMetadataContainers(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	q := api.SloOperationalQuality{State: "partial", Freshness: "stale", LatestReceiptAt: &at,
		FirstObservationAt: &at, LastObservationAt: &at, ObservedIntervals: 9, MissingIntervals: 1, ExpectedIntervals: 10,
		KnownEmptyIntervals: 2, ExpiredIntervals: 3, ZeroTrafficIntervals: 4, Coverage: new("0.9"), Reason: new("partial")}
	p := api.SloEvaluationPerformance{Good: new("12345678901234567890.123456789012"), Total: new("12345678901234567891.123456789012"), State: "unknown", Provisional: true}
	sample := api.SloOperationalSample{Quality: q, ReceivedAt: &at, EvaluationAt: at, BucketStart: at, BucketEnd: at, Good: p.Good, Total: p.Total, Revision: "7", State: "unknown", RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{"first", "maximum"}}
	current := api.SloOperationalValue{Quality: q, Performance: p, EvaluationAt: at}
	burn := api.SloBurnValue{Quality: q, Performance: p}
	day := api.SloDailyComplianceDay{Quality: q, Performance: p, DefinitionRevision: "7", Status: "unknown", Explanation: "partial", DayStart: at, DayEnd: at, EffectiveAt: at, RequestedAt: at}
	mark := extremaMark{Good: *p.Good, Total: *p.Total, LatestReceipt: &at, FirstObservation: &at, LastObservation: &at, At: at, Observed: 9, KnownEmpty: 2, ZeroTraffic: 4, Quality: "partial", State: "unknown", Freshness: "stale"}
	fixtures := map[string]any{
		"current":            api.SloOperationalOverviewItem{Current: current, Burn1h: burn, Burn6h: burn, BudgetSparkline: []api.SloOperationalSample{sample}, Generation: &api.DerivedGeneration{SourceGeneration: "19", DefinitionRevision: "7", CalculationVersion: operationalCalculationVersion}},
		"history":            api.SloOperationalDetail{Current: current, Burn1h: burn, Burn6h: burn, AttainmentSeries: api.SloOperationalSeries{Samples: []api.SloOperationalSample{sample}}, ComplianceSeries: api.SloDailyComplianceSeries{Days: []api.SloDailyComplianceDay{day}}},
		"extrema":            extremaLanes{Window: &extremaLane{Marks: []extremaMark{mark}, Coarse: true}, Compliance: &extremaCompliance{DayEnd: at, Mark: mark}},
		"tile":               retainedSloTile{Attainment: []api.SloOperationalSample{sample}, Budget: []api.SloOperationalSample{sample}, Burn1h: []api.SloOperationalSample{sample}, Burn6h: []api.SloOperationalSample{sample}, Compliance: &day},
		"point":              retainedSloPoint{Current: current, Burn1h: burn, Burn6h: burn, Attainment: sample},
		"budget":             extremaBudgetCache{ClosedThrough: at, PublishedAt: at, RangeStart: at, LastStart: at, Closed: []api.SloOperationalSample{sample}},
		"sample_page":        []api.SloOperationalSample{sample},
		"compliance_page":    []api.SloDailyComplianceDay{day},
		"evaluation_quality": api.SloEvaluationQuality{LatestReceiptAt: &at, ObservedIntervals: 9, FirstObservationAt: &at, LastObservationAt: &at, State: "partial", Freshness: "stale"},
	}
	for name, original := range fixtures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			certified, err := marshalReceiptMetadata(original)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(certified, []byte(`"_receipt_metadata_version":2`)) {
				t.Fatal("new receipt is not certified")
			}
			legacy, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			for _, version := range []string{"current", "legacy", "1", "3", "null", `"2"`, `[]`, `{}`} {
				t.Run(version, func(t *testing.T) {
					t.Parallel()
					raw := certified
					if version == "legacy" {
						raw = legacy
					} else if version != "current" {
						raw = bytes.ReplaceAll(raw, []byte(`"_receipt_metadata_version":2`), []byte(`"_receipt_metadata_version":`+version))
					}
					decoded := reflect.New(reflect.TypeOf(original)).Interface()
					if err := unmarshalReceiptMetadata(raw, decoded); err != nil {
						t.Fatal(err)
					}
					public, err := json.Marshal(decoded)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(public, []byte("_receipt_metadata_version")) {
						t.Fatal("private marker leaked to public API")
					}
					want := legacy
					if version != "current" {
						want = withoutReceiptFields(t, legacy)
					}
					actual := public
					if version != "current" {
						actual = withoutReceiptFields(t, public)
						if bytes.Contains(public, []byte(at.Format(time.RFC3339))) == false {
							t.Fatal("non-receipt timestamps lost")
						}
						var tree any
						if err := json.Unmarshal(public, &tree); err != nil {
							t.Fatal(err)
						}
						assertNoReceiptValues(t, tree)
					}
					var a, b any
					_ = json.Unmarshal(actual, &a)
					_ = json.Unmarshal(want, &b)
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("non-receipt evidence changed: got %s want %s", actual, want)
					}
				})
			}
		})
	}
}

func withoutReceiptFields(t *testing.T, raw []byte) []byte {
	t.Helper()
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			delete(x, "latest_receipt_at")
			delete(x, "received_at")
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(tree)
	out, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func assertNoReceiptValues(t *testing.T, tree any) {
	t.Helper()
	switch x := tree.(type) {
	case map[string]any:
		for key, v := range x {
			if (key == "received_at" || key == "latest_receipt_at") && v != nil {
				t.Fatalf("legacy receipt survived: %s=%v", key, v)
			}
			assertNoReceiptValues(t, v)
		}
	case []any:
		for _, v := range x {
			assertNoReceiptValues(t, v)
		}
	}
}

func TestReceiptMetadataExtractedPageAndMixedTrust(t *testing.T) {
	// A new root cannot bless an old nested quality, and vice versa.
	raw := []byte(`{"_receipt_metadata_version":2,"received_at":"2026-11-01T12:00:00Z","quality":{"latest_receipt_at":"2026-11-01T11:00:00Z","observed_intervals":9},"good":"9007199254740993","total":"9007199254740994"}`)
	var sample api.SloOperationalSample
	if err := unmarshalReceiptMetadata(raw, &sample); err != nil {
		t.Fatal(err)
	}
	if sample.ReceivedAt == nil || sample.Quality.LatestReceiptAt != nil || sample.Quality.ObservedIntervals != 9 {
		t.Fatal("mixed trust was not local to receipt owner")
	}
	// Persisting a read quarantine must not resurrect missing metadata.
	raw, err := marshalReceiptMetadata(sample)
	if err != nil {
		t.Fatal(err)
	}
	if err := unmarshalReceiptMetadata(raw, &sample); err != nil {
		t.Fatal(err)
	}
	if sample.Quality.LatestReceiptAt != nil || *sample.Good != "9007199254740993" {
		t.Fatal("rewrite manufactured receipt or changed exact count")
	}
}
