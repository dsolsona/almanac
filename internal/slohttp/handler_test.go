package slohttp

import (
	"crypto/tls"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

func TestObjectiveRoutesRequireSessionAndCSRF(t *testing.T) {
	auth, err := authhttp.New(&authentication.Service{}, authhttp.Options{PublicOrigin: "https://example.test", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New("postgres://example.test/db", auth, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/services", "/api/v1/slos"} {
		request := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil)
		request.TLS = &tls.ConnectionState{}
		request.RemoteAddr = "192.0.2.20:443"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated: %d", path, w.Code)
		}
		secret, _, err := identity.NewSessionSecret()
		if err != nil {
			t.Fatal(err)
		}
		request = httptest.NewRequest(http.MethodPost, "https://example.test"+path, nil)
		request.TLS = &tls.ConnectionState{}
		request.RemoteAddr = "192.0.2.20:443"
		request.Header.Set("Origin", "https://example.test")
		request.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: secret})
		w = httptest.NewRecorder()
		h.ServeHTTP(w, request)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s without CSRF: %d", path, w.Code)
		}
	}
}

func TestResourceETagAndCursorValidation(t *testing.T) {
	id := "b13e0739-3c86-4a7f-b7e5-c15e9626e71c"
	for _, value := range []string{`W/"slo:` + id + `:1"`, `"slo:` + id + `:01"`, `*`, `"service:` + id + `:1"`} {
		if _, ok := parseETag([]string{value}, "slo", id); ok {
			t.Fatalf("accepted invalid ETag %q", value)
		}
	}
	if v, ok := parseETag([]string{etag("slo", id, 2)}, "slo", id); !ok || v != 2 {
		t.Fatalf("valid ETag: %d %v", v, ok)
	}
	if _, _, _, ok := pageQuery(url.Values{"limit": {"201"}}, true); ok {
		t.Fatal("accepted oversized page")
	}
	if _, _, _, ok := pageQuery(url.Values{"service_id": {id}}, false); ok {
		t.Fatal("accepted service filter for services")
	}
}

func TestFieldProblemsHaveStableSafeCodes(t *testing.T) {
	cases := []struct {
		err         error
		field, code string
	}{
		{storage.ErrObjectiveTarget, "target", "target_invalid"},
		{storage.ErrObjectiveWindow, "rolling_window_seconds", "window_invalid"},
		{storage.ErrObjectiveHistoricalStart, "evaluation_valid_from", "historical_start_invalid"},
		{storage.ErrObjectiveTimelineRange, "timeline_start", "timeline_range_invalid"},
		{storage.ErrObjectiveEvaluationTime, "at", "evaluation_time_invalid"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		storageError(w, tc.err, "request-id")
		var p api.Problem
		if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Code != "invalid_request" || len(p.FieldErrors) != 1 || p.FieldErrors[0].Field != tc.field || p.FieldErrors[0].Code != tc.code {
			t.Errorf("%v: %d %+v", tc.err, w.Code, p)
		}
	}
}
