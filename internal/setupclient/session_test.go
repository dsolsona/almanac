package setupclient

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionClientReusesLoginAcrossProcessesAndLogsOut(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(fmt.Sprint(restricted), func(t *testing.T) {
			var logins, logouts atomic.Int32
			var revoked atomic.Bool
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Origin") != "https://"+r.Host || r.Header.Get("Idempotency-Key") != "" {
					t.Error("authentication headers differ")
				}
				w.Header().Set("Content-Type", "application/json")
				writeIdentity := func() {
					_, _ = fmt.Fprintf(w, `{"csrf_token":"session-csrf","session":{"user_id":"u-1","login":"owner","role":"administrator","restricted":%t}}`, restricted)
				}
				switch r.URL.Path {
				case "/api/v1/auth/context":
					cookie, err := r.Cookie("__Host-almanac-session")
					if err == nil && cookie.Value == testCookie && !revoked.Load() {
						writeIdentity()
						return
					}
					w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
					_, _ = fmt.Fprint(w, `{"csrf_token":"preauth-csrf","session":null}`)
				case "/api/v1/auth/login":
					logins.Add(1)
					if r.Header.Get("X-CSRF-Token") != "preauth-csrf" {
						t.Error("missing fresh login proof")
					}
					w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
					writeIdentity()
				case "/api/v1/auth/logout":
					logouts.Add(1)
					if r.Header.Get("X-CSRF-Token") != "session-csrf" {
						t.Error("missing fresh logout proof")
					}
					revoked.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Error("unexpected authentication operation")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			options := SessionOptions{BackendURL: server.URL, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}
			first, err := NewSession(options)
			if err != nil {
				t.Fatal("construct session client failed")
			}
			defer first.Close()
			identity, err := first.Login(t.Context(), "owner", "protected password")
			if err != nil || identity == nil || identity.Restricted != restricted || first.Session() != testCookie {
				t.Fatal("login did not establish the expected reusable session")
			}
			options.Session = first.Session()
			second, err := NewSession(options)
			if err != nil {
				t.Fatal("construct imported session client failed")
			}
			defer second.Close()
			identity, err = second.Context(t.Context())
			if err != nil || identity == nil || identity.UserId != "u-1" || identity.Restricted != restricted {
				t.Fatal("imported session did not resolve current identity")
			}
			if _, err := second.Login(t.Context(), "other", "another password"); err == nil || logins.Load() != 1 {
				t.Fatal("login silently replaced an active session")
			}
			if err := second.Logout(t.Context()); err != nil || second.Session() != "" || logouts.Load() != 1 {
				t.Fatal("logout did not revoke and clear the session")
			}
			if err := second.Logout(t.Context()); err != nil || logouts.Load() != 1 {
				t.Fatal("anonymous logout sent an unnecessary mutation")
			}
		})
	}
}

func TestAgentContextRefreshesAndRestrictsAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		restricted bool
		allow      bool
	}{
		{"administrator", "administrator", false, true},
		{"restricted administrator", "administrator", true, false},
		{"viewer", "viewer", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/auth/context" {
					t.Errorf("unexpected path %s", r.URL.Path)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"csrf_token":"fresh-csrf","session":{"user_id":"u-1","login":"admin","role":%q,"restricted":%t}}`, tc.role, tc.restricted)
			}))
			defer server.Close()
			client, err := NewSession(SessionOptions{BackendURL: server.URL, Session: testCookie, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})})
			if err != nil {
				t.Fatal("construct session client failed")
			}
			defer client.Close()
			ops, editor, err := client.AgentContext(t.Context())
			if tc.allow {
				if err != nil || ops == nil || editor == nil {
					t.Fatalf("administrator access rejected: %v", err)
				}
			} else if err == nil || ops != nil || editor != nil {
				t.Fatal("restricted or non-admin authority accepted")
			}
		})
	}
}

func TestSessionClientReconcilesMalformedLoginWithoutRepeating(t *testing.T) {
	var logins, contexts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/login" {
			logins.Add(1)
			w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{`)
			return
		}
		if r.URL.Path != "/api/v1/auth/context" {
			t.Error("unexpected operation")
			return
		}
		contexts.Add(1)
		if logins.Load() == 0 {
			w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"preauth","session":null}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"csrf_token":"session","session":{"user_id":"u-1","login":"owner","role":"administrator","restricted":false}}`)
	}))
	defer server.Close()
	client, err := NewSession(SessionOptions{BackendURL: server.URL, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})})
	if err != nil {
		t.Fatal("construct client failed")
	}
	defer client.Close()
	_, err = client.Login(t.Context(), "owner", "password")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || !failure.OutcomeUnknown || failure.SessionState != "authenticated" || client.Session() != testCookie || logins.Load() != 1 || contexts.Load() != 2 {
		t.Fatal("uncertain login was repeated or lost confirmed session recovery")
	}
}

func TestSessionClientUncertainLogoutRetainsOriginalCredential(t *testing.T) {
	var logouts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/context" {
			_, _ = fmt.Fprint(w, `{"csrf_token":"session","session":{"user_id":"u-1","login":"owner","role":"administrator","restricted":false}}`)
			return
		}
		if r.URL.Path != "/api/v1/auth/logout" {
			t.Error("unexpected operation")
			return
		}
		logouts.Add(1)
		w.Header().Add("Set-Cookie", "__Host-almanac-session=; Path=/; Max-Age=0; Secure; HttpOnly; SameSite=Lax")
		w.WriteHeader(http.StatusOK) // Invalid logout completion, regardless of cookie deletion.
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	client, err := NewSession(SessionOptions{BackendURL: server.URL, Session: testCookie, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})})
	if err != nil {
		t.Fatal("construct client failed")
	}
	defer client.Close()
	err = client.Logout(t.Context())
	failure, ok := errors.AsType[*Failure](err)
	if !ok || !failure.OutcomeUnknown || client.Session() != testCookie || logouts.Load() != 1 {
		t.Fatal("uncertain logout discarded recovery state or repeated the mutation")
	}
}

func TestSessionClientRejectsUnsafeConfigurationAndCancelledLogin(t *testing.T) {
	for _, options := range []SessionOptions{{BackendURL: "http://localhost"}, {BackendURL: "https://example.test", Session: "not-a-session"}, {BackendURL: "https://example.test/path"}} {
		if client, err := NewSession(options); err == nil {
			client.Close()
			t.Error("unsafe session configuration accepted")
		}
	}
	client, err := NewSession(SessionOptions{BackendURL: "https://example.test"})
	if err != nil {
		t.Fatal("construct client failed")
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Login(ctx, "owner", "password")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || failure.Kind != KindCancelled || failure.OutcomeUnknown {
		t.Fatal("cancelled pre-dispatch login was misclassified")
	}
}

func TestSessionClientReusesValidatedPreauthAfterUncertainLogin(t *testing.T) {
	var logins, contexts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/login" {
			logins.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{}`)
			return
		}
		contexts.Add(1)
		if _, err := r.Cookie("__Host-almanac-preauth"); err != nil {
			w.Header().Set("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
		}
		_, _ = fmt.Fprint(w, `{"csrf_token":"preauth","session":null}`)
	}))
	defer server.Close()
	client, err := NewSession(SessionOptions{BackendURL: server.URL, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})})
	if err != nil {
		t.Fatal("construct client failed")
	}
	defer client.Close()
	_, err = client.Login(t.Context(), "owner", "password")
	f, ok := errors.AsType[*Failure](err)
	if !ok || !f.OutcomeUnknown || f.SessionState != "anonymous" || logins.Load() != 1 || contexts.Load() != 2 || client.Session() != "" {
		t.Fatal("reused preauth prevented anonymous reconciliation")
	}
	if identity, err := client.Context(t.Context()); err != nil || identity != nil {
		t.Fatal("repeated anonymous context rejected existing preauth")
	}
}

func TestSessionClientNeverPersistsInvalidCookieOrReplaysRejectedLogin(t *testing.T) {
	for _, scenario := range []string{"insecure-session", "duplicate-session", "bad-preauth", "rejected"} {
		t.Run(scenario, func(t *testing.T) {
			var logins atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/context" {
					attrs := cookieAttributes
					if scenario == "bad-preauth" {
						attrs = strings.ReplaceAll(attrs, "; HttpOnly", "")
					}
					w.Header().Set("Set-Cookie", "__Host-almanac-preauth="+testCookie+attrs)
					if _, err := r.Cookie("__Host-almanac-session"); err == nil {
						t.Error("invalid session cookie reached reconciliation")
					}
					_, _ = fmt.Fprint(w, `{"csrf_token":"preauth","session":null}`)
					return
				}
				logins.Add(1)
				if scenario == "rejected" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = fmt.Fprint(w, `{"code":"unauthenticated","message":"private server message","request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`)
					return
				}
				attrs := cookieAttributes
				if scenario == "insecure-session" {
					attrs = strings.ReplaceAll(attrs, "; Secure", "")
				}
				w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+attrs)
				if scenario == "duplicate-session" {
					w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+attrs)
				}
				_, _ = fmt.Fprint(w, `{"csrf_token":"csrf","session":{"user_id":"u-1","login":"owner","role":"administrator","restricted":false}}`)
			}))
			defer server.Close()
			client, err := NewSession(SessionOptions{BackendURL: server.URL, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})})
			if err != nil {
				t.Fatal("construct client failed")
			}
			defer client.Close()
			_, err = client.Login(t.Context(), "owner", "password")
			f, ok := errors.AsType[*Failure](err)
			if !ok || client.Session() != "" || strings.Contains(f.Message, "private") {
				t.Fatal("invalid response exposed credential or unsafe error")
			}
			wantCalls := int32(1)
			if scenario == "bad-preauth" {
				wantCalls = 0
			}
			if logins.Load() != wantCalls {
				t.Fatal("rejected login sent unexpected mutations")
			}
			if scenario == "rejected" && (f.OutcomeUnknown || f.Kind != KindAuthentication || f.RequestID != "0123456789abcdef0123456789abcdef") {
				t.Fatal("definitive rejection classified as uncertain")
			}
		})
	}
}

func TestSessionClientTransportLossAndCancellationNeverReplay(t *testing.T) {
	for _, scenario := range []string{"no-headers", "truncated-body", "reconcile-fails", "cancel-login", "lost-logout", "context-replacement"} {
		t.Run(scenario, func(t *testing.T) {
			var mutations atomic.Int32
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			releaseHandler := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				identity := `{"csrf_token":"csrf","session":{"user_id":"u-1","login":"owner","role":"administrator","restricted":false}}`
				if r.URL.Path == "/api/v1/auth/context" {
					if scenario == "reconcile-fails" && mutations.Load() > 0 {
						w.WriteHeader(503)
						_, _ = fmt.Fprint(w, `{}`)
						return
					}
					if scenario == "context-replacement" {
						w.Header().Add("Set-Cookie", "__Host-almanac-session="+strings.Repeat("B", 42)+"A"+cookieAttributes)
					}
					if cookie, err := r.Cookie("__Host-almanac-session"); err == nil && cookie.Value == testCookie {
						_, _ = fmt.Fprint(w, identity)
						return
					}
					if _, err := r.Cookie("__Host-almanac-preauth"); err != nil {
						w.Header().Set("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
					}
					_, _ = fmt.Fprint(w, `{"csrf_token":"preauth","session":null}`)
					return
				}
				mutations.Add(1)
				if scenario == "cancel-login" {
					cancel()
					// Keep the response pending until the client has returned. An
					// unread HTTP/1 request body can delay server disconnect notice;
					// fixture cleanup must not depend on that notification.
					<-releaseHandler
					return
				}
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error("hijack failed")
					return
				}
				defer conn.Close()
				if scenario == "truncated-body" || scenario == "reconcile-fails" {
					_, _ = fmt.Fprintf(rw, "HTTP/1.1 200 OK\r\nContent-Length: 900\r\nContent-Type: application/json\r\nSet-Cookie: __Host-almanac-session=%s%s\r\n\r\n{", testCookie, cookieAttributes)
					_ = rw.Flush()
				}
			}))
			defer server.Close()
			defer close(releaseHandler)
			options := SessionOptions{BackendURL: server.URL, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}
			if scenario == "lost-logout" || scenario == "context-replacement" {
				options.Session = testCookie
			}
			client, err := NewSession(options)
			if err != nil {
				t.Fatal("construct client failed")
			}
			defer client.Close()
			if scenario == "context-replacement" {
				identity, err := client.Context(ctx)
				if err != nil || identity == nil || client.Session() != testCookie || mutations.Load() != 0 {
					t.Fatal("context changed trusted credential")
				}
				return
			}
			if scenario == "lost-logout" {
				err = client.Logout(ctx)
			} else {
				_, err = client.Login(ctx, "owner", "password")
			}
			f, ok := errors.AsType[*Failure](err)
			if !ok || !f.OutcomeUnknown || mutations.Load() != 1 {
				t.Fatal("transport loss was replayed or treated as definitive")
			}
			switch scenario {
			case "truncated-body":
				if f.SessionState != "authenticated" || client.Session() != testCookie {
					t.Fatal("truncated response discarded recovered session")
				}
			case "reconcile-fails", "lost-logout":
				if client.Session() != testCookie || f.SessionState != "unchecked" {
					t.Fatal("uncertain operation discarded recoverable credential")
				}
			case "no-headers":
				if f.SessionState != "anonymous" || client.Session() != "" {
					t.Fatal("headerless loss did not reconcile anonymously")
				}
			case "cancel-login":
				if f.Kind != KindCancelled || f.SessionState != "unavailable" {
					t.Fatal("in-flight cancellation was misclassified")
				}
			}
		})
	}
}
