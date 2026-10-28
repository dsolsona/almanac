package agentprotocol

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const validHello = `{"kind":"hello","message_id":"hello-1","session_generation":"0","payload":{"protocol_versions":["almanac.agent.v1"],"software_version":"1.2.3","capabilities":[],"resume_token":"AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI","request_nonce":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"}}`

func TestDecodeAcceptsUnownedHelloAndOptionalFields(t *testing.T) {
	data := strings.Replace(validHello, `"capabilities":[]`, `"capabilities":[],"future_optional":true`, 1)
	message, err := Decode([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if message.SessionGeneration != InitialGeneration || message.Kind != KindHello {
		t.Fatalf("unexpected envelope: %#v", message)
	}
	var payload map[string]jsontext.Value
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["future_optional"]; !ok {
		t.Fatal("unknown optional field was discarded")
	}
}

func TestDecodeAcceptsStructurallyValidUnsupportedProtocolForNegotiation(t *testing.T) {
	message := strings.Replace(validHello, `"protocol_versions":["almanac.agent.v1"]`, `"protocol_versions":["almanac.agent.future"]`, 1)
	envelope, err := Decode([]byte(message))
	if err != nil {
		t.Fatalf("decode structurally valid unsupported protocol: %v", err)
	}
	if envelope.Kind != KindHello {
		t.Fatalf("decoded kind = %q, want hello", envelope.Kind)
	}
	var hello Hello
	if err := json.Unmarshal(envelope.Payload, &hello); err != nil || len(hello.ProtocolVersions) != 1 || hello.ProtocolVersions[0] != "almanac.agent.future" {
		t.Fatalf("decoded unsupported hello = %+v, %v", hello, err)
	}
}

func TestDecodeRejectsUnknownKindsAndUnconfirmedNonHello(t *testing.T) {
	unknown := strings.Replace(validHello, `"hello"`, `"future-operation"`, 1)
	if _, err := Decode([]byte(unknown)); err == nil {
		t.Fatal("unknown mandatory kind was accepted")
	}
	resumeGeneration := strings.Replace(validHello, `"session_generation":"0"`, `"session_generation":"1"`, 1)
	if _, err := Decode([]byte(resumeGeneration)); err != nil {
		t.Fatalf("same-process resume hello with known generation was rejected: %v", err)
	}
	postHelloZero := strings.Replace(validHello, `"kind":"hello"`, `"kind":"heartbeat"`, 1)
	if _, err := Decode([]byte(postHelloZero)); err == nil {
		t.Fatal("generation zero accepted for post-hello message")
	}
}

func TestDecodeBoundsAndValidatesPayload(t *testing.T) {
	if _, err := Decode(make([]byte, MaxMessageBytes+1)); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatal("oversized control message was accepted")
	}
	missing := strings.Replace(validHello, `,"request_nonce":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"`, "", 1)
	if _, err := Decode([]byte(missing)); err == nil {
		t.Fatal("missing required field was accepted")
	}
	badNonce := strings.Replace(validHello, "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI", "not-a-nonce", 1)
	if _, err := Decode([]byte(badNonce)); err == nil {
		t.Fatal("invalid proof was accepted")
	}
	badOperation := strings.Replace(validHello, `"capabilities":[]`, `"capabilities":[{"provider":"example","configuration_formats":[],"operations":["execute-shell"]}]`, 1)
	if _, err := Decode([]byte(badOperation)); err == nil {
		t.Fatal("unknown mandatory operation was accepted")
	}
}

func TestDiagnosticResultKeepsSuccessfulEmptySeriesOnWire(t *testing.T) {
	result := DiagnosticResult{DiagnosticID: "00000000-0000-4000-8000-000000000001", State: "succeeded", ResultClass: "empty", Series: []DiagnosticSeries{}}
	body, err := result.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"series":[]`) {
		t.Fatalf("successful empty result omitted its series array: %s", body)
	}
	var roundTrip DiagnosticResult
	if err := json.Unmarshal(body, &roundTrip); err != nil || roundTrip.Series == nil || len(roundTrip.Series) != 0 {
		t.Fatalf("empty result round trip = %#v, %v", roundTrip, err)
	}
	warning := DiagnosticResult{DiagnosticID: result.DiagnosticID, State: "succeeded", ResultClass: "incomplete", Series: []DiagnosticSeries{}}
	body, err = warning.CanonicalJSON()
	if err != nil || !strings.Contains(string(body), `"series":[]`) {
		t.Fatalf("warning result did not retain its empty series array: %s, %v", body, err)
	}
	if err := json.Unmarshal(body, &roundTrip); err != nil || roundTrip.ResultClass != "incomplete" || roundTrip.Series == nil {
		t.Fatalf("warning result round trip = %#v, %v", roundTrip, err)
	}
}

func TestDiagnosticResultTerminalStatesKeepEmptySeriesOnWire(t *testing.T) {
	diagnosticID := "00000000-0000-4000-8000-000000000001"
	for _, result := range []DiagnosticResult{
		{DiagnosticID: diagnosticID, State: "failed", ErrorCode: "query"},
		{DiagnosticID: diagnosticID, State: "canceled"},
		{DiagnosticID: diagnosticID, State: "timed_out"},
	} {
		t.Run(result.State, func(t *testing.T) {
			message := Envelope[DiagnosticResult]{Kind: KindDiagnosticResult, MessageID: "result-" + result.State,
				SessionGeneration: "1", Payload: result}
			if err := message.Validate(); err != nil {
				t.Fatalf("terminal result was rejected before serialization: %v", err)
			}
			wire, err := json.Marshal(message)
			if err != nil || !strings.Contains(string(wire), `"series":[]`) {
				t.Fatalf("terminal result did not retain empty series on wire: %s, %v", wire, err)
			}
			decoded, err := Decode(wire)
			if err != nil {
				t.Fatalf("terminal result wire round trip failed: %v", err)
			}
			var roundTrip DiagnosticResult
			if err := json.Unmarshal(decoded.Payload, &roundTrip); err != nil || roundTrip.State != result.State || roundTrip.Series == nil || len(roundTrip.Series) != 0 {
				t.Fatalf("terminal result round trip = %#v, %v", roundTrip, err)
			}
			missingSeries := strings.Replace(string(wire), `,"series":[]`, "", 1)
			if _, err := Decode([]byte(missingSeries)); err == nil {
				t.Fatal("terminal result without its required series array was accepted")
			}
			nullSeries := strings.Replace(string(wire), `"series":[]`, `"series":null`, 1)
			if _, err := Decode([]byte(nullSeries)); err == nil {
				t.Fatal("terminal result with a null series array was accepted")
			}
		})
	}
	nonemptyFailure := DiagnosticResult{DiagnosticID: diagnosticID, State: "failed", ErrorCode: "query",
		Series: []DiagnosticSeries{{Labels: map[string]string{"service": "api"}, Time: "1700000000", Value: "1"}}}
	if err := nonemptyFailure.Validate(); err == nil {
		t.Fatal("failed diagnostic result with partial series was accepted")
	}
}

func TestDecodeEnforcesProtocolVersionAndCapabilityUniqueness(t *testing.T) {
	for _, message := range []string{
		strings.Replace(validHello, `"protocol_versions":["almanac.agent.v1"]`, `"protocol_versions":["almanac.agent.v1","almanac.agent.v1"]`, 1),
		strings.Replace(validHello, `"protocol_versions":["almanac.agent.v1"]`, `"protocol_versions":["almanac.agent.v1","`+strings.Repeat("x", 65)+`"]`, 1),
		strings.Replace(validHello, `"capabilities":[]`, `"capabilities":[{"provider":"db","configuration_formats":["v1","v1"],"operations":["collect"]}]`, 1),
		strings.Replace(validHello, `"capabilities":[]`, `"capabilities":[{"provider":"db","configuration_formats":["v1"],"operations":["collect","collect"]}]`, 1),
	} {
		if _, err := Decode([]byte(message)); err == nil {
			t.Fatal("accepted duplicate or oversized protocol capability value")
		}
	}
}

func TestDecodeRejectsMismatchedLeaseGeneration(t *testing.T) {
	message := validWelcomeJSON()
	message = strings.Replace(message, `"execution_lease":{"lease_id":"lease-1","agent_id":"00000000-0000-4000-8000-000000000001","session_generation":"1"`, `"execution_lease":{"lease_id":"lease-1","agent_id":"00000000-0000-4000-8000-000000000001","session_generation":"2"`, 1)
	if _, err := Decode([]byte(message)); err == nil {
		t.Fatal("welcome accepted a lease from another ownership generation")
	}
}

func TestDecodeRequiresCanonicalDecimalStrings(t *testing.T) {
	for _, generation := range []string{`"+1"`, `"-0"`, `"01"`} {
		message := strings.Replace(validHello, `"session_generation":"0"`, `"session_generation":`+generation, 1)
		if _, err := Decode([]byte(message)); err == nil {
			t.Fatalf("accepted non-canonical generation %s", generation)
		}
	}
	for _, invalid := range []string{`"+1"`, `"-0"`} {
		message := strings.Replace(validWelcomeJSON(), `"configuration_revision":"1"`, `"configuration_revision":`+invalid, 1)
		if _, err := Decode([]byte(message)); err == nil {
			t.Fatalf("accepted non-canonical configuration revision %s", invalid)
		}
	}
}

func TestDecodeAllowsFirstHeartbeatRenewalSequenceZero(t *testing.T) {
	message := `{"kind":"heartbeat","message_id":"hb-1","session_generation":"1","payload":{"request_nonce":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE","applied_configuration_revision":"0","scheduler":"ready","collection_progress":"not_started","last_attempt_at":null,"last_successful_query_at":null,"next_expected_execution_at":null,"lease_id":"lease-1","renewal_sequence":"0"}}`
	if _, err := Decode([]byte(message)); err != nil {
		t.Fatalf("first renewal sequence zero was rejected: %v", err)
	}
}

func TestDecodeRequiresDistinctCollectionProgress(t *testing.T) {
	valid := `{"kind":"heartbeat","message_id":"hb-1","session_generation":"1","payload":{"request_nonce":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE","applied_configuration_revision":"0","scheduler":"starting","collection_progress":"not_started","last_attempt_at":null,"last_successful_query_at":null,"next_expected_execution_at":null,"lease_id":"lease-1","renewal_sequence":"0"}}`
	if _, err := Decode([]byte(valid)); err != nil {
		t.Fatalf("valid heartbeat was rejected: %v", err)
	}
	missing := strings.Replace(valid, `,"collection_progress":"not_started"`, "", 1)
	if _, err := Decode([]byte(missing)); err == nil {
		t.Fatal("heartbeat without collection progress was accepted")
	}
	unknown := strings.Replace(valid, `"collection_progress":"not_started"`, `"collection_progress":"complete"`, 1)
	if _, err := Decode([]byte(unknown)); err == nil {
		t.Fatal("heartbeat with unknown collection progress was accepted")
	}
}

func TestDecodeRejectsZeroHeartbeatAckSequence(t *testing.T) {
	valid := validHeartbeatAckJSON()
	if _, err := Decode([]byte(valid)); err != nil {
		t.Fatalf("heartbeat-ack fixture is invalid: %v", err)
	}
	message := strings.Replace(valid, `"renewal_sequence":"1"`, `"renewal_sequence":"0"`, 1)
	if _, err := Decode([]byte(message)); err == nil {
		t.Fatal("heartbeat acknowledgement accepted zero renewal sequence")
	}
	mismatchedGeneration := strings.Replace(valid, `"session_generation":"1"`, `"session_generation":"2"`, 1)
	if _, err := Decode([]byte(mismatchedGeneration)); err == nil {
		t.Fatal("heartbeat acknowledgement accepted a lease from another generation")
	}
}

func TestDecodeRejectsAllZeroResumeProofAndNonce(t *testing.T) {
	zeroNonce := strings.Replace(validHello, "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 1)
	if _, err := Decode([]byte(zeroNonce)); err == nil {
		t.Fatal("all-zero nonce was accepted")
	}
	zeroProof := strings.Replace(validHello, "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 1)
	if _, err := Decode([]byte(zeroProof)); err == nil {
		t.Fatal("all-zero resume proof was accepted")
	}
}

func TestDecodeBindsLeaseAgentAndCanonicalMeasurementGrants(t *testing.T) {
	valid := validWelcomeJSONWithGrants(`[{"sli_id":"00000000-0000-4000-8000-000000000010","measurement_revision":"1"}]`)
	if _, err := Decode([]byte(valid)); err != nil {
		t.Fatalf("canonical measurement grant was rejected: %v", err)
	}
	duplicate := validWelcomeJSONWithGrants(`[{"sli_id":"00000000-0000-4000-8000-000000000010","measurement_revision":"1"},{"sli_id":"00000000-0000-4000-8000-000000000010","measurement_revision":"2"}]`)
	if _, err := Decode([]byte(duplicate)); err == nil {
		t.Fatal("duplicate SLI grant was accepted")
	}
	wrongAgent := strings.Replace(validWelcomeJSON(), `"agent_id":"00000000-0000-4000-8000-000000000001","session_generation":"1"`, `"agent_id":"00000000-0000-4000-8000-000000000002","session_generation":"1"`, 1)
	if _, err := Decode([]byte(wrongAgent)); err == nil {
		t.Fatal("welcome accepted a lease for another agent")
	}
}

func TestDecodeRejectsNullArraysAndMissingLeaseFields(t *testing.T) {
	for _, message := range []string{
		strings.Replace(validHello, `"capabilities":[]`, `"capabilities":null`, 1),
		strings.Replace(validWelcomeJSON(), `"allowed_measurements":[]`, `"allowed_measurements":null`, 1),
		strings.Replace(validWelcomeJSON(), `,"valid_until":"2026-10-27T13:00:00Z"`, "", 1),
	} {
		if _, err := Decode([]byte(message)); err == nil {
			t.Fatalf("accepted invalid required/null value: %s", message)
		}
	}
}

func TestDecodeErrorsDoNotEchoUntrustedValues(t *testing.T) {
	unknownKind := strings.Replace(validHello, `"hello"`, `"secret-token"`, 1)
	if _, err := Decode([]byte(unknownKind)); err == nil || strings.Contains(err.Error(), "secret-token") || !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("unknown kind error leaked input: %v", err)
	}
	unknownOperation := strings.Replace(validHello, `"capabilities":[]`, `"capabilities":[{"provider":"db","configuration_formats":[],"operations":["secret-token"]}]`, 1)
	if _, err := Decode([]byte(unknownOperation)); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("operation error leaked input: %v", err)
	}
	if _, err := Decode([]byte(`{"kind":"hello","payload":{"api_key":"secret-token"`)); err == nil || strings.Contains(err.Error(), "secret-token") || !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("malformed JSON error leaked input: %v", err)
	}
}

func TestConfigurationSnapshotRequiresTypedEmptyAssignments(t *testing.T) {
	snapshot, err := DecodeConfigurationSnapshot([]byte(`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1","assignments":[],"slis":[]}`))
	if err != nil {
		t.Fatalf("valid empty snapshot: %v", err)
	}
	if snapshot.ConfigurationRevision != "1" || len(snapshot.Assignments) != 0 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	for _, body := range []string{
		`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"0","assignments":[]}`,
		`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1","assignments":null,"slis":[]}`,
		`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1"}`,
		`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1","assignments":[],"slis":null}`,
	} {
		if _, err := DecodeConfigurationSnapshot([]byte(body)); err == nil {
			t.Fatalf("accepted invalid snapshot: %s", body)
		}
	}
}

func TestConfigurationSnapshotFencesLeaseAuthority(t *testing.T) {
	snapshot := ConfigurationSnapshot{AgentID: "00000000-0000-4000-8000-000000000001", ConfigurationRevision: "1", Assignments: []DatasourceAssignment{}, SLIs: []SLICollection{}}
	lease := Lease{LeaseID: "lease-1", AgentID: snapshot.AgentID, SessionGeneration: "1", ConfigurationRevision: "1", AllowedMeasurements: []MeasurementGrant{}, ValidFrom: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), ValidUntil: time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC)}
	if err := ValidateSnapshotLease(snapshot, lease); err != nil {
		t.Fatalf("empty configuration with no authority: %v", err)
	}
	snapshot.Assignments = []DatasourceAssignment{{DatasourceID: "00000000-0000-4000-8000-000000000002", DatasourceVersion: "3", Name: "primary-metrics", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test", AuthenticationMode: "none", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}}
	snapshot.SLIs = []SLICollection{{SLIID: "00000000-0000-4000-8000-000000000010", MeasurementRevision: "1", DatasourceID: "00000000-0000-4000-8000-000000000002", DatasourceVersion: "3", MeasurementKind: MeasurementKindEventCounts, ComponentMode: ComponentModeIntervalCount, GoodQuery: "sum(rate(request_success[1m]))", TotalQuery: "sum(rate(request_total[1m]))", EffectiveFrom: time.Date(2026, 10, 27, 12, 1, 0, 0, time.UTC)}}
	lease.AllowedMeasurements = []MeasurementGrant{{SLIID: "00000000-0000-4000-8000-000000000010", MeasurementRevision: "1"}}
	if err := ValidateSnapshotLease(snapshot, lease); err != nil {
		t.Fatalf("matching acknowledged measurement grant: %v", err)
	}
	lease.AllowedMeasurements[0].MeasurementRevision = "2"
	if err := ValidateSnapshotLease(snapshot, lease); err == nil {
		t.Fatal("grant for another measurement revision was accepted")
	}
}

func TestConfigurationSnapshotRejectsUnsupportedMeasurementSemantics(t *testing.T) {
	snapshot := ConfigurationSnapshot{AgentID: "00000000-0000-4000-8000-000000000001", ConfigurationRevision: "1", Assignments: []DatasourceAssignment{{DatasourceID: "00000000-0000-4000-8000-000000000002", DatasourceVersion: "3", Name: "primary-metrics", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test", AuthenticationMode: "none", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}}, SLIs: []SLICollection{{SLIID: "00000000-0000-4000-8000-000000000010", MeasurementRevision: "1", DatasourceID: "00000000-0000-4000-8000-000000000002", DatasourceVersion: "3", MeasurementKind: MeasurementKindEventCounts, ComponentMode: ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", EffectiveFrom: time.Date(2026, 10, 27, 12, 1, 0, 0, time.UTC)}}}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("valid event-count interval configuration rejected: %v", err)
	}
	snapshot.SLIs[0].MeasurementKind = "availability_ratio"
	if err := snapshot.Validate(); err == nil {
		t.Fatal("unsupported measurement kind was accepted")
	}
	snapshot.SLIs[0].MeasurementKind = MeasurementKindEventCounts
	snapshot.SLIs[0].ComponentMode = "sum_over_interval"
	if err := snapshot.Validate(); err == nil {
		t.Fatal("unsupported component mode was accepted")
	}
}

func TestConfigurationHTTPResponseUsesRevisionBoundETagAndNotModifiedState(t *testing.T) {
	snapshot := &ConfigurationSnapshot{AgentID: "00000000-0000-4000-8000-000000000001", ConfigurationRevision: "1", Assignments: []DatasourceAssignment{}, SLIs: []SLICollection{}}
	response := ConfigurationHTTPResponse{StatusCode: 200, ETag: snapshot.ETag(), Snapshot: snapshot}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid snapshot response: %v", err)
	}
	response = ConfigurationHTTPResponse{IfNoneMatch: snapshot.ETag(), StatusCode: 304, ETag: snapshot.ETag()}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid not-modified response: %v", err)
	}
	response.ETag = `"agent-config:00000000-0000-4000-8000-000000000001:2"`
	if err := response.Validate(); err == nil {
		t.Fatal("304 response mismatching If-None-Match was accepted")
	}
	response.ETag = snapshot.ETag()
	response.Snapshot = snapshot
	if err := response.Validate(); err == nil {
		t.Fatal("304 response with body was accepted")
	}
	response = ConfigurationHTTPResponse{StatusCode: 200, ETag: `"agent-config:00000000-0000-4000-8000-000000000002:1"`, Snapshot: snapshot}
	if err := response.Validate(); err == nil {
		t.Fatal("ETag for another agent was accepted")
	}
}

func TestConfigurationMessagesAreBoundedAndTyped(t *testing.T) {
	for _, name := range []string{"configuration-changed", "configuration-applied", "configuration-failed", "shutdown", "revoked"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "api", "agent", "examples", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(data); err != nil {
			t.Fatalf("%s example invalid: %v", name, err)
		}
	}
	badCode := strings.Replace(`{"kind":"configuration-failed","message_id":"cf-1","session_generation":"1","payload":{"configuration_revision":"1","failure_code":"incompatible"}}`, "incompatible", strings.Repeat("x", 512), 1)
	if _, err := Decode([]byte(badCode)); err == nil {
		t.Fatal("unbounded failure code was accepted")
	}
	for _, data := range []string{
		`{"kind":"configuration-changed","message_id":"cc-1","session_generation":"1","payload":{"desired_configuration_revision":"0"}}`,
		`{"kind":"configuration-applied","message_id":"ca-1","session_generation":"1","payload":{"configuration_revision":"0"}}`,
		`{"kind":"configuration-failed","message_id":"cf-1","session_generation":"1","payload":{"configuration_revision":"1","failure_code":"source_password"}}`,
		`{"kind":"shutdown","message_id":"sd-1","session_generation":"1","payload":{"reason":"token-value"}}`,
		`{"kind":"revoked","message_id":"rv-1","session_generation":"1","payload":{"reason":"internal-detail"}}`,
	} {
		if _, err := Decode([]byte(data)); err == nil {
			t.Fatalf("accepted invalid control message: %s", data)
		}
	}
}

func TestConfigurationSnapshotValidatesTypedDatasourceAssignments(t *testing.T) {
	body := `{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"2","assignments":[{"datasource_id":"00000000-0000-4000-8000-000000000002","datasource_version":"3","name":"primary-metrics","provider":"prometheus","configuration_format":"prometheus-http-v1","endpoint":"https://metrics.example.test","tls":{"insecure_skip_verify":false,"ca_pem":""},"authentication_mode":"bearer","bearer_token":"secret-token","polling_interval_seconds":60,"settling_delay_seconds":60}],"slis":[]}`
	snapshot, err := DecodeConfigurationSnapshot([]byte(body))
	if err != nil || len(snapshot.Assignments) != 1 || snapshot.Assignments[0].DatasourceID != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("DecodeConfigurationSnapshot() = %+v, %v", snapshot, err)
	}
	for _, invalid := range []string{
		strings.Replace(body, `"bearer_token":"secret-token"`, `"bearer_token":""`, 1),
		strings.Replace(body, `"configuration_format":"prometheus-http-v1"`, `"configuration_format":"unknown"`, 1),
		strings.Replace(body, `"endpoint":"https://metrics.example.test"`, `"endpoint":"http://user:pw@metrics.example.test"`, 1),
	} {
		if _, err := DecodeConfigurationSnapshot([]byte(invalid)); err == nil {
			t.Fatalf("invalid assignment accepted: %s", invalid)
		}
	}
}

func TestDiagnosticProtocolBoundsRequestsAndResults(t *testing.T) {
	deadline := time.Date(2026, 10, 27, 12, 0, 30, 0, time.UTC)
	assignment := DatasourceAssignment{DatasourceID: "00000000-0000-4000-8000-000000000002", DatasourceVersion: "3", Name: "metrics-east", Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Endpoint: "https://metrics.example.test", AuthenticationMode: "none", PollingIntervalSeconds: 60, SettlingDelaySeconds: 60}
	queryEndpoint := assignment
	queryEndpoint.Endpoint = "https://metrics.example.test?tenant=blue"
	if err := queryEndpoint.Validate(); err == nil {
		t.Fatal("datasource assignment accepted an endpoint query string")
	}
	request := DiagnosticRequest{DiagnosticID: "00000000-0000-4000-8000-000000000003", AgentID: "00000000-0000-4000-8000-000000000001", Operation: "query_preview", DeadlineAt: deadline, Datasource: assignment, Query: "up"}
	if err := (Envelope[DiagnosticRequest]{Kind: KindDiagnosticRequest, MessageID: "dreq-1", SessionGeneration: "1", Payload: request}).Validate(); err != nil {
		t.Fatalf("valid diagnostic request rejected: %v", err)
	}
	request.Query = " "
	if err := request.Validate(); err == nil {
		t.Fatal("empty query was accepted")
	}
	result := DiagnosticResult{DiagnosticID: request.DiagnosticID, State: "succeeded", ResultClass: "complete", Series: []DiagnosticSeries{{Labels: map[string]string{"job": "api"}, Time: "1793102400.000", Value: "1"}}}
	if err := result.Validate(); err != nil {
		t.Fatalf("valid diagnostic result rejected: %v", err)
	}
	result.State, result.ResultClass, result.Series, result.ErrorCode = "failed", "", nil, "source leaked credentials"
	if err := result.Validate(); err == nil {
		t.Fatal("unrecognized error detail was accepted")
	}
	result.ErrorCode = "network"
	result.Message = strings.Repeat("x", 513)
	if err := result.Validate(); err == nil {
		t.Fatal("oversized diagnostic message was accepted")
	}
}

func TestDiagnosticResultSupportsSeparateBoundedHTTPSPayloadWithoutChangingInlineLimit(t *testing.T) {
	series := make([]DiagnosticSeries, 20)
	for seriesIndex := range series {
		labels := make(map[string]string, 32)
		for labelIndex := range 32 {
			labels[strings.Repeat("k", 120)+strconv.Itoa(labelIndex)] = strings.Repeat("v", 256)
		}
		series[seriesIndex] = DiagnosticSeries{Labels: labels, Time: "1793102400.000", Value: "1"}
	}
	result := DiagnosticResult{DiagnosticID: "00000000-0000-4000-8000-000000000003", State: "succeeded", ResultClass: "complete", Series: series}
	if err := result.Validate(); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("inline validator error = %v, want the 64KiB limit", err)
	}
	if err := result.ValidateHTTPS(); err != nil {
		t.Fatalf("bounded HTTPS validator rejected a valid large result: %v", err)
	}
}

func validWelcomeJSON() string { return validWelcomeJSONWithGrants(`[]`) }

func validWelcomeJSONWithGrants(grants string) string {
	return `{"kind":"welcome","message_id":"welcome-1","session_generation":"1","payload":{"protocol":"almanac.agent.v1","request_nonce":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE","server_time":"2026-10-27T12:00:00Z","agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1","renewal_sequence":"0","heartbeat_interval_ms":10000,"execution_lease":{"lease_id":"lease-1","agent_id":"00000000-0000-4000-8000-000000000001","session_generation":"1","configuration_revision":"1","allowed_measurements":` + grants + `,"valid_from":"2026-10-27T12:00:00Z","valid_until":"2026-10-27T13:00:00Z"}}}`
}

func TestEnvelopeRequiresPositiveGenerationAfterHello(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	lease := Lease{LeaseID: "lease-1", AgentID: "00000000-0000-4000-8000-000000000001", SessionGeneration: "1", ConfigurationRevision: "1", AllowedMeasurements: []MeasurementGrant{}, ValidFrom: now, ValidUntil: now.Add(time.Hour)}
	payload := Welcome{Protocol: ProtocolV1, RequestNonce: "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE", ServerTime: now, AgentID: lease.AgentID, ConfigurationRevision: "1", RenewalSequence: "0", HeartbeatIntervalMS: 10_000, ExecutionLease: lease}
	if err := (Envelope[Welcome]{Kind: KindWelcome, MessageID: "welcome-1", SessionGeneration: "1", Payload: payload}).Validate(); err != nil {
		t.Fatalf("valid empty-authority welcome: %v", err)
	}
	if err := (Envelope[Welcome]{Kind: KindWelcome, MessageID: "welcome-1", SessionGeneration: "0", Payload: payload}).Validate(); err == nil {
		t.Fatal("welcome with unconfirmed generation was accepted")
	}
	lease.AllowedMeasurements = []MeasurementGrant{{SLIID: "00000000-0000-4000-8000-000000000010", MeasurementRevision: "0"}}
	if err := lease.Validate(); err == nil {
		t.Fatal("zero measurement revision was accepted")
	}
}

func TestProtocolExamplesDecode(t *testing.T) {
	for _, name := range []string{"hello", "welcome", "heartbeat", "heartbeat-ack", "configuration-changed", "configuration-applied", "configuration-failed", "shutdown", "revoked"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "api", "agent", "examples", name+".json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(data); err != nil {
				t.Fatalf("example is invalid: %v", err)
			}
		})
	}
}

func TestProtocolSchemasAndEmptyConfigurationExample(t *testing.T) {
	for _, path := range []string{"../../api/agent/protocol-v1.schema.json", "../../api/agent/configuration-v1.schema.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]jsontext.Value
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatalf("invalid JSON schema %s: %v", path, err)
		}
		if strings.HasSuffix(path, "protocol-v1.schema.json") {
			var protocolSchema struct {
				OneOf []struct {
					Ref string `json:"$ref"`
				} `json:"oneOf"`
			}
			if err := json.Unmarshal(data, &protocolSchema); err != nil {
				t.Fatal(err)
			}
			want := []string{"#/$defs/hello", "#/$defs/welcome", "#/$defs/heartbeat", "#/$defs/heartbeatAck", "#/$defs/configurationChanged", "#/$defs/configurationApplied", "#/$defs/configurationFailed", "#/$defs/diagnosticRequest", "#/$defs/diagnosticResult", "#/$defs/diagnosticCancel", "#/$defs/shutdown", "#/$defs/revoked"}
			got := make(map[string]struct{}, len(protocolSchema.OneOf))
			for _, item := range protocolSchema.OneOf {
				got[item.Ref] = struct{}{}
			}
			if len(got) != len(want) {
				t.Fatalf("protocol schema declares %d unique message kinds, want %d", len(got), len(want))
			}
			for _, ref := range want {
				if _, ok := got[ref]; !ok {
					t.Fatalf("protocol schema is missing %s", ref)
				}
			}
		}
		if strings.HasSuffix(path, "configuration-v1.schema.json") {
			var configSchema struct {
				Properties map[string]struct {
					MaxItems int `json:"maxItems"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(data, &configSchema); err != nil {
				t.Fatal(err)
			}
			if configSchema.Properties["assignments"].MaxItems != 256 {
				t.Fatal("configuration schema must bound typed datasource assignments to 256")
			}
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "agent", "examples", "configuration-empty.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeConfigurationSnapshot(data); err != nil {
		t.Fatalf("empty configuration example is invalid: %v", err)
	}
}

func validHeartbeatAckJSON() string {
	return `{"kind":"heartbeat-ack","message_id":"heartbeat-ack-1","session_generation":"1","payload":{"request_nonce":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE","renewal_sequence":"1","execution_lease":{"lease_id":"lease-1","agent_id":"00000000-0000-4000-8000-000000000001","session_generation":"1","configuration_revision":"1","allowed_measurements":[],"valid_from":"2026-10-27T12:00:00Z","valid_until":"2026-10-27T13:00:00Z"}}}`
}
