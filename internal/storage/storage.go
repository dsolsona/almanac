package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed bootstrap.sql
var embeddedBootstrapSQL string

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

const advisoryLockKey int64 = 0x414c4d414e4143

var (
	ErrNewerSchema         = errors.New("storage: database schema is newer than this binary")
	ErrInconsistentHistory = errors.New("storage: database migration history is inconsistent")
	ErrCancelled           = errors.New("storage: operation cancelled or timed out")
)

type UpgradeResult struct {
	DatabaseSchemaVersion int `json:"database_schema_version"`
	Applied               int `json:"applied"`
}

type Compatibility int

const (
	CompatibilityMissing Compatibility = iota
	CompatibilityPending
	CompatibilityCurrent
	CompatibilityNewer
	CompatibilityInconsistent
)

func (c Compatibility) String() string {
	switch c {
	case CompatibilityMissing:
		return "missing"
	case CompatibilityPending:
		return "pending"
	case CompatibilityCurrent:
		return "current"
	case CompatibilityNewer:
		return "newer"
	case CompatibilityInconsistent:
		return "inconsistent"
	default:
		return "unknown"
	}
}

func ParseDatabaseURL(raw string) (*pgx.ConnConfig, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.Path == "" || u.Path == "/" {
		return nil, errors.New("storage: invalid PostgreSQL database URL")
	}
	config, err := pgx.ParseConfig(raw)
	if err != nil || config.Database == "" {
		return nil, errors.New("storage: invalid PostgreSQL database URL")
	}
	config.RuntimeParams["timezone"] = "UTC"
	config.ConnectTimeout = 5 * time.Second
	return config, nil
}

func Upgrade(ctx context.Context, databaseURL string) (UpgradeResult, error) {
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		return UpgradeResult{}, err
	}
	return UpgradeWithMigrations(ctx, databaseURL, migrations)
}

func UpgradeWithMigrations(ctx context.Context, databaseURL string, migrations []Migration) (result UpgradeResult, retErr error) {
	if err := validateMigrations(migrations); err != nil {
		return UpgradeResult{}, err
	}
	config, err := ParseDatabaseURL(databaseURL)
	if err != nil {
		return UpgradeResult{}, err
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return UpgradeResult{}, operationError(ctx, "storage: database connection failed", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := conn.Close(cleanupCtx); err != nil && retErr == nil {
			retErr = errors.New("storage: database connection close failed")
		}
	}()
	if err := setSessionBounds(ctx, conn); err != nil {
		return UpgradeResult{}, operationError(ctx, "storage: database setup failed", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return UpgradeResult{}, operationError(ctx, "storage: database upgrade lock failed", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanupCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil && retErr == nil {
			retErr = errors.New("storage: database upgrade unlock failed")
		}
	}()
	if _, err := conn.Exec(ctx, embeddedBootstrapSQL); err != nil {
		return UpgradeResult{}, operationError(ctx, "storage: migration metadata bootstrap failed", err)
	}
	history, err := dbgen.New(conn).GetMigrationHistory(ctx)
	if err != nil {
		return UpgradeResult{}, operationError(ctx, "storage: migration metadata read failed", err)
	}
	compatibility, version := compareHistory(history, migrations)
	if compatibility == CompatibilityNewer {
		return UpgradeResult{}, ErrNewerSchema
	}
	if compatibility == CompatibilityInconsistent {
		return UpgradeResult{}, ErrInconsistentHistory
	}
	result.DatabaseSchemaVersion = version
	for _, migration := range migrations[len(history):] {
		if err := applyMigration(ctx, conn, migration); err != nil {
			message := fmt.Sprintf("storage: migration %d failed", migration.Version)
			if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && safeSQLState(pgErr.Code) {
				message += " (SQLSTATE " + pgErr.Code + ")"
			}
			return UpgradeResult{}, operationError(ctx, message, err)
		}
		result.Applied++
		result.DatabaseSchemaVersion = migration.Version
	}
	return result, nil
}

func safeSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, character := range code {
		if !(character >= '0' && character <= '9' || character >= 'A' && character <= 'Z') {
			return false
		}
	}
	return true
}

func CheckCompatibility(ctx context.Context, databaseURL string) (Compatibility, error) {
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		return CompatibilityInconsistent, err
	}
	config, err := ParseDatabaseURL(databaseURL)
	if err != nil {
		return CompatibilityMissing, err
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return CompatibilityMissing, operationError(ctx, "storage: database connection failed", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(cleanupCtx)
	}()
	if err := setSessionBounds(ctx, conn); err != nil {
		return CompatibilityMissing, operationError(ctx, "storage: database setup failed", err)
	}
	var tableName *string
	if err := conn.QueryRow(ctx, "SELECT to_regclass('public.almanac_schema_migrations')::text").Scan(&tableName); err != nil {
		return CompatibilityMissing, operationError(ctx, "storage: migration metadata check failed", err)
	}
	if tableName == nil {
		return CompatibilityMissing, nil
	}
	history, err := dbgen.New(conn).GetMigrationHistory(ctx)
	if err != nil {
		return CompatibilityInconsistent, operationError(ctx, "storage: migration metadata read failed", err)
	}
	state, _ := compareHistory(history, migrations)
	return state, nil
}

func setSessionBounds(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, "SET statement_timeout = '25s'"); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, "SET lock_timeout = '5s'")
	return err
}

func operationError(ctx context.Context, message string, cause error) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(errors.New(message), ErrCancelled, err)
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(errors.New(message), ErrCancelled, cause)
	}
	if networkErr, ok := errors.AsType[net.Error](cause); ok && networkErr.Timeout() {
		return errors.Join(errors.New(message), ErrCancelled, cause)
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](cause); ok && (pgErr.Code == "57014" || pgErr.Code == "55P03") {
		return errors.Join(errors.New(message), ErrCancelled)
	}
	return errors.New(message)
}

func applyMigration(ctx context.Context, conn *pgx.Conn, migration Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '25s'"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, migration.SQL, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	queries := dbgen.New(tx)
	if err := queries.RecordMigration(ctx, dbgen.RecordMigrationParams{
		Version: int64(migration.Version), Filename: migration.Name, Checksum: migration.Checksum[:],
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validateMigrations(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("storage: invalid migration set")
	}
	for i, migration := range migrations {
		if migration.Version != i+1 || migration.Name == "" || migration.SQL == "" || sha256.Sum256([]byte(migration.SQL)) != migration.Checksum {
			return errors.New("storage: invalid migration set")
		}
	}
	return nil
}

func compareHistory(history []dbgen.AlmanacSchemaMigration, migrations []Migration) (Compatibility, int) {
	newer := false
	for i, row := range history {
		wantVersion := int64(i + 1)
		if row.Version != wantVersion {
			return CompatibilityInconsistent, 0
		}
		if row.Version <= int64(len(migrations)) {
			migration := migrations[i]
			if row.Filename != migration.Name || !bytes.Equal(row.Checksum, migration.Checksum[:]) {
				return CompatibilityInconsistent, 0
			}
		} else {
			newer = true
		}
	}
	if newer {
		return CompatibilityNewer, int(history[len(history)-1].Version)
	}
	if len(history) < len(migrations) {
		version := 0
		if len(history) > 0 {
			version = int(history[len(history)-1].Version)
		}
		return CompatibilityPending, version
	}
	version := 0
	if len(history) > 0 {
		version = int(history[len(history)-1].Version)
	}
	return CompatibilityCurrent, version
}
