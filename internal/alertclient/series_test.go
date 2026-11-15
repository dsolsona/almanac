package alertclient

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/api"
)

const objectiveJSON = `{"target":"0.999","cadence_seconds":60,"definition_valid_from":"2026-10-01T00:00:00Z","measurement_valid_from":"2026-10-01T00:00:00Z","retention_start":"2025-11-15T12:00:00Z"}`

var seriesJSON = `{"policy_id":"` + policyID + `","evaluation_revision":"9007199254740993","objective":` + objectiveJSON + `,"condition":{"id":"fast","short_window_seconds":300,"long_window_seconds":3600,"threshold":"14.4"},"start":"2026-11-15T10:00:00Z","end":"2026-11-15T12:00:00Z","points":[{"evaluation_id":"` + policyID + `","snapshot":` + snapshotJSON + `,"scheduled_at":"2026-11-15T12:00:00Z","committed_at":"2026-11-15T12:00:01Z","condition":{"id":"fast","threshold":"14.4","outcome":"matching","short":` + strings.Replace(windowJSON, "11:00:00", "11:55:00", 1) + `,"long":` + windowJSON + `}}],"gaps":[{"start":"2026-11-15T10:00:00Z","end":"2026-11-15T12:00:00Z","start_inclusive":true,"end_inclusive":false,"basis":"effective_cutoff","kind":"not_retained","reason":"no_retained_evaluation"}],"sampling":{"total":1,"returned":1,"reduced":false,"omitted_extrema":false,"method":"exact"}}`

func TestAlertActivationRangeAndSeriesResponses(t *testing.T) {
	rangeJSON := `{"items":[{"id":"` + policyID + `","kind":"before_policy_activation","reason":"before_policy_activation","start":"2026-11-15T11:58:00Z","end":"2026-11-15T11:59:00Z","recorded_at":"2026-11-15T12:00:00Z","cadence_seconds":60}]}`
	activationSeries := strings.ReplaceAll(strings.ReplaceAll(seriesJSON, `"kind":"not_retained"`, `"kind":"before_policy_activation"`), `"basis":"effective_cutoff"`, `"basis":"scheduled_boundary"`)
	for _, tc := range []struct {
		input Input
		body  string
	}{
		{Input{Kind: "alert-policies", Action: "ranges", ID: policyID, Revision: "1", Limit: 20}, rangeJSON},
		{Input{Kind: "alerts", Action: "series", ID: policyID, ConditionID: "fast"}, activationSeries},
	} {
		t.Run(tc.input.Action, func(t *testing.T) {
			ops, err := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			body, _, err := New(ops, nil).Do(t.Context(), tc.input)
			if err != nil || string(body) != tc.body {
				t.Fatalf("activation provenance rejected/changed: %s %v", body, err)
			}
		})
	}
	invalid := strings.ReplaceAll(activationSeries, `"basis":"scheduled_boundary"`, `"basis":"effective_cutoff"`)
	if validSeriesResponse([]byte(invalid), Input{Kind: "alerts", Action: "series", ID: policyID, ConditionID: "fast", Limit: 120}) {
		t.Fatal("scheduled activation skip accepted as cutoff gap")
	}
}

func TestAlertSeriesGeneratedRequestAndExactResponse(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit"}[explicit], func(t *testing.T) {
			in := Input{Kind: "alerts", Action: "series", ID: policyID, ConditionID: "fast"}
			if explicit {
				in.Start = "2026-11-15T10:00:00Z"
				in.End = "2026-11-15T12:00:00Z"
				in.Limit = 2
			}
			calls := 0
			ops, err := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				q := r.URL.Query()
				wantLimit := "120"
				if explicit {
					wantLimit = "2"
				}
				if r.Method != "GET" || r.URL.Path != "/api/v1/alerts/"+policyID+"/series" || q.Get("condition_id") != "fast" || q.Get("limit") != wantLimit || q.Get("start") != in.Start || q.Get("end") != in.End {
					t.Errorf("wrong request: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(seriesJSON))}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			got, tag, err := New(ops, nil).Do(t.Context(), in)
			if err != nil || calls != 1 || tag != "" || string(got) != seriesJSON {
				t.Fatalf("exact series response: %s %s %v calls%d", got, tag, err, calls)
			}
		})
	}
}

func TestAlertSeriesAndFilterValidation(t *testing.T) {
	base := Input{Kind: "alerts", Action: "series", ID: policyID, ConditionID: "fast"}
	for _, mutate := range []func(*Input){func(i *Input) { i.ConditionID = "" }, func(i *Input) { i.ConditionID = "\n" }, func(i *Input) { i.Limit = 1 }, func(i *Input) { i.Limit = 121 }, func(i *Input) { i.Start = "2026-11-15T10:00:00+00:00" }, func(i *Input) { i.Start = "2026-11-15T12:00:01Z"; i.End = "2026-11-15T12:00:00Z" }, func(i *Input) { i.Start = "2026-11-14T11:59:59Z"; i.End = "2026-11-15T12:00:00Z" }, func(i *Input) { i.Kind = "alert-policies"; i.Action = "list" }} {
		in := base
		mutate(&in)
		if ValidateOptions(in) == nil {
			t.Errorf("accepted invalid series options %+v", in)
		}
	}
	for _, duration := range []string{"-1", "01", "1e2", "9223372036854775808"} {
		if ValidateOptions(Input{Kind: "alerts", Action: "list", MinimumDuration: duration}) == nil {
			t.Errorf("accepted duration %q", duration)
		}
	}
	if ValidateOptions(Input{Kind: "alerts", Action: "list", Evidence: "unknown"}) == nil {
		t.Fatal("accepted unknown evidence")
	}
}

func TestAlertListGeneratedEvidenceAndDuration(t *testing.T) {
	ops, err := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if q.Get("evidence") != "waiting" || q.Get("minimum_duration_seconds") != "9223372036854775807" {
			t.Errorf("lost exact filters: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"items":[],"groups":[],"total":"0"}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(ops, nil).Do(t.Context(), Input{Kind: "alerts", Action: "list", Evidence: "waiting", MinimumDuration: "9223372036854775807"}); err != nil {
		t.Fatal(err)
	}
}

func TestAlertSeriesRejectsMalformedResponse(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"condition identity", strings.ReplaceAll(seriesJSON, `"id":"fast"`, `"id":"other"`)},
		{"numeric version", strings.Replace(seriesJSON, `"evaluation_revision":"9007199254740993"`, `"evaluation_revision":9007199254740993`, 1)},
		{"wrong requested bounds", strings.Replace(seriesJSON, `"start":"2026-11-15T10:00:00Z"`, `"start":"2026-11-15T09:00:00Z"`, 1)},
		{"wrong count", strings.Replace(seriesJSON, `"returned":1`, `"returned":2`, 1)},
		{"oversized source count", strings.Replace(seriesJSON, `"total":1`, `"total":1442`, 1)},
		{"missing sampling bool", strings.Replace(seriesJSON, `"reduced":false,`, ``, 1)},
		{"invented gap basis", strings.Replace(seriesJSON, `"basis":"effective_cutoff"`, `"basis":"scheduled_boundary"`, 1)},
		{"missing interval inclusion", strings.Replace(seriesJSON, `"end_inclusive":false,`, ``, 1)},
		{"numeric count", strings.ReplaceAll(seriesJSON, `"good":"9007199254740992.125"`, `"good":9007199254740992.125`)},
		{"nullable derived count", strings.ReplaceAll(seriesJSON, `"quality":"complete"`, `"quality":"complete","bad":null`)},
		{"mismatched window snapshot", strings.Replace(seriesJSON, `"source_generation":"9007199254740993"`, `"source_generation":"2"`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops, err := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			data, _, err := New(ops, nil).Do(t.Context(), Input{Kind: "alerts", Action: "series", ID: policyID, ConditionID: "fast", Start: "2026-11-15T10:00:00Z", End: "2026-11-15T12:00:00Z"})
			if err == nil || len(data) != 0 {
				t.Fatalf("malformed series accepted: %s %v", data, err)
			}
		})
	}
}

func TestAlertSeriesUnavailableEvidenceRemainsExact(t *testing.T) {
	for _, body := range []string{
		strings.ReplaceAll(strings.ReplaceAll(seriesJSON, `"quality":"complete"`, `"quality":"partial"`), `"outcome":"matching"`, `"outcome":"unavailable"`),
		strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(seriesJSON, `"good":"9007199254740992.125",`, ``), `"total":"9007199254740993.125"`, `"new_additive_field":null`), `"quality":"complete"`, `"quality":"missing"`), `"outcome":"matching"`, `"outcome":"unavailable"`),
	} {
		ops, err := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}))
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := New(ops, nil).Do(t.Context(), Input{Kind: "alerts", Action: "series", ID: policyID, ConditionID: "fast"})
		if err != nil || string(got) != body {
			t.Fatalf("unavailable evidence changed: %s %v", got, err)
		}
	}
}

func TestAlertListRequiresExactGroupFiring(t *testing.T) {
	for _, field := range []string{"", `,"firing":"2"`, `,"firing":1`} {
		body := `{"items":[],"groups":[{"slo_id":"` + sloID + `","slo_name":"Checkout","total":"1"` + field + `}],"total":"1"}`
		ops, err := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}))
		if err != nil {
			t.Fatal(err)
		}
		if data, _, err := New(ops, nil).Do(t.Context(), Input{Kind: "alerts", Action: "list"}); err == nil || len(data) > 0 {
			t.Errorf("invalid group firing accepted: %s %v", data, err)
		}
	}
}

func TestAlertSeriesPreservesFractionalInclusiveBounds(t *testing.T) {
	start, end := "2026-11-15T10:00:00.000000001Z", "2026-11-15T12:00:00.000000001Z"
	body := strings.Replace(seriesJSON, `"start":"2026-11-15T10:00:00Z"`, `"start":"`+start+`"`, 1)
	body = strings.Replace(body, `"end":"2026-11-15T12:00:00Z"`, `"end":"`+end+`"`, 1)
	ops, err := api.NewClient("https://almanac.test", api.WithHTTPClient(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("start") != start || r.URL.Query().Get("end") != end {
			t.Errorf("fractional boundary changed: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	if data, _, err := New(ops, nil).Do(t.Context(), Input{Kind: "alerts", Action: "series", ID: policyID, ConditionID: "fast", Start: start, End: end}); err != nil || string(data) != body {
		t.Fatalf("fractional response changed: %s %v", data, err)
	}
}
