package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrInvalidInitialOwner = errors.New("storage: invalid initial owner input")

type InitialOwnerInput struct {
	Login            string
	PasswordVerifier string
	Now              time.Time
	RequestID        string
}

type InitialOwnerState struct {
	Provisioned        bool
	AdministratorID    string
	OnboardingComplete bool
}

type InitialOwnerResult struct {
	State   InitialOwnerState
	Created bool
}

func ReadInitialOwnerState(ctx context.Context, databaseURL string) (InitialOwnerState, error) {
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return InitialOwnerState{}, err
	}
	defer closeInitialOwnerConnection(conn)

	row, err := dbgen.New(conn).GetInitialOwnerState(ctx)
	if err != nil {
		return InitialOwnerState{}, operationError(ctx, "storage: initial owner state read failed", err)
	}
	return initialOwnerState(row.BootstrapAdminID, row.OnboardingCompletedAt), nil
}

func ProvisionInitialOwner(ctx context.Context, databaseURL string, input InitialOwnerInput) (result InitialOwnerResult, retErr error) {
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return InitialOwnerResult{}, err
	}
	defer closeInitialOwnerConnection(conn)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return InitialOwnerResult{}, operationError(ctx, "storage: initial owner transaction failed", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return InitialOwnerResult{}, operationError(ctx, "storage: initial owner transaction setup failed", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '25s'"); err != nil {
		return InitialOwnerResult{}, operationError(ctx, "storage: initial owner transaction setup failed", err)
	}

	queries := dbgen.New(tx)
	locked, err := queries.LockInitialOwnerState(ctx)
	if err != nil {
		return InitialOwnerResult{}, operationError(ctx, "storage: initial owner state lock failed", err)
	}
	if locked.BootstrapAdminID.Valid {
		state := initialOwnerState(locked.BootstrapAdminID, locked.OnboardingCompletedAt)
		if err := tx.Commit(ctx); err != nil {
			return InitialOwnerResult{}, operationError(ctx, "storage: initial owner transaction commit failed", err)
		}
		return InitialOwnerResult{State: state}, nil
	}
	if err := validateInitialOwnerInput(input); err != nil {
		return InitialOwnerResult{}, err
	}

	now := pgtype.Timestamptz{Time: input.Now.UTC(), Valid: true}
	verifier := pgtype.Text{String: input.PasswordVerifier, Valid: true}
	userID, err := queries.InsertInitialOwner(ctx, dbgen.InsertInitialOwnerParams{
		Login: input.Login, PasswordVerifier: verifier, CreatedAt: now,
	})
	if err != nil {
		return InitialOwnerResult{}, operationError(ctx, "storage: initial owner insert failed", err)
	}
	if err := queries.InsertInitialOwnerAudit(ctx, dbgen.InsertInitialOwnerAuditParams{
		SubjectID: userID, OccurredAt: now, RequestID: input.RequestID,
	}); err != nil {
		return InitialOwnerResult{}, operationError(ctx, "storage: initial owner audit insert failed", err)
	}
	if err := queries.MarkInitialOwnerProvisioned(ctx, dbgen.MarkInitialOwnerProvisionedParams{
		BootstrapAdminID: userID, BootstrappedAt: now,
	}); err != nil {
		return InitialOwnerResult{}, operationError(ctx, "storage: initial owner state update failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InitialOwnerResult{}, operationError(ctx, "storage: initial owner transaction commit failed", err)
	}
	return InitialOwnerResult{State: InitialOwnerState{
		Provisioned: true, AdministratorID: userID.String(),
	}, Created: true}, nil
}

func connectCurrentSchema(ctx context.Context, databaseURL string) (*pgx.Conn, error) {
	config, err := ParseDatabaseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, operationError(ctx, "storage: database connection failed", err)
	}
	if err := setSessionBounds(ctx, conn); err != nil {
		closeInitialOwnerConnection(conn)
		return nil, operationError(ctx, "storage: database setup failed", err)
	}
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		closeInitialOwnerConnection(conn)
		return nil, err
	}
	history, err := dbgen.New(conn).GetMigrationHistory(ctx)
	if err != nil {
		closeInitialOwnerConnection(conn)
		return nil, operationError(ctx, "storage: schema compatibility read failed", err)
	}
	compatibility, _ := compareHistory(history, migrations)
	if compatibility != CompatibilityCurrent {
		closeInitialOwnerConnection(conn)
		return nil, fmt.Errorf("storage: database schema is %s", compatibility)
	}
	return conn, nil
}

func closeInitialOwnerConnection(conn *pgx.Conn) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Close(cleanupCtx)
}

func initialOwnerState(id pgtype.UUID, completedAt pgtype.Timestamptz) InitialOwnerState {
	state := InitialOwnerState{OnboardingComplete: completedAt.Valid}
	if id.Valid {
		state.Provisioned = true
		state.AdministratorID = id.String()
	}
	return state
}

func validateInitialOwnerInput(input InitialOwnerInput) error {
	if input.Login == "" || len(input.Login) > 256 || !utf8.ValidString(input.Login) ||
		identity.ValidatePasswordVerifier(input.PasswordVerifier) != nil || input.Now.IsZero() ||
		input.RequestID == "" || len(input.RequestID) > 128 || !utf8.ValidString(input.RequestID) {
		return ErrInvalidInitialOwner
	}
	return nil
}
