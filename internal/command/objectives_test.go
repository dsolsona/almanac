package command

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestObjectiveInputAndVersion(t *testing.T) {
	cases := []struct {
		kind, action, input string
		want                bool
	}{
		{"services", "create", `{"name":"checkout","display_name":"Checkout"}`, true},
		{"services", "create", `{"name":"checkout","display_name":"Checkout","extra":1}`, false},
		{"slos", "update", `{"display_name":"Checkout"}`, true},
		{"slos", "update", `{"display_name":null}`, false},
		{"slos", "create", `{"name":"availability","display_name":"Availability","service_id":"x","sli_id":"y","target":"0.999","rolling_window_seconds":2592000}`, true},
		{"slos", "create", `{"name":"availability","display_name":"Availability","service_id":"x","sli_id":"y","target":"0.999","rolling_window_seconds":2592000,"evaluation_valid_from":"2026-01-01T00:00:00Z"}`, true},
	}
	for _, tc := range cases {
		if got := validObjectiveInput([]byte(tc.input), tc.kind, tc.action); got != tc.want {
			t.Errorf("%s %s: got %v", tc.kind, tc.action, got)
		}
	}
	id := "11111111-1111-1111-1111-111111111111"
	if !validObjectiveETag(`"slo:`+id+`:1"`, "slo", id) || validObjectiveETag(`"slo:`+id+`:01"`, "slo", id) {
		t.Fatal("ETag validation")
	}
}
func TestObjectiveCommandRejectsInvalidFlags(t *testing.T) {
	for _, tc := range []struct {
		kind string
		args []string
	}{{"services", []string{"list", "--slo-id=x", "--output=json"}}, {"slos", []string{"evaluate", "--timeline-start=2026-01-01T00:00:00Z", "--output=json"}}, {"slos", []string{"create", "--profile-file=x", "--input-file=x", "--output=json"}}} {
		var out, err bytes.Buffer
		code := objectivesCommand(context.Background(), tc.kind, tc.args, &out, &err)
		if code != 2 || !strings.Contains(out.String(), "invalid_request") {
			t.Errorf("%s: code=%d output=%s stderr=%s", tc.kind, code, out.String(), err.String())
		}
	}
}

func TestOperationalTimestampRequiresUTCAndMicrosecondPrecision(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"2026-10-29T12:00:00Z", true}, {"2026-10-29T12:00:00.123456Z", true}, {"2026-10-29T12:00:00+00:00", false}, {"2026-10-29T12:00:00.1234567Z", false}, {"tomorrow", false}} {
		_, ok := parseOperationalTime(tc.value)
		if ok != tc.valid {
			t.Errorf("parseOperationalTime(%q) valid=%t, want %t", tc.value, ok, tc.valid)
		}
	}
}

func TestOperationalRangeRequiresPairedPositiveAtMostYearBounds(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	tooWide := start.Add(366 * 24 * time.Hour)
	if !validOperationalRange(&start, &end) || validOperationalRange(&start, nil) || validOperationalRange(&end, &start) || validOperationalRange(&start, &tooWide) {
		t.Fatal("operational range accepted an invalid bound pair")
	}
}

func TestOperationalCommandRejectsInvalidFiltersBeforeProfileRead(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	for _, args := range [][]string{
		{"status", "--profile-file=unused", "--output=json"},
		{"values", "--profile-file=unused", "--output=json", "--slo-id=" + id, "--generation-id=" + id, "--lane=budget", "--offset=-1"},
		{"values", "--profile-file=unused", "--output=json", "--slo-id=" + id, "--generation-id=" + id, "--lane=budget", "--limit=101"},
		{"values", "--profile-file=unused", "--output=json", "--slo-id=" + id, "--generation-id=" + id, "--lane=unknown"},
		{"overview", "--profile-file=unused", "--output=json", "--urgency=critical"},
		{"overview", "--profile-file=unused", "--output=json", "--q=" + strings.Repeat("x", 129)},
		{"operational", "--profile-file=unused", "--output=json", "--slo-id=" + id, "--burn-window-seconds=60"},
		{"operational", "--profile-file=unused", "--output=json", "--slo-id=" + id, "--at=2026-10-29T12:00:00+00:00"},
		{"operational", "--profile-file=unused", "--output=json", "--slo-id=" + id, "--start=2026-10-01T00:00:00Z"},
	} {
		var out, stderr bytes.Buffer
		if code := objectivesCommand(context.Background(), "slos", args, &out, &stderr); code != 2 || !strings.Contains(out.String(), "invalid_request") {
			t.Errorf("args=%v code=%d output=%s stderr=%s", args, code, out.String(), stderr.String())
		}
	}
}
