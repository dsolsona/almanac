// Package authentication composes local identity persistence and password proof.
package authentication

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/storage"
)

var (
	ErrUnauthenticated    = errors.New("authentication: unauthenticated")
	ErrInvalidInput       = errors.New("authentication: invalid input")
	ErrPasswordUnchanged  = errors.New("authentication: password unchanged")
	ErrInitialCredentials = errors.New("authentication: initial credentials unavailable")
	ErrAttemptExpired     = errors.New("authentication: attempt expired")
)

const operationTimeout = 30 * time.Second

type passwordWork interface {
	Hash(context.Context, string) (string, error)
	Verify(context.Context, string, string) (bool, error)
}

type Service struct {
	databaseURL string
	now         func() time.Time
	clockMu     sync.Mutex
	hasher      passwordWork
	dummy       string
}

type LoginInput struct {
	Login     string
	Password  string `json:"-"`
	Source    netip.Addr
	RequestID string
}

type ChangePasswordInput struct {
	SessionSecret   string `json:"-"`
	CurrentPassword string `json:"-"`
	NewPassword     string `json:"-"`
	Source          netip.Addr
	RequestID       string
}

type SessionResult struct {
	Secret string `json:"-"`
	Access storage.SessionAccess
}

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "authentication: rate limit exceeded" }

func New(ctx context.Context, databaseURL string, now func() time.Time) (*Service, error) {
	if now == nil {
		return nil, ErrInvalidInput
	}
	if _, err := storage.ParseDatabaseURL(databaseURL); err != nil {
		return nil, ErrInvalidInput
	}
	hasher := identity.NewPasswordHasher()
	randomInput, _, err := identity.NewSessionSecret()
	if err != nil {
		return nil, errors.New("authentication: initialization failed")
	}
	bounded, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	dummy, err := hasher.Hash(bounded, randomInput)
	if err != nil {
		if cause := bounded.Err(); cause != nil {
			return nil, cause
		}
		return nil, errors.New("authentication: initialization failed")
	}
	return &Service{databaseURL: databaseURL, now: now, hasher: hasher, dummy: dummy}, nil
}

func (s *Service) EnsureInitialOwner(ctx context.Context, login, passwordFile, requestID string) (storage.InitialOwnerResult, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	state, err := storage.ReadInitialOwnerState(ctx, s.databaseURL)
	if err != nil {
		return storage.InitialOwnerResult{}, err
	}
	if state.Provisioned {
		return storage.InitialOwnerResult{State: state}, nil
	}
	if !validLogin(login) || !validRequestID(requestID) || passwordFile == "" {
		return storage.InitialOwnerResult{}, ErrInitialCredentials
	}
	data, err := secretfile.Read(passwordFile, 512)
	if err != nil || !utf8.Valid(data) {
		return storage.InitialOwnerResult{}, ErrInitialCredentials
	}
	password := string(data)
	if err := identity.ValidateNewPassword(password); err != nil {
		return storage.InitialOwnerResult{}, ErrInitialCredentials
	}
	verifier, err := s.hasher.Hash(ctx, password)
	if err != nil {
		if ctx.Err() != nil {
			return storage.InitialOwnerResult{}, ctx.Err()
		}
		return storage.InitialOwnerResult{}, ErrInitialCredentials
	}
	return storage.ProvisionInitialOwner(ctx, s.databaseURL, storage.InitialOwnerInput{
		Login: login, PasswordVerifier: verifier, Now: s.currentTime(), RequestID: requestID,
	})
}

func (s *Service) Login(ctx context.Context, input LoginInput) (SessionResult, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if !validLogin(input.Login) || !validRequestID(input.RequestID) || !validCandidate(input.Password) || !validSource(input.Source) {
		return SessionResult{}, ErrInvalidInput
	}
	admission, err := storage.BeginLoginAttempt(ctx, s.databaseURL, storage.BeginLoginAttemptInput{Login: input.Login, Source: input.Source.Unmap(), Now: s.currentTime()})
	if err != nil {
		return SessionResult{}, err
	}
	if !admission.Allowed {
		return SessionResult{}, &RateLimitError{RetryAfter: admission.RetryAfter}
	}
	snapshot, err := storage.ReadLocalIdentity(ctx, s.databaseURL, input.Login)
	if err != nil {
		return SessionResult{}, err
	}
	valid, eligible, err := s.verify(ctx, input.Password, snapshot)
	if err != nil {
		return SessionResult{}, err
	}
	proof := valid && eligible
	finished, err := storage.FinishLoginAttempt(ctx, s.databaseURL, admission.AttemptID, proof, s.currentTime())
	if err != nil {
		return SessionResult{}, err
	}
	if !finished {
		if proof {
			return SessionResult{}, ErrAttemptExpired
		}
		return SessionResult{}, ErrUnauthenticated
	}
	if !proof {
		return SessionResult{}, ErrUnauthenticated
	}
	secret, digest, err := identity.NewSessionSecret()
	if err != nil {
		return SessionResult{}, errors.New("authentication: session creation failed")
	}
	access, err := storage.IssueSession(ctx, s.databaseURL, storage.IssueSessionInput{
		UserID: snapshot.UserID, ExpectedCredentialEpoch: snapshot.CredentialEpoch,
		ExpectedCredentialRevision: snapshot.CredentialRevision, Verifier: digest,
		Now: s.currentTime(), RequestID: input.RequestID,
	})
	if errors.Is(err, storage.ErrInvalidSession) {
		return SessionResult{}, ErrUnauthenticated
	}
	if err != nil {
		return SessionResult{}, err
	}
	return SessionResult{Secret: secret, Access: access}, nil
}

func (s *Service) ChangePassword(ctx context.Context, input ChangePasswordInput) (SessionResult, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if !validRequestID(input.RequestID) || !validCandidate(input.CurrentPassword) || !validSource(input.Source) {
		return SessionResult{}, ErrInvalidInput
	}
	sessionVerifier, err := identity.SessionSecretVerifier(input.SessionSecret)
	if err != nil {
		return SessionResult{}, ErrUnauthenticated
	}
	access, err := storage.AuthenticateSession(ctx, s.databaseURL, sessionVerifier, s.currentTime())
	if errors.Is(err, storage.ErrInvalidSession) {
		return SessionResult{}, ErrUnauthenticated
	}
	if err != nil {
		return SessionResult{}, err
	}
	snapshot, err := storage.ReadLocalIdentity(ctx, s.databaseURL, access.Login)
	if err != nil {
		return SessionResult{}, err
	}
	if !snapshot.Found || snapshot.UserID != access.UserID {
		return SessionResult{}, ErrUnauthenticated
	}
	admission, err := storage.BeginLoginAttempt(ctx, s.databaseURL, storage.BeginLoginAttemptInput{Login: access.Login, Source: input.Source.Unmap(), Now: s.currentTime()})
	if err != nil {
		return SessionResult{}, err
	}
	if !admission.Allowed {
		return SessionResult{}, &RateLimitError{RetryAfter: admission.RetryAfter}
	}
	valid, eligible, err := s.verify(ctx, input.CurrentPassword, snapshot)
	if err != nil {
		return SessionResult{}, err
	}
	proof := valid && eligible
	finished, err := storage.FinishLoginAttempt(ctx, s.databaseURL, admission.AttemptID, proof, s.currentTime())
	if err != nil {
		return SessionResult{}, err
	}
	if !finished {
		if proof {
			return SessionResult{}, ErrAttemptExpired
		}
		return SessionResult{}, ErrUnauthenticated
	}
	if !proof {
		return SessionResult{}, ErrUnauthenticated
	}
	if input.CurrentPassword == input.NewPassword {
		return SessionResult{}, ErrPasswordUnchanged
	}
	if err := identity.ValidateNewPassword(input.NewPassword); err != nil {
		return SessionResult{}, err
	}
	newVerifier, err := s.hasher.Hash(ctx, input.NewPassword)
	if err != nil {
		return SessionResult{}, err
	}
	secret, digest, err := identity.NewSessionSecret()
	if err != nil {
		return SessionResult{}, errors.New("authentication: session creation failed")
	}
	rotated, err := storage.RotateLocalPassword(ctx, s.databaseURL, storage.RotatePasswordInput{
		CurrentSessionVerifier: sessionVerifier, ExpectedCredentialRevision: snapshot.CredentialRevision,
		ExpectedCredentialEpoch: snapshot.CredentialEpoch, NewPasswordVerifier: newVerifier,
		NewSessionVerifier: digest, Now: s.currentTime(), RequestID: input.RequestID,
	})
	if errors.Is(err, storage.ErrInvalidSession) {
		return SessionResult{}, ErrUnauthenticated
	}
	if err != nil {
		return SessionResult{}, err
	}
	return SessionResult{Secret: secret, Access: rotated}, nil
}

func (s *Service) Authenticate(ctx context.Context, secret string) (storage.SessionAccess, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	digest, err := identity.SessionSecretVerifier(secret)
	if err != nil {
		return storage.SessionAccess{}, ErrUnauthenticated
	}
	access, err := storage.AuthenticateSession(ctx, s.databaseURL, digest, s.currentTime())
	if errors.Is(err, storage.ErrInvalidSession) {
		return storage.SessionAccess{}, ErrUnauthenticated
	}
	return access, err
}

func (s *Service) Logout(ctx context.Context, secret, requestID string) (bool, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if !validRequestID(requestID) {
		return false, ErrInvalidInput
	}
	digest, err := identity.SessionSecretVerifier(secret)
	if err != nil {
		return false, nil
	}
	return storage.RevokeSession(ctx, s.databaseURL, digest, s.currentTime(), requestID)
}

func (s *Service) verifierFor(snapshot storage.LocalIdentitySnapshot) (string, bool) {
	if snapshot.Found && snapshot.Enabled && snapshot.PasswordVerifier != "" && identity.ValidatePasswordVerifier(snapshot.PasswordVerifier) == nil {
		return snapshot.PasswordVerifier, true
	}
	return s.dummy, false
}

func (s *Service) verify(ctx context.Context, password string, snapshot storage.LocalIdentitySnapshot) (bool, bool, error) {
	verifier, eligible := s.verifierFor(snapshot)
	valid, err := s.hasher.Verify(ctx, password, verifier)
	return valid, eligible, err
}

func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, operationTimeout)
}

func (s *Service) currentTime() time.Time {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	return s.now()
}
func validRequestID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value)
}
func validLogin(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value)
}
func validCandidate(value string) bool {
	return len(value) <= 512 && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 128
}
func validSource(source netip.Addr) bool {
	return source.IsValid() && source.Zone() == "" && !source.Unmap().IsUnspecified() && !source.Unmap().IsMulticast()
}
