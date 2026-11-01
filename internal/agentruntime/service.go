// Package agentruntime coordinates protocol messages with durable agent ownership.
package agentruntime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/agentownership"
	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/storage"
)

const HeartbeatInterval = 10 * time.Second

var (
	ErrIncompatible = errors.New("agent runtime: incompatible protocol")
	ErrInvalidInput = errors.New("agent runtime: invalid message")
)

type Service struct {
	ownership *agentownership.Service
	now       func() time.Time
}

type Session struct {
	AgentID     string
	Generation  int64
	ResumeToken []byte
	Welcome     agentprotocol.Envelope[agentprotocol.Welcome]
}

func New(databaseURL string, now func() time.Time, runtimes ...*storage.Runtime) (*Service, error) {
	if _, err := storage.ParseDatabaseURL(databaseURL); err != nil || now == nil || len(runtimes) > 1 {
		return nil, ErrInvalidInput
	}
	var runtime *storage.Runtime
	if len(runtimes) == 1 {
		runtime = runtimes[0]
	}
	return &Service{ownership: agentownership.NewWithRuntime(databaseURL, now, runtime), now: now}, nil
}

func (s *Service) Open(ctx context.Context, token string, envelope agentprotocol.Envelope[agentprotocol.Hello]) (Session, error) {
	return s.OpenProtocol(ctx, token, envelope, agentprotocol.ProtocolV1)
}

func (s *Service) OpenProtocol(ctx context.Context, token string, envelope agentprotocol.Envelope[agentprotocol.Hello], protocol string) (Session, error) {
	if s == nil || s.ownership == nil || envelope.Kind != agentprotocol.KindHello {
		return Session{}, ErrInvalidInput
	}
	if err := envelope.Validate(); err != nil {
		return Session{}, ErrInvalidInput
	}
	resumeToken, err := base64.RawURLEncoding.DecodeString(envelope.Payload.ResumeToken)
	if err != nil {
		return Session{}, ErrInvalidInput
	}
	generation, err := strconv.ParseInt(envelope.SessionGeneration, 10, 64)
	if err != nil || generation < 0 {
		return Session{}, ErrInvalidInput
	}
	if (protocol != agentprotocol.ProtocolV1 && protocol != agentprotocol.ProtocolV2) || !containsProtocol(envelope.Payload.ProtocolVersions, protocol) {
		if _, err := s.ownership.RecordIncompatibleAttempt(ctx, token, generation, resumeToken); err != nil {
			return Session{}, err
		}
		return Session{}, ErrIncompatible
	}
	capabilities, err := json.Marshal(envelope.Payload.Capabilities)
	if err != nil {
		return Session{}, ErrInvalidInput
	}
	hello := &storage.AgentRuntimeHello{
		Protocol: protocol, SoftwareVersion: envelope.Payload.SoftwareVersion,
		Capabilities: capabilities,
	}
	var owner agentownership.Result
	if generation == 0 {
		owner, err = s.ownership.AcquireWithHello(ctx, token, resumeToken, hello)
	} else {
		owner, err = s.ownership.ResumeWithHello(ctx, token, generation, resumeToken, hello)
	}
	if err != nil {
		return Session{}, err
	}
	var allowed []agentprotocol.MeasurementGrant
	if json.Unmarshal(owner.AllowedMeasurements, &allowed) != nil || allowed == nil {
		return Session{}, ErrInvalidInput
	}
	if owner.LeaseID == "" || owner.ConfigurationRevision <= 0 || owner.LeaseValidFrom.IsZero() || owner.LeaseValidUntil.IsZero() {
		return Session{}, ErrInvalidInput
	}
	lease := agentprotocol.Lease{
		LeaseID: owner.LeaseID, AgentID: owner.AgentID,
		SessionGeneration:     strconv.FormatInt(owner.Generation, 10),
		ConfigurationRevision: strconv.FormatInt(owner.ConfigurationRevision, 10),
		AllowedMeasurements:   allowed, ValidFrom: owner.LeaseValidFrom.UTC(), ValidUntil: owner.LeaseValidUntil.UTC(),
	}
	messageID, err := newMessageID()
	if err != nil {
		return Session{}, ErrInvalidInput
	}
	welcome := agentprotocol.Envelope[agentprotocol.Welcome]{
		Kind: agentprotocol.KindWelcome, MessageID: messageID, SessionGeneration: strconv.FormatInt(owner.Generation, 10),
		Payload: agentprotocol.Welcome{
			Protocol: protocol, RequestNonce: envelope.Payload.RequestNonce,
			ServerTime: s.now().UTC(), AgentID: owner.AgentID,
			ConfigurationRevision: strconv.FormatInt(owner.ConfigurationRevision, 10),
			RenewalSequence:       strconv.FormatInt(owner.RenewalSequence, 10),
			HeartbeatIntervalMS:   uint32(HeartbeatInterval / time.Millisecond), ExecutionLease: lease,
		},
	}
	if err := welcome.Validate(); err != nil {
		return Session{}, ErrInvalidInput
	}
	return Session{AgentID: owner.AgentID, Generation: owner.Generation,
		ResumeToken: append([]byte(nil), resumeToken...), Welcome: welcome}, nil
}

func (s *Service) Renew(ctx context.Context, token string, session Session, envelope agentprotocol.Envelope[agentprotocol.Heartbeat], receivedAt time.Time) (agentprotocol.Envelope[agentprotocol.HeartbeatAck], error) {
	if s == nil || envelope.Kind != agentprotocol.KindHeartbeat || envelope.SessionGeneration != strconv.FormatInt(session.Generation, 10) {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, ErrInvalidInput
	}
	if err := envelope.Validate(); err != nil {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, ErrInvalidInput
	}
	payload := envelope.Payload
	sequence, err := strconv.ParseInt(payload.RenewalSequence, 10, 64)
	if err != nil || sequence < 0 {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, ErrInvalidInput
	}
	configurationRevision, err := strconv.ParseInt(payload.AppliedConfigurationRevision, 10, 64)
	if err != nil || configurationRevision < 0 {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, ErrInvalidInput
	}
	nonce, err := base64.RawURLEncoding.DecodeString(payload.RequestNonce)
	if err != nil {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, ErrInvalidInput
	}
	heartbeat := &storage.AgentRuntimeHeartbeat{
		ReceivedAt: receivedAt.UTC(), LeaseID: payload.LeaseID,
		AppliedConfigurationRevision: configurationRevision,
		SchedulerHealth:              payload.Scheduler, CollectionProgress: payload.CollectionProgress,
		LastAttemptAt: payload.LastAttemptAt, LastSuccessfulQueryAt: payload.LastSuccessfulQueryAt,
		NextExpectedExecutionAt: payload.NextExpectedExecutionAt,
	}
	owner, err := s.ownership.RenewWithHeartbeat(ctx, token, session.Generation, sequence,
		session.ResumeToken, nonce, heartbeat)
	if err != nil {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, err
	}
	var allowed []agentprotocol.MeasurementGrant
	if json.Unmarshal(owner.AllowedMeasurements, &allowed) != nil || allowed == nil {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, ErrInvalidInput
	}
	lease := agentprotocol.Lease{
		LeaseID: owner.LeaseID, AgentID: owner.AgentID,
		SessionGeneration:     strconv.FormatInt(owner.Generation, 10),
		ConfigurationRevision: strconv.FormatInt(owner.ConfigurationRevision, 10),
		AllowedMeasurements:   allowed, ValidFrom: owner.LeaseValidFrom.UTC(), ValidUntil: owner.LeaseValidUntil.UTC(),
	}
	messageID, err := newMessageID()
	if err != nil {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, ErrInvalidInput
	}
	ack := agentprotocol.Envelope[agentprotocol.HeartbeatAck]{
		Kind: agentprotocol.KindHeartbeatAck, MessageID: messageID, SessionGeneration: strconv.FormatInt(owner.Generation, 10),
		Payload: agentprotocol.HeartbeatAck{
			RequestNonce: payload.RequestNonce, ExecutionLease: lease,
			RenewalSequence: strconv.FormatInt(owner.RenewalSequence, 10),
		},
	}
	if err := ack.Validate(); err != nil {
		return agentprotocol.Envelope[agentprotocol.HeartbeatAck]{}, ErrInvalidInput
	}
	return ack, nil
}

func (s *Service) Applied(ctx context.Context, token string, session Session, envelope agentprotocol.Envelope[agentprotocol.ConfigurationApplied]) error {
	if s == nil || envelope.Kind != agentprotocol.KindConfigurationApplied || envelope.SessionGeneration != strconv.FormatInt(session.Generation, 10) {
		return ErrInvalidInput
	}
	if err := envelope.Validate(); err != nil {
		return ErrInvalidInput
	}
	revision, err := strconv.ParseInt(envelope.Payload.ConfigurationRevision, 10, 64)
	if err != nil || revision <= 0 {
		return ErrInvalidInput
	}
	return s.ownership.RecordAppliedProtocol(ctx, token, session.Generation, revision, session.ResumeToken, session.Welcome.Payload.Protocol)
}

func (s *Service) Failed(ctx context.Context, token string, session Session, envelope agentprotocol.Envelope[agentprotocol.ConfigurationFailed]) error {
	if s == nil || envelope.Kind != agentprotocol.KindConfigurationFailed || envelope.SessionGeneration != strconv.FormatInt(session.Generation, 10) {
		return ErrInvalidInput
	}
	if err := envelope.Validate(); err != nil {
		return ErrInvalidInput
	}
	revision, err := strconv.ParseInt(envelope.Payload.ConfigurationRevision, 10, 64)
	if err != nil || revision <= 0 {
		return ErrInvalidInput
	}
	return s.ownership.RecordConfigurationFailed(ctx, token, session.Generation, revision,
		envelope.Payload.FailureCode, session.ResumeToken)
}

func containsProtocol(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func newMessageID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}
