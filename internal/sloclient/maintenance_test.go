package sloclient

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/api"
)

func TestMaintenanceSampleContract(t *testing.T) {
	for _, tc := range []struct {
		name, extra, quality string
		valid                bool
	}{
		{"legacy omission", "", "", true},
		{"unknown excluded population", "", `,"excluded_intervals":2,"excluded_good":null,"excluded_total":null`, true},
		{"exact zero excluded population", "", `,"excluded_intervals":2,"excluded_good":"0","excluded_total":"0"`, true},
		{"one missing count", "", `,"excluded_intervals":2,"excluded_good":"0"`, false},
		{"negative count", "", `,"excluded_intervals":2,"excluded_good":"-1","excluded_total":"0"`, false},
		{"one unknown count", "", `,"excluded_intervals":2,"excluded_good":null,"excluded_total":"0"`, false},
		{"inverse population", "", `,"excluded_intervals":2,"excluded_good":"9007199254740993","excluded_total":"9007199254740992"`, false},
		{"invalid revision", `,"calculation_provenance":{"requested_revision":"1","computed_revision":"-1","source_generation":"1"}`, "", false},
		{"missing build", `,"calculation_provenance":{"requested_revision":"1","computed_revision":"1","source_generation":"1"}`, "", false},
		{"unproven reuse", `,"calculation_provenance":{"requested_revision":"2","computed_revision":"0","source_generation":"1"}`, "", false},
		{"same mask", `,"calculation_provenance":{"requested_revision":"1","computed_revision":"1","source_generation":"1","build_id":"11111111-1111-1111-1111-111111111111"}`, "", true},
		{"canonical reuse", `,"calculation_provenance":{"requested_revision":"2","computed_revision":"0","source_generation":"1","compatibility":{"kind":"indexed_absence","computed_revision":"0","requested_revision":"2","indexed_through":"2","start":"2026-11-15T10:00:00Z","end":"2026-11-15T11:00:00Z"}}`, "", true},
		{"incomplete index", `,"calculation_provenance":{"requested_revision":"2","computed_revision":"0","source_generation":"1","compatibility":{"kind":"indexed_absence","computed_revision":"0","requested_revision":"2","indexed_through":"1","start":"2026-11-15T10:00:00Z","end":"2026-11-15T11:00:00Z"}}`, "", false},
		{"missing comparison", `,"calculation_provenance":{"requested_revision":"2","computed_revision":"0","source_generation":"1","compatibility":{"kind":"equal_union","computed_revision":"0","requested_revision":"2","indexed_through":"2","start":"2026-11-15T10:00:00Z","end":"2026-11-15T11:00:00Z"}}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.extra != "" && tc.quality == "" {
				tc.quality = `,"excluded_intervals":2,"excluded_good":null,"excluded_total":null`
			}
			wire := maintenanceSampleJSON(tc.extra, tc.quality)
			var sample api.SloOperationalSample
			if err := json.Unmarshal([]byte(wire), &sample); err != nil {
				t.Fatal(err)
			}
			if got := validOperationalSample(sample); got != tc.valid {
				t.Fatalf("accepted=%v, want %v", got, tc.valid)
			}
		})
	}
}

func TestMaintenanceClientRejectsNullProvenance(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"generation":{"generation_id":%q,"source_generation":"1","definition_revision":"1","measurement_revision":"1","exclusion_revision":"0","calculation_version":"1","evaluated_through":"2026-11-15T11:00:00Z","published_at":"2026-11-15T11:00:00Z","calculation_lag_seconds":0},"lane":"budget","offset":0,"next_offset":null,"total_values":1,"samples":[%s],"days":[]}`, id, maintenanceSampleJSON(`,"calculation_provenance":null`, ""))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := New(ops, func(context.Context, *http.Request) error { return nil })
	if _, _, err := client.Do(t.Context(), Input{Kind: "slos", Action: "values", ID: id, GenerationID: id, Lane: "budget", Limit: 50}); err == nil {
		t.Fatal("explicit null provenance accepted as legacy omission")
	}
}

func TestMaintenanceExcludedSample(t *testing.T) {
	wire := maintenanceSampleJSON("", `,"excluded_intervals":60,"excluded_good":null,"excluded_total":null`)
	wire = strings.ReplaceAll(strings.ReplaceAll(wire, `"state":"observed"`, `"state":"excluded"`), `"state":"complete"`, `"state":"excluded"`)
	var sample api.SloOperationalSample
	if err := json.Unmarshal([]byte(wire), &sample); err != nil {
		t.Fatal(err)
	}
	sample.DisplayCoverage = &api.SloDisplayCoverage{States: []api.SloDisplayCoverageStates{"excluded"}, QualityStates: []api.SloDisplayCoverageQualityStates{"excluded"}, Freshness: []api.SloDisplayCoverageFreshness{"fresh"}}
	if !validOperationalSample(sample) || !validUrgency(api.SloUrgencyReason{Code: "excluded"}) {
		t.Fatal("excluded sample/reason rejected")
	}
	sample.DisplayCoverage.States = append(sample.DisplayCoverage.States, "excluded")
	if validOperationalSample(sample) {
		t.Fatal("duplicate display presence accepted")
	}
}

func TestMaintenancePopulationWireRoundTrip(t *testing.T) {
	for _, suffix := range []string{"", `,"excluded_intervals":2,"excluded_good":null,"excluded_total":null`, `,"excluded_intervals":2,"excluded_good":"0","excluded_total":"0"`} {
		var sample api.SloOperationalSample
		if err := json.Unmarshal([]byte(maintenanceSampleJSON("", suffix)), &sample); err != nil {
			t.Fatal(err)
		}
		for name, marshal := range map[string]func(any) ([]byte, error){"v2": func(v any) ([]byte, error) { return json.Marshal(v) }, "v1": jsonv1.Marshal} {
			t.Run(name+suffix, func(t *testing.T) {
				data, err := marshal(sample.Quality)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]jsontext.Value
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				if suffix == "" {
					if _, ok := fields["excluded_good"]; ok {
						t.Fatal("legacy omission became supplied metadata")
					}
					return
				}
				want := `null`
				if strings.Contains(suffix, `"excluded_good":"0"`) {
					want = `"0"`
				}
				if string(fields["excluded_good"]) != want || string(fields["excluded_total"]) != want {
					t.Fatalf("lost null/zero pair: %s", data)
				}
			})
		}
	}
}

func maintenanceSampleJSON(extra, quality string) string {
	return `{"bucket_start":"2026-11-15T10:00:00Z","bucket_end":"2026-11-15T11:00:00Z","evaluation_at":"2026-11-15T11:00:00Z","revision":"1","state":"observed","retained_extrema":[],"quality":{"state":"complete","freshness":"fresh","expected_intervals":58,"observed_intervals":58,"known_empty_intervals":0,"missing_intervals":0,"expired_intervals":0,"zero_traffic_intervals":0,"coverage":"1","first_observation_at":null,"last_observation_at":null,"latest_receipt_at":null` + quality + `}` + extra + `}`
}
