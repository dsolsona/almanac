package command

import (
	"bytes"
	"github.com/dsolsona/almanac/internal/datasourceclient"
	"strings"
	"testing"
)

func TestAlertCommandRejectsInvalidFlagsBeforeProfile(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	for _, args := range [][]string{
		{"alert-policies", "get", "--alert-id=" + id},
		{"alert-policies", "list", "--scope=active"},
		{"alerts", "get", "--policy-id=" + id},
		{"alerts", "list", "--limit=201"},
		{"alerts", "list", "--state=firing,firing"},
		{"alerts", "list", "--severity=urgent"},
		{"alerts", "list", "--timeout=3m"},
		{"alerts", "acknowledge", "--alert-id=" + id, "--if-match=*", "--idempotency-key=key"},
		{"alert-policies", "update", "--policy-id=" + id, "--if-match=\"alert-policy:" + id + ":01\"", "--idempotency-key=key", "--input-file=missing"},
		{"alerts", "command-result", "--operation=alert-policy.create"},
	} {
		var out, stderr bytes.Buffer
		args = append(args, "--profile-file=missing", "--output=json")
		code := alertsCommand(t.Context(), args[0], args[1:], &out, &stderr)
		if code != 2 || !strings.Contains(out.String(), "invalid_request") {
			t.Errorf("%v: code=%d out=%s err=%s", args, code, out.String(), stderr.String())
		}
	}
}

func TestAlertFailurePreservesConflictCodeAndExit(t *testing.T) {
	for _, code := range []string{"version_conflict", "name_conflict", "command_body_conflict"} {
		var out, stderr bytes.Buffer
		exit := writeAlertFailure(&out, &stderr, "alert-policies update", true, &datasourceclient.Failure{Code: code, Message: "Refresh the current policy.", Status: 409})
		if exit != 5 || !strings.Contains(out.String(), `"code":"`+code+`"`) || stderr.Len() != 0 {
			t.Fatalf("conflict %s: exit=%d output=%s stderr=%s", code, exit, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	exit := writeAlertFailure(&out, &stderr, "alert-policies update", true, &datasourceclient.Failure{Code: "command_expired", Message: "The result expired; inspect current state and history.", Status: 410})
	if exit != 1 || !strings.Contains(out.String(), `"code":"command_expired"`) || strings.Contains(out.String(), `"outcome_unknown":true`) {
		t.Fatalf("expired command lost explicit failure: %d %s", exit, out.String())
	}
}

func TestAlertCommandSeriesAndEvidenceOptions(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	for _, args := range [][]string{
		{"alerts", "series", "--alert-id=" + id, "--condition-id=fast"},
		{"alerts", "series", "--alert-id=" + id, "--condition-id=fast", "--start=2026-11-15T10:00:00Z", "--end=2026-11-15T12:00:00Z", "--limit=2"},
		{"alerts", "list", "--evidence=matching", "--minimum-duration-seconds=0"},
		{"alerts", "list", "--evidence=unavailable", "--minimum-duration-seconds=9223372036854775807"},
	} {
		var out, stderr bytes.Buffer
		args = append(args, "--profile-file="+t.TempDir()+"/missing", "--output=json")
		code := alertsCommand(t.Context(), args[0], args[1:], &out, &stderr)
		if code != 1 || !strings.Contains(out.String(), "profile_unavailable") {
			t.Errorf("valid flags failed before profile: %v code%d %s", args, code, out.String())
		}
	}
	for _, args := range [][]string{
		{"alerts", "get", "--alert-id=" + id, "--condition-id=fast"},
		{"alert-policies", "list", "--evidence=matching"},
		{"alerts", "series", "--alert-id=" + id, "--condition-id=fast", "--cursor=next"},
		{"alerts", "series", "--alert-id=" + id, "--condition-id=fast", "--evidence=waiting"},
		{"alerts", "series", "--alert-id=" + id, "--condition-id=fast", "--limit=1"},
		{"alerts", "series", "--alert-id=" + id, "--condition-id=fast", "--limit=121"},
		{"alerts", "series", "--alert-id=" + id, "--condition-id=fast", "--start="},
		{"alerts", "list", "--evidence="},
		{"alerts", "list", "--minimum-duration-seconds="},
		{"alerts", "list", "--minimum-duration-seconds=01"},
		{"alerts", "list", "--minimum-duration-seconds=-1"},
		{"alerts", "list", "--minimum-duration-seconds=9223372036854775808"},
	} {
		var out, stderr bytes.Buffer
		args = append(args, "--profile-file=missing", "--output=json")
		code := alertsCommand(t.Context(), args[0], args[1:], &out, &stderr)
		if code != 2 || !strings.Contains(out.String(), "invalid_request") {
			t.Errorf("invalid flags touched profile: %v code%d %s", args, code, out.String())
		}
	}
}
