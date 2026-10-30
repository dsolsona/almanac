package sloclient

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

func TestCreateSLOUsesExplicitKeyAndConfirmsETag(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/slos" || r.Header.Get("Idempotency-Key") != "stable-key" || r.Header.Get("X-CSRF-Token") != "csrf" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("ETag", `"slo:`+id+`:1"`)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + id + `","name":"availability","display_name":"Availability","version":"1","definition_revision":"1","service_id":"22222222-2222-2222-2222-222222222222","service_display_name":"Checkout","sli_id":"33333333-3333-3333-3333-333333333333","sli_display_name":"Requests","target":"0.999","rolling_window_seconds":2592000,"cadence_seconds":60,"activated_at":"2026-01-01T00:00:00Z","evaluation_valid_from":"2026-01-01T00:00:00Z","historical_requested_by":null,"historical_requested_at":null,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	editor := func(_ context.Context, r *http.Request) error { r.Header.Set("X-CSRF-Token", "csrf"); return nil }
	body := []byte(`{"name":"availability","display_name":"Availability","service_id":"22222222-2222-2222-2222-222222222222","sli_id":"33333333-3333-3333-3333-333333333333","target":"0.999","rolling_window_seconds":2592000}`)
	_, tag, err := New(ops, editor).Do(t.Context(), Input{Kind: "slos", Action: "create", Body: body, Key: "stable-key"})
	if err != nil || tag != `"slo:`+id+`:1"` || calls != 1 {
		t.Fatalf("tag=%q calls=%d err=%v", tag, calls, err)
	}
}

func TestMalformedPageAndEvaluationAreRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/evaluation") {
			_, _ = w.Write([]byte(`{"timeline":{"items":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":null,"next_cursor":""}`))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := New(ops, func(context.Context, *http.Request) error { return nil })
	for _, input := range []Input{{Kind: "slos", Action: "list", Limit: 50}, {Kind: "slos", Action: "evaluate", ID: "11111111-1111-1111-1111-111111111111"}} {
		_, _, err = client.Do(t.Context(), input)
		failure, ok := errors.AsType[*datasourceclient.Failure](err)
		if !ok || failure.Code != "invalid_response" {
			t.Fatalf("expected invalid response, got %v", err)
		}
	}
}

func TestHistoricalValidationUsesSafeGuidance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_request","request_id":"0123456789abcdef0123456789abcdef","field_errors":[{"field":"evaluation_valid_from","code":"historical_start_invalid","message":"untrusted text"}]}`))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Kind: "slos", Action: "create", Key: "key", Body: []byte(`{"name":"availability","display_name":"Availability","service_id":"22222222-2222-2222-2222-222222222222","sli_id":"33333333-3333-3333-3333-333333333333","target":"0.99","rolling_window_seconds":60}`)}
	_, _, err = New(ops, func(context.Context, *http.Request) error { return nil }).Do(t.Context(), input)
	failure, ok := errors.AsType[*datasourceclient.Failure](err)
	if !ok || !strings.Contains(failure.Message, "historical start") || strings.Contains(failure.Message, "untrusted") || failure.RequestID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected safe failure: %v", err)
	}
}
func TestReadFailureDoesNotEchoBackendData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"code":"secret-value"}`))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = New(ops, func(context.Context, *http.Request) error { return nil }).Do(t.Context(), Input{Kind: "services", Action: "list", Limit: 50})
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatal("unsafe error")
	}
}

func TestOperationalOverviewUsesServerFiltersAndValidatesProjection(t *testing.T) {
	serviceID := "22222222-2222-2222-2222-222222222222"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/slos/overview" || r.URL.Query().Get("q") != "checkout api" || r.URL.Query().Get("service_id") != serviceID || r.URL.Query().Get("urgency") != "watch" || r.URL.Query().Get("limit") != "25" || r.URL.Query().Get("cursor") != "opaque" {
			t.Errorf("unexpected overview request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"snapshot_id":"11111111-1111-1111-1111-111111111111","items":[],"group_counts":{"needs_attention":2,"watch":3,"healthy":4},"next_cursor":"","previous_cursor":""}`))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := New(ops, func(context.Context, *http.Request) error { return nil })
	data, _, err := client.Do(t.Context(), Input{Kind: "slos", Action: "overview", Query: "checkout api", ServiceID: serviceID, Urgency: "watch", Limit: 25, Cursor: "opaque"})
	if err != nil || calls != 1 || !strings.Contains(string(data), `"watch":3`) {
		t.Fatalf("calls=%d data=%s err=%v", calls, data, err)
	}
}

func TestOperationalOverviewAccepts128UnicodeCharacters(t *testing.T) {
	query := strings.Repeat("é", 65)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.URL.Query().Get("q"); got != query {
			t.Errorf("query rune round trip = %q, want %q", got, query)
		}
		_, _ = w.Write([]byte(`{"snapshot_id":"11111111-1111-1111-1111-111111111111","items":[],"group_counts":{"needs_attention":0,"watch":0,"healthy":0},"next_cursor":"","previous_cursor":""}`))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := New(ops, func(context.Context, *http.Request) error { return nil })
	if _, _, err := client.Do(t.Context(), Input{Kind: "slos", Action: "overview", Query: query, Limit: 50}); err != nil || calls != 1 {
		t.Fatalf("65-character multibyte overview request calls=%d err=%v", calls, err)
	}
	if _, _, err := client.Do(t.Context(), Input{Kind: "slos", Action: "overview", Query: strings.Repeat("é", 129), Limit: 50}); err == nil || calls != 1 {
		t.Fatalf("129-character overview query was sent: calls=%d err=%v", calls, err)
	}
	if _, _, err := client.Do(t.Context(), Input{Kind: "slos", Action: "overview", Query: string([]byte{0xff}), Limit: 50}); err == nil || calls != 1 {
		t.Fatalf("invalid UTF-8 overview query was sent: calls=%d err=%v", calls, err)
	}
}

func TestOperationalOverviewUsesLargerBoundedResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"snapshot_id":"11111111-1111-1111-1111-111111111111","items":[],"group_counts":{"needs_attention":0,"watch":0,"healthy":0},"next_cursor":"","previous_cursor":""}`+strings.Repeat(" ", 5<<20))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := New(ops, func(context.Context, *http.Request) error { return nil }).Do(t.Context(), Input{Kind: "slos", Action: "overview", Limit: 50})
	if err != nil || len(data) <= 4<<20 || len(data) > 20<<20 {
		t.Fatalf("response bytes=%d err=%v", len(data), err)
	}
}

func TestOperationalReadRejectsResponseAboveBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"snapshot_id":"11111111-1111-1111-1111-111111111111","items":[],"group_counts":{"needs_attention":0,"watch":0,"healthy":0},"next_cursor":"","previous_cursor":""}`+strings.Repeat(" ", 20<<20))
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = New(ops, func(context.Context, *http.Request) error { return nil }).Do(t.Context(), Input{Kind: "slos", Action: "overview", Limit: 50})
	failure, ok := errors.AsType[*datasourceclient.Failure](err)
	if !ok || failure.Code != "invalid_response" {
		t.Fatalf("expected bounded invalid response, got %v", err)
	}
}

func TestOperationalDetailUsesExactRangeAndSelectedBurnWindow(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/slos/"+id+"/operational" || r.URL.Query().Get("start") != "2026-10-01T00:00:00Z" || r.URL.Query().Get("end") != "2026-10-02T00:00:00Z" {
			t.Errorf("unexpected operational request: %s %s", r.Method, r.URL.String())
		}
		want := "3600"
		if calls == 2 {
			want = "21600"
		}
		if got := r.URL.Query().Get("burn_window_seconds"); got != want {
			t.Errorf("burn window=%q, want %q", got, want)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	ops, err := api.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := New(ops, func(context.Context, *http.Request) error { return nil })
	for _, window := range []int{0, 21600} {
		_, _, err = client.Do(t.Context(), Input{Kind: "slos", Action: "operational", ID: id, Start: timestamp("2026-10-01T00:00:00Z"), End: timestamp("2026-10-02T00:00:00Z"), BurnWindowSeconds: window})
		failure, ok := errors.AsType[*datasourceclient.Failure](err)
		if !ok || failure.Code != "invalid_response" {
			t.Fatalf("expected response validation after request, got %v", err)
		}
	}
}

func timestamp(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return &parsed
}
