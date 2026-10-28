package datasourceclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/setupclient"
)

type savedConnectionTestOperations struct {
	setupclient.DatasourceOperations
	response *http.Response
	params   *api.PostSavedDatasourceConnectionTestParams
	body     api.PostSavedDatasourceConnectionTestJSONRequestBody
}

func (operations *savedConnectionTestOperations) PostSavedDatasourceConnectionTest(_ context.Context, _ api.DatasourceId, params *api.PostSavedDatasourceConnectionTestParams, body api.PostSavedDatasourceConnectionTestJSONRequestBody, _ ...api.RequestEditorFn) (*http.Response, error) {
	operations.params = params
	operations.body = body
	return operations.response, nil
}

func TestSliResponseAcceptsNullableActivationAndRejectsInvalidFields(t *testing.T) {
	valid := `{"id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","name":"requests","display_name":"Requests","version":"1","measurement_revision":"1","datasource_id":"b13e0739-3c86-4a7f-b7e5-c15e9626e71c","datasource_version":"1","agent_id":"4b13e073-3c86-4a7f-b7e5-c15e9626e71c","agent_reachability":"connected","collection_progress":"idle","measurement_kind":"event_counts","component_mode":"interval_count","good_query":"vector(99)","total_query":"vector(100)","polling_interval_seconds":60,"settling_delay_seconds":60,"effective_from":"2026-10-27T11:58:00Z","activated_at":null,"activation_state":"preparing","freshness":"unknown","latest_observation":null,"last_attempt_at":null,"last_observation_at":null,"next_expected_execution_at":null,"created_at":"2026-10-27T12:00:20Z","updated_at":"2026-10-27T12:00:20Z"}`
	for _, activation := range []string{"null", `"2026-10-27T12:00:21Z"`} {
		if !validSli([]byte(strings.Replace(valid, `"activated_at":null`, `"activated_at":`+activation, 1))) {
			t.Fatalf("valid activation %s rejected", activation)
		}
	}
	for _, malformed := range []string{
		strings.Replace(valid, `,"activated_at":null`, "", 1),
		strings.Replace(valid, `"activated_at":null`, `"activated_at":"not-a-time"`, 1),
		strings.Replace(valid, `"activated_at":null`, `"activated_at":"0001-01-01T00:00:00Z"`, 1),
		strings.Replace(valid, `"activated_at":null`, `"activated_at":null,"extra":true`, 1),
	} {
		if validSli([]byte(malformed)) {
			t.Fatalf("invalid response accepted: %s", malformed)
		}
	}
}

func TestDatasourceResponseValidationRejectsSecretValues(t *testing.T) {
	valid := []byte(`{"id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","name":"metrics","display_name":"Metrics","version":"1","provider":"prometheus","configuration_format":"prometheus-http-v1","endpoint":"https://metrics.example.test","tls":{"insecure_skip_verify":false,"ca_pem":null},"authentication":{"mode":"bearer","username":null,"password_configured":false,"token_configured":true},"polling_interval_seconds":60,"settling_delay_seconds":60,"agent_id":"b13e0739-3c86-4a7f-b7e5-c15e9626e71c","created_at":"2026-10-27T12:00:00Z","updated_at":"2026-10-27T12:00:00Z"}`)
	if !validDatasource(valid) {
		t.Fatal("redacted datasource was rejected")
	}
	ordinaryName := []byte(strings.Replace(string(valid), `"name":"metrics"`, `"name":"token"`, 1))
	if !validDatasource(ordinaryName) {
		t.Fatal("datasource with an ordinary name containing a credential word was rejected")
	}
	pending := []byte(strings.TrimSuffix(string(valid), "}") + `,"reassignment":{"source_agent_id":"b13e0739-3c86-4a7f-b7e5-c15e9626e71c","target_agent_id":"9c5b4ef2-75e8-4e76-a4de-baa6a4d77277","requested_at":"2026-10-27T12:00:00Z","authority_expires_at":"2026-10-27T13:00:00Z","state":"waiting_for_previous_agent"}}`)
	if !validDatasource(pending) {
		t.Fatal("valid pending reassignment rejected")
	}
	if validDatasource([]byte(strings.Replace(string(pending), "waiting_for_previous_agent", "unknown", 1))) {
		t.Fatal("unknown reassignment state accepted")
	}
	unsafe := []byte(strings.Replace(string(valid), `"token_configured":true}`, `"token_configured":true,"token":"private-token"}`, 1))
	if validDatasource(unsafe) {
		t.Fatal("datasource containing a plaintext token was accepted")
	}
}

func TestDiagnosticResultAllowsCredentialWordsInMetricLabels(t *testing.T) {
	data := []byte(`{"id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","state":"succeeded","operation":"query_preview","created_at":"2026-10-27T12:00:00Z","deadline_at":"2026-10-27T12:00:30Z","result_class":"complete","result":{"diagnostic_id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","state":"succeeded","result_class":"complete","series":[{"labels":{"token":"ordinary label","password":"ordinary label"},"time":"1700000000.000","value":"1"}]},"error_code":null,"message":null}`)
	if !validDiagnostic(data) {
		t.Fatal("valid metric labels containing credential words were rejected")
	}
}

func TestDiagnosticResponseReadAllowsBoundedTypedLargeResultReceipt(t *testing.T) {
	series := make([]agentprotocol.DiagnosticSeries, 20)
	for index := range series {
		labels := make(map[string]string, 32)
		for label := range 32 {
			labels[fmt.Sprintf("label_%02d", label)] = strings.Repeat("x", 256)
		}
		series[index] = agentprotocol.DiagnosticSeries{Labels: labels, Time: "1700000000.000", Value: "1"}
	}
	result := agentprotocol.DiagnosticResult{DiagnosticID: "8ed93d7e-51e3-4a3e-9222-3e8a612de20a", State: "succeeded", ResultClass: "complete", Series: series}
	encodedResult, err := result.CanonicalJSON()
	if err != nil || len(encodedResult) <= agentprotocol.MaxInlineDiagnosticResultBytes {
		t.Fatalf("large result size=%d err=%v", len(encodedResult), err)
	}
	data := []byte(`{"id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","state":"succeeded","operation":"query_preview","created_at":"2026-10-27T12:00:00Z","deadline_at":"2026-10-27T12:00:30Z","result_class":"complete","result":` + string(encodedResult) + `,"error_code":null,"message":null}`)
	client := &Client{}
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data))}
	if _, err := client.readLimit(response, nil, http.StatusOK, validDiagnostic, maxDiagnosticResponseBody); err != nil {
		t.Fatalf("bounded large diagnostic receipt rejected: %v", err)
	}
}

func TestDiagnosticResponseReadUsesItsOwnBoundedBodyLimit(t *testing.T) {
	data := []byte(`{"payload":"` + strings.Repeat("x", maxBody+1024) + `"}`)
	client := &Client{}
	validate := func([]byte) bool { return true }
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data))}
	if _, err := client.readLimit(response, nil, http.StatusOK, validate, maxDiagnosticResponseBody); err != nil {
		t.Fatalf("large diagnostic response exceeded its bounded limit: %v", err)
	}
	response = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data))}
	if _, err := client.readLimit(response, nil, http.StatusOK, validate, maxBody); err == nil {
		t.Fatal("large response passed the ordinary response limit")
	}
}

func TestDiagnosticValidationRequiresKnownOperationAndUUID(t *testing.T) {
	valid := []byte(`{"id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","state":"pending","operation":"query_preview","created_at":"2026-10-27T12:00:00Z","deadline_at":"2026-10-27T12:00:30Z","result_class":null,"result":null,"error_code":null,"message":null}`)
	if !validDiagnostic(valid) {
		t.Fatal("pending diagnostic was rejected")
	}
	invalid := []byte(`{"id":"not-a-uuid","operation":"query_preview"}`)
	if validDiagnostic(invalid) {
		t.Fatal("diagnostic without canonical ID was accepted")
	}
	unsafe := []byte(strings.Replace(string(valid), `"message":null}`, `"message":null,"token":"private-token"}`, 1))
	if validDiagnostic(unsafe) {
		t.Fatal("diagnostic with an unexpected secret field was accepted")
	}
}

func TestDatasourceETagVersionValidation(t *testing.T) {
	id := "8ed93d7e-51e3-4a3e-9222-3e8a612de20a"
	if got := etagVersion(`"datasource:`+id+`:9"`, id); got != "9" {
		t.Fatalf("etag version = %q", got)
	}
	for _, invalid := range []string{`W/"datasource:` + id + `:9"`, `"datasource:` + id + `:0"`, `"datasource:` + id + `:09"`} {
		if etagVersion(invalid, id) != "" {
			t.Fatalf("accepted invalid ETag %q", invalid)
		}
	}
}

func TestProblemFailureRetainsOnlyBoundedStatusAndCanonicalRequestID(t *testing.T) {
	requestID := "0123456789abcdef0123456789abcdef"
	failure := problemFailure(503, "agent_unavailable", requestID)
	if failure.Code != "agent_unavailable" || failure.Status != 503 || failure.RequestID != requestID {
		t.Fatalf("remote failure evidence = %+v", failure)
	}
	invalid := problemFailure(503, "unknown_private_detail", "secret-request-id")
	if invalid.Code != "management_unavailable" || invalid.RequestID != "" || invalid.Status != 503 {
		t.Fatalf("invalid remote failure evidence was retained: %+v", invalid)
	}
	if !validRequestID("8ed93d7e-51e3-4a3e-9222-3e8a612de20a") || validRequestID("8ed93d7e-51e3-4a3e-9222-secret") {
		t.Fatal("canonical UUID request ID validation failed")
	}
}

func TestProblemFailurePreservesDatasourceBackendErrorCodes(t *testing.T) {
	codes := []string{"invalid_request", "configuration_pending", "configuration_failed", "source_secret_unavailable", "unauthorized", "invalid_agent", "datasource_unavailable", "diagnostic_unavailable", "agent_unavailable", "unsupported", "configuration_too_large"}
	for _, code := range codes {
		failure := problemFailure(503, code, "")
		if failure.Code != code || failure.BackendCode != code {
			t.Errorf("problem code %q became code=%q backend_code=%q", code, failure.Code, failure.BackendCode)
		}
	}
	for _, code := range []string{"configuration_pending", "configuration_failed", "source_secret_unavailable", "invalid_agent", "datasource_unavailable", "diagnostic_unavailable", "agent_unavailable", "unsupported"} {
		if !definiteNoDiagnostic(code) {
			t.Errorf("known no-receipt failure %q was classified as an uncertain job", code)
		}
	}
	if definiteNoDiagnostic("management_unavailable") {
		t.Fatal("ambiguous failure was classified as definitely not admitted")
	}
}

func TestSavedConnectionTestUsesExactVersionKeyAndNoSettingsOverride(t *testing.T) {
	id := "8ed93d7e-51e3-4a3e-9222-3e8a612de20a"
	body := `{"id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","state":"pending","operation":"connection_test","created_at":"2026-10-27T12:00:00Z","deadline_at":"2026-10-27T12:00:30Z","result_class":null,"result":null,"error_code":null,"message":null}`
	operations := &savedConnectionTestOperations{response: &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(body))}}
	client := New(operations, func(context.Context, *http.Request) error { return nil })
	if _, err := client.TestSaved(context.Background(), id, `"datasource:`+id+`:4"`, "saved-test-key"); err != nil {
		t.Fatalf("saved connection test failed: %v", err)
	}
	if operations.params == nil || string(operations.params.IfMatch) != `"datasource:`+id+`:4"` || string(operations.params.IdempotencyKey) != "saved-test-key" {
		t.Fatalf("saved test headers were not bound to the exact resource version and key: %+v", operations.params)
	}
	encoded, err := json.Marshal(operations.body)
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("saved test sent settings instead of an empty body: %s (err=%v)", encoded, err)
	}
}

func TestSavedConnectionTestKeepsGuaranteedNoJobFailureCertain(t *testing.T) {
	problem := `{"code":"configuration_pending","message":"untrusted"}`
	operations := &savedConnectionTestOperations{response: &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(problem))}}
	client := New(operations, func(context.Context, *http.Request) error { return nil })
	_, err := client.TestSaved(context.Background(), "8ed93d7e-51e3-4a3e-9222-3e8a612de20a", `"datasource:8ed93d7e-51e3-4a3e-9222-3e8a612de20a:4"`, "saved-test-key")
	failure, ok := errors.AsType[*Failure](err)
	if !ok || failure.Code != "configuration_pending" || failure.Unknown {
		t.Fatalf("guaranteed no-job result = %+v, error=%v", failure, err)
	}
}
