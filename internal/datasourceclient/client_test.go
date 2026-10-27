package datasourceclient

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/agentprotocol"
)

func TestDatasourceResponseValidationRejectsSecretValues(t *testing.T) {
	valid := []byte(`{"id":"8ed93d7e-51e3-4a3e-9222-3e8a612de20a","name":"metrics","display_name":"Metrics","version":"1","provider":"prometheus","configuration_format":"prometheus-http-v1","endpoint":"https://metrics.example.test","tls":{"insecure_skip_verify":false,"ca_pem":null},"authentication":{"mode":"bearer","username":null,"password_configured":false,"token_configured":true},"polling_interval_seconds":60,"settling_delay_seconds":60,"agent_id":"b13e0739-3c86-4a7f-b7e5-c15e9626e71c","created_at":"2026-10-27T12:00:00Z","updated_at":"2026-10-27T12:00:00Z"}`)
	if !validDatasource(valid) {
		t.Fatal("redacted datasource was rejected")
	}
	unsafe := []byte(strings.Replace(string(valid), `"token_configured":true}`, `"token_configured":true,"token":"private-token"}`, 1))
	if validDatasource(unsafe) {
		t.Fatal("datasource containing a plaintext token was accepted")
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
