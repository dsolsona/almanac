package command

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func TestDatasourceOperationDispatch(t *testing.T) {
	tests := []struct {
		args      []string
		operation string
		rest      int
	}{
		{[]string{"datasources", "list", "--profile-file=x"}, "datasources list", 1},
		{[]string{"datasources", "create"}, "datasources create", 0},
		{[]string{"datasources", "update"}, "datasources update", 0},
		{[]string{"datasources", "test"}, "datasources test", 0},
		{[]string{"providers", "list"}, "providers list", 0},
		{[]string{"slis", "list", "--profile-file=x"}, "slis list", 1},
		{[]string{"slis", "get"}, "slis get", 0},
		{[]string{"slis", "create"}, "slis create", 0},
		{[]string{"slis", "update"}, "slis update", 0},
		{[]string{"slis", "observations"}, "slis observations", 0},
		{[]string{"slis", "test"}, "slis test", 0},
		{[]string{"datasources", "delete"}, "", 2},
	}
	for _, test := range tests {
		operation, rest := datasourceOperation(test.args)
		if operation != test.operation || len(rest) != test.rest {
			t.Fatalf("dispatch %q = %q with %d args", test.args, operation, len(rest))
		}
	}
}

func TestSLIETagRequiresExactCanonicalResourceVersion(t *testing.T) {
	id := "8ed93d7e-51e3-4a3e-9222-3e8a612de20a"
	if !validSliETag(`"sli:`+id+`:7"`, id) {
		t.Fatal("valid SLI ETag was rejected")
	}
	for _, etag := range []string{`W/"sli:` + id + `:7"`, `"sli:` + id + `:0"`, `"sli:` + id + `:07"`, `"sli:` + id + `:7,8"`, `"sli:` + id + `:9223372036854775808"`} {
		if validSliETag(etag, id) {
			t.Fatalf("accepted invalid SLI ETag %q", etag)
		}
	}
}

func TestSLIInputRequiresNativeMeasurementAndMetadataOnly(t *testing.T) {
	validCreate := []byte(`{"name":"request-success","display_name":"Request success","datasource_id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","measurement_kind":"event_counts","component_mode":"interval_count","good_query":"sum(rate(requests_total{status=~\"2..\"}[1m]))","total_query":"sum(rate(requests_total[1m]))"}`)
	if !validSliCreateInput(validCreate) {
		t.Fatal("supported interval-count SLI input was rejected")
	}
	digitFirst := []byte(strings.Replace(string(validCreate), `"name":"request-success"`, `"name":"9ready"`, 1))
	if !validSliCreateInput(digitFirst) {
		t.Fatal("RFC1123 SLI name with a digit first was rejected")
	}
	trailingHyphen := []byte(strings.Replace(string(validCreate), `"name":"request-success"`, `"name":"trailing-"`, 1))
	if validSliCreateInput(trailingHyphen) {
		t.Fatal("RFC1123 SLI name with a trailing hyphen was accepted")
	}
	unsupported := []byte(strings.Replace(string(validCreate), `"component_mode":"interval_count"`, `"component_mode":"ratio"`, 1))
	if validSliCreateInput(unsupported) {
		t.Fatal("unsupported conversion mode was accepted")
	}
	unknown := []byte(strings.TrimSuffix(string(validCreate), "}") + `,"measurement_revision":"4"}`)
	if validSliCreateInput(unknown) {
		t.Fatal("unknown SLI measurement edit field was accepted")
	}
	if !validSliMetadataInput([]byte(`{"display_name":"Renamed requests"}`)) {
		t.Fatal("metadata-only SLI update was rejected")
	}
	if validSliMetadataInput([]byte(`{"display_name":"Renamed requests","good_query":"vector(1)"}`)) {
		t.Fatal("SLI metadata update accepted a measurement edit")
	}
}

func TestDatasourceFailureMapsBackendCodesWithoutInventingUncertainty(t *testing.T) {
	var stdout, stderr bytes.Buffer
	failure := &datasourceclient.Failure{Code: "configuration_pending", BackendCode: "configuration_pending", Message: "no diagnostic was started", Status: 503}
	if exit := writeDatasourceFailure(&stdout, &stderr, "slis test", true, failure); exit != 1 {
		t.Fatalf("pending diagnostic exit code = %d", exit)
	}
	if !strings.Contains(stdout.String(), `"code":"configuration_pending"`) || !strings.Contains(stdout.String(), `"backend_code":"configuration_pending"`) || !strings.Contains(stdout.String(), `"outcome_unknown":false`) {
		t.Fatalf("pending diagnostic receipt was misreported: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	failure = &datasourceclient.Failure{Code: "outcome_unknown", BackendCode: "management_unavailable", Message: "diagnostic outcome is unknown", Status: 503, Unknown: true}
	if exit := writeDatasourceFailure(&stdout, &stderr, "slis test", true, failure); exit != 1 || !strings.Contains(stdout.String(), `"outcome_unknown":true`) {
		t.Fatalf("ambiguous diagnostic failure classification exit=%d output=%s", exit, stdout.String())
	}
}

func TestDatasourceTestSupportsDistinctSavedAndUnsavedInputs(t *testing.T) {
	id := "8ed93d7e-51e3-4a3e-9222-3e8a612de20a"
	if err := validateDatasourceTestArgs(map[string]bool{"datasource-id": true, "if-match": true, "idempotency-key": true}, id, "", `"datasource:`+id+`:4"`, "test-key"); err != nil {
		t.Fatalf("valid saved test arguments rejected: %v", err)
	}
	if err := validateDatasourceTestArgs(map[string]bool{"input-file": true}, "", "settings.json", "", ""); err != nil {
		t.Fatalf("valid unsaved test arguments rejected: %v", err)
	}
	if err := validateDatasourceTestArgs(map[string]bool{"datasource-id": true, "if-match": true}, id, "", `"datasource:`+id+`:4"`, ""); err == nil {
		t.Fatal("saved test without idempotency key accepted")
	}
	if err := validateDatasourceTestArgs(map[string]bool{"datasource-id": true, "if-match": true, "idempotency-key": true, "input-file": true}, id, "settings.json", `"datasource:`+id+`:4"`, "test-key"); err == nil {
		t.Fatal("saved test with unsaved settings override accepted")
	}
}

func TestDatasourceDeadlinePollingRaceRequestsCancellation(t *testing.T) {
	deadlineContext, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	canceled := 0
	poll := func(context.Context, string) ([]byte, error) { t.Fatal("poll ran after the deadline"); return nil, nil }
	cancel := func(context.Context, string) { canceled++ }
	diagnostic := api.Diagnostic{State: api.DiagnosticStatePending, Operation: api.ConnectionTest}
	_, err := waitForDatasourceDiagnostic(deadlineContext, diagnostic, time.Now().Add(time.Minute), true, poll, cancel)
	failure, ok := errors.AsType[*datasourceclient.Failure](err)
	if !ok || failure.Code != "request_timeout" || canceled != 1 {
		t.Fatalf("expired polling deadline failure=%v cancellation calls=%d", err, canceled)
	}
	if !strings.Contains(failure.Message, "cancellation was requested") {
		t.Fatalf("deadline message does not report requested cancellation: %q", failure.Message)
	}
}

func TestDatasourceWaitResultSerializesTerminalDiagnostic(t *testing.T) {
	var id openapi_types.UUID
	if err := json.Unmarshal([]byte(`"8ed93d7e-51e3-4a3e-9222-3e8a612de20a"`), &id); err != nil {
		t.Fatal("parse diagnostic ID failed")
	}
	diagnostic := api.Diagnostic{
		Id:         id,
		State:      api.DiagnosticStatePending,
		Operation:  api.ConnectionTest,
		CreatedAt:  time.Now(),
		DeadlineAt: time.Now().Add(time.Minute),
	}
	terminal := diagnostic
	terminal.State = api.DiagnosticStateSucceeded
	response, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal("marshal terminal diagnostic failed")
	}
	polls := 0
	result, err := waitForDatasourceDiagnosticResult(t.Context(), diagnostic, time.Now().Add(time.Second), false,
		func(context.Context, string) ([]byte, error) {
			polls++
			return response, nil
		}, func(context.Context, string) {})
	if err != nil {
		t.Fatalf("wait for terminal diagnostic failed: %v", err)
	}
	var got api.Diagnostic
	if err := json.Unmarshal(result, &got); err != nil || got.State != api.DiagnosticStateSucceeded || got.Id != diagnostic.Id {
		t.Fatalf("wait result did not contain the terminal diagnostic: state=%q id=%q error=%v", got.State, got.Id, err)
	}
	if polls != 1 {
		t.Fatalf("diagnostic polling calls = %d, want 1", polls)
	}
}

func TestDatasourceExplicitCancellationDoesNotCancelRemoteDiagnostic(t *testing.T) {
	explicitContext, cancelExplicit := context.WithCancel(context.Background())
	cancelExplicit()
	canceled := 0
	poll := func(context.Context, string) ([]byte, error) {
		t.Fatal("poll ran after explicit cancellation")
		return nil, nil
	}
	cancel := func(context.Context, string) { canceled++ }
	diagnostic := api.Diagnostic{State: api.DiagnosticStatePending, Operation: api.ConnectionTest}
	_, err := waitForDatasourceDiagnostic(explicitContext, diagnostic, time.Now().Add(time.Minute), true, poll, cancel)
	failure, ok := errors.AsType[*datasourceclient.Failure](err)
	if !ok || failure.Code != "request_canceled" || canceled != 0 {
		t.Fatalf("explicit cancellation failure=%v cancellation calls=%d", err, canceled)
	}
}

func TestDatasourceETagRequiresCanonicalResourceVersion(t *testing.T) {
	id := "8ed93d7e-51e3-4a3e-9222-3e8a612de20a"
	if !validDatasourceETag(`"datasource:`+id+`:7"`, id) {
		t.Fatal("valid datasource ETag was rejected")
	}
	for _, etag := range []string{`W/"datasource:` + id + `:7"`, `"datasource:` + id + `:0"`, `"datasource:` + id + `:07"`, `"datasource:` + id + `:7,8"`} {
		if validDatasourceETag(etag, id) {
			t.Fatalf("accepted invalid ETag %q", etag)
		}
	}
}
