package maintenanceclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
)

func TestTimelinePreservesFractionalRange(t *testing.T) {
	start := time.Date(2026, 11, 18, 2, 0, 0, 123456000, time.UTC)
	end := start.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		query := req.URL.Query()
		if query.Get("start") != start.Format(time.RFC3339Nano) || query.Get("end") != end.Format(time.RFC3339Nano) {
			t.Errorf("wire timeline bounds: %s", req.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer server.Close()
	client, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Do(t.Context(), client, func(context.Context, *http.Request) error { return nil }, Input{Action: "timeline", ID: "89524066-c358-4fce-b1f0-f4e11f614e55", Limit: 20, Start: start, End: end})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleBodies(t *testing.T) {
	for _, tc := range []struct {
		action, body string
		valid        bool
	}{
		{"end", `{"reason":"done","end":"2026-11-18T02:00:00Z"}`, true},
		{"end", `{"reason":"done"}`, false},
		{"cancel", `{"reason":"postpone"}`, true},
		{"withdraw", `{"reason":"restore"}`, true},
		{"withdraw", `{"reason":"restore","end":null}`, false},
	} {
		if got := ValidBody(Input{Action: tc.action, Body: []byte(tc.body)}); got != tc.valid {
			t.Errorf("%s with %s: valid=%v, want %v", tc.action, tc.body, got, tc.valid)
		}
	}
}

type maintenanceDoer func(*http.Request) (*http.Response, error)

func (do maintenanceDoer) Do(req *http.Request) (*http.Response, error) { return do(req) }

func TestLifecycleUncertainWrite(t *testing.T) {
	const id = "89524066-c358-4fce-b1f0-f4e11f614e55"
	for _, tc := range []struct {
		name    string
		status  int
		code    string
		unknown bool
	}{
		{"connection lost", 0, "outcome_unknown", true},
		{"server failure", 503, "outcome_unknown", true},
		{"revision conflict", 409, "conflict", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client, err := api.NewClient("https://example.test", api.WithHTTPClient(maintenanceDoer(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.GetBody != nil || req.Header.Get("If-Match") != tag(id, "1") || req.Header.Get("Idempotency-Key") != "end-once" {
					t.Error("maintenance write can be automatically replayed or is missing prerequisites")
				}
				if tc.status == 0 {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})))
			if err != nil {
				t.Fatal(err)
			}
			_, err = Do(t.Context(), client, func(context.Context, *http.Request) error { return nil }, Input{
				Action: "end", ID: id, ETag: tag(id, "1"), Key: "end-once",
				Body: []byte(`{"reason":"done","end":"2026-11-18T02:00:00Z"}`),
			})
			failure, ok := errors.AsType[*datasourceclient.Failure](err)
			if !ok || failure.Code != tc.code || failure.Unknown != tc.unknown || calls != 1 {
				t.Fatalf("calls=%d, failure=%+v", calls, failure)
			}
		})
	}
}

func TestScopedPreviewUsesReadOnlySelectorRequest(t *testing.T) {
	const input = `{"scope":{"kind":"all_services"}}`
	client, err := api.NewClient("https://example.test", api.WithHTTPClient(maintenanceDoer(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/v1/scoped-maintenance/preview" ||
			req.URL.Query().Get("limit") != "20" || req.Header.Get("Idempotency-Key") != "" ||
			req.Header.Get("X-CSRF-Token") == "" {
			t.Errorf("wrong preview request: %s %s", req.Method, req.URL)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || string(body) != input {
			t.Errorf("preview body %s: %v", body, err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(
			`{"inventory_at":"2026-11-18T02:00:00Z","total":"0","ready":true,"items":[]}`))}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	editor := func(_ context.Context, req *http.Request) error {
		req.Header.Set("X-CSRF-Token", "example")
		return nil
	}
	result, err := DoScoped(t.Context(), client, editor, Input{Action: "preview", Scoped: true, Limit: 20, Body: []byte(input)})
	if err != nil || result.Impact == nil || !result.Impact.Ready || result.Impact.Total != "0" {
		t.Fatalf("preview result=%+v error=%v", result, err)
	}
}

func TestScopedImpactCountsDistinctSLOs(t *testing.T) {
	const id = "89524066-c358-4fce-b1f0-f4e11f614e55"
	for _, tc := range []struct {
		name, action, secondID string
		withPeriods, accepted  bool
	}{
		{name: "two periods for the same SLO", action: "impact", secondID: id, withPeriods: true, accepted: true},
		{name: "two SLOs exceed impact total", action: "impact", secondID: "89524066-c358-4fce-b1f0-f4e11f614e56", withPeriods: true},
		{name: "two rows exceed preview total", action: "preview", secondID: id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := make([]string, 0, 2)
			for i, sloID := range []string{id, tc.secondID} {
				period := ""
				if tc.withPeriods {
					period = fmt.Sprintf(`,"start":"2026-11-18T0%d:00:00Z","end":"2026-11-18T0%d:30:00Z"`, i+1, i+1)
				}
				members = append(members, fmt.Sprintf(`{"slo_id":"%s","service_id":"%s","slo_name":"Checkout availability","service_name":"Checkout"%s}`, sloID, id, period))
			}
			payload := `{"inventory_at":"2026-11-18T03:00:00Z","ready":true,"total":"1","items":[` + strings.Join(members, ",") + `]}`
			client, err := api.NewClient("https://example.test", api.WithHTTPClient(maintenanceDoer(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
			})))
			if err != nil {
				t.Fatal(err)
			}
			in := Input{Action: tc.action, Scoped: true, Limit: 20}
			if tc.action == "preview" {
				in.Body = []byte(`{"scope":{"kind":"all_services"}}`)
			} else {
				in.ID = id
			}
			result, err := DoScoped(t.Context(), client, func(context.Context, *http.Request) error { return nil }, in)
			if tc.accepted {
				if err != nil || result.Impact == nil || result.Impact.Total != "1" || len(result.Impact.Items) != 2 {
					t.Fatalf("impact result=%+v error=%v", result, err)
				}
			} else if failure, ok := errors.AsType[*datasourceclient.Failure](err); !ok || failure.Code != "management_unavailable" || failure.Status != http.StatusOK {
				t.Fatalf("expected an invalid response, got %v", err)
			}
		})
	}
}

func TestScopedWindowListAcceptsLargeValidPage(t *testing.T) {
	const recordStart = `{"id":"89524066-c358-4fce-b1f0-f4e11f614e55","title":"Work","reason":"`
	const recordEnd = `","scope":{"kind":"all_services"},"revision":"1","operation":"create","start":"2026-11-18T02:00:00Z","end":"2026-11-18T03:00:00Z"}`
	window := recordStart + strings.Repeat(`\u0001`, 4096) + recordEnd
	const pageSize = 200
	payload := `{"next_cursor":"next","items":[` + strings.Repeat(window+",", pageSize-1) + window + `]}`
	client, err := api.NewClient("https://example.test", api.WithHTTPClient(maintenanceDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	result, err := DoScoped(t.Context(), client, func(context.Context, *http.Request) error { return nil }, Input{Action: "list", Scoped: true, Limit: pageSize})
	if err != nil || result.Page == nil || len(result.Page.Items) != pageSize || result.Page.NextCursor == nil || *result.Page.NextCursor != "next" {
		t.Fatalf("large page rejected or incomplete: %v", err)
	}
}
