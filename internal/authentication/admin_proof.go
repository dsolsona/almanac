package authentication

import (
	"context"
	"errors"
	"net/netip"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

// AdministratorProofInput supplies fresh password proof for a sensitive operation.
type AdministratorProofInput struct {
	SessionSecret   string `json:"-"`
	CurrentPassword string `json:"-"`
	Source          netip.Addr
	RequestID       string
}

// ProveAdministrator returns a request-local proof bound to the password-verified
// identity snapshot. Storage must recheck current authority before committing.
func (s *Service) ProveAdministrator(ctx context.Context, input AdministratorProofInput) (storage.AgentAdminProof, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if !validRequestID(input.RequestID) || !validCandidate(input.CurrentPassword) || !validSource(input.Source) {
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
	if access.Restricted {
		return storage.AgentAdminProof{}, ErrForbidden
	}
	if access.Role != "administrator" {
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
	admission, err := storage.BeginLoginAttempt(ctx, s.databaseURL, storage.BeginLoginAttemptInput{
		Login: access.Login, Source: input.Source.Unmap(), Now: s.currentTime(),
	})
	if err != nil {
		return storage.AgentAdminProof{}, err
	}
	if !admission.Allowed {
		return storage.AgentAdminProof{}, &RateLimitError{RetryAfter: admission.RetryAfter}
	}
	valid, eligible, err := s.verify(ctx, input.CurrentPassword, snapshot)
	if err != nil {
		return storage.AgentAdminProof{}, err
	}
	proof := valid && eligible
	finished, err := storage.FinishLoginAttempt(ctx, s.databaseURL, admission.AttemptID, proof, s.currentTime())
	if err != nil {
		return storage.AgentAdminProof{}, err
	}
	if !finished {
		if proof {
			return storage.AgentAdminProof{}, ErrAttemptExpired
		}
		return storage.AgentAdminProof{}, ErrUnauthenticated
	}
	if !proof {
		return storage.AgentAdminProof{}, ErrUnauthenticated
	}
	return storage.AgentAdminProof{
		SessionVerifier: sessionVerifier, UserID: snapshot.UserID, CredentialEpoch: snapshot.CredentialEpoch,
		CredentialRevision: snapshot.CredentialRevision, VerifiedAt: s.currentTime(),
	}, nil
}
