package command

import (
	"bytes"
	"strings"
	"testing"
)

func TestAuthRejectsInvalidFlagsWithoutDisclosingInputs(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "--output=json"},
		{"auth", "invalid-secret-subcommand", "--output=json"},
		{"auth", "context", "--output=json"},
		{"auth", "logout", "--profile-file=private-profile", "--timeout=0", "--output=json"},
		{"auth", "login", "--profile-file=private-profile", "--password=private-password", "--output=json"},
	} {
		var out, diagnostic bytes.Buffer
		if code := Run(t.Context(), args, &out, &diagnostic, BuildInfo{}); code != 2 {
			t.Fatalf("invalid flags exit=%d", code)
		}
		if !strings.Contains(out.String(), `"schema_version":1`) || strings.Contains(out.String()+diagnostic.String(), "private-") || strings.Contains(out.String()+diagnostic.String(), "invalid-secret") {
			t.Fatal("invalid flags lacked safe JSON envelope")
		}
	}
}
