package storage

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrInvalidSession      = errors.New("storage: invalid session")
	ErrInvalidSessionInput = errors.New("storage: invalid session input")
)

const (
	sessionIdleLifetime     = 30 * time.Minute
	sessionAbsoluteLifetime = 12 * time.Hour
	sessionIssuedAction     = "identity.session.issued"
	sessionRevokedAction    = "identity.session.revoked"
)

type LocalIdentitySnapshot struct {
	Found              bool
	UserID             string
	Login              string
	Role               string
	CredentialEpoch    string
	PasswordVerifier   string `json:"-"`
	Enabled            bool
	MustChangePassword bool
	CredentialRevision int64
}

type IssueSessionInput struct {
	UserID                     string
	ExpectedCredentialEpoch    string
	ExpectedCredentialRevision int64
	Verifier                   [32]byte
	Now                        time.Time
	RequestID                  string
}

type SessionAccess struct {
	SessionID          string
	UserID             string
	Login              string
	Role               string
	Restricted         bool
	CreatedAt          time.Time
	LastSeenAt         time.Time
	AbsoluteExpiresAt  time.Time
	CredentialRevision int64
}

func ReadLocalIdentity(ctx context.Context, databaseURL, login string) (LocalIdentitySnapshot, error) {
	if login == "" || len(login) > 256 || !utf8.ValidString(login) {
		return LocalIdentitySnapshot{}, ErrInvalidSessionInput
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return LocalIdentitySnapshot{}, err
	}
	defer closeInitialOwnerConnection(conn)
	row, err := dbgen.New(conn).ReadLocalIdentity(ctx, login)
	if errors.Is(err, pgx.ErrNoRows) {
		return LocalIdentitySnapshot{}, nil
	}
	if err != nil {
		return LocalIdentitySnapshot{}, operationError(ctx, "storage: local identity read failed", err)
	}
	if !row.CredentialEpoch.Valid {
		return LocalIdentitySnapshot{}, errors.New("storage: local identity installation state unavailable")
	}
	snapshot := LocalIdentitySnapshot{
		Found: true, UserID: row.ID.String(), Login: row.Login, Role: row.Role,
		CredentialEpoch: row.CredentialEpoch.String(), Enabled: row.Enabled,
		MustChangePassword: row.MustChangePassword, CredentialRevision: row.CredentialRevision,
	}
	if row.PasswordVerifier.Valid {
		snapshot.PasswordVerifier = row.PasswordVerifier.String
	}
	return snapshot, nil
}

func IssueSession(ctx context.Context, databaseURL string, input IssueSessionInput) (SessionAccess, error) {
	userID, ok := canonicalUUID(input.UserID)
	if !ok || input.ExpectedCredentialRevision <= 0 || input.Now.IsZero() || !validRequestID(input.RequestID) {
		return SessionAccess{}, ErrInvalidSessionInput
	}
	epoch, ok := canonicalUUID(input.ExpectedCredentialEpoch)
	if !ok {
		return SessionAccess{}, ErrInvalidSessionInput
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return SessionAccess{}, err
	}
	defer closeInitialOwnerConnection(conn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session issuance transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session issuance setup failed", err)
	}
	queries := dbgen.New(tx)
	installation, err := queries.LockSessionInstallation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session installation lock failed", err)
	}
	user, err := queries.LockSessionUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session user lock failed", err)
	}
	if !installation.BootstrapAdminID.Valid || installation.CredentialEpoch.String() != epoch.String() ||
		user.CredentialRevision != input.ExpectedCredentialRevision || !user.Enabled || !user.PasswordVerifier.Valid ||
		identity.ValidatePasswordVerifier(user.PasswordVerifier.String) != nil {
		return SessionAccess{}, ErrInvalidSession
	}
	now := timestamp(input.Now)
	restricted := user.MustChangePassword || !installation.OnboardingCompletedAt.Valid
	sessionID, err := queries.InsertSession(ctx, dbgen.InsertSessionParams{
		UserID: userID, Verifier: input.Verifier[:], CredentialRevision: user.CredentialRevision,
		CredentialEpoch: epoch, Restricted: restricted, CreatedAt: now,
	})
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session issuance failed", err)
	}
	if err := queries.InsertSessionAudit(ctx, dbgen.InsertSessionAuditParams{
		Actor: input.UserID, SubjectID: userID, Action: sessionIssuedAction,
		OccurredAt: now, RequestID: input.RequestID,
	}); err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session issuance audit failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session issuance commit failed", err)
	}
	createdAt := now.Time
	return SessionAccess{
		SessionID: sessionID.String(), UserID: input.UserID, Login: user.Login, Role: user.Role,
		Restricted: restricted, CreatedAt: createdAt, LastSeenAt: createdAt,
		AbsoluteExpiresAt: createdAt.Add(sessionAbsoluteLifetime), CredentialRevision: user.CredentialRevision,
	}, nil
}

func AuthenticateSession(ctx context.Context, databaseURL string, verifier [32]byte, now time.Time) (SessionAccess, error) {
	return inspectSession(ctx, databaseURL, verifier, now, true)
}

// InspectSession validates current session authority without extending its idle lifetime.
func InspectSession(ctx context.Context, databaseURL string, verifier [32]byte, now time.Time) (SessionAccess, error) {
	return inspectSession(ctx, databaseURL, verifier, now, false)
}

func inspectSession(ctx context.Context, databaseURL string, verifier [32]byte, now time.Time, touch bool) (SessionAccess, error) {
	if now.IsZero() {
		return SessionAccess{}, ErrInvalidSessionInput
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return SessionAccess{}, err
	}
	defer closeInitialOwnerConnection(conn)
	queries := dbgen.New(conn)
	userID, err := queries.FindSessionUserID(ctx, verifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session lookup failed", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session authentication transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session authentication setup failed", err)
	}
	queries = dbgen.New(tx)
	installation, err := queries.LockSessionInstallation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session installation lock failed", err)
	}
	user, err := queries.LockSessionUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session user lock failed", err)
	}
	session, err := queries.LockSession(ctx, verifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionAccess{}, ErrInvalidSession
	}
	if err != nil {
		return SessionAccess{}, operationError(ctx, "storage: session lock failed", err)
	}
	nowTime := now.UTC().Truncate(time.Microsecond)
	if invalidSessionState(installation, user, session, nowTime) {
		return SessionAccess{}, ErrInvalidSession
	}
	if touch {
		lastSeenAt := session.LastSeenAt.Time.UTC()
		if nowTime.After(lastSeenAt) {
			lastSeenAt = nowTime
		}
		rows, err := queries.TouchSession(ctx, dbgen.TouchSessionParams{LastSeenAt: timestamp(lastSeenAt), ID: session.ID})
		if err != nil {
			return SessionAccess{}, operationError(ctx, "storage: session touch failed", err)
		}
		if rows != 1 {
			return SessionAccess{}, ErrInvalidSession
		}
		session.LastSeenAt = timestamp(lastSeenAt)
	}
	if err := tx.Commit(ctx); err != nil {
		if touch {
			return SessionAccess{}, operationError(ctx, "storage: session authentication commit failed", err)
		}
		return SessionAccess{}, operationError(ctx, "storage: session inspection commit failed", err)
	}
	return sessionAccess(session, user, installation), nil
}

func RevokeSession(ctx context.Context, databaseURL string, verifier [32]byte, now time.Time, requestID string) (bool, error) {
	if now.IsZero() || !validRequestID(requestID) {
		return false, ErrInvalidSessionInput
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return false, err
	}
	defer closeInitialOwnerConnection(conn)
	queries := dbgen.New(conn)
	userID, err := queries.FindSessionUserID(ctx, verifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, operationError(ctx, "storage: session lookup failed", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, operationError(ctx, "storage: session revocation transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return false, operationError(ctx, "storage: session revocation setup failed", err)
	}
	queries = dbgen.New(tx)
	installation, err := queries.LockSessionInstallation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, operationError(ctx, "storage: session installation lock failed", err)
	}
	user, err := queries.LockSessionUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, operationError(ctx, "storage: session user lock failed", err)
	}
	session, err := queries.LockSession(ctx, verifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, operationError(ctx, "storage: session lock failed", err)
	}
	if invalidSessionState(installation, user, session, now.UTC().Truncate(time.Microsecond)) {
		return false, nil
	}
	rows, err := queries.DeleteSession(ctx, session.ID)
	if err != nil {
		return false, operationError(ctx, "storage: session revocation failed", err)
	}
	if rows != 1 {
		return false, nil
	}
	if err := queries.InsertSessionAudit(ctx, dbgen.InsertSessionAuditParams{
		Actor: userID.String(), SubjectID: userID, Action: sessionRevokedAction,
		OccurredAt: timestamp(now), RequestID: requestID,
	}); err != nil {
		return false, operationError(ctx, "storage: session revocation audit failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, operationError(ctx, "storage: session revocation commit failed", err)
	}
	return true, nil
}

func invalidSessionState(installation dbgen.LockSessionInstallationRow, user dbgen.LockSessionUserRow, session dbgen.AlmanacSession, now time.Time) bool {
	if !session.CreatedAt.Valid || !session.LastSeenAt.Valid || !installation.BootstrapAdminID.Valid || !user.Enabled || session.UserID != user.ID ||
		session.CredentialRevision != user.CredentialRevision || session.CredentialEpoch != installation.CredentialEpoch ||
		now.Before(session.CreatedAt.Time) {
		return true
	}
	return !now.Before(session.LastSeenAt.Time.Add(sessionIdleLifetime)) ||
		!now.Before(session.CreatedAt.Time.Add(sessionAbsoluteLifetime))
}

func sessionAccess(session dbgen.AlmanacSession, user dbgen.LockSessionUserRow, installation dbgen.LockSessionInstallationRow) SessionAccess {
	return SessionAccess{
		SessionID: session.ID.String(), UserID: user.ID.String(), Login: user.Login, Role: user.Role,
		Restricted: session.Restricted || user.MustChangePassword || !installation.OnboardingCompletedAt.Valid,
		CreatedAt:  session.CreatedAt.Time.UTC(), LastSeenAt: session.LastSeenAt.Time.UTC(),
		AbsoluteExpiresAt:  session.CreatedAt.Time.UTC().Add(sessionAbsoluteLifetime),
		CredentialRevision: user.CredentialRevision,
	}
}

func canonicalUUID(raw string) (pgtype.UUID, bool) {
	var value pgtype.UUID
	if err := value.Scan(raw); err != nil || value.String() != raw {
		return pgtype.UUID{}, false
	}
	return value, true
}

func validRequestID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value)
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC().Truncate(time.Microsecond), Valid: true}
}

func setTransactionBounds(ctx context.Context, tx pgx.Tx) error {
	if boundRuntime(ctx) != nil {
		_, err := tx.Exec(ctx, "SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='2s'; SET LOCAL jit=off")
		return err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '25s'")
	return err
}

func rollbackTransaction(tx pgx.Tx) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanupCtx)
}
