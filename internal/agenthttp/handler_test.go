package agenthttp

import (
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentmanagement"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

func TestAgentRouteGrammar(t *testing.T) {
	valid := map[string]string{
		"/api/v1/agents": "collection",
		"/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee":                                                         "detail",
		"/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee/credentials":                                             "credentials",
		"/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee/disable":                                                 "disable",
		"/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee/credentials/rotate":                                      "rotate",
		"/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee/credentials/abcdefab-cdef-4abc-8def-abcdefabcdee/revoke": "revoke",
	}
	for route, want := range valid {
		got, _, ok := matchRoute(route)
		if !ok || got != want {
			t.Errorf("matchRoute(%q) = %q, %t; want %q", route, got, ok, want)
		}
	}
	for _, route := range []string{"/api/v1/agents/ABCDEFAB-CDEF-4ABC-8DEF-ABCDEFABCDEE", "/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee/credentials/", "/api/v1/agents/abcdefab-cdef-4abc-8def-abcdefabcdee/credentials/no/revoke", "/api/v1/agents//disable"} {
		if _, _, ok := matchRoute(route); ok {
			t.Errorf("noncanonical route %q matched", route)
		}
	}
}

func TestAgentRequestGrammar(t *testing.T) {
	id := "abcdefab-cdef-4abc-8def-abcdefabcdee"
	for _, raw := range []string{`"agent:` + id + `:1"`, `"agent:` + id + `:9223372036854775807"`} {
		version, ok := parseETag([]string{raw}, id)
		if !ok || version < 1 {
			t.Errorf("parseETag(%q) = %d,%t", raw, version, ok)
		}
	}
	for _, raw := range []string{`W/"agent:` + id + `:1"`, `*`, `"agent:` + id + `:0"`, `"agent:` + id + `:01"`, `"agent:` + id + `:+1"`, `"agent:` + id + `:9223372036854775808"`, `"agent:` + id + `:1", "agent:` + id + `:2"`} {
		if _, ok := parseETag([]string{raw}, id); ok {
			t.Errorf("accepted invalid If-Match %q", raw)
		}
	}
	if version, ok := parseETag([]string{`"agent:` + id + `:1"`}, "abcdefab-cdef-4abc-8def-abcdefabcdee"); !ok || version != 1 {
		t.Fatalf("canonical validator rejected valid tag")
	}
	for _, tc := range []struct {
		values []string
		want   bool
	}{
		{[]string{"a"}, true}, {[]string{"a", "b"}, false}, {[]string{""}, false},
		{[]string{strings.Repeat("x", 129)}, false}, {[]string{"has space"}, false}, {[]string{"comma,key"}, true},
	} {
		if got := validIdempotencyKey(tc.values); got != tc.want {
			t.Errorf("validIdempotencyKey(%q) = %t, want %t", tc.values, got, tc.want)
		}
	}
	if _, _, ok := pageQuery(map[string][]string{"limit": {"01"}}, false); ok {
		t.Fatal("accepted noncanonical limit")
	}
	if _, _, ok := pageQuery(map[string][]string{"limit": {"1", "2"}}, false); ok {
		t.Fatal("accepted duplicate limit")
	}
}

func TestIssuanceJSONKeepsLargeVersionsAsStringsAndOmitsMissingToken(t *testing.T) {
	value := api.AgentIssuance{Agent: api.Agent{Version: "9007199254740993"}, SecretState: api.AgentIssuanceSecretStateSecretUnavailable, Replayed: true}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["token"]; present {
		t.Fatalf("replay exposed token member: %s", data)
	}
	agent, ok := got["agent"].(map[string]any)
	if !ok || agent["version"] != "9007199254740993" {
		t.Fatalf("large version lost string precision: %s", data)
	}
}

func TestOptionalSecondsRequireCanonicalIntegerTokens(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  int64
		valid bool
	}{
		{`1`, 1, true}, {`0`, 0, true}, {`1.0`, 0, false}, {`1e2`, 0, false}, {`-0`, 0, false}, {`null`, 0, false}, {`"1"`, 0, false},
	} {
		got, ok := seconds(jsontext.Value(tc.raw), 31536000, true)
		if ok != tc.valid || (ok && got != tc.want) {
			t.Errorf("seconds(%s) = %d,%t; want %d,%t", tc.raw, got, ok, tc.want, tc.valid)
		}
	}
}

func TestAgentDTOMapsExactDecimalVersionsAndCounts(t *testing.T) {
	for _, version := range []int64{9007199254740991, 9007199254740992, 9007199254740993, math.MaxInt64} {
		got := agentDTO(storage.AgentRecord{ID: "abcdefab-cdef-4abc-8def-abcdefabcdee", Version: version})
		if got.Version != strconv.FormatInt(version, 10) {
			t.Errorf("agent version %d mapped to %q", version, got.Version)
		}
	}
	result := lifecycleDTO(storage.AgentLifecycleResult{Agent: storage.AgentRecord{ID: "abcdefab-cdef-4abc-8def-abcdefabcdee", Version: 1}})
	if result.RevokedCredentialCount != "0" || result.Credential != nil {
		t.Fatalf("lifecycle null/count mapping = %#v", result)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["revoked_credential_count"] != "0" {
		t.Fatalf("lifecycle count lost decimal-string shape: %s", data)
	}
	if _, present := got["credential"]; !present || got["credential"] != nil {
		t.Fatalf("lifecycle credential is not explicit null: %s", data)
	}
}

func TestManagementResponseHeadersAndMethodAllow(t *testing.T) {
	h := &Handler{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/agents", nil)
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, POST" || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" || recorder.Header().Get("X-Request-ID") == "" {
		t.Fatalf("method response status=%d headers=%v", recorder.Code, recorder.Header())
	}
}

func TestManagementErrorMappingIsFixedAndBounded(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{authentication.ErrUnauthenticated, 401, "unauthenticated"},
		{authentication.ErrForbidden, 403, "forbidden"},
		{storage.ErrAgentNotFound, 404, "not_found"},
		{storage.ErrAgentConflict, 409, "conflict"},
		{storage.ErrInvalidAgentInput, 422, "invalid_agent_input"},
		{errors.New("private dependency detail"), 503, "management_unavailable"},
	} {
		recorder := httptest.NewRecorder()
		h.writeError(recorder, tc.err, "0123456789abcdef0123456789abcdef")
		var problem api.Problem
		if recorder.Code != tc.status || json.Unmarshal(recorder.Body.Bytes(), &problem) != nil || problem.Code != tc.code || strings.Contains(recorder.Body.String(), "private dependency detail") {
			t.Errorf("error mapped unsafely (status=%d code=%q)", recorder.Code, problem.Code)
		}
	}
	recorder := httptest.NewRecorder()
	h.writeError(recorder, &authentication.RateLimitError{RetryAfter: time.Second + time.Nanosecond}, "0123456789abcdef0123456789abcdef")
	if recorder.Code != 429 || recorder.Header().Get("Retry-After") != "2" {
		t.Fatalf("rate-limit response status=%d retry-after=%q", recorder.Code, recorder.Header().Get("Retry-After"))
	}
}

func TestManagementPreflightFailureUsesSafeUnavailableProblem(t *testing.T) {
	secret, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	auth, err := authhttp.New(&authentication.Service{}, authhttp.Options{PublicOrigin: "https://example.test", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(&agentmanagement.Service{}, auth, Options{Preflight: func(context.Context) error { return errors.New("private database detail") }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/agents", nil)
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = "192.0.2.15:443"
	request.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: secret})
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	var problem api.Problem
	if recorder.Code != 503 || json.Unmarshal(recorder.Body.Bytes(), &problem) != nil || problem.Code != "management_unavailable" || strings.Contains(recorder.Body.String(), "private database detail") {
		t.Fatalf("preflight failure mapped unsafely (status=%d code=%q)", recorder.Code, problem.Code)
	}
}
