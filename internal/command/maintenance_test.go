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

func TestScopedMaintenanceTextReportsOutputFailure(t *testing.T) {
	var stderr bytes.Buffer
	page := api.ScopedMaintenancePage{Items: []api.ScopedMaintenanceWindow{}, NextCursor: new("next")}
	code := writeScopedMaintenance(alwaysFailWriter{}, &stderr, "maintenance list", false, maintenanceclient.ScopedResult{Page: &page})
	if code == 0 || !strings.Contains(stderr.String(), "output failed") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestScopedMaintenanceImpactSeparatesRowsAndCursor(t *testing.T) {
	start := time.Date(2026, time.November, 18, 2, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	service := api.ServiceId{15: 3}
	page := api.MaintenanceImpactPage{
		InventoryAt: start, Ready: true, Total: "2", NextCursor: new("next"),
		Items: []api.MaintenanceImpactMember{
			{SloId: api.SloId{15: 1}, SloName: "First", ServiceId: service, ServiceName: "Service", Start: &start, End: &end},
			{SloId: api.SloId{15: 2}, SloName: "Second", ServiceId: service, ServiceName: "Service", Start: &start, End: &end},
		},
	}
	var stdout, stderr bytes.Buffer
	code := writeScopedMaintenance(&stdout, &stderr, "maintenance impact", false, maintenanceclient.ScopedResult{Impact: &page})
	want := "Affected SLOs: 2 (distinct SLOs, inventory at 2026-11-18T02:00:00Z)\n" +
		"00000000-0000-0000-0000-000000000001\t\"First\"\t00000000-0000-0000-0000-000000000003\t\"Service\"\t2026-11-18T02:00:00Z\t2026-11-18T03:00:00Z\n" +
		"00000000-0000-0000-0000-000000000002\t\"Second\"\t00000000-0000-0000-0000-000000000003\t\"Service\"\t2026-11-18T02:00:00Z\t2026-11-18T03:00:00Z\n" +
		"Next cursor: next\n"
	if code != 0 || stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q; want %q", code, stdout.String(), stderr.String(), want)
	}
}
