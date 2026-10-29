package sloclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
