//go:build integration && !linux

package command

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Setup integration still exercises the real executable's unsupported-profile
// response on platforms where protected profile storage is unavailable.
func assertExecutableAuthProfile(t *testing.T, binary, _, _, _ string, _ bool) {
	t.Helper()
	profile := filepath.Join(t.TempDir(), "private-profile.json")
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "auth", "context", "--profile-file="+profile, "--output=json")
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
	if code != 2 || diagnostic.Len() != 0 || !strings.Contains(out.String(), `"code":"profile_unsupported"`) || !strings.Contains(out.String(), `"session_state":"unavailable"`) || strings.Contains(out.String(), profile) {
		t.Fatal("auth executable did not safely reject unsupported profile storage")
	}
}
