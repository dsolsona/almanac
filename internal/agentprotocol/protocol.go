// Package agentprotocol defines the bounded v1 agent control protocol.
package agentprotocol

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	ProtocolV1            = "almanac.agent.v1"
	MaxMessageBytes       = 256 << 10
	MaxQueueMessages      = 128
	MaxQueueBytes         = 2 << 20
	MaxCapabilities       = 256
	MaxMeasurements       = 10_000
	MaxConfigurationBytes = 1 << 20
	MaxHeartbeatMillis    = 30_000
	InitialGeneration     = "0"
	MaxEnvelopeIDLength   = 128
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
	AgentID               string `json:"agent_id"`
	ConfigurationRevision string `json:"configuration_revision"`
	// Assignments is empty in this increment. Its datasource item contract is
	// deferred until datasource configuration has an accepted schema.
	Assignments []jsontext.Value `json:"assignments"`
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
		KindShutdown, KindRevoked:
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
	if len(p.ProtocolVersions) == 0 || len(p.ProtocolVersions) > 8 || hasDuplicates(p.ProtocolVersions) || !slices.Contains(p.ProtocolVersions, ProtocolV1) {
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
	if s.Assignments == nil || len(s.Assignments) != 0 {
		return invalidField("configuration_snapshot.assignments")
	}
	return nil
}

func invalidField(field string) error { return fmt.Errorf("%w: %s", ErrInvalidMessage, field) }

func invalidIndexedField(field string, index int) error {
	return fmt.Errorf("%w: %s[%d]", ErrInvalidMessage, field, index)
}

// ValidateSnapshotLease ensures a lease grants only measurements present in the
// exact coherent configuration snapshot. An empty assignment therefore permits
// only an empty measurement grant.
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
	if len(snapshot.Assignments) == 0 && len(lease.AllowedMeasurements) != 0 {
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
