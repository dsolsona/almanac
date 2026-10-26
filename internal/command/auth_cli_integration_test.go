//go:build integration

package command

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/cliprofile"
)

// Each invocation starts a separate compiled CLI process against the existing
// real TLS/PostgreSQL fixture. Both restricted and ordinary sessions are covered.
func assertExecutableAuthProfile(t *testing.T, binary, origin, passwordFile, caFile string, restricted bool) {
	t.Helper()
	profile := filepath.Join(t.TempDir(), "profile.json")
	password, err := os.ReadFile(passwordFile)
	if err != nil {
		t.Fatal("read protected fixture failed")
	}
	var credential string
	run := func(operation string, extra ...string) (int, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
		defer cancel()
		args := append([]string{"auth", operation, "--profile-file=" + profile, "--output=json"}, extra...)
		cmd := exec.CommandContext(ctx, binary, args...)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err := cmd.Run()
		code := 0
		if err != nil {
			if failure, ok := errors.AsType[*exec.ExitError](err); ok {
				code = failure.ExitCode()
			} else {
				t.Fatal("start auth executable failed")
			}
		}
		for _, secret := range []string{string(password), credential, profile, origin} {
			if secret != "" && strings.Contains(out.String()+diagnostic.String(), secret) {
				t.Fatal("auth executable leaked protected input")
			}
		}
		if diagnostic.Len() != 0 {
			t.Fatal("JSON auth executable emitted unexpected diagnostics")
		}
		return code, out.String()
	}
	args := []string{"--backend-url=" + origin, "--login=owner", "--password-file=" + passwordFile, "--ca-file=" + caFile}
	code, output := run("login", args...)
	if code != 0 {
		t.Fatalf("real auth login exit=%d", code)
	}
	checkIdentity := func(output string) {
		t.Helper()
		var doc authDocument
		if json.Unmarshal([]byte(output), &doc) != nil || doc.SchemaVersion != 1 || doc.Identity == nil || doc.Identity.Login != "owner" || doc.Identity.Role != "administrator" || doc.Identity.Restricted != restricted {
			t.Fatal("auth executable returned wrong identity")
		}
		want := "authenticated"
		if restricted {
			want = "restricted"
		}
		if doc.SessionState != want {
			t.Fatal("auth executable changed restriction state")
		}
	}
	checkIdentity(output)
	data, err := os.ReadFile(profile)
	var record cliprofile.Record
	if err != nil || json.Unmarshal(data, &record) != nil || record.Session == "" || record.BackendURL != origin {
		t.Fatal("real login did not save protected session")
	}
	credential = record.Session
	if strings.Contains(output, credential) || strings.Contains(string(data), string(password)) {
		t.Fatal("login leaked session or retained password")
	}
	if info, err := os.Stat(profile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("profile permissions are unsafe")
	}
	code, output = run("context")
	if code != 0 {
		t.Fatal("separate auth context process failed")
	}
	checkIdentity(output)
	if code, output := run("login", args...); code != 5 || !strings.Contains(output, `"code":"session_present"`) {
		t.Fatal("auth executable replaced active session")
	}
	if code, output := run("logout"); code != 0 || !strings.Contains(output, `"session_state":"anonymous"`) {
		t.Fatal("real auth logout failed")
	}
	data, err = os.ReadFile(profile)
	if err != nil || json.Unmarshal(data, &record) != nil || record.Session != "" {
		t.Fatal("logout did not persist anonymous profile")
	}
	if code, output := run("context"); code != 0 || !strings.Contains(output, `"session_state":"anonymous"`) {
		t.Fatal("post-logout process reused old identity")
	}
}
