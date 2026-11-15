package alerthttp

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage"
)

func TestAlertWirePreservesExactEvidenceAndAbsence(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.FixedZone("fixture", 7200))
	identity := alert.SnapshotIdentity{SLOID: "11111111-1111-4111-8111-111111111111", SLORevision: 1, SLIRevision: 2, SourceGeneration: 3, CalculationVersion: "v1", Cutoff: at}
	for _, test := range []struct {
		name        string
		quality     alert.Quality
		good, total string
		present     bool
	}{
		{"complete", alert.Complete, "9856.000000000001", "10000", true},
		{"partial observed population", alert.Partial, "10", "11", true},
		{"zero traffic", alert.ZeroTraffic, "0", "0", true},
		{"missing", alert.Missing, "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := windowDTO(alert.WindowEvidence{Snapshot: identity, Start: at.Add(-time.Hour), End: at, Quality: test.quality, Good: test.good, Total: test.total})
			body, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			_, hasGood := value["good"]
			_, hasTotal := value["total"]
			if hasGood != test.present || hasTotal != test.present || value["quality"] != string(test.quality) || value["end"] != "2026-11-15T10:00:00Z" {
				t.Fatalf("wrong immutable window: %s", body)
			}
			if test.present && (value["good"] != test.good || value["total"] != test.total) {
				t.Fatalf("counts rounded or reinterpreted: %s", body)
			}
			snapshot := value["snapshot"].(map[string]any)
			if snapshot["slo_revision"] != "1" || snapshot["source_generation"] != "3" || snapshot["exclusion_revision"] != "0" {
				t.Fatalf("identity precision lost: %s", body)
			}
		})
	}
}

func TestAlertPolicyWireUsesExplicitUnitsAndVersions(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	value := policyDTO(storage.AlertPolicyRecord{ID: id, SLOID: id, Name: "Checkout", Severity: "high", Enabled: true, Version: 9007199254740993, EvaluationRevision: 2, Conditions: []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "14.4"}}})
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["version"] != "9007199254740993" || wire["evaluation_revision"] != "2" || strings.Contains(string(body), "Conditions") {
		t.Fatalf("internal format leaked: %s", body)
	}
	condition := wire["conditions"].([]any)[0].(map[string]any)
	if condition["short_window_seconds"] != float64(300) || condition["long_window_seconds"] != float64(3600) || condition["threshold"] != "14.4" {
		t.Fatalf("wrong units: %s", body)
	}
}
