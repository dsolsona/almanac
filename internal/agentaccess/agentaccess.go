package agentaccess

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage"
)

var (
	ErrInvalidCredential = errors.New("invalid agent credential")
	ErrUnavailable       = errors.New("agent authentication unavailable")
	ErrCancelled         = errors.New("agent authentication cancelled")
)

type Principal struct {
	AgentID      string
	CredentialID string
	ExpiresAt    time.Time
}

type Service struct {
	databaseURL   string
	now           func() time.Time
	sourceSecrets *sourcesecret.Adapter
}

func New(databaseURL string, now func() time.Time, adapters ...*sourcesecret.Adapter) *Service {
	service := &Service{databaseURL: databaseURL, now: now}
	if len(adapters) == 1 {
		service.sourceSecrets = adapters[0]
	}
	return service
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, errors.Join(ErrCancelled, err)
	}
	if s == nil || s.now == nil {
		return Principal{}, ErrUnavailable
	}
	credentialID, verifier, err := identity.AgentTokenVerifier(token)
	if err != nil {
		return Principal{}, ErrInvalidCredential
	}
	result, err := storage.AuthenticateAgentCredential(ctx, s.databaseURL, credentialID, verifier, s.now)
	if errors.Is(err, storage.ErrInvalidAgentCredential) {
		return Principal{}, ErrInvalidCredential
	}
	if errors.Is(err, storage.ErrCancelled) {
		return Principal{}, errors.Join(ErrCancelled, err)
	}
	if err != nil {
		return Principal{}, errors.Join(ErrUnavailable, err)
	}
	return Principal{AgentID: result.AgentID, CredentialID: result.CredentialID, ExpiresAt: result.ExpiresAt}, nil
}

func (s *Service) Configuration(ctx context.Context, token string) (storage.AgentConfigurationSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return storage.AgentConfigurationSnapshot{}, errors.Join(ErrCancelled, err)
	}
	if s == nil || s.now == nil {
		return storage.AgentConfigurationSnapshot{}, ErrUnavailable
	}
	credentialID, verifier, err := identity.AgentTokenVerifier(token)
	if err != nil {
		return storage.AgentConfigurationSnapshot{}, ErrInvalidCredential
	}
	result, err := storage.ReadCurrentAgentConfiguration(ctx, s.databaseURL, credentialID, verifier, s.now, s.sourceSecrets)
	if errors.Is(err, storage.ErrInvalidAgentCredential) {
		return storage.AgentConfigurationSnapshot{}, ErrInvalidCredential
	}
	if errors.Is(err, storage.ErrAgentOwnershipDenied) {
		return storage.AgentConfigurationSnapshot{}, ErrInvalidCredential
	}
	if errors.Is(err, storage.ErrCancelled) {
		return storage.AgentConfigurationSnapshot{}, errors.Join(ErrCancelled, err)
	}
	if err != nil {
		return storage.AgentConfigurationSnapshot{}, errors.Join(ErrUnavailable, err)
	}
	return result, nil
}

func (s *Service) CompleteDiagnostic(ctx context.Context, token string, generation int64, result agentprotocol.DiagnosticResult) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCancelled, err)
	}
	if s == nil || s.now == nil || generation <= 0 {
		return storage.ErrInvalidAgentInput
	}
	credentialID, verifier, err := identity.AgentTokenVerifier(token)
	if err != nil {
		return ErrInvalidCredential
	}
	err = storage.CompleteDatasourceDiagnostic(ctx, s.databaseURL, credentialID, verifier, generation, result, s.now())
	switch {
	case errors.Is(err, storage.ErrInvalidAgentCredential):
		return ErrInvalidCredential
	case errors.Is(err, storage.ErrCancelled):
		return errors.Join(ErrCancelled, err)
	case errors.Is(err, storage.ErrInvalidAgentInput), errors.Is(err, storage.ErrDiagnosticNotFound), errors.Is(err, storage.ErrDiagnosticConflict):
		return err
	case err != nil:
		return errors.Join(ErrUnavailable, err)
	default:
		return nil
	}
}

func (s *Service) IngestObservation(ctx context.Context, token string, item sli.ObservationItem) (storage.SliIngestResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.SliIngestResult{}, errors.Join(ErrCancelled, err)
	}
	if s == nil || s.now == nil {
		return storage.SliIngestResult{}, ErrUnavailable
	}
	credentialID, verifier, err := identity.AgentTokenVerifier(token)
	if err != nil {
		return storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "rejected", ErrorCode: "invalid_credential"}, ErrInvalidCredential
	}
	result, err := storage.IngestAgentObservation(ctx, s.databaseURL, credentialID, verifier, item, s.now)
	switch {
	case errors.Is(err, storage.ErrInvalidAgentCredential):
		return storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "rejected", ErrorCode: "invalid_credential"}, ErrInvalidCredential
	case errors.Is(err, storage.ErrObservationDenied):
		return storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "rejected", ErrorCode: "execution_not_authorized"}, nil
	case errors.Is(err, storage.ErrObservationInvalid):
		return storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "rejected", ErrorCode: "invalid_observation"}, nil
	case errors.Is(err, storage.ErrObservationConflict):
		return storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "conflicting", ErrorCode: "canonical_interval_conflict"}, nil
	case errors.Is(err, storage.ErrCancelled):
		return storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "retryable", ErrorCode: "temporarily_unavailable"}, errors.Join(ErrCancelled, err)
	case err != nil:
		return storage.SliIngestResult{DeliveryID: item.DeliveryID, Status: "retryable", ErrorCode: "temporarily_unavailable"}, errors.Join(ErrUnavailable, err)
	default:
		return result, nil
	}
}
