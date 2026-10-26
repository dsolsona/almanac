package authhttp

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
)

func TestNewRejectsNonCanonicalPublicOrigins(t *testing.T) {
	for _, origin := range []string{"http://example.test", "https://example.test/", "https://user@example.test", "https://*.example.test", "https://example.test/path", "https://example.test:99999", "https://example.test:", "https://example.test?", "https://example.test:443", "https://[fe80::1%25eth0]"} {
		t.Run(origin, func(t *testing.T) {
			if _, err := New(&authentication.Service{}, Options{PublicOrigin: origin, Now: time.Now}); err == nil {
				t.Fatalf("New accepted noncanonical origin %q", origin)
			}
		})
	}
}

type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (deadlineRecorder) SetReadDeadline(time.Time) error { return nil }

type failingDeadlineRecorder struct{ *httptest.ResponseRecorder }

func (failingDeadlineRecorder) SetReadDeadline(time.Time) error {
	return errors.New("deadline unavailable")
}

func TestSourceAddressTrustBoundary(t *testing.T) {
	created, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := created.(*Handler)
	forwarded := httptest.NewRequest(http.MethodPost, "/", nil)
	forwarded.RemoteAddr = "10.2.3.4:443"
	forwarded.Header.Add("X-Forwarded-Proto", "https")
	forwarded.Header.Add("X-Forwarded-For", "198.51.100.9")
	if got, ok := h.requestSource(forwarded); !ok || got.String() != "198.51.100.9" {
		t.Fatalf("trusted forwarded source=%v ok=%t", got, ok)
	}
	for _, tc := range []struct {
		name           string
		forwardedFor   []string
		forwardedProto []string
	}{
		{name: "duplicate forwarded proto", forwardedFor: []string{"198.51.100.9"}, forwardedProto: []string{"https", "https"}},
		{name: "malformed forwarded address", forwardedFor: []string{"not-an-address"}, forwardedProto: []string{"https"}},
		{name: "ported forwarded address", forwardedFor: []string{"198.51.100.9:443"}, forwardedProto: []string{"https"}},
		{name: "zoned forwarded address", forwardedFor: []string{"fe80::1%eth0"}, forwardedProto: []string{"https"}},
		{name: "comma separated forwarding chain", forwardedFor: []string{"198.51.100.9, 203.0.113.8"}, forwardedProto: []string{"https"}},
		{name: "multiple forwarded headers", forwardedFor: []string{"198.51.100.9", "203.0.113.8"}, forwardedProto: []string{"https"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.RemoteAddr = "10.2.3.4:443"
			for _, value := range tc.forwardedProto {
				r.Header.Add("X-Forwarded-Proto", value)
			}
			for _, value := range tc.forwardedFor {
				r.Header.Add("X-Forwarded-For", value)
			}
			if _, ok := h.requestSource(r); ok {
				t.Fatal("invalid trusted forwarding accepted")
			}
		})
	}
	untrusted := httptest.NewRequest(http.MethodPost, "/", nil)
	untrusted.TLS = &tls.ConnectionState{}
	untrusted.RemoteAddr = "192.0.2.4:443"
	untrusted.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got, ok := h.requestSource(untrusted); !ok || got.String() != "192.0.2.4" {
		t.Fatalf("untrusted source=%v ok=%t", got, ok)
	}
	untrusted.TLS = nil
	if _, ok := h.requestSource(untrusted); ok {
		t.Fatal("untrusted plaintext accepted")
	}
}

func TestTypedRateLimitRetryAfterRoundsUp(t *testing.T) {
	created, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := created.(*Handler)
	for _, tc := range []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{name: "zero", duration: 0, want: "1"},
		{name: "subsecond", duration: time.Nanosecond, want: "1"},
		{name: "exact second", duration: time.Second, want: "1"},
		{name: "fraction above second", duration: time.Second + time.Nanosecond, want: "2"},
		{name: "fraction below minute", duration: time.Minute - time.Nanosecond, want: "60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.mapError(w, &authentication.RateLimitError{RetryAfter: tc.duration}, "0123456789abcdef0123456789abcdef", "")
			if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != tc.want {
				t.Fatalf("status=%d retry-after=%q want=%q", w.Code, w.Header().Get("Retry-After"), tc.want)
			}
		})
	}
}

func TestDeadlineFailureAndOversizedBodyFailClosed(t *testing.T) {
	created, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := created.(*Handler)
	for _, tc := range []struct {
		name   string
		writer http.ResponseWriter
		body   string
		want   int
	}{
		{name: "deadline", writer: failingDeadlineRecorder{httptest.NewRecorder()}, body: `{}`, want: http.StatusServiceUnavailable},
		{name: "large", writer: deadlineRecorder{httptest.NewRecorder()}, body: strings.Repeat("x", bodyLimit+1), want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/context", strings.NewReader(tc.body))
			r.TLS = &tls.ConnectionState{}
			r.RemoteAddr = "192.0.2.1:443"
			r.Header.Set("Origin", h.origin)
			r.Header.Set("Content-Type", "application/json")
			h.ServeHTTP(tc.writer, r)
			if recorder, ok := tc.writer.(interface{ Result() *http.Response }); ok {
				if got := recorder.Result().StatusCode; got != tc.want {
					t.Fatalf("status=%d want=%d", got, tc.want)
				}
			}
		})
	}
}

func TestStorageFailureDoesNotBecomeAnonymousContext(t *testing.T) {
	service, err := authentication.New(t.Context(), "postgres://almanac:test@127.0.0.1:1/almanac?sslmode=disable", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(service, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := handler.(*Handler)
	secret, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/context", strings.NewReader(`{}`))
	r.TLS = &tls.ConnectionState{}
	r.RemoteAddr = "192.0.2.1:443"
	r.Header.Set("Origin", h.origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: secret})
	w := deadlineRecorder{httptest.NewRecorder()}
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || len(w.Result().Cookies()) != 0 {
		t.Fatalf("storage failure status=%d cookies-cleared=%t", w.Code, len(w.Result().Cookies()) != 0)
	}
}

func TestLogoutStorageFailurePreservesSessionCookie(t *testing.T) {
	service, err := authentication.New(t.Context(), "postgres://almanac:test@127.0.0.1:1/almanac?sslmode=disable", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	created, err := New(service, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := created.(*Handler)
	secret, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := csrfNonce()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(`{}`))
	r.TLS = &tls.ConnectionState{}
	r.RemoteAddr = "192.0.2.1:443"
	r.Header.Set("Origin", h.origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", h.csrf("session", secret, nonce))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: secret})
	w := deadlineRecorder{httptest.NewRecorder()}
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || len(w.Result().Cookies()) != 0 {
		t.Fatalf("logout storage failure status=%d set-cookie-count=%d", w.Code, len(w.Result().Cookies()))
	}
}

func TestOriginAndStrictJSONFailBeforeAuthentication(t *testing.T) {
	created, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := created.(*Handler)
	for _, tc := range []struct {
		name, origin, body string
		duplicateOrigin    bool
		status             int
	}{
		{name: "missing origin", body: `{}`, status: http.StatusForbidden},
		{name: "cross origin", origin: "https://attacker.example", body: `{}`, status: http.StatusForbidden},
		{name: "duplicate key", origin: "https://almanac.example", body: `{"login":"a","login":"b","password":"x"}`, status: http.StatusBadRequest},
		{name: "unknown key", origin: "https://almanac.example", body: `{"login":"a","password":"x","extra":true}`, status: http.StatusBadRequest},
		{name: "null password", origin: "https://almanac.example", body: `{"login":"a","password":null}`, status: http.StatusBadRequest},
		{name: "null login", origin: "https://almanac.example", body: `{"login":null,"password":"x"}`, status: http.StatusBadRequest},
		{name: "invalid utf8", origin: "https://almanac.example", body: string([]byte{'{', '"', 'l', 'o', 'g', 'i', 'n', '"', ':', '"', 0xff, '"', '}'}), status: http.StatusBadRequest},
		{name: "duplicate origin", origin: "https://almanac.example", duplicateOrigin: true, body: `{}`, status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(tc.body))
			r.TLS = &tls.ConnectionState{}
			r.RemoteAddr = net.JoinHostPort("192.0.2.10", "443")
			r.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				r.Header.Add("Origin", tc.origin)
			}
			if tc.duplicateOrigin {
				r.Header.Add("Origin", tc.origin)
			}
			w := deadlineRecorder{httptest.NewRecorder()}
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestHTTPDecodeMediaAndCanonicalPathRejections(t *testing.T) {
	created, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := created.(*Handler)
	for _, tc := range []struct {
		name, path, body, contentType, contentEncoding string
		want                                           int
	}{
		{name: "unsupported media type", path: "/api/v1/auth/context", body: `{}`, contentType: "text/plain", want: http.StatusUnsupportedMediaType},
		{name: "unsupported content encoding", path: "/api/v1/auth/context", body: `{}`, contentType: "application/json", contentEncoding: "gzip", want: http.StatusUnsupportedMediaType},
		{name: "trailing JSON value", path: "/api/v1/auth/login", body: `{"login":"a","password":"b"} {}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "wrong non-null member type", path: "/api/v1/auth/login", body: `{"login":7,"password":"b"}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "empty object", path: "/api/v1/auth/login", body: `{}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "null object", path: "/api/v1/auth/login", body: `null`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "encoded route alias", path: "/api/v1/auth/%63ontext", body: `{}`, contentType: "application/json", want: http.StatusNotFound},
		{name: "dot segment alias", path: "/api/v1/auth/./context", body: `{}`, contentType: "application/json", want: http.StatusNotFound},
		{name: "parent segment alias", path: "/api/v1/auth/../auth/context", body: `{}`, contentType: "application/json", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r.TLS = &tls.ConnectionState{}
			r.RemoteAddr = "192.0.2.1:443"
			r.Header.Set("Origin", h.origin)
			r.Header.Set("Content-Type", tc.contentType)
			if tc.contentEncoding != "" {
				r.Header.Set("Content-Encoding", tc.contentEncoding)
			}
			w := deadlineRecorder{httptest.NewRecorder()}
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
		})
	}
}

func TestCSRFTokenIsBoundToPurposeAndCookie(t *testing.T) {
	created, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	h := created.(*Handler)
	preauth, err := randomPreauth()
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := csrfNonce()
	if err != nil {
		t.Fatal(err)
	}
	otherPreauth, err := randomPreauth()
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{h.csrf("session", preauth, nonce), h.csrf("login", otherPreauth, nonce), "!", "", strings.Repeat("A", 86)} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"login":"x","password":"x"}`))
		r.TLS = &tls.ConnectionState{}
		r.RemoteAddr = net.JoinHostPort("192.0.2.10", "443")
		r.Header.Set("Origin", h.origin)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: preauthCookie, Value: preauth})
		r.Header.Set("X-CSRF-Token", token)
		w := deadlineRecorder{httptest.NewRecorder()}
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("CSRF rejection status=%d want=403", w.Code)
		}
	}
	sessionA, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	sessionB, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	sessionNonce, err := csrfNonce()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(`{"current_password":"x","new_password":"y"}`))
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = net.JoinHostPort("192.0.2.10", "443")
	request.Header.Set("Origin", h.origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", h.csrf("session", sessionA, sessionNonce))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionB})
	w := deadlineRecorder{httptest.NewRecorder()}
	h.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-session CSRF status=%d want=403", w.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(`{}`))
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = net.JoinHostPort("192.0.2.10", "443")
	request.Header.Set("Origin", h.origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", h.csrf("login", preauth, nonce))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionA})
	request.AddCookie(&http.Cookie{Name: preauthCookie, Value: preauth})
	w = deadlineRecorder{httptest.NewRecorder()}
	h.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("session logout accepted pre-auth token: status=%d", w.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"login":"x","password":"x"}`))
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = net.JoinHostPort("192.0.2.10", "443")
	request.Header.Set("Origin", h.origin)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: preauthCookie, Value: preauth})
	request.Header.Add("X-CSRF-Token", h.csrf("login", preauth, nonce))
	request.Header.Add("X-CSRF-Token", h.csrf("login", preauth, nonce))
	w = deadlineRecorder{httptest.NewRecorder()}
	h.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("duplicate CSRF headers status=%d", w.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/context", strings.NewReader(`{}`))
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = net.JoinHostPort("192.0.2.10", "443")
	request.Header.Set("Origin", h.origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", sessionCookie+"=; "+sessionCookie+"=")
	w = deadlineRecorder{httptest.NewRecorder()}
	h.ServeHTTP(w, request)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate empty session cookies status=%d", w.Code)
	}
}

func TestIdempotencyKeyIsRejectedBeforeServiceWork(t *testing.T) {
	h, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/context", strings.NewReader(`not json`))
	r.TLS = &tls.ConnectionState{}
	r.RemoteAddr = net.JoinHostPort("192.0.2.10", "443")
	r.Header.Set("Origin", h.(*Handler).origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "request-1")
	w := deadlineRecorder{httptest.NewRecorder()}
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), `"capability_unavailable"`) {
		t.Fatalf("idempotency response status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHandlerOwnsOnlyFourPostRoutes(t *testing.T) {
	h, err := New(&authentication.Service{}, Options{PublicOrigin: "https://almanac.example", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
		allow        string
	}{
		{http.MethodGet, "/api/v1/auth/context", http.StatusMethodNotAllowed, http.MethodPost},
		{http.MethodPost, "/api/v1/auth/unknown", http.StatusNotFound, ""},
		{http.MethodPost, "/livez", http.StatusNotFound, ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("security headers = %v", w.Header())
			}
			if w.Header().Get("Allow") != tc.allow {
				t.Fatalf("Allow = %q, want %q", w.Header().Get("Allow"), tc.allow)
			}
			requestID := w.Header().Get("X-Request-ID")
			if len(requestID) != 32 || !strings.Contains(w.Body.String(), `"request_id":"`+requestID+`"`) {
				t.Fatal("response request ID was absent or did not match the problem body")
			}
		})
	}
}
