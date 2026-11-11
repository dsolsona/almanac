package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type SessionPolicy struct {
	IdleMinutes int32
	Version     int32
}

func GetSessionPolicy(ctx context.Context, databaseURL string, verifier [32]byte, clock func() time.Time) (SessionPolicy, error) {
	conn, q, tx, user, installation, session, started, err := beginAgentRead(ctx, databaseURL, verifier, clock)
	if err != nil {
		return SessionPolicy{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)

	row, err := q.ReadSessionPolicy(ctx)
	if err != nil {
		return SessionPolicy{}, operationError(ctx, "storage: session policy read failed", err)
	}
	if err := finishAgentRead(ctx, tx, clock, started, user, installation, session); err != nil {
		return SessionPolicy{}, err
	}
	return SessionPolicy(row), nil
}

func UpdateSessionPolicy(ctx context.Context, databaseURL string, verifier [32]byte, clock func() time.Time, idleMinutes, version int32, requestID string) (SessionPolicy, error) {
	if idleMinutes < 1 || idleMinutes > 720 || version < 1 || !validRequestID(requestID) {
		return SessionPolicy{}, ErrUserInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentRead(ctx, databaseURL, verifier, clock)
	if err != nil {
		return SessionPolicy{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)

	row, err := q.UpdateSessionPolicy(ctx, dbgen.UpdateSessionPolicyParams{IdleMinutes: idleMinutes, ExpectedVersion: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionPolicy{}, ErrUserConflict
	}
	if err != nil {
		return SessionPolicy{}, operationError(ctx, "storage: session policy update failed", err)
	}
	if err := q.InsertSessionAudit(ctx, dbgen.InsertSessionAuditParams{
		Actor: user.ID.String(), SubjectID: user.ID, Action: "identity.session_policy.updated",
		OccurredAt: timestamp(started), RequestID: requestID,
	}); err != nil {
		return SessionPolicy{}, operationError(ctx, "storage: session policy audit failed", err)
	}
	if err := finishAgentRead(ctx, tx, clock, started, user, installation, session); err != nil {
		return SessionPolicy{}, err
	}
	return SessionPolicy(row), nil
}
