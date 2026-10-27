package agentprotocol

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
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
	snapshot, err := DecodeConfigurationSnapshot([]byte(`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1","assignments":[]}`))
	if err != nil {
		t.Fatalf("valid empty snapshot: %v", err)
	}
	if snapshot.ConfigurationRevision != "1" || len(snapshot.Assignments) != 0 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	for _, body := range []string{
		`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"0","assignments":[]}`,
		`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1","assignments":null}`,
		`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1"}`,
	} {
		if _, err := DecodeConfigurationSnapshot([]byte(body)); err == nil {
			t.Fatalf("accepted invalid snapshot: %s", body)
		}
	}
}

func TestConfigurationSnapshotFencesLeaseAuthority(t *testing.T) {
	snapshot := ConfigurationSnapshot{AgentID: "00000000-0000-4000-8000-000000000001", ConfigurationRevision: "1", Assignments: []jsontext.Value{}}
	lease := Lease{LeaseID: "lease-1", AgentID: snapshot.AgentID, SessionGeneration: "1", ConfigurationRevision: "1", AllowedMeasurements: []MeasurementGrant{}, ValidFrom: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), ValidUntil: time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC)}
	if err := ValidateSnapshotLease(snapshot, lease); err != nil {
		t.Fatalf("empty configuration with no authority: %v", err)
	}
	lease.AllowedMeasurements = []MeasurementGrant{{SLIID: "00000000-0000-4000-8000-000000000010", MeasurementRevision: "1"}}
	if err := ValidateSnapshotLease(snapshot, lease); err == nil {
		t.Fatal("empty assignments retained query authority")
	}
}

func TestConfigurationHTTPResponseUsesRevisionBoundETagAndNotModifiedState(t *testing.T) {
	snapshot := &ConfigurationSnapshot{AgentID: "00000000-0000-4000-8000-000000000001", ConfigurationRevision: "1", Assignments: []jsontext.Value{}}
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

func TestConfigurationSnapshotRejectsAssignmentsUntilDatasourceContract(t *testing.T) {
	for _, body := range []string{
		`{"agent_id":"00000000-0000-4000-8000-000000000001","configuration_revision":"1","assignments":[{"future_datasource_contract":true}]}`,
	} {
		if _, err := DecodeConfigurationSnapshot([]byte(body)); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
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
			want := []string{"#/$defs/hello", "#/$defs/welcome", "#/$defs/heartbeat", "#/$defs/heartbeatAck", "#/$defs/configurationChanged", "#/$defs/configurationApplied", "#/$defs/configurationFailed", "#/$defs/shutdown", "#/$defs/revoked"}
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
			if configSchema.Properties["assignments"].MaxItems != 0 {
				t.Fatal("initial configuration schema must constrain assignments to empty")
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
