package datasourcehttp

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
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

func TestPublicDiagnosticErrorCodePreservesSafeProviderCauses(t *testing.T) {
	for raw, want := range map[string]string{
		"authentication": "authentication_failed", "network": "network_failed", "tls": "tls_failed",
		"query": "query_failed", "incomplete": "query_failed", "timeout": "request_timeout",
		"deadline": "request_timeout", "assignment_revoked": "assignment_revoked",
		"rate_limited": "rate_limited", "unsupported": "unsupported", "private detail": "diagnostic_failed",
	} {
		if got := publicDiagnosticErrorCode(raw); got != want {
			t.Errorf("public diagnostic code for %q = %q, want %q", raw, got, want)
		}
	}
}

func TestConfigurationAdmissionProblemsAreRetryableAndSafe(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "pending", err: storage.ErrDiagnosticConfigurationPending, code: "configuration_pending"},
		{name: "failed", err: storage.ErrDiagnosticConfigurationFailed, code: "configuration_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			(&Handler{}).writeStorageError(response, test.err, "0123456789abcdef0123456789abcdef")
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("configuration admission problem = status %d body %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestProviderAvailabilitySeparatesCapabilityAndRuntimeReadiness(t *testing.T) {
	failureCode := "apply_failed"
	tests := []struct {
		name       string
		capability storage.DatasourceAgentCapability
		operations int
		want       api.ProviderDescriptorAvailability
	}{
		{name: "disabled", capability: storage.DatasourceAgentCapability{Reported: true, Enabled: false, Reachability: "connected"}, operations: 2, want: api.ProviderDescriptorAvailabilityDisabled},
		{name: "never reported", capability: storage.DatasourceAgentCapability{Enabled: true, Reachability: "unreachable"}, want: api.ProviderDescriptorAvailabilityUnreported},
		{name: "offline but compatible", capability: storage.DatasourceAgentCapability{Reported: true, Enabled: true, Reachability: "unreachable"}, operations: 2, want: api.ProviderDescriptorAvailabilityOffline},
		{name: "configuration applying", capability: storage.DatasourceAgentCapability{Reported: true, Enabled: true, Reachability: "connected", DesiredConfigurationRevision: new(int64(2)), AppliedConfigurationRevision: new(int64(1))}, operations: 2, want: api.ProviderDescriptorAvailabilityApplying},
		{name: "configuration failed", capability: storage.DatasourceAgentCapability{Reported: true, Enabled: true, Reachability: "connected", DesiredConfigurationRevision: new(int64(2)), AppliedConfigurationRevision: new(int64(1)), ConfigurationFailureCode: &failureCode, ConfigurationFailureRevision: new(int64(2))}, operations: 2, want: api.ProviderDescriptorAvailabilityConfigurationFailed},
		{name: "connected without capability", capability: storage.DatasourceAgentCapability{Reported: true, Enabled: true, Reachability: "connected"}, want: api.ProviderDescriptorAvailabilityUnsupported},
		{name: "available", capability: storage.DatasourceAgentCapability{Reported: true, Enabled: true, Reachability: "connected", DesiredConfigurationRevision: new(int64(2)), AppliedConfigurationRevision: new(int64(2))}, operations: 2, want: api.ProviderDescriptorAvailabilityAvailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := providerAvailability(test.capability, test.operations); got != test.want {
				t.Fatalf("providerAvailability() = %q, want %q", got, test.want)
			}
		})
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
