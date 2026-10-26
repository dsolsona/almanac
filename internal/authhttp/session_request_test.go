package authhttp

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
)

func TestValidateSessionRequestSharesCookieOriginTransportAndCSRFRules(t *testing.T) {
	now := time.Now
	first, err := New(&authentication.Service{}, Options{PublicOrigin: "https://example.test", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(&authentication.Service{}, Options{PublicOrigin: "https://example.test", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	request := func(method string) *http.Request {
		r := httptest.NewRequest(method, "https://example.test/api/v1/agents", nil)
		r.TLS = &tls.ConnectionState{}
		r.RemoteAddr = netip.MustParseAddr("192.0.2.20").String() + ":443"
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: secret})
		return r
	}
	if got, err := first.ValidateSessionRequest(request(http.MethodGet)); err != nil || got.SessionSecret != secret || got.Source != netip.MustParseAddr("192.0.2.20") {
		t.Fatalf("same-origin-free GET request = %#v, %v", got, err)
	}
	post := request(http.MethodPost)
	post.Header.Set("Origin", "https://example.test")
	post.Header.Set("X-CSRF-Token", first.csrf("session", secret, [32]byte{1}))
	if _, err := first.ValidateSessionRequest(post); err != nil {
		t.Fatalf("valid session POST rejected: %v", err)
	}
	if _, err := other.ValidateSessionRequest(post); err != ErrRequestCSRF {
		t.Fatalf("foreign-handler CSRF error = %v", err)
	}
	noOrigin := request(http.MethodPost)
	if _, err := first.ValidateSessionRequest(noOrigin); err != ErrRequestOrigin {
		t.Fatalf("missing POST Origin error = %v", err)
	}
	duplicateCookie := request(http.MethodGet)
	duplicateCookie.AddCookie(&http.Cookie{Name: sessionCookie, Value: secret})
	if _, err := first.ValidateSessionRequest(duplicateCookie); err != ErrSessionRequest {
		t.Fatalf("duplicate session cookie error = %v", err)
	}
	wrongOrigin := request(http.MethodGet)
	wrongOrigin.Header.Add("Origin", "https://example.test")
	wrongOrigin.Header.Add("Origin", "https://evil.test")
	if _, err := first.ValidateSessionRequest(wrongOrigin); err != ErrRequestOrigin {
		t.Fatalf("duplicate Origin error = %v", err)
	}
	wrongPurpose := request(http.MethodPost)
	wrongPurpose.Header.Set("Origin", "https://example.test")
	wrongPurpose.Header.Set("X-CSRF-Token", first.csrf("login", secret, [32]byte{1}))
	if _, err := first.ValidateSessionRequest(wrongPurpose); err != ErrRequestCSRF {
		t.Fatalf("wrong-purpose CSRF error = %v", err)
	}
	wrongSession := request(http.MethodPost)
	wrongSession.Header.Set("Origin", "https://example.test")
	wrongSession.Header.Set("X-CSRF-Token", first.csrf("session", secret+"x", [32]byte{1}))
	if _, err := first.ValidateSessionRequest(wrongSession); err != ErrRequestCSRF {
		t.Fatalf("wrong-session CSRF error = %v", err)
	}
	duplicateCSRF := request(http.MethodPost)
	duplicateCSRF.Header.Set("Origin", "https://example.test")
	duplicateCSRF.Header.Add("X-CSRF-Token", first.csrf("session", secret, [32]byte{1}))
	duplicateCSRF.Header.Add("X-CSRF-Token", first.csrf("session", secret, [32]byte{2}))
	if _, err := first.ValidateSessionRequest(duplicateCSRF); err != ErrRequestCSRF {
		t.Fatalf("duplicate CSRF error = %v", err)
	}
	duplicatePreauth := request(http.MethodGet)
	duplicatePreauth.AddCookie(&http.Cookie{Name: preauthCookie, Value: "preauth-one"})
	duplicatePreauth.AddCookie(&http.Cookie{Name: preauthCookie, Value: "preauth-two"})
	if _, err := first.ValidateSessionRequest(duplicatePreauth); err != ErrSessionRequest {
		t.Fatalf("duplicate preauth error = %v", err)
	}
	spoofed := request(http.MethodGet)
	spoofed.TLS = nil
	spoofed.Header.Set("X-Forwarded-Proto", "https")
	spoofed.Header.Set("X-Forwarded-For", "203.0.113.20")
	if _, err := first.ValidateSessionRequest(spoofed); err != ErrRequestTLS {
		t.Fatalf("untrusted forwarding error = %v", err)
	}
	trusted, err := New(&authentication.Service{}, Options{PublicOrigin: "https://example.test", TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	trustedRequest := request(http.MethodGet)
	trustedRequest.TLS = nil
	trustedRequest.RemoteAddr = "10.2.3.4:443"
	trustedRequest.Header.Set("X-Forwarded-Proto", "https")
	trustedRequest.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got, err := trusted.ValidateSessionRequest(trustedRequest); err != nil || got.Source != netip.MustParseAddr("198.51.100.9") {
		t.Fatalf("trusted forwarded source=%v err=%v", got.Source, err)
	}
	if _, err := first.ValidateSessionRequest(httptest.NewRequest(http.MethodGet, "http://example.test/api/v1/agents", nil)); err != ErrRequestTLS {
		t.Fatalf("cleartext error = %v", err)
	}
	authorized := request(http.MethodGet)
	authorized.Header.Set("Authorization", "Bearer unsupported")
	if _, err := first.ValidateSessionRequest(authorized); err != ErrAuthorization {
		t.Fatalf("Authorization error = %v", err)
	}
}
