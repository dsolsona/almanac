package command

import (
	"bytes"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/setupclient"
)

const setupTestCookie = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const setupCookieAttributes = "; Path=/; Expires=Wed, 21 Oct 2037 07:28:00 GMT; Max-Age=600; Secure; HttpOnly; SameSite=Lax"

func TestSetupRejectsMissingAndUnknownFlagsWithoutNetwork(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode string
	}{
		{name: "missing required", args: []string{"--output=json"}, wantCode: "invalid_request"},
		{name: "unknown", args: []string{"--output=json", "--not-a-setup-flag"}, wantCode: "invalid_request"},
		{name: "positional", args: []string{"--output=json", "unexpected"}, wantCode: "invalid_request"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := setup(t.Context(), tc.args, &out, &stderr); code != 2 {
				t.Fatalf("exit = %d", code)
			}
			if !strings.Contains(out.String(), `"code":"`+tc.wantCode+`"`) || strings.Contains(out.String(), "https://") {
				t.Fatalf("unexpected output: %s", out.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	}
}

func TestSetupValidatesLocalInputsBeforeContactingBackend(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	dir := t.TempDir()
	initial := filepath.Join(dir, "initial")
	permanent := filepath.Join(dir, "permanent")
	if err := os.WriteFile(initial, []byte("initial password"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(permanent, []byte("Permanent-password-456"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--backend-url=" + server.URL, "--initial-admin-login=admin", "--initial-admin-password-file=" + initial, "--new-password-file=" + permanent, "--output=json"}
	var out, stderr bytes.Buffer
	if code := setup(t.Context(), args, &out, &stderr); code != 2 {
		t.Fatalf("exit = %d; output=%s", code, out.String())
	}
	if hits != 0 {
		t.Fatalf("backend requests before local validation = %d", hits)
	}
	if strings.Contains(out.String()+stderr.String(), initial) || strings.Contains(out.String()+stderr.String(), permanent) || strings.Contains(out.String()+stderr.String(), server.URL) {
		t.Fatal("diagnostic disclosed local path or backend URL")
	}
}

func TestSetupRejectsUnchangedPasswordBeforeNetwork(t *testing.T) {
	hits := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	dir := t.TempDir()
	initial, newPassword := filepath.Join(dir, "initial"), filepath.Join(dir, "new")
	for _, path := range []string{initial, newPassword} {
		if err := os.WriteFile(path, []byte("Permanent-password-456"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--backend-url=" + server.URL, "--initial-admin-login=admin", "--initial-admin-password-file=" + initial, "--new-password-file=" + newPassword, "--output=json"}
	var out, stderr bytes.Buffer
	if code := setup(t.Context(), args, &out, &stderr); code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if hits != 0 {
		t.Fatalf("backend requests before unchanged-password rejection = %d", hits)
	}
	if !strings.Contains(out.String(), `"code":"invalid_password"`) || strings.Contains(out.String()+stderr.String(), server.URL) {
		t.Fatalf("unsafe or unexpected output: %s %s", out.String(), stderr.String())
	}
}

func TestSetupFailureExitCodes(t *testing.T) {
	for _, tc := range []struct {
		kind setupclient.Kind
		want int
	}{
		{setupclient.KindInvalid, 2},
		{setupclient.KindAuthentication, 3},
		{setupclient.KindConflict, 5},
		{setupclient.KindCancelled, 6},
		{setupclient.KindRemote, 1},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			var out, stderr bytes.Buffer
			f := &setupclient.Failure{Kind: tc.kind, Code: "stable_test_code", Message: "safe", OutcomeUnknown: true, SessionState: "unchecked"}
			if code := writeSetupFailure(&out, &stderr, true, f); code != tc.want {
				t.Fatalf("exit=%d; want %d", code, tc.want)
			}
			if !strings.Contains(out.String(), `"outcome_unknown":true`) || !strings.Contains(out.String(), `"session_state":"unchecked"`) || stderr.Len() != 0 {
				t.Fatalf("JSON/stderr = %q / %q", out.String(), stderr.String())
			}
		})
	}
}

type setupFailWriter struct{}

func (setupFailWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestSetupHonorsResultWriteFailure(t *testing.T) {
	var stderr bytes.Buffer
	if code := writeSetupError(setupFailWriter{}, &stderr, true, 5, "setup_already_complete", "safe", false, "authenticated", "", 0); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if stderr.String() != "setup: output failed\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSetupRejectsOversizedPasswordAndCABundlesBeforeNetwork(t *testing.T) {
	hits := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	dir := t.TempDir()
	initial, newFile, caFile := filepath.Join(dir, "initial"), filepath.Join(dir, "new"), filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(initial, []byte(strings.Repeat("a", 513)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("Permanent-password-456"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := []string{"--backend-url=" + server.URL, "--initial-admin-login=admin", "--initial-admin-password-file=" + initial, "--new-password-file=" + newFile, "--ca-file=" + caFile, "--output=json"}
	var out, stderr bytes.Buffer
	if code := setup(t.Context(), base, &out, &stderr); code != 2 {
		t.Fatalf("oversized password exit=%d", code)
	}
	if err := os.WriteFile(initial, []byte("initial password"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, make([]byte, (64<<10)+1), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := setup(t.Context(), base, &out, &stderr); code != 2 {
		t.Fatalf("oversized CA exit=%d", code)
	}
	base[len(base)-2] = "--ca-file="
	out.Reset()
	stderr.Reset()
	if code := setup(t.Context(), base, &out, &stderr); code != 2 {
		t.Fatalf("explicit empty CA exit=%d", code)
	}
	if hits != 0 {
		t.Fatalf("network requests before file validation=%d", hits)
	}
}

func TestRunExposesSetupHelp(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"help"}, &out, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "almanac setup --backend-url=ORIGIN") {
		t.Fatalf("setup usage missing: %s", out.String())
	}
}

func TestSetupCommandWritesOnlyRequestedSuccessFormat(t *testing.T) {
	const initial, permanent = "Initial password\n", "Permanent-password-456\n"
	var calls int
	logoutFailure := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Origin") != "https://"+r.Host {
			t.Error("origin header is invalid")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/context":
			w.Header().Add("Set-Cookie", "__Host-almanac-preauth="+setupTestCookie+setupCookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"preauth-csrf","session":null}`)
		case "/api/v1/auth/login":
			var body struct {
				Login    string `json:"login"`
				Password string `json:"password"`
			}
			if err := json.UnmarshalRead(r.Body, &body); err != nil || body.Login != "admin" || body.Password != initial {
				t.Error("login request did not preserve its inputs")
			}
			if r.Header.Get("X-CSRF-Token") != "preauth-csrf" {
				t.Error("login CSRF token missing")
			}
			w.Header().Add("Set-Cookie", "__Host-almanac-preauth=; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=0")
			w.Header().Add("Set-Cookie", "__Host-almanac-session="+setupTestCookie+setupCookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"restricted-csrf","session":{"user_id":"user-1","login":"admin","role":"administrator","restricted":true}}`)
		case "/api/v1/auth/password":
			var body struct {
				Current string `json:"current_password"`
				New     string `json:"new_password"`
			}
			if err := json.UnmarshalRead(r.Body, &body); err != nil || body.Current != initial || body.New != permanent {
				t.Error("password request did not preserve its inputs")
			}
			if r.Header.Get("X-CSRF-Token") != "restricted-csrf" {
				t.Error("password CSRF token missing")
			}
			w.Header().Add("Set-Cookie", "__Host-almanac-session="+setupTestCookie+setupCookieAttributes)
			_, _ = fmt.Fprint(w, `{"csrf_token":"normal-csrf","session":{"user_id":"user-1","login":"admin","role":"administrator","restricted":false}}`)
		case "/api/v1/auth/logout":
			if r.Header.Get("X-CSRF-Token") != "normal-csrf" {
				t.Error("logout CSRF token missing")
			}
			if logoutFailure {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = fmt.Fprint(w, `{"message":"secret detail"}`)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			t.Errorf("unexpected route %q", r.URL.Path)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	initialFile, newFile, caFile := filepath.Join(dir, "initial"), filepath.Join(dir, "new"), filepath.Join(dir, "ca.pem")
	for path, value := range map[string]string{initialFile: initial, newFile: permanent} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, ca, 0o644); err != nil {
		t.Fatal(err)
	}
	baseArgs := []string{"setup", "--backend-url=" + server.URL, "--initial-admin-login=admin", "--initial-admin-password-file=" + initialFile, "--new-password-file=" + newFile, "--ca-file=" + caFile}
	var jsonOut, jsonErr bytes.Buffer
	jsonArgs := append(append([]string{}, baseArgs...), "--output=json")
	if code := Run(t.Context(), jsonArgs, &jsonOut, &jsonErr, BuildInfo{}); code != 0 {
		t.Fatalf("JSON setup exit=%d, stderr=%s", code, jsonErr.String())
	}
	wantJSON := "{\"schema_version\":1,\"operation\":\"setup\",\"completed\":true,\"session_cleanup\":\"complete\"}\n"
	if jsonOut.String() != wantJSON {
		t.Fatalf("JSON output = %q", jsonOut.String())
	}
	logoutFailure = true
	var cleanupJSON, cleanupJSONErr bytes.Buffer
	if code := Run(t.Context(), jsonArgs, &cleanupJSON, &cleanupJSONErr, BuildInfo{}); code != 0 {
		t.Fatalf("cleanup-failure JSON setup exit=%d", code)
	}
	wantCleanupJSON := "{\"schema_version\":1,\"operation\":\"setup\",\"completed\":true,\"session_cleanup\":\"unknown\"}\n"
	if cleanupJSON.String() != wantCleanupJSON || cleanupJSONErr.String() != "setup: password change completed; session cleanup could not be confirmed\n" {
		t.Fatalf("cleanup-failure result/stderr = %q / %q", cleanupJSON.String(), cleanupJSONErr.String())
	}
	var textOut, textErr bytes.Buffer
	if code := Run(t.Context(), baseArgs, &textOut, &textErr, BuildInfo{}); code != 0 {
		t.Fatalf("text setup exit=%d, stderr=%s", code, textErr.String())
	}
	if textOut.String() != "administrator setup completed\n" || textErr.String() != "setup: password change completed; session cleanup could not be confirmed\n" {
		t.Fatalf("text output/stderr = %q / %q", textOut.String(), textErr.String())
	}
	allOutput := jsonOut.String() + cleanupJSON.String() + textOut.String() + jsonErr.String() + cleanupJSONErr.String() + textErr.String()
	if strings.Contains(allOutput, initial) || strings.Contains(allOutput, permanent) || strings.Contains(allOutput, server.URL) || strings.Contains(allOutput, initialFile) {
		t.Fatal("setup output disclosed a secret, path, or URL")
	}
	if calls != 12 {
		t.Fatalf("backend requests=%d; want 12", calls)
	}
}
