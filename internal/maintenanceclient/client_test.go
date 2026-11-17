package maintenanceclient

import (
	"context"
	"errors"
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
