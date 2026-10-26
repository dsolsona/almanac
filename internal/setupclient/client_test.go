package setupclient

import (
	"context"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testCookie = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const cookieAttributes = "; Path=/; Expires=Wed, 21 Oct 2037 07:28:00 GMT; Max-Age=600; Secure; HttpOnly; SameSite=Lax"

func TestNewRejectsNoncanonicalAndUnsafeOrigins(t *testing.T) {
	for _, origin := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/path", "https://example.test?x=1", "https://EXAMPLE.test", "https://example.test:443"} {
		t.Run(origin, func(t *testing.T) {
			if _, err := New(Options{BackendURL: origin, Login: "admin"}); err == nil {
				t.Fatal("New accepted invalid origin")
			}
		})
	}
}

func TestCompleteUsesVerifiedTLSAndAuthenticationSequence(t *testing.T) {
	const initial, permanent = "Initial-password-123\n", "Permanent-password-456\n"
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/context":
			if r.Header.Get("Origin") != "https://"+r.Host {
				t.Errorf("origin = %q", r.Header.Get("Origin"))
			}
			w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"preauth-csrf","session":null}`)
		case "/api/v1/auth/login":
			if r.Header.Get("X-CSRF-Token") != "preauth-csrf" {
				t.Errorf("login csrf = %q", r.Header.Get("X-CSRF-Token"))
			}
			w.Header().Add("Set-Cookie", "__Host-almanac-preauth=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0")
			var body struct {
				Login    string `json:"login"`
				Password string `json:"password"`
			}
			if err := json.UnmarshalRead(r.Body, &body); err != nil || body.Login != "admin" || body.Password != initial {
				t.Errorf("unexpected login body")
			}
			w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"restricted-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":true}}`)
		case "/api/v1/auth/password":
			if r.Header.Get("X-CSRF-Token") != "restricted-csrf" {
				t.Errorf("password csrf = %q", r.Header.Get("X-CSRF-Token"))
			}
			var body struct {
				Current string `json:"current_password"`
				New     string `json:"new_password"`
			}
			if err := json.UnmarshalRead(r.Body, &body); err != nil || body.Current != initial || body.New != permanent {
				t.Errorf("unexpected password body")
			}
			w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"normal-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":false}}`)
		case "/api/v1/auth/logout":
			if r.Header.Get("X-CSRF-Token") != "normal-csrf" {
				t.Errorf("logout csrf = %q", r.Header.Get("X-CSRF-Token"))
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected route %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if _, err := x509.ParseCertificate(server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
	client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: cert})
	if err != nil {
		t.Fatal("New:", err)
	}
	defer client.Close()
	result, err := client.Complete(t.Context(), initial, permanent)
	if err != nil {
		t.Fatal("Complete:", err)
	}
	if !result.Completed || result.SessionCleanup != "complete" {
		t.Fatalf("result = %#v", result)
	}
	want := []string{"/api/v1/auth/context", "/api/v1/auth/login", "/api/v1/auth/password", "/api/v1/auth/logout"}
	if fmt.Sprint(paths) != fmt.Sprint(want) {
		t.Fatalf("routes = %v; want %v", paths, want)
	}
}

func TestUncertainLoginDoesNotReplayOrContinue(t *testing.T) {
	var contexts, logins, passwordChanges int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/context":
			contexts++
			w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"csrf","session":null}`)
		case "/api/v1/auth/login":
			logins++
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"code":"authentication_unavailable","message":"hidden","request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`)
		case "/api/v1/auth/password":
			passwordChanges++
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/auth/logout":
			t.Error("logout followed uncertain login")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected route %q", r.URL.Path)
		}
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: ca})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Complete(t.Context(), "Initial-password-123", "Permanent-password-456")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || !failure.OutcomeUnknown || failure.Code != "outcome_unknown" {
		t.Fatalf("failure = %#v", err)
	}
	if contexts != 2 || logins != 1 || passwordChanges != 0 {
		t.Fatalf("context/login/password calls = %d/%d/%d", contexts, logins, passwordChanges)
	}
	if strings.Contains(failure.Message, "hidden") || failure.RequestID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("unsafe or missing failure metadata: %#v", failure)
	}
}

func TestCompleteHonorsCancelledContextBeforeNetwork(t *testing.T) {
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: ca})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Complete(ctx, "Initial-password-123", "Permanent-password-456")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || failure.Kind != KindCancelled || failure.OutcomeUnknown {
		t.Fatalf("failure = %#v", err)
	}
	if calls != 0 {
		t.Fatalf("requests after cancellation = %d", calls)
	}
}

func TestUntrustedCertificateFailsBeforeMutation(t *testing.T) {
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	client, err := New(Options{BackendURL: server.URL, Login: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Complete(t.Context(), "Initial-password-123", "Permanent-password-456")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || failure.Kind != KindRemote || failure.OutcomeUnknown || failure.SessionState != "anonymous" {
		t.Fatalf("failure = %#v", err)
	}
	if calls != 0 {
		t.Fatalf("requests reached handler = %d; want TLS rejection before HTTP", calls)
	}
}

func TestUncertainPasswordResponseStaysUnknownWithAuthenticatedContext(t *testing.T) {
	var contexts, logins, changes, logouts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/context":
			contexts++
			if contexts == 1 {
				w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
				_, _ = fmt.Fprint(w, `{"csrf_token":"preauth-csrf","session":null}`)
			} else {
				_, _ = fmt.Fprint(w, `{"csrf_token":"session-csrf","session":{"user_id":"user-1","login":"admin","role":"administrator","restricted":false}}`)
			}
		case "/api/v1/auth/login":
			logins++
			w.Header().Add("Set-Cookie", "__Host-almanac-preauth=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0")
			w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"restricted-csrf","session":{"user_id":"user-1","login":"admin","role":"administrator","restricted":true}}`)
		case "/api/v1/auth/password":
			changes++
			w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"code":"authentication_unavailable","message":"hidden","request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`)
		case "/api/v1/auth/logout":
			logouts++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected route %q", r.URL.Path)
		}
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: ca})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Complete(t.Context(), "Initial-password-123", "Permanent-password-456")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || !failure.OutcomeUnknown || failure.Code != "outcome_unknown" || failure.SessionState != "authenticated" {
		t.Fatalf("failure = %#v", err)
	}
	if contexts != 2 || logins != 1 || changes != 1 || logouts != 0 {
		t.Fatalf("context/login/password/logout calls = %d/%d/%d/%d", contexts, logins, changes, logouts)
	}
}

func TestLoginResponseFailuresDoNotReplayMutation(t *testing.T) {
	targetHits := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits++ }))
	defer target.Close()
	for _, tc := range []struct {
		name                       string
		status                     int
		body                       string
		cookie, badCookie, unknown bool
	}{
		{name: "malformed success", status: http.StatusOK, body: `{"csrf_token":`, cookie: true, unknown: true},
		{name: "missing cookie", status: http.StatusOK, body: `{"csrf_token":"csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":true}}`, unknown: true},
		{name: "cookie with domain", status: http.StatusOK, body: `{"csrf_token":"csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":true}}`, cookie: true, badCookie: true, unknown: true},
		{name: "redirect", status: http.StatusFound, cookie: false, unknown: true},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"code":"rate_limited","message":"hidden","request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`, unknown: false},
		{name: "partial problem", status: http.StatusServiceUnavailable, body: `{"code":"authentication_unavailable"`, unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var contexts, logins, logouts int
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/auth/context":
					contexts++
					w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
					_, _ = fmt.Fprint(w, `{"csrf_token":"csrf","session":null}`)
				case "/api/v1/auth/login":
					logins++
					if tc.cookie {
						attrs := cookieAttributes
						if tc.badCookie {
							attrs += "; Domain=example.test"
						}
						w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+attrs)
					}
					if tc.status == http.StatusFound {
						w.Header().Set("Location", target.URL+"/steal")
					}
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.body)
				case "/api/v1/auth/logout":
					logouts++
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected route %q", r.URL.Path)
				}
			}))
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: ca})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Complete(t.Context(), "Initial-password-123", "Permanent-password-456")
			failure, ok := errors.AsType[*Failure](err)
			if !ok || failure.OutcomeUnknown != tc.unknown {
				t.Fatalf("failure = %#v", err)
			}
			if contexts != 1+btoi(tc.unknown) || logins != 1 {
				t.Fatalf("context/login calls = %d/%d", contexts, logins)
			}
			if tc.name == "rate limited" && (failure.Kind != KindAuthentication || logouts != 1) {
				t.Fatalf("rate-limit cleanup/failure = %#v; logout calls=%d", failure, logouts)
			}
			if tc.unknown && logouts != 0 {
				t.Fatalf("logout after uncertain response: %d", logouts)
			}
		})
	}
	if targetHits != 0 {
		t.Fatalf("redirect target received %d requests", targetHits)
	}
}

func TestDroppedMutationResponsesAreNeverReplayed(t *testing.T) {
	for _, tc := range []struct{ name, mutation, response, recoveredState string }{
		{"login lost before headers", "login", "before", "anonymous"},
		{"login truncated after headers", "login", "after", "restricted"},
		{"password lost before headers", "password", "before", "restricted"},
		{"password truncated after headers", "password", "after", "authenticated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var contexts, logins, changes, logouts int
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/auth/context":
					contexts++
					if contexts == 1 {
						w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
						_, _ = fmt.Fprint(w, `{"csrf_token":"preauth-csrf","session":null}`)
						return
					}
					switch tc.recoveredState {
					case "anonymous":
						w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
						_, _ = fmt.Fprint(w, `{"csrf_token":"recovered-csrf","session":null}`)
					case "restricted":
						_, _ = fmt.Fprint(w, `{"csrf_token":"recovered-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":true}}`)
					case "authenticated":
						_, _ = fmt.Fprint(w, `{"csrf_token":"recovered-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":false}}`)
					}
				case "/api/v1/auth/login":
					logins++
					if tc.mutation == "login" {
						if tc.response == "after" {
							w.Header().Add("Set-Cookie", "__Host-almanac-preauth=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0")
						}
						failTransportResponse(t, w, tc.response, "__Host-almanac-session")
						return
					}
					w.Header().Add("Set-Cookie", "__Host-almanac-preauth=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0")
					w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
					_, _ = fmt.Fprint(w, `{"csrf_token":"restricted-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":true}}`)
				case "/api/v1/auth/password":
					changes++
					if tc.mutation == "password" {
						failTransportResponse(t, w, tc.response, "__Host-almanac-session")
						return
					}
					t.Error("password mutation followed a lost login response")
				case "/api/v1/auth/logout":
					logouts++
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected route %q", r.URL.Path)
				}
			}))
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: ca})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Complete(t.Context(), "Initial-password-123", "Permanent-password-456")
			failure, ok := errors.AsType[*Failure](err)
			if !ok || !failure.OutcomeUnknown || failure.Code != "outcome_unknown" {
				t.Fatalf("failure=%#v", err)
			}
			if failure.SessionState != tc.recoveredState {
				t.Fatalf("session state=%q; want %q", failure.SessionState, tc.recoveredState)
			}
			wantLogins, wantChanges := 1, 0
			if tc.mutation == "password" {
				wantChanges = 1
			}
			if contexts != 2 || logins != wantLogins || changes != wantChanges || logouts != 0 {
				t.Fatalf("context/login/password/logout=%d/%d/%d/%d", contexts, logins, changes, logouts)
			}
		})
	}
}

func failTransportResponse(t *testing.T, w http.ResponseWriter, when, cookieName string) {
	t.Helper()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("TLS test writer cannot hijack")
	}
	if when == "after" {
		w.Header().Add("Set-Cookie", cookieName+"="+testCookie+cookieAttributes)
		partial := `{"csrf_token":"lost-response"`
		w.Header().Set("Content-Length", fmt.Sprint(len(partial)+50))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, partial)
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		t.Fatal("hijack test connection failed")
	}
	_ = conn.Close()
}

func TestInitialContextBodyIsBounded(t *testing.T) {
	var logins int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			logins++
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: ca})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Complete(t.Context(), "Initial-password-123", "Permanent-password-456")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || failure.Kind != KindRemote || failure.OutcomeUnknown {
		t.Fatalf("failure = %#v", err)
	}
	if logins != 0 {
		t.Fatalf("login requests after oversized context body = %d", logins)
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
