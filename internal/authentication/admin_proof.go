package authentication

import (
	"context"
	"errors"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

// AgentRegistrationSessionInput authorizes registration from the current administrator session.
type AgentRegistrationSessionInput struct {
	SessionSecret string `json:"-"`
	RequestID     string
}

// AuthorizeAgentRegistration returns a request-local session authorization
// snapshot. Storage must recheck the session and current administrator authority
// in the transaction that commits registration.
func (s *Service) AuthorizeAgentRegistration(ctx context.Context, input AgentRegistrationSessionInput) (storage.AgentAdminProof, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if !validRequestID(input.RequestID) {
		return storage.AgentAdminProof{}, ErrInvalidInput
	}
	sessionVerifier, err := identity.SessionSecretVerifier(input.SessionSecret)
	if err != nil {
		return storage.AgentAdminProof{}, ErrUnauthenticated
	}
	access, err := storage.AuthenticateSession(ctx, s.databaseURL, sessionVerifier, s.currentTime())
	if errors.Is(err, storage.ErrInvalidSession) {
		return storage.AgentAdminProof{}, ErrUnauthenticated
	}
	if err != nil {
		return storage.AgentAdminProof{}, err
	}
	if access.Restricted || access.Role != "administrator" {
		return storage.AgentAdminProof{}, ErrForbidden
	}
	snapshot, err := storage.ReadLocalIdentity(ctx, s.databaseURL, access.Login)
	if err != nil {
		return storage.AgentAdminProof{}, err
	}
	if !snapshot.Found || !snapshot.Enabled || snapshot.UserID != access.UserID || snapshot.Role != "administrator" || snapshot.MustChangePassword ||
		snapshot.CredentialRevision != access.CredentialRevision {
		return storage.AgentAdminProof{}, ErrUnauthenticated
	}
	return storage.AgentAdminProof{
		SessionVerifier: sessionVerifier, UserID: snapshot.UserID, CredentialEpoch: snapshot.CredentialEpoch,
		CredentialRevision: snapshot.CredentialRevision, VerifiedAt: s.currentTime(),
	}, nil
}
