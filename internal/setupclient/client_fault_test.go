package setupclient

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMissingAndNullAuthFieldsAfterLoginAreUncertain(t *testing.T) {
	for _, body := range []string{
		`{"csrf_token":"csrf","session":{"user_id":"u-1","login":"admin","role":"administrator"}}`,
		`{"csrf_token":"csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":null}}`,
		`{"session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":true}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var contexts, logins, changes, logouts int
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/auth/context":
					contexts++
					if contexts == 1 {
						w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
						_, _ = fmt.Fprint(w, `{"csrf_token":"pre","session":null}`)
					} else {
						_, _ = fmt.Fprint(w, `{"csrf_token":"check","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":true}}`)
					}
				case "/api/v1/auth/login":
					logins++
					w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
					_, _ = fmt.Fprint(w, body)
				case "/api/v1/auth/password":
					changes++
				case "/api/v1/auth/logout":
					logouts++
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
			if !ok || !failure.OutcomeUnknown {
				t.Fatalf("failure = %#v", err)
			}
			if contexts != 2 || logins != 1 || changes != 0 || logouts != 0 {
				t.Fatalf("context/login/password/logout = %d/%d/%d/%d", contexts, logins, changes, logouts)
			}
		})
	}
}

func TestConflictSessionStateDependsOnLogoutEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, wantState string
		logoutStatus    int
	}{
		{name: "logout succeeds", wantState: "anonymous", logoutStatus: http.StatusNoContent},
		{name: "logout fails", wantState: "unchecked", logoutStatus: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/auth/context":
					w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
					_, _ = fmt.Fprint(w, `{"csrf_token":"csrf","session":null}`)
				case "/api/v1/auth/login":
					w.WriteHeader(http.StatusConflict)
					_, _ = fmt.Fprint(w, `{"code":"session_present","message":"hidden","request_id":"","field_errors":[]}`)
				case "/api/v1/auth/logout":
					w.WriteHeader(tc.logoutStatus)
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
			if !ok || failure.Code != "setup_already_complete" || failure.SessionState != tc.wantState || failure.OutcomeUnknown {
				t.Fatalf("failure=%#v; want state %q", err, tc.wantState)
			}
		})
	}
}

func TestCancelledBudgetDuringPasswordMutationDoesNotCheckContext(t *testing.T) {
	var contexts, logins, changes, logouts int
	passwordStarted := make(chan struct{})
	releasePassword := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/context":
			contexts++
			w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"csrf","session":null}`)
		case "/api/v1/auth/login":
			logins++
			w.Header().Add("Set-Cookie", "__Host-almanac-session="+testCookie+cookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"restricted","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":true}}`)
		case "/api/v1/auth/password":
			changes++
			close(passwordStarted)
			<-releasePassword
		case "/api/v1/auth/logout":
			logouts++
		}
	}))
	defer server.Close()
	defer close(releasePassword)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: ca})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	watchdogCtx, cancelWatchdog := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelWatchdog()
	ctx, cancel := context.WithCancel(watchdogCtx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Complete(ctx, "Initial-password-123", "Permanent-password-456"); done <- err }()
	select {
	case <-passwordStarted:
	case <-watchdogCtx.Done():
		t.Fatal("password mutation handler was not reached before the test watchdog expired")
	}
	cancel()
	select {
	case err = <-done:
	case <-watchdogCtx.Done():
		t.Fatal("setup client did not return after cancellation before the test watchdog expired")
	}
	failure, ok := errors.AsType[*Failure](err)
	if !ok || failure.Kind != KindCancelled || !failure.OutcomeUnknown || failure.SessionState != "unavailable" {
		t.Fatalf("failure = %#v", err)
	}
	if contexts != 1 || logins != 1 || changes != 1 || logouts != 0 {
		t.Fatalf("context/login/password/logout = %d/%d/%d/%d", contexts, logins, changes, logouts)
	}
}

func TestExpiredDeadlineSkipsRecoveryContext(t *testing.T) {
	var contexts int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/context" {
			contexts++
		}
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	client, err := New(Options{BackendURL: server.URL, Login: "admin", CABundle: ca})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	err = client.uncertain(ctx, errors.New("mutation response unavailable"), "authenticated")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || failure.Kind != KindCancelled || !failure.OutcomeUnknown || failure.SessionState != "unavailable" {
		t.Fatalf("failure = %#v", err)
	}
	if contexts != 0 {
		t.Fatalf("recovery context requests = %d; want 0", contexts)
	}
}
