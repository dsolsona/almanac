package storage

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/netip"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

const (
	loginAccountLockNamespace int32 = 0x414c0001
	loginSourceLockNamespace  int32 = 0x414c0002
	loginAccountLimit               = 5
	loginSourceLimit                = 20
	loginAttemptLease               = time.Minute
	loginFailureWindow              = 15 * time.Minute
)

var ErrInvalidLoginAttemptInput = errors.New("storage: invalid login attempt input")

type BeginLoginAttemptInput struct {
	Login  string
	Source netip.Addr
	Now    time.Time
}

type LoginAdmission struct {
	Allowed    bool
	AttemptID  string
	ExpiresAt  time.Time
	RetryAfter time.Duration
}

func BeginLoginAttempt(ctx context.Context, databaseURL string, input BeginLoginAttemptInput) (LoginAdmission, error) {
	if input.Login == "" || len(input.Login) > 256 || !utf8.ValidString(input.Login) || input.Now.IsZero() ||
		!input.Source.IsValid() || input.Source.Zone() != "" {
		return LoginAdmission{}, ErrInvalidLoginAttemptInput
	}
	source := input.Source.Unmap()
	if source.IsUnspecified() || source.IsMulticast() {
		return LoginAdmission{}, ErrInvalidLoginAttemptInput
	}
	now := timestamp(input.Now)
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return LoginAdmission{}, err
	}
	defer closeInitialOwnerConnection(conn)
	queries := dbgen.New(conn)
	keys, err := queries.LoginAttemptKeys(ctx, dbgen.LoginAttemptKeysParams{Login: input.Login, Source: source.String()})
	if err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login admission key calculation failed", err)
	}
	if len(keys.AccountKey) != sha256.Size || len(keys.SourceKey) != sha256.Size {
		return LoginAdmission{}, errors.New("storage: login admission key calculation failed")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login admission transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login admission transaction setup failed", err)
	}
	if err := lockLoginKey(ctx, tx, loginAccountLockNamespace, keys.AccountKey); err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login admission account lock failed", err)
	}
	if err := lockLoginKey(ctx, tx, loginSourceLockNamespace, keys.SourceKey); err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login admission source lock failed", err)
	}
	queries = dbgen.New(tx)
	if err := queries.PruneExpiredLoginAttempts(ctx, now); err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login attempt cleanup failed", err)
	}
	account, err := queries.CountAccountLoginAttempts(ctx, dbgen.CountAccountLoginAttemptsParams{
		AccountKey: keys.AccountKey, NowAt: now, LimitCount: loginAccountLimit,
	})
	if err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login account attempt count failed", err)
	}
	sourceCount, err := queries.CountSourceLoginAttempts(ctx, dbgen.CountSourceLoginAttemptsParams{
		SourceKey: keys.SourceKey, NowAt: now, LimitCount: loginSourceLimit,
	})
	if err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login source attempt count failed", err)
	}
	if account.AttemptCount >= loginAccountLimit || sourceCount.AttemptCount >= loginSourceLimit {
		var retryAfter time.Duration
		if account.AttemptCount >= loginAccountLimit {
			retryAfter = max(retryAfter, account.RetryAt.Time.Sub(now.Time))
		}
		if sourceCount.AttemptCount >= loginSourceLimit {
			retryAfter = max(retryAfter, sourceCount.RetryAt.Time.Sub(now.Time))
		}
		if retryAfter <= 0 {
			return LoginAdmission{}, errors.New("storage: login admission retry window unavailable")
		}
		if err := tx.Commit(ctx); err != nil {
			return LoginAdmission{}, operationError(ctx, "storage: login admission transaction commit failed", err)
		}
		return LoginAdmission{RetryAfter: retryAfter}, nil
	}
	expiresAt := timestamp(now.Time.Add(loginAttemptLease))
	id, err := queries.InsertLoginAttempt(ctx, dbgen.InsertLoginAttemptParams{
		AccountKey: keys.AccountKey, SourceKey: keys.SourceKey, StartedAt: now, ExpiresAt: expiresAt,
	})
	if err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login attempt reservation failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return LoginAdmission{}, operationError(ctx, "storage: login admission transaction commit failed", err)
	}
	return LoginAdmission{Allowed: true, AttemptID: id.String(), ExpiresAt: expiresAt.Time, RetryAfter: 0}, nil
}

func FinishLoginAttempt(ctx context.Context, databaseURL, attemptID string, succeeded bool, now time.Time) (bool, error) {
	id, valid := canonicalUUID(attemptID)
	if !valid || now.IsZero() {
		return false, ErrInvalidLoginAttemptInput
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return false, err
	}
	defer closeInitialOwnerConnection(conn)
	keys, err := dbgen.New(conn).LoginAttemptKeysByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, operationError(ctx, "storage: login attempt lookup failed", err)
	}
	if len(keys.AccountKey) != sha256.Size || len(keys.SourceKey) != sha256.Size {
		return false, errors.New("storage: login attempt keys unavailable")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, operationError(ctx, "storage: login completion transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return false, operationError(ctx, "storage: login completion transaction setup failed", err)
	}
	if err := lockLoginKey(ctx, tx, loginAccountLockNamespace, keys.AccountKey); err != nil {
		return false, operationError(ctx, "storage: login completion account lock failed", err)
	}
	if err := lockLoginKey(ctx, tx, loginSourceLockNamespace, keys.SourceKey); err != nil {
		return false, operationError(ctx, "storage: login completion source lock failed", err)
	}
	queries := dbgen.New(tx)
	attempt, err := queries.LockLoginAttemptForFinish(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, operationError(ctx, "storage: login completion attempt lock failed", err)
	}
	nowAt := timestamp(now)
	if string(attempt.AccountKey) != string(keys.AccountKey) || string(attempt.SourceKey) != string(keys.SourceKey) ||
		attempt.State != "pending" || nowAt.Time.Before(attempt.StartedAt.Time) || !nowAt.Time.Before(attempt.ExpiresAt.Time) {
		return false, nil
	}
	if succeeded {
		err = queries.DeleteLoginAttempt(ctx, id)
	} else {
		err = queries.FailLoginAttempt(ctx, dbgen.FailLoginAttemptParams{
			ID: id, ExpiresAt: timestamp(nowAt.Time.Add(loginFailureWindow)),
		})
	}
	if err != nil {
		return false, operationError(ctx, "storage: login attempt completion failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, operationError(ctx, "storage: login completion transaction commit failed", err)
	}
	return true, nil
}

func lockLoginKey(ctx context.Context, tx pgx.Tx, namespace int32, digest []byte) error {
	key := int32(binary.BigEndian.Uint32(digest[:4]))
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", namespace, key)
	return err
}
