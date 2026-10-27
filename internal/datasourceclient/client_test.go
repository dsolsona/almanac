package datasourceclient

import (
	"strings"
	"testing"
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
