//go:build !linux

package command

import (
	"bytes"
	"strings"
	"testing"
)

func TestAuthUnsupportedProfileReturnsSafeResponse(t *testing.T) {
	var out, diagnostic bytes.Buffer
	code := Run(t.Context(), []string{"auth", "context", "--profile-file=private-profile", "--output=json"}, &out, &diagnostic, BuildInfo{})
	if code != 2 || diagnostic.Len() != 0 || !strings.Contains(out.String(), `"code":"profile_unsupported"`) || !strings.Contains(out.String(), `"session_state":"unavailable"`) || strings.Contains(out.String(), "private-profile") {
		t.Fatal("unsupported profile did not return a safe JSON error")
	}
}
