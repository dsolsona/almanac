package authentication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

func (s *Service) RedeemUserChallenge(ctx context.Context, token, password string, source netip.Addr, requestID string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	if !validSource(source) || !validRequestID(requestID) || len(token) > 128 {
		return ErrInvalidInput
	}
	digest := sha256.Sum256([]byte(token))
	admission, err := storage.BeginLoginAttempt(ctx, s.databaseURL, storage.BeginLoginAttemptInput{Login: "challenge:" + hex.EncodeToString(digest[:]), Source: source.Unmap(), Now: s.currentTime()})
	if err != nil {
		return err
	}
	if !admission.Allowed {
		return &RateLimitError{RetryAfter: admission.RetryAfter}
	}
	if err := identity.ValidateNewPassword(password); err != nil {
		return err
	}
	verifier, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return err
	}
	err = storage.RedeemUserChallenge(ctx, s.databaseURL, token, verifier, requestID, s.currentTime)
	if _, finishErr := storage.FinishLoginAttempt(ctx, s.databaseURL, admission.AttemptID, err == nil, s.currentTime()); finishErr != nil {
		return finishErr
	}
	return err
}
