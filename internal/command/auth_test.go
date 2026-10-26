//go:build linux

package command

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/cliprofile"
)

func TestAuthCommandsPersistOnlySessionAndRequireExplicitLogout(t *testing.T) {
	var logins, logouts atomic.Int32
	var revoked atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Origin") != "https://"+r.Host || r.Header.Get("Idempotency-Key") != "" {
			t.Error("invalid auth request headers")
		}
		identity := `{"csrf_token":"private-csrf","session":{"user_id":"user-1","login":"owner","role":"administrator","restricted":false}}`
		switch r.URL.Path {
		case "/api/v1/auth/context":
			cookie, err := r.Cookie("__Host-almanac-session")
			if err == nil && cookie.Value == setupTestCookie && !revoked.Load() {
				_, _ = fmt.Fprint(w, identity)
				return
			}
			w.Header().Set("Set-Cookie", "__Host-almanac-preauth="+setupTestCookie+setupCookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"private-preauth","session":null}`)
		case "/api/v1/auth/login":
			logins.Add(1)
			w.Header().Set("Set-Cookie", "__Host-almanac-session="+setupTestCookie+setupCookieAttributes)
			_, _ = fmt.Fprint(w, identity)
		case "/api/v1/auth/logout":
			logouts.Add(1)
			revoked.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Error("unexpected authentication operation")
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	profile, password, ca := filepath.Join(dir, "profile.json"), filepath.Join(dir, "password"), filepath.Join(dir, "ca.pem")
	for path, value := range map[string][]byte{password: []byte("private-password"), ca: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal("write input fixture failed")
		}
	}
	run := func(operation string, extra ...string) (int, string) {
		t.Helper()
		args := append([]string{"auth", operation, "--profile-file=" + profile, "--output=json"}, extra...)
		var out, diagnostic bytes.Buffer
		code := Run(t.Context(), args, &out, &diagnostic, BuildInfo{})
		for _, secret := range []string{setupTestCookie, "private-password", "private-csrf", "private-preauth", profile, server.URL} {
			if strings.Contains(out.String()+diagnostic.String(), secret) {
				t.Fatal("auth output disclosed protected data")
			}
		}
		if diagnostic.Len() != 0 {
			t.Fatal("JSON command emitted unexpected diagnostic")
		}
		return code, out.String()
	}
	loginArgs := []string{"--backend-url=" + server.URL, "--login=owner", "--password-file=" + password, "--ca-file=" + ca}
	if code, output := run("login", loginArgs...); code != 0 || !strings.Contains(output, `"session_state":"authenticated"`) {
		t.Fatalf("login failed: exit=%d", code)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal("profile not saved")
	}
	var record map[string]any
	if json.Unmarshal(data, &record) != nil || len(record) != 4 || record["session"] != setupTestCookie || record["backend_url"] != server.URL {
		t.Fatal("profile does not contain exactly the binding and session")
	}
	if strings.Contains(string(data), "private-password") || strings.Contains(string(data), "private-csrf") {
		t.Fatal("profile retained password or CSRF")
	}
	if code, output := run("context"); code != 0 || !strings.Contains(output, `"login":"owner"`) {
		t.Fatal("fresh command could not reuse profile")
	}
	if code, _ := run("login", loginArgs...); code != 5 || logins.Load() != 1 {
		t.Fatal("active profile login was silently replaced")
	}
	if code, _ := run("logout"); code != 0 || logouts.Load() != 1 {
		t.Fatal("logout failed")
	}
	if code, output := run("context"); code != 0 || !strings.Contains(output, `"session_state":"anonymous"`) {
		t.Fatal("logout did not persist anonymous session")
	}
	if code, _ := run("logout"); code != 0 || logouts.Load() != 1 {
		t.Fatal("anonymous logout sent another mutation")
	}
	if code, _ := run("login", "--backend-url=https://different.invalid", "--login=owner", "--password-file="+password); code != 5 || logins.Load() != 1 {
		t.Fatal("profile was rebound to another origin")
	}
}

func TestAuthPersistenceAndOutputFailuresAreNotSuccess(t *testing.T) {
	for _, scenario := range []string{"save-fails", "output-fails", "uncertain-login"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			profile, password, ca := filepath.Join(dir, "profile.json"), filepath.Join(dir, "password"), filepath.Join(dir, "ca.pem")
			var logins atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				identity := `{"csrf_token":"private-csrf","session":{"user_id":"user-1","login":"owner","role":"administrator","restricted":false}}`
				if r.URL.Path == "/api/v1/auth/context" {
					if _, err := r.Cookie("__Host-almanac-session"); err == nil {
						_, _ = fmt.Fprint(w, identity)
						return
					}
					w.Header().Set("Set-Cookie", "__Host-almanac-preauth="+setupTestCookie+setupCookieAttributes)
					_, _ = fmt.Fprint(w, `{"csrf_token":"preauth","session":null}`)
					return
				}
				logins.Add(1)
				if scenario == "save-fails" {
					if err := os.Chmod(profile, 0o644); err != nil {
						t.Error("prepare persistence failure failed")
					}
				}
				w.Header().Set("Set-Cookie", "__Host-almanac-session="+setupTestCookie+setupCookieAttributes)
				if scenario == "uncertain-login" {
					_, _ = fmt.Fprint(w, `{`)
					return
				}
				_, _ = fmt.Fprint(w, identity)
			}))
			defer server.Close()
			for path, value := range map[string][]byte{password: []byte("private-password"), ca: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})} {
				if os.WriteFile(path, value, 0o600) != nil {
					t.Fatal("prepare input failed")
				}
			}
			args := []string{"auth", "login", "--profile-file=" + profile, "--backend-url=" + server.URL, "--login=owner", "--password-file=" + password, "--ca-file=" + ca, "--output=json"}
			var out, diagnostic bytes.Buffer
			code := 0
			if scenario == "output-fails" {
				code = Run(t.Context(), args, setupFailWriter{}, &diagnostic, BuildInfo{})
			} else {
				code = Run(t.Context(), args, &out, &diagnostic, BuildInfo{})
			}
			if code == 0 || logins.Load() != 1 {
				t.Fatal("failure reported success or retried login")
			}
			if strings.Contains(out.String()+diagnostic.String(), setupTestCookie) || strings.Contains(out.String()+diagnostic.String(), "private-password") {
				t.Fatal("failure disclosed credential")
			}
			data, err := os.ReadFile(profile)
			var record cliprofile.Record
			if err != nil || json.Unmarshal(data, &record) != nil {
				t.Fatal("failure destroyed profile")
			}
			if scenario == "save-fails" {
				if record.Session != "" || !strings.Contains(out.String(), `"outcome_unknown":true`) || !strings.Contains(out.String(), `"session_state":"unavailable"`) {
					t.Fatal("failed persistence implied recoverable saved session")
				}
			} else if record.Session != setupTestCookie {
				t.Fatal("known or recovered session was not persisted before output")
			}
			if scenario == "uncertain-login" && (!strings.Contains(out.String(), `"outcome_unknown":true`) || !strings.Contains(out.String(), `"session_state":"authenticated"`)) {
				t.Fatal("reconciliation converted unknown login to success")
			}
			if scenario == "output-fails" && diagnostic.String() != "auth: output failed\n" {
				t.Fatal("output failure diagnostic differed")
			}
		})
	}
}

func TestAuthProfileLockUsesWholeOperationDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	locked, err := cliprofile.Open(t.Context(), path, true)
	if err != nil {
		t.Fatal("open profile failed")
	}
	defer locked.Close()
	if locked.Save(cliprofile.Record{SchemaVersion: 1, BackendURL: "https://example.invalid"}) != nil {
		t.Fatal("save profile failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	var out, diagnostic bytes.Buffer
	if code := Run(ctx, []string{"auth", "context", "--profile-file=" + path, "--output=json"}, &out, &diagnostic, BuildInfo{}); code != 6 || !strings.Contains(out.String(), `"outcome_unknown":false`) {
		t.Fatal("lock wait ignored operation deadline or implied dispatch")
	}
}
