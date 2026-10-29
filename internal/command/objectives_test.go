package command

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
