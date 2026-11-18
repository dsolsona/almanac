package command

import (
	"bytes"
	"strings"
	"testing"
)

func TestMaintenanceScopeCommandsValidateBeforeOpeningProfile(t *testing.T) {
	for _, args := range [][]string{
		{"preview", "--profile-file", "missing", "--limit", "0"},
		{"impact", "--profile-file", "missing", "--maintenance-id", "not-a-uuid"},
		{"get", "--scoped", "--profile-file", "missing", "--maintenance-id", "not-a-uuid"},
	} {
		var stdout, stderr bytes.Buffer
		code := maintenanceCommand(t.Context(), append(args, "--output", "json"), &stdout, &stderr)
		if code == 0 || !strings.Contains(stdout.String(), "invalid_request") {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}
