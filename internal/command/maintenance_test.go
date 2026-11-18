package command

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/maintenanceclient"
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

func TestScopedTimelineTextIncludesWindowRevision(t *testing.T) {
	start := time.Date(2026, time.November, 18, 2, 0, 0, 0, time.UTC)
	page := api.ScopedMaintenanceTimelinePage{Items: []api.ScopedMaintenanceTimelineInterval{{
		Window: api.ScopedMaintenanceWindow{Title: "Repair", Revision: "17"},
		Start:  start, End: start.Add(time.Hour),
	}}}
	var stdout, stderr bytes.Buffer
	code := writeScopedMaintenance(&stdout, &stderr, "maintenance timeline", false, maintenanceclient.ScopedResult{Timeline: &page})
	if code != 0 || !strings.Contains(stdout.String(), "\t2026-11-18T03:00:00Z\trevision 17\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
