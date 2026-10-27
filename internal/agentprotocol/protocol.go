// Package agentprotocol defines the bounded v1 agent control protocol.
package agentprotocol

import (
	"bytes"
	"crypto/x509"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ProtocolV1                     = "almanac.agent.v1"
	MaxMessageBytes                = 256 << 10
	MaxQueueMessages               = 128
	MaxQueueBytes                  = 2 << 20
	MaxCapabilities                = 256
	MaxMeasurements                = 10_000
	MaxConfigurationBytes          = 1 << 20
	MaxHeartbeatMillis             = 30_000
	InitialGeneration              = "0"
	MaxEnvelopeIDLength            = 128
	MaxInlineDiagnosticResultBytes = 64 << 10
	MaxHTTPSDiagnosticResultBytes  = 1 << 20
)

type Kind string

const (
	KindHello                Kind = "hello"
	KindWelcome              Kind = "welcome"
	KindHeartbeat            Kind = "heartbeat"
	KindHeartbeatAck         Kind = "heartbeat-ack"
	KindConfigurationChanged Kind = "configuration-changed"
	KindConfigurationApplied Kind = "configuration-applied"
	KindConfigurationFailed  Kind = "configuration-failed"
	KindDiagnosticRequest    Kind = "diagnostic-request"
	KindDiagnosticResult     Kind = "diagnostic-result"
	KindDiagnosticCancel     Kind = "diagnostic-cancel"
	KindShutdown             Kind = "shutdown"
	KindRevoked              Kind = "revoked"
)

var (
	ErrInvalidMessage  = errors.New("invalid agent protocol message")
	ErrMessageTooLarge = errors.New("agent protocol message exceeds size limit")
	ErrUnknownKind     = errors.New("unknown agent protocol message kind")
)

// Envelope is one application message. A first-acquire hello uses generation
// "0"; a same-process resume hello and all non-hello messages use a positive
// generation.
type Envelope[T any] struct {
	Kind              Kind   `json:"kind"`
	MessageID         string `json:"message_id"`
	SessionGeneration string `json:"session_generation"`
	Payload           T      `json:"payload"`
}

type Hello struct {
	ProtocolVersions []string     `json:"protocol_versions"`
	SoftwareVersion  string       `json:"software_version"`
	Capabilities     []Capability `json:"capabilities"`
	ResumeToken      string       `json:"resume_token"`
	RequestNonce     string       `json:"request_nonce"`
}

type Capability struct {
	Provider             string   `json:"provider"`
	ConfigurationFormats []string `json:"configuration_formats"`
	Operations           []string `json:"operations"`
}

type Welcome struct {
	Protocol              string    `json:"protocol"`
	RequestNonce          string    `json:"request_nonce"`
	ServerTime            time.Time `json:"server_time"`
	AgentID               string    `json:"agent_id"`
	ConfigurationRevision string    `json:"configuration_revision"`
	RenewalSequence       string    `json:"renewal_sequence"`
	HeartbeatIntervalMS   uint32    `json:"heartbeat_interval_ms"`
	ExecutionLease        Lease     `json:"execution_lease"`
}

type Heartbeat struct {
	RequestNonce                 string     `json:"request_nonce"`
	AppliedConfigurationRevision string     `json:"applied_configuration_revision"`
	Scheduler                    string     `json:"scheduler"`
	CollectionProgress           string     `json:"collection_progress"`
	LastAttemptAt                *time.Time `json:"last_attempt_at"`
	LastSuccessfulQueryAt        *time.Time `json:"last_successful_query_at"`
	NextExpectedExecutionAt      *time.Time `json:"next_expected_execution_at"`
	LeaseID                      string     `json:"lease_id"`
	RenewalSequence              string     `json:"renewal_sequence"`
}

type HeartbeatAck struct {
	RequestNonce    string `json:"request_nonce"`
	ExecutionLease  Lease  `json:"execution_lease"`
	RenewalSequence string `json:"renewal_sequence"`
}

type ConfigurationChanged struct {
	DesiredRevision string `json:"desired_configuration_revision"`
}
type ConfigurationApplied struct {
	ConfigurationRevision string `json:"configuration_revision"`
}
type ConfigurationFailed struct {
	ConfigurationRevision string `json:"configuration_revision"`
	FailureCode           string `json:"failure_code"`
}
type DiagnosticRequest struct {
	DiagnosticID string               `json:"diagnostic_id"`
	AgentID      string               `json:"agent_id"`
	Operation    string               `json:"operation"`
	DeadlineAt   time.Time            `json:"deadline_at"`
	Datasource   DatasourceAssignment `json:"datasource"`
	Query        string               `json:"query,omitempty"`
}
type DiagnosticSeries struct {
	Labels map[string]string `json:"labels"`
	Time   string            `json:"time"`
	Value  string            `json:"value"`
}
type DiagnosticResult struct {
	DiagnosticID string             `json:"diagnostic_id"`
	State        string             `json:"state"`
	ResultClass  string             `json:"result_class,omitempty"`
	Series       []DiagnosticSeries `json:"series,omitempty"`
	ErrorCode    string             `json:"error_code,omitempty"`
	Message      string             `json:"message,omitempty"`
}
type DiagnosticCancel struct {
	DiagnosticID string `json:"diagnostic_id"`
}
type Shutdown struct {
	Reason string `json:"reason"`
}
type Revoked struct {
	Reason string `json:"reason"`
}

type MeasurementGrant struct {
	SLIID               string `json:"sli_id"`
	MeasurementRevision string `json:"measurement_revision"`
}

// Lease is durable backend evidence. An empty AllowedMeasurements slice is valid
// and grants no SLI query authority.
type Lease struct {
	LeaseID               string             `json:"lease_id"`
	AgentID               string             `json:"agent_id"`
	SessionGeneration     string             `json:"session_generation"`
	ConfigurationRevision string             `json:"configuration_revision"`
	AllowedMeasurements   []MeasurementGrant `json:"allowed_measurements"`
	ValidFrom             time.Time          `json:"valid_from"`
	ValidUntil            time.Time          `json:"valid_until"`
}

type ConfigurationSnapshot struct {
	AgentID               string                 `json:"agent_id"`
	ConfigurationRevision string                 `json:"configuration_revision"`
	Assignments           []DatasourceAssignment `json:"assignments"`
}

// DatasourceAssignment is an authorized agent-only configuration view. Secret
// fields are delivered over the authenticated TLS configuration endpoint only.
type DatasourceAssignment struct {
	DatasourceID           string        `json:"datasource_id"`
	DatasourceVersion      string        `json:"datasource_version"`
	Name                   string        `json:"name"`
	Provider               string        `json:"provider"`
	ConfigurationFormat    string        `json:"configuration_format"`
	Endpoint               string        `json:"endpoint"`
	TLS                    DatasourceTLS `json:"tls"`
	AuthenticationMode     string        `json:"authentication_mode"`
	Username               string        `json:"username,omitempty"`
	Password               string        `json:"password,omitempty"`
	BearerToken            string        `json:"bearer_token,omitempty"`
	PollingIntervalSeconds int           `json:"polling_interval_seconds"`
	SettlingDelaySeconds   int           `json:"settling_delay_seconds"`
}

type DatasourceTLS struct {
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	CAPEM              string `json:"ca_pem"`
}

// Validate checks a decoded, typed v1 envelope and its bounded payload.
func (e Envelope[T]) Validate() error {
	if !knownKind(e.Kind) {
		return fmt.Errorf("%w: %w", ErrInvalidMessage, ErrUnknownKind)
	}
	if e.MessageID == "" || len(e.MessageID) > MaxEnvelopeIDLength {
		return invalidField("message_id")
	}
	if _, err := positiveDecimal(e.SessionGeneration, true); err != nil || (e.Kind != KindHello && e.SessionGeneration == InitialGeneration) {
		return invalidField("session_generation")
	}
	return validateEnvelopePayload(e.Kind, e.SessionGeneration, any(e.Payload))
}

// Decode validates limits and required fields while preserving additive unknown
// fields in the payload as forward-compatible JSON.
func Decode(data []byte) (Envelope[jsontext.Value], error) {
	if len(data) == 0 {
		return Envelope[jsontext.Value]{}, invalidField("envelope")
	}
	if len(data) > MaxMessageBytes {
		return Envelope[jsontext.Value]{}, ErrMessageTooLarge
	}
	var e Envelope[jsontext.Value]
	if json.Unmarshal(data, &e) != nil {
		return Envelope[jsontext.Value]{}, fmt.Errorf("%w: malformed JSON envelope", ErrInvalidMessage)
	}
	if err := e.Validate(); err != nil {
		return Envelope[jsontext.Value]{}, err
	}
	return e, nil
}

func knownKind(kind Kind) bool {
	switch kind {
	case KindHello, KindWelcome, KindHeartbeat, KindHeartbeatAck,
		KindConfigurationChanged, KindConfigurationApplied, KindConfigurationFailed,
		KindDiagnosticRequest, KindDiagnosticResult, KindDiagnosticCancel, KindShutdown, KindRevoked:
		return true
	default:
		return false
	}
}

func validatePayload(kind Kind, raw jsontext.Value) error {
	switch kind {
	case KindHello:
		if err := requireFields(raw, "protocol_versions", "software_version", "capabilities", "resume_token", "request_nonce"); err != nil {
			return err
		}
		var p Hello
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("hello.payload")
		}
		return p.Validate()
	case KindWelcome:
		if err := requireFields(raw, "protocol", "request_nonce", "server_time", "agent_id", "configuration_revision", "renewal_sequence", "heartbeat_interval_ms", "execution_lease"); err != nil {
			return err
		}
		var p Welcome
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("welcome.payload")
		}
		return p.Validate()
	case KindHeartbeat:
		if err := requireFields(raw, "request_nonce", "applied_configuration_revision", "scheduler", "collection_progress", "lease_id", "renewal_sequence"); err != nil {
			return err
		}
		var p Heartbeat
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("heartbeat.payload")
		}
		return p.Validate()
	case KindHeartbeatAck:
		if err := requireFields(raw, "request_nonce", "execution_lease", "renewal_sequence"); err != nil {
			return err
		}
		var p HeartbeatAck
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("heartbeat_ack.payload")
		}
		return p.Validate()
	case KindConfigurationChanged:
		if err := requireFields(raw, "desired_configuration_revision"); err != nil {
			return err
		}
		var p ConfigurationChanged
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("configuration_changed.payload")
		}
		return p.Validate()
	case KindConfigurationApplied:
		if err := requireFields(raw, "configuration_revision"); err != nil {
			return err
		}
		var p ConfigurationApplied
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("configuration_applied.payload")
		}
		return p.Validate()
	case KindConfigurationFailed:
		if err := requireFields(raw, "configuration_revision", "failure_code"); err != nil {
			return err
		}
		var p ConfigurationFailed
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("configuration_failed.payload")
		}
		return p.Validate()
	case KindDiagnosticRequest:
		if err := requireFields(raw, "diagnostic_id", "agent_id", "operation", "deadline_at", "datasource"); err != nil {
			return err
		}
		var p DiagnosticRequest
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("diagnostic_request.payload")
		}
		return p.Validate()
	case KindDiagnosticResult:
		if err := requireFields(raw, "diagnostic_id", "state"); err != nil {
			return err
		}
		var p DiagnosticResult
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("diagnostic_result.payload")
		}
		return p.Validate()
	case KindDiagnosticCancel:
		if err := requireFields(raw, "diagnostic_id"); err != nil {
			return err
		}
		var p DiagnosticCancel
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("diagnostic_cancel.payload")
		}
		return p.Validate()
	case KindShutdown:
		if err := requireFields(raw, "reason"); err != nil {
			return err
		}
		var p Shutdown
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("shutdown.payload")
		}
		return p.Validate()
	case KindRevoked:
		if err := requireFields(raw, "reason"); err != nil {
			return err
		}
		var p Revoked
		if json.Unmarshal(raw, &p) != nil {
			return invalidField("revoked.payload")
		}
		return p.Validate()
	default:
		return fmt.Errorf("%w: %w", ErrInvalidMessage, ErrUnknownKind)
	}
}

func requireFields(raw jsontext.Value, names ...string) error {
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return invalidField("payload")
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return invalidField("payload.required_field")
		}
	}
	return nil
}

func validateTypedPayload(kind Kind, payload any) error {
	if raw, ok := payload.(jsontext.Value); ok {
		return validatePayload(kind, raw)
	}
	switch kind {
	case KindHello:
		p, ok := payload.(Hello)
		if !ok {
			return fmt.Errorf("hello requires Hello payload")
		}
		return p.Validate()
	case KindWelcome:
		p, ok := payload.(Welcome)
		if !ok {
			return fmt.Errorf("welcome requires Welcome payload")
		}
		return p.Validate()
	case KindHeartbeat:
		p, ok := payload.(Heartbeat)
		if !ok {
			return fmt.Errorf("heartbeat requires Heartbeat payload")
		}
		return p.Validate()
	case KindHeartbeatAck:
		p, ok := payload.(HeartbeatAck)
		if !ok {
			return fmt.Errorf("heartbeat-ack requires HeartbeatAck payload")
		}
		return p.Validate()
	case KindConfigurationChanged:
		p, ok := payload.(ConfigurationChanged)
		if !ok {
			return invalidField("configuration_changed.payload")
		}
		return p.Validate()
	case KindConfigurationApplied:
		p, ok := payload.(ConfigurationApplied)
		if !ok {
			return invalidField("configuration_applied.payload")
		}
		return p.Validate()
	case KindConfigurationFailed:
		p, ok := payload.(ConfigurationFailed)
		if !ok {
			return invalidField("configuration_failed.payload")
		}
		return p.Validate()
	case KindDiagnosticRequest:
		p, ok := payload.(DiagnosticRequest)
		if !ok {
			return invalidField("diagnostic_request.payload")
		}
		return p.Validate()
	case KindDiagnosticResult:
		p, ok := payload.(DiagnosticResult)
		if !ok {
			return invalidField("diagnostic_result.payload")
		}
		return p.Validate()
	case KindDiagnosticCancel:
		p, ok := payload.(DiagnosticCancel)
		if !ok {
			return invalidField("diagnostic_cancel.payload")
		}
		return p.Validate()
	case KindShutdown:
		p, ok := payload.(Shutdown)
		if !ok {
			return invalidField("shutdown.payload")
		}
		return p.Validate()
	case KindRevoked:
		p, ok := payload.(Revoked)
		if !ok {
			return invalidField("revoked.payload")
		}
		return p.Validate()
	default:
		return fmt.Errorf("%w: %w", ErrInvalidMessage, ErrUnknownKind)
	}
}

func validateEnvelopePayload(kind Kind, generation string, payload any) error {
	if err := validateTypedPayload(kind, payload); err != nil {
		return err
	}
	switch kind {
	case KindWelcome:
		var value Welcome
		switch typed := payload.(type) {
		case Welcome:
			value = typed
		case jsontext.Value:
			if json.Unmarshal(typed, &value) != nil {
				return invalidField("welcome.payload")
			}
		}
		if value.ExecutionLease.SessionGeneration != generation || value.ExecutionLease.ConfigurationRevision != value.ConfigurationRevision || value.ExecutionLease.AgentID != value.AgentID {
			return invalidField("welcome.execution_lease_binding")
		}
	case KindHeartbeatAck:
		var value HeartbeatAck
		switch typed := payload.(type) {
		case HeartbeatAck:
			value = typed
		case jsontext.Value:
			if json.Unmarshal(typed, &value) != nil {
				return invalidField("heartbeat_ack.payload")
			}
		}
		if value.ExecutionLease.SessionGeneration != generation {
			return invalidField("heartbeat_ack.execution_lease_binding")
		}
	}
	return nil
}

func (p Hello) Validate() error {
	if len(p.ProtocolVersions) == 0 || len(p.ProtocolVersions) > 8 || hasDuplicates(p.ProtocolVersions) {
		return invalidField("hello.protocol_versions")
	}
	for _, version := range p.ProtocolVersions {
		if version == "" || len(version) > 64 {
			return invalidField("hello.protocol_versions")
		}
	}
	if strings.TrimSpace(p.SoftwareVersion) == "" || len(p.SoftwareVersion) > 128 {
		return invalidField("hello.software_version")
	}
	if err := validateNonce(p.ResumeToken); err != nil {
		return invalidField("hello.resume_token")
	}
	if err := validateNonce(p.RequestNonce); err != nil {
		return invalidField("hello.request_nonce")
	}
	if p.Capabilities == nil || len(p.Capabilities) > MaxCapabilities {
		return invalidField("hello.capabilities")
	}
	for i, c := range p.Capabilities {
		if strings.TrimSpace(c.Provider) == "" || len(c.Provider) > 128 {
			return invalidIndexedField("hello.capabilities", i)
		}
		if c.ConfigurationFormats == nil || c.Operations == nil || len(c.ConfigurationFormats) > 64 || len(c.Operations) > 16 || hasDuplicates(c.ConfigurationFormats) || hasDuplicates(c.Operations) {
			return invalidIndexedField("hello.capabilities", i)
		}
		for _, op := range c.Operations {
			if !validOperation(op) {
				return invalidIndexedField("hello.capabilities.operations", i)
			}
		}
		for _, format := range c.ConfigurationFormats {
			if format == "" || len(format) > 128 {
				return invalidIndexedField("hello.capabilities.configuration_formats", i)
			}
		}
	}
	return nil
}

func (p Welcome) Validate() error {
	if p.Protocol != ProtocolV1 {
		return invalidField("welcome.protocol")
	}
	if err := validateNonce(p.RequestNonce); err != nil {
		return invalidField("welcome.request_nonce")
	}
	if !isUTC(p.ServerTime) {
		return invalidField("welcome.server_time")
	}
	if !canonicalUUID(p.AgentID) {
		return invalidField("welcome.agent_id")
	}
	if _, err := positiveDecimal(p.ConfigurationRevision, false); err != nil {
		return invalidField("welcome.configuration_revision")
	}
	if _, err := positiveDecimal(p.RenewalSequence, true); err != nil {
		return invalidField("welcome.renewal_sequence")
	}
	if p.HeartbeatIntervalMS == 0 || p.HeartbeatIntervalMS > MaxHeartbeatMillis {
		return invalidField("welcome.heartbeat_interval_ms")
	}
	if err := p.ExecutionLease.Validate(); err != nil {
		return err
	}
	if p.ExecutionLease.ConfigurationRevision != p.ConfigurationRevision {
		return invalidField("welcome.execution_lease.configuration_revision")
	}
	if p.ExecutionLease.AgentID != p.AgentID {
		return invalidField("welcome.execution_lease.agent_id")
	}
	return nil
}

func (p Heartbeat) Validate() error {
	if err := validateNonce(p.RequestNonce); err != nil {
		return invalidField("heartbeat.request_nonce")
	}
	if _, err := positiveDecimal(p.AppliedConfigurationRevision, true); err != nil {
		return invalidField("heartbeat.applied_configuration_revision")
	}
	if p.Scheduler != "starting" && p.Scheduler != "ready" && p.Scheduler != "degraded" && p.Scheduler != "stopped" {
		return invalidField("heartbeat.scheduler")
	}
	if p.CollectionProgress != "not_started" && p.CollectionProgress != "idle" && p.CollectionProgress != "running" && p.CollectionProgress != "blocked" {
		return invalidField("heartbeat.collection_progress")
	}
	for _, field := range []struct {
		name  string
		value *time.Time
	}{
		{name: "heartbeat.last_attempt_at", value: p.LastAttemptAt},
		{name: "heartbeat.last_successful_query_at", value: p.LastSuccessfulQueryAt},
		{name: "heartbeat.next_expected_execution_at", value: p.NextExpectedExecutionAt},
	} {
		if field.value != nil && !isUTC(*field.value) {
			return invalidField(field.name)
		}
	}
	if p.LeaseID == "" || len(p.LeaseID) > 128 {
		return invalidField("heartbeat.lease_id")
	}
	if _, err := positiveDecimal(p.RenewalSequence, true); err != nil {
		return invalidField("heartbeat.renewal_sequence")
	}
	return nil
}

func (p HeartbeatAck) Validate() error {
	if err := validateNonce(p.RequestNonce); err != nil {
		return invalidField("heartbeat_ack.request_nonce")
	}
	if err := p.ExecutionLease.Validate(); err != nil {
		return err
	}
	if _, err := positiveDecimal(p.RenewalSequence, false); err != nil {
		return invalidField("heartbeat_ack.renewal_sequence")
	}
	return nil
}

func (l Lease) Validate() error {
	if l.LeaseID == "" || len(l.LeaseID) > 128 {
		return invalidField("execution_lease.lease_id")
	}
	if !canonicalUUID(l.AgentID) {
		return invalidField("execution_lease.agent_id")
	}
	if _, err := positiveDecimal(l.SessionGeneration, false); err != nil {
		return invalidField("execution_lease.session_generation")
	}
	if _, err := positiveDecimal(l.ConfigurationRevision, false); err != nil {
		return invalidField("execution_lease.configuration_revision")
	}
	if l.AllowedMeasurements == nil || len(l.AllowedMeasurements) > MaxMeasurements {
		return invalidField("execution_lease.allowed_measurements")
	}
	seen := make(map[string]struct{}, len(l.AllowedMeasurements))
	for i, grant := range l.AllowedMeasurements {
		if err := grant.Validate(); err != nil {
			return invalidIndexedField("execution_lease.allowed_measurements", i)
		}
		if _, ok := seen[grant.SLIID]; ok {
			return invalidIndexedField("execution_lease.allowed_measurements", i)
		}
		seen[grant.SLIID] = struct{}{}
	}
	if !isUTC(l.ValidFrom) || !isUTC(l.ValidUntil) || !l.ValidUntil.After(l.ValidFrom) {
		return invalidField("execution_lease.validity")
	}
	if l.ValidUntil.Sub(l.ValidFrom) > time.Hour {
		return invalidField("execution_lease.validity")
	}
	return nil
}

func (m MeasurementGrant) Validate() error {
	if !canonicalUUID(m.SLIID) {
		return invalidField("measurement.sli_id")
	}
	if _, err := positiveDecimal(m.MeasurementRevision, false); err != nil {
		return invalidField("measurement.measurement_revision")
	}
	return nil
}

func (p ConfigurationChanged) Validate() error {
	if _, err := positiveDecimal(p.DesiredRevision, false); err != nil {
		return invalidField("configuration_changed.desired_configuration_revision")
	}
	return nil
}

func (p ConfigurationApplied) Validate() error {
	if _, err := positiveDecimal(p.ConfigurationRevision, false); err != nil {
		return invalidField("configuration_applied.configuration_revision")
	}
	return nil
}

func (p ConfigurationFailed) Validate() error {
	if _, err := positiveDecimal(p.ConfigurationRevision, false); err != nil {
		return invalidField("configuration_failed.configuration_revision")
	}
	if p.FailureCode != "invalid_snapshot" && p.FailureCode != "incompatible" && p.FailureCode != "apply_failed" {
		return invalidField("configuration_failed.failure_code")
	}
	return nil
}

func (p DiagnosticRequest) Validate() error {
	if !canonicalUUID(p.DiagnosticID) || !canonicalUUID(p.AgentID) || (p.Operation != "connection_test" && p.Operation != "query_preview") || p.DeadlineAt.IsZero() || p.DeadlineAt.Nanosecond()%1000 != 0 || p.Datasource.Validate() != nil || (p.Operation == "query_preview" && (strings.TrimSpace(p.Query) == "" || len(p.Query) > 4096)) || (p.Operation == "connection_test" && p.Query != "") {
		return invalidField("diagnostic_request")
	}
	return nil
}

func (p DiagnosticResult) Validate() error {
	return p.validate(MaxInlineDiagnosticResultBytes)
}

func (p DiagnosticResult) ValidateHTTPS() error {
	return p.validate(MaxHTTPSDiagnosticResultBytes)
}

func (p DiagnosticResult) CanonicalJSON() ([]byte, error) {
	if err := p.ValidateHTTPS(); err != nil {
		return nil, err
	}
	return json.Marshal(p, json.Deterministic(true))
}

func (p DiagnosticResult) validate(maxSerializedBytes int) error {
	if !canonicalUUID(p.DiagnosticID) || len(p.Series) > 20 || len(p.Message) > 512 || !utf8.ValidString(p.Message) {
		return invalidField("diagnostic_result")
	}
	switch p.State {
	case "succeeded":
		if (p.ResultClass != "complete" && p.ResultClass != "empty" && p.ResultClass != "incomplete") || p.ErrorCode != "" || p.Message != "" || p.Series == nil {
			return invalidField("diagnostic_result.success")
		}
	case "failed":
		if p.ResultClass != "" || !ValidDiagnosticErrorCode(p.ErrorCode) || p.Series != nil {
			return invalidField("diagnostic_result.failure")
		}
	case "canceled", "timed_out":
		if p.ResultClass != "" || p.ErrorCode != "" || p.Series != nil {
			return invalidField("diagnostic_result.terminal")
		}
	default:
		return invalidField("diagnostic_result.state")
	}
	for i, series := range p.Series {
		if len(series.Labels) > 32 || len(series.Time) > 32 || len(series.Value) > 64 {
			return invalidIndexedField("diagnostic_result.series", i)
		}
		for key, value := range series.Labels {
			if key == "" || len(key) > 128 || len(value) > 256 {
				return invalidIndexedField("diagnostic_result.series.labels", i)
			}
		}
	}
	encoded, err := json.Marshal(p, json.Deterministic(true))
	if err != nil || len(encoded) > maxSerializedBytes {
		return ErrMessageTooLarge
	}
	return nil
}

func ValidDiagnosticErrorCode(value string) bool {
	switch value {
	case "authentication", "incomplete", "network", "query", "rate_limited", "tls", "timeout", "unsupported", "assignment_revoked", "deadline":
		return true
	default:
		return false
	}
}

func (p DiagnosticCancel) Validate() error {
	if !canonicalUUID(p.DiagnosticID) {
		return invalidField("diagnostic_cancel.diagnostic_id")
	}
	return nil
}

func (p Shutdown) Validate() error {
	if p.Reason != "server_shutdown" && p.Reason != "maintenance" {
		return invalidField("shutdown.reason")
	}
	return nil
}

func (p Revoked) Validate() error {
	if p.Reason != "credential_revoked" && p.Reason != "agent_disabled" && p.Reason != "ownership_replaced" {
		return invalidField("revoked.reason")
	}
	return nil
}

func DecodeConfigurationSnapshot(data []byte) (ConfigurationSnapshot, error) {
	if len(data) == 0 {
		return ConfigurationSnapshot{}, invalidField("configuration_snapshot")
	}
	if len(data) > MaxConfigurationBytes {
		return ConfigurationSnapshot{}, ErrMessageTooLarge
	}
	if err := requireFields(jsontext.Value(data), "agent_id", "configuration_revision", "assignments"); err != nil {
		return ConfigurationSnapshot{}, err
	}
	var snapshot ConfigurationSnapshot
	if json.Unmarshal(data, &snapshot) != nil {
		return ConfigurationSnapshot{}, invalidField("configuration_snapshot")
	}
	if err := snapshot.Validate(); err != nil {
		return ConfigurationSnapshot{}, err
	}
	return snapshot, nil
}

func (s ConfigurationSnapshot) Validate() error {
	if !canonicalUUID(s.AgentID) {
		return invalidField("configuration_snapshot.agent_id")
	}
	if _, err := positiveDecimal(s.ConfigurationRevision, false); err != nil {
		return invalidField("configuration_snapshot.configuration_revision")
	}
	if s.Assignments == nil || len(s.Assignments) > 256 {
		return invalidField("configuration_snapshot.assignments")
	}
	seen := make(map[string]struct{}, len(s.Assignments))
	for i, assignment := range s.Assignments {
		if err := assignment.Validate(); err != nil {
			return invalidIndexedField("configuration_snapshot.assignments", i)
		}
		if _, exists := seen[assignment.DatasourceID]; exists {
			return invalidIndexedField("configuration_snapshot.assignments", i)
		}
		seen[assignment.DatasourceID] = struct{}{}
	}
	return nil
}

func (a DatasourceAssignment) Validate() error {
	endpoint, _ := url.Parse(a.Endpoint)
	if !canonicalUUID(a.DatasourceID) || !positiveDecimalCheck(a.DatasourceVersion, false) ||
		!validDatasourceName(a.Name) || a.Provider != "prometheus" || a.ConfigurationFormat != "prometheus-http-v1" ||
		!validDatasourceEndpoint(a.Endpoint) || a.PollingIntervalSeconds < 15 || a.PollingIntervalSeconds > 86400 || a.SettlingDelaySeconds != 60 || len(a.TLS.CAPEM) > 64<<10 {
		return invalidField("datasource_assignment")
	}
	if endpoint.Scheme == "http" && (a.TLS.InsecureSkipVerify || a.TLS.CAPEM != "") {
		return invalidField("datasource_assignment.tls.ca_pem")
	}
	remaining := bytes.TrimSpace([]byte(a.TLS.CAPEM))
	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return invalidField("datasource_assignment.tls.ca_pem")
		}
		certificates, err := x509.ParseCertificates(block.Bytes)
		if err != nil || len(certificates) != 1 || !certificates[0].IsCA {
			return invalidField("datasource_assignment.tls.ca_pem")
		}
		remaining = bytes.TrimSpace(rest)
	}
	if a.TLS.CAPEM != "" && len(bytes.TrimSpace([]byte(a.TLS.CAPEM))) == 0 {
		return invalidField("datasource_assignment.tls.ca_pem")
	}
	switch a.AuthenticationMode {
	case "none":
		if a.Username != "" || a.Password != "" || a.BearerToken != "" {
			return invalidField("datasource_assignment.authentication")
		}
	case "basic":
		if a.Username == "" || len(a.Username) > 256 || a.Password == "" || len(a.Password) > 16<<10 || a.BearerToken != "" {
			return invalidField("datasource_assignment.authentication")
		}
	case "bearer":
		if a.Username != "" || a.Password != "" || a.BearerToken == "" || len(a.BearerToken) > 16<<10 {
			return invalidField("datasource_assignment.authentication")
		}
	default:
		return invalidField("datasource_assignment.authentication_mode")
	}
	return nil
}

func validDatasourceName(value string) bool {
	if len(value) < 1 || len(value) > 63 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value[1:] {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

func validDatasourceEndpoint(value string) bool {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(u.Host, "\\\r\n\t") {
		return false
	}
	if port := u.Port(); port != "" {
		number, parseErr := strconv.Atoi(port)
		if parseErr != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
			return false
		}
	}
	return true
}

func invalidField(field string) error { return fmt.Errorf("%w: %s", ErrInvalidMessage, field) }

func invalidIndexedField(field string, index int) error {
	return fmt.Errorf("%w: %s[%d]", ErrInvalidMessage, field, index)
}

// ValidateSnapshotLease keeps datasource diagnostics separate from production
// observation grants. This increment has no SLI definitions to authorize.
func ValidateSnapshotLease(snapshot ConfigurationSnapshot, lease Lease) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if err := lease.Validate(); err != nil {
		return err
	}
	if snapshot.AgentID != lease.AgentID || snapshot.ConfigurationRevision != lease.ConfigurationRevision {
		return invalidField("configuration_snapshot.execution_lease_binding")
	}
	if len(lease.AllowedMeasurements) != 0 {
		// Datasource-only snapshots contain no SLI definitions; datasource
		// assignments therefore do not grant production observation queries.
		return invalidField("execution_lease.allowed_measurements")
	}
	return nil
}

// ConfigurationPath is the authenticated agent HTTP endpoint for coherent
// versioned snapshots; ETag and 304 behavior are represented by the response.
const ConfigurationPath = "/api/agent/v1/configuration"

type ConfigurationHTTPResponse struct {
	IfNoneMatch string
	StatusCode  int
	ETag        string
	Snapshot    *ConfigurationSnapshot
}

func (r ConfigurationHTTPResponse) Validate() error {
	if r.IfNoneMatch != "" && !validConfigurationETag(r.IfNoneMatch) {
		return invalidField("configuration_http.if_none_match")
	}
	if !validConfigurationETag(r.ETag) {
		return invalidField("configuration_http.etag")
	}
	switch r.StatusCode {
	case 200:
		if r.Snapshot == nil {
			return invalidField("configuration_http.snapshot")
		}
		if err := r.Snapshot.Validate(); err != nil {
			return err
		}
		if r.ETag != r.Snapshot.ETag() {
			return invalidField("configuration_http.etag_binding")
		}
		return nil
	case 304:
		if r.Snapshot != nil || r.IfNoneMatch == "" || r.ETag != r.IfNoneMatch {
			return invalidField("configuration_http.not_modified_body")
		}
		return nil
	default:
		return invalidField("configuration_http.status")
	}
}

func (s ConfigurationSnapshot) ETag() string {
	return `"agent-config:` + s.AgentID + `:` + s.ConfigurationRevision + `"`
}

func validConfigurationETag(value string) bool {
	if !strings.HasPrefix(value, `"agent-config:`) || !strings.HasSuffix(value, `"`) || strings.ContainsAny(value, "\r\n") || len(value) > 128 {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(value, `"agent-config:`), `"`)
	agentID, revision, found := strings.Cut(body, ":")
	return found && canonicalUUID(agentID) && positiveDecimalCheck(revision, false)
}

func positiveDecimalCheck(value string, allowZero bool) bool {
	_, err := positiveDecimal(value, allowZero)
	return err == nil
}
