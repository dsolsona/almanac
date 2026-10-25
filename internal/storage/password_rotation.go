package storage

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	passwordChangedAction     = "identity.password.changed"
	onboardingCompletedAction = "identity.onboarding.completed"
)

type RotatePasswordInput struct {
	CurrentSessionVerifier     [32]byte
	ExpectedCredentialRevision int64
	ExpectedCredentialEpoch    string
	NewPasswordVerifier        string
	NewSessionVerifier         [32]byte
	Now                        time.Time
	RequestID                  string
}

func RotateLocalPassword(ctx context.Context, databaseURL string, input RotatePasswordInput) (SessionAccess, error) {
	if err := validateRotatePasswordInput(input); err != nil {
		return SessionAccess{}, err
	}
	epoch, _ := canonicalUUID(input.ExpectedCredentialEpoch)
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return SessionAccess{}, err
	}
	defer closeInitialOwnerConnection(conn)

	queries := dbgen.New(conn)
	userID, err := queries.FindSessionUserID(ctx, input.CurrentSessionVerifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation session lookup failed", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation setup failed", err)
	}
	queries = dbgen.New(tx)
	lockedInstallation, err := queries.LockPasswordRotationInstallation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation installation lock failed", err)
	}
	user, err := queries.LockSessionUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation user lock failed", err)
	}
	session, err := queries.LockSession(ctx, input.CurrentSessionVerifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation session lock failed", err)
	}
	installation := dbgen.LockSessionInstallationRow{
		CredentialEpoch:       lockedInstallation.CredentialEpoch,
		BootstrapAdminID:      lockedInstallation.BootstrapAdminID,
		OnboardingCompletedAt: lockedInstallation.OnboardingCompletedAt,
	}
	now := timestamp(input.Now)
	nowTime := now.Time
	if invalidSessionState(installation, user, session, nowTime) ||
		lockedInstallation.CredentialEpoch.String() != epoch.String() ||
		user.CredentialRevision != input.ExpectedCredentialRevision || !user.PasswordVerifier.Valid ||
		identity.ValidatePasswordVerifier(user.PasswordVerifier.String) != nil {
		return SessionAccess{}, ErrInvalidSession
	}
	if user.PasswordVerifier.String == input.NewPasswordVerifier {
		return SessionAccess{}, ErrInvalidSessionInput
	}
	completingOnboarding := !lockedInstallation.OnboardingCompletedAt.Valid
	if completingOnboarding && (user.ID != lockedInstallation.BootstrapAdminID || user.Role != "administrator") {
		return SessionAccess{}, ErrInvalidSession
	}
	if user.CredentialRevision == math.MaxInt64 {
		return SessionAccess{}, ErrInvalidSession
	}
	collision, err := queries.SessionVerifierExists(ctx, input.NewSessionVerifier[:])
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation collision check failed", err)
	}
	if collision {
		return SessionAccess{}, ErrInvalidSessionInput
	}
	updated, err := queries.RotateLocalPassword(ctx, dbgen.RotateLocalPasswordParams{
		PasswordVerifier: pgtype.Text{String: input.NewPasswordVerifier, Valid: true},
		UpdatedAt:        now, UserID: userID,
	})
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation update failed", err)
	}
	if updated != 1 {
		return SessionAccess{}, ErrInvalidSession
	}
	user.CredentialRevision++
	user.PasswordVerifier = pgtype.Text{String: input.NewPasswordVerifier, Valid: true}
	user.MustChangePassword = false
	if completingOnboarding {
		rows, err := queries.CompletePasswordRotationOnboarding(ctx, now)
		if err != nil {
			return SessionAccess{}, operationError(ctx, "storage: password rotation onboarding update failed", err)
		}
		if rows != 1 {
			return SessionAccess{}, ErrInvalidSession
		}
		lockedInstallation.OnboardingCompletedAt = now
		installation.OnboardingCompletedAt = now
	}
	if err := queries.DeleteUserSessions(ctx, userID); err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation session invalidation failed", err)
	}
	newSessionID, err := queries.InsertSession(ctx, dbgen.InsertSessionParams{
		UserID: userID, Verifier: input.NewSessionVerifier[:], CredentialRevision: user.CredentialRevision,
		CredentialEpoch: epoch, Restricted: false, CreatedAt: now,
	})
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation session insertion failed", err)
	}
	for _, action := range []string{passwordChangedAction, sessionIssuedAction} {
		if err := queries.InsertPasswordRotationAudit(ctx, dbgen.InsertPasswordRotationAuditParams{
			Actor: userID.String(), SubjectID: userID, Action: action,
			OccurredAt: now, RequestID: input.RequestID,
		}); err != nil {
			return SessionAccess{}, operationError(ctx, "storage: password rotation audit failed", err)
		}
	}
	if completingOnboarding {
		if err := queries.InsertPasswordRotationAudit(ctx, dbgen.InsertPasswordRotationAuditParams{
			Actor: userID.String(), SubjectID: userID, Action: onboardingCompletedAction,
			OccurredAt: now, RequestID: input.RequestID,
		}); err != nil {
			return SessionAccess{}, operationError(ctx, "storage: password rotation onboarding audit failed", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return SessionAccess{}, operationError(ctx, "storage: password rotation commit failed", err)
	}
	createdAt := now.Time.UTC()
	return SessionAccess{
		SessionID: newSessionID.String(), UserID: userID.String(), Login: user.Login, Role: user.Role,
		Restricted: false, CreatedAt: createdAt, LastSeenAt: createdAt,
		AbsoluteExpiresAt: createdAt.Add(sessionAbsoluteLifetime), CredentialRevision: user.CredentialRevision,
	}, nil
}

func validateRotatePasswordInput(input RotatePasswordInput) error {
	if input.ExpectedCredentialRevision <= 0 || input.Now.IsZero() || !validRequestID(input.RequestID) ||
		identity.ValidatePasswordVerifier(input.NewPasswordVerifier) != nil {
		return ErrInvalidSessionInput
	}
	if _, ok := canonicalUUID(input.ExpectedCredentialEpoch); !ok ||
		input.CurrentSessionVerifier == input.NewSessionVerifier {
		return ErrInvalidSessionInput
	}
	return nil
}
