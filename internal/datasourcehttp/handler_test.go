package datasourcehttp

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
)

func TestDatasourceETagRequiresOneCanonicalStrongVersion(t *testing.T) {
	const id = "123e4567-e89b-42d3-a456-426614174000"
	valid := datasourceETag(id, 12)
	if !singleETag([]string{valid}, id) {
		t.Fatalf("singleETag(%q) = false, want true", valid)
	}
	for _, values := range [][]string{
		{`W/"datasource:` + id + `:12"`},
		{`"datasource:` + id + `:012"`},
		{`"datasource:` + id + `:12", "datasource:` + id + `:12"`},
		{`"datasource:` + id + `:12"`, `"datasource:` + id + `:13"`},
		{`"datasource:123e4567-e89b-42d3-a456-426614174001:12"`},
		{`"datasource:` + id + `:0"`},
	} {
		if singleETag(values, id) {
			t.Errorf("singleETag(%q) = true, want false", values)
		}
	}
}

func TestProvidersRouteUsesAuthenticatedSessionAndReturnsPrometheusDiagnostics(t *testing.T) {
	auth, err := authhttp.New(&authentication.Service{}, authhttp.Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New("postgres://unused", nil, auth, Options{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://almanac.example/api/v1/providers", nil)
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = netip.MustParseAddr("192.0.2.9").String() + ":443"
	request.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: secret})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"provider":"prometheus"`) || !strings.Contains(response.Body.String(), `"connection_test"`) || !strings.Contains(response.Body.String(), `"query_preview"`) {
		t.Fatalf("authenticated provider catalog = status %d body %s", response.Code, response.Body.String())
	}
}

func TestMutationHeadersRejectDuplicateIdempotencyKey(t *testing.T) {
	request := &http.Request{Header: make(http.Header)}
	request.Header.Add("Idempotency-Key", "first")
	request.Header.Add("Idempotency-Key", "second")
	if singlePrintableHeader(request.Header, "Idempotency-Key", 1, 128) {
		t.Fatal("duplicate Idempotency-Key accepted")
	}
	request.Header = make(http.Header)
	request.Header.Set("Idempotency-Key", "safe-123")
	if !singlePrintableHeader(request.Header, "Idempotency-Key", 1, 128) {
		t.Fatal("valid Idempotency-Key rejected")
	}
}
