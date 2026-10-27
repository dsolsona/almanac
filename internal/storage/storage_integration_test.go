//go:build integration

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/testpostgres"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationUpgradeIsIdempotentAndPreservesInstallationIdentity(t *testing.T) {
	databaseURL := integrationDatabase(t)
	state, err := CheckCompatibility(t.Context(), databaseURL)
	if err != nil || state != CompatibilityMissing {
		t.Fatalf("compatibility before upgrade = %s, error = %v", state, err)
	}
	assertTableAbsent(t, databaseURL, "almanac_schema_migrations")
	bootstrapConn := integrationConnect(t, databaseURL)
	if _, err := bootstrapConn.Exec(t.Context(), embeddedBootstrapSQL); err != nil {
		t.Fatal("prepare empty migration history failed")
	}
	closeIntegrationConn(bootstrapConn)
	state, err = CheckCompatibility(t.Context(), databaseURL)
	if err != nil || state != CompatibilityPending {
		t.Fatalf("compatibility with empty history = %s, error = %v", state, err)
	}

	first, err := Upgrade(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if first.Applied != 10 || first.DatabaseSchemaVersion != 10 {
		t.Fatalf("first upgrade = %+v, want ten applied migrations at version 10", first)
	}
	firstID, firstEpoch, firstCreatedAt := installationIdentity(t, databaseURL)
	second, err := Upgrade(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if second.Applied != 0 || second.DatabaseSchemaVersion != 10 {
		t.Fatalf("second upgrade = %+v, want zero applied migrations at version 10", second)
	}
	secondID, secondEpoch, secondCreatedAt := installationIdentity(t, databaseURL)
	if firstID != secondID || firstEpoch != secondEpoch || !firstCreatedAt.Equal(secondCreatedAt) {
		t.Fatal("repeated upgrade changed installation identity or creation time")
	}
	state, err = CheckCompatibility(t.Context(), databaseURL)
	if err != nil || state != CompatibilityCurrent {
		t.Fatalf("compatibility after upgrade = %s, error = %v", state, err)
	}
}

func TestIntegrationConcurrentUpgradesApplyOnce(t *testing.T) {
	databaseURL := integrationDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	start := make(chan struct{})
	type outcome struct {
		result UpgradeResult
		err    error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := Upgrade(ctx, databaseURL)
			results <- outcome{result: result, err: err}
		}()
	}
	close(start)
	var totalApplied int
	for range 2 {
		outcome := <-results
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		totalApplied += outcome.result.Applied
	}
	if totalApplied != 10 {
		t.Fatalf("concurrent upgrades applied %d migrations, want 10", totalApplied)
	}
	conn := integrationConnect(t, databaseURL)
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM public.almanac_schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10 {
		t.Fatalf("history row count = %d, want 10", count)
	}
	var installationCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM public.almanac_installation").Scan(&installationCount); err != nil {
		t.Fatal(err)
	}
	if installationCount != 1 {
		t.Fatalf("installation row count = %d, want 1", installationCount)
	}
}

func TestIntegrationUpgradeFromVersionOneAppliesInitialOwnerSchema(t *testing.T) {
	databaseURL := integrationDatabase(t)
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 10 || migrations[0].Version != 1 || migrations[1].Version != 2 || migrations[2].Version != 3 || migrations[3].Version != 4 || migrations[4].Version != 5 || migrations[5].Version != 6 || migrations[6].Version != 7 || migrations[7].Version != 8 || migrations[8].Version != 9 || migrations[9].Version != 10 {
		t.Fatalf("embedded migration versions = %v, want [1 2 3 4 5 6 7 8 9 10]", versions(migrations))
	}
	first, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:1])
	if err != nil || first.Applied != 1 || first.DatabaseSchemaVersion != 1 {
		t.Fatalf("version 1 upgrade = %+v, %v", first, err)
	}
	identityID, identityEpoch, createdAt := installationIdentity(t, databaseURL)
	second, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:2])
	if err != nil || second.Applied != 1 || second.DatabaseSchemaVersion != 2 {
		t.Fatalf("version 1 to 2 upgrade = %+v, %v", second, err)
	}
	third, err := Upgrade(t.Context(), databaseURL)
	if err != nil || third.Applied != 8 || third.DatabaseSchemaVersion != 10 {
		t.Fatalf("version 2 to 10 upgrade = %+v, %v", third, err)
	}
	gotID, gotEpoch, gotCreatedAt := installationIdentity(t, databaseURL)
	if gotID != identityID || gotEpoch != identityEpoch || !gotCreatedAt.Equal(createdAt) {
		t.Fatal("version 2 migration changed installation identity or creation time")
	}
}

func TestIntegrationFailedMigrationRollsBackAndReleasesLock(t *testing.T) {
	databaseURL := integrationDatabase(t)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	base, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	failedSQL := "CREATE TABLE public.storage_rolled_back (id integer); SELECT 1 / 0;"
	failed := Migration{Version: 11, Name: "0011_fails.sql", SQL: failedSQL, Checksum: sha256.Sum256([]byte(failedSQL))}
	if _, err := UpgradeWithMigrations(t.Context(), databaseURL, append(base, failed)); err == nil {
		t.Fatal("upgrade accepted a migration that failed after DDL")
	}
	assertTableAbsent(t, databaseURL, "storage_rolled_back")
	conn := integrationConnect(t, databaseURL)
	var historyCount int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_schema_migrations").Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != 10 {
		t.Fatalf("history rows after rollback = %d, want 10", historyCount)
	}
	closeIntegrationConn(conn)

	validSQL := "CREATE TABLE public.storage_after_failure (id integer);"
	valid := Migration{Version: 11, Name: "0011_valid.sql", SQL: validSQL, Checksum: sha256.Sum256([]byte(validSQL))}
	result, err := UpgradeWithMigrations(t.Context(), databaseURL, append(base, valid))
	if err != nil {
		t.Fatalf("upgrade after failed migration could not acquire lock: %v", err)
	}
	if result.Applied != 1 || result.DatabaseSchemaVersion != 11 {
		t.Fatalf("upgrade after failure = %+v", result)
	}
}

func TestIntegrationAlteredAndNewerHistoryAreRejected(t *testing.T) {
	t.Run("altered checksum", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		if _, err := Upgrade(t.Context(), databaseURL); err != nil {
			t.Fatal(err)
		}
		conn := integrationConnect(t, databaseURL)
		if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_schema_migrations SET checksum = decode(repeat('00', 32), 'hex') WHERE version = 1"); err != nil {
			t.Fatal(err)
		}
		closeIntegrationConn(conn)
		if _, err := Upgrade(t.Context(), databaseURL); !errors.Is(err, ErrInconsistentHistory) {
			t.Fatalf("upgrade error = %v, want inconsistent history", err)
		}
	})

	t.Run("newer version", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		if _, err := Upgrade(t.Context(), databaseURL); err != nil {
			t.Fatal(err)
		}
		conn := integrationConnect(t, databaseURL)
		if _, err := conn.Exec(t.Context(), "INSERT INTO public.almanac_schema_migrations (version, filename, checksum) VALUES (11, '0011_future.sql', decode(repeat('ab', 32), 'hex'))"); err != nil {
			t.Fatal(err)
		}
		closeIntegrationConn(conn)
		state, err := CheckCompatibility(t.Context(), databaseURL)
		if err != nil || state != CompatibilityNewer {
			t.Fatalf("compatibility = %s, error = %v, want newer", state, err)
		}
		if _, err := Upgrade(t.Context(), databaseURL); !errors.Is(err, ErrNewerSchema) {
			t.Fatalf("upgrade error = %v, want newer schema", err)
		}
	})

	t.Run("gapped history", func(t *testing.T) {
		databaseURL := integrationDatabase(t)
		if _, err := Upgrade(t.Context(), databaseURL); err != nil {
			t.Fatal(err)
		}
		conn := integrationConnect(t, databaseURL)
		if _, err := conn.Exec(t.Context(), "INSERT INTO public.almanac_schema_migrations (version, filename, checksum) VALUES (12, '0012_gap.sql', decode(repeat('cd', 32), 'hex'))"); err != nil {
			t.Fatal(err)
		}
		closeIntegrationConn(conn)
		state, err := CheckCompatibility(t.Context(), databaseURL)
		if err != nil || state != CompatibilityInconsistent {
			t.Fatalf("compatibility = %s, error = %v, want inconsistent", state, err)
		}
	})
}

func TestIntegrationCancelledLockWaitLeavesNoMetadataAndCanRetry(t *testing.T) {
	databaseURL := integrationDatabase(t)
	blocker := integrationConnect(t, databaseURL)
	if _, err := blocker.Exec(t.Context(), "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		t.Fatal(err)
	}
	blockerClosed := false
	t.Cleanup(func() {
		if !blockerClosed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = blocker.Exec(cleanupCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
			_ = blocker.Close(cleanupCtx)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := Upgrade(ctx, databaseURL)
		result <- err
	}()

	observer := integrationConnect(t, databaseURL)
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		var count int
		err := observer.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%pg_advisory_lock%'").Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		cancel()
		t.Fatal("upgrader never reached the advisory lock wait")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("cancelled upgrade error = %v, want cancellation sentinel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled advisory-lock wait did not return promptly")
	}
	assertTableAbsent(t, databaseURL, "almanac_schema_migrations")
	if _, err := blocker.Exec(t.Context(), "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil {
		t.Fatal(err)
	}
	blockerClosed = true
	closeIntegrationConn(blocker)
	closeIntegrationConn(observer)
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatalf("upgrade after lock release failed: %v", err)
	}
}

func TestIntegrationDBUpgradeBinarySuccessErrorsAndSignalCancellation(t *testing.T) {
	databaseURL := integrationDatabase(t)
	urlFile := protectedDatabaseURLFile(t, databaseURL)
	binary := filepath.Join("..", "..", "bin", "almanac")

	firstCode, firstStdout, firstStderr := runAlmanac(t, binary, []string{"db", "upgrade", "--database-url-file=" + urlFile, "--output=json"}, nil)
	if firstCode != 0 || len(firstStderr) != 0 {
		t.Fatalf("first binary upgrade: code=%d stdout=%q stderr=%q", firstCode, firstStdout, firstStderr)
	}
	first := decodeBinaryUpgrade(t, firstStdout)
	if first.DatabaseSchemaVersion != 10 || first.Applied != 10 || first.Error != nil {
		t.Fatalf("first binary upgrade result = %+v", first)
	}
	identityID, identityEpoch, identityCreated := installationIdentity(t, databaseURL)
	secondCode, secondStdout, secondStderr := runAlmanac(t, binary, []string{"db", "upgrade", "--database-url-file=" + urlFile, "--output=json"}, nil)
	if secondCode != 0 || len(secondStderr) != 0 {
		t.Fatalf("second binary upgrade: code=%d stdout=%q stderr=%q", secondCode, secondStdout, secondStderr)
	}
	second := decodeBinaryUpgrade(t, secondStdout)
	if second.DatabaseSchemaVersion != 10 || second.Applied != 0 || second.Error != nil {
		t.Fatalf("second binary upgrade result = %+v", second)
	}
	secondID, secondEpoch, secondCreated := installationIdentity(t, databaseURL)
	if identityID != secondID || identityEpoch != secondEpoch || !identityCreated.Equal(secondCreated) {
		t.Fatal("real CLI repeated upgrade changed the installation identity")
	}

	invalidCode, invalidStdout, invalidStderr := runAlmanac(t, binary, []string{"db", "upgrade", "--output=json"}, []string{"ALMANAC_DATABASE_URL=postgres://operator:binary-secret@127.0.0.1/"})
	invalid := decodeBinaryUpgrade(t, invalidStdout)
	if invalidCode != 2 || invalid.Error == nil || invalid.Error.Code != "invalid_configuration" || len(invalidStderr) != 0 {
		t.Fatalf("invalid binary URL: code=%d output=%q stderr=%q", invalidCode, invalidStdout, invalidStderr)
	}
	if bytes.Contains(invalidStdout, []byte("binary-secret")) || bytes.Contains(invalidStderr, []byte("binary-secret")) {
		t.Fatal("binary configuration error exposed a password")
	}

	unreachableFile := protectedDatabaseURLFile(t, "postgres://operator:binary-secret@127.0.0.1:1/almanac?sslmode=disable")
	unreachableCode, unreachableStdout, unreachableStderr := runAlmanac(t, binary, []string{"db", "upgrade", "--database-url-file=" + unreachableFile, "--timeout=2s", "--output=json"}, nil)
	unreachable := decodeBinaryUpgrade(t, unreachableStdout)
	if unreachableCode != 1 || unreachable.Error == nil || unreachable.Error.Code != "database_failure" || len(unreachableStderr) != 0 {
		t.Fatalf("unreachable binary database: code=%d output=%q stderr=%q", unreachableCode, unreachableStdout, unreachableStderr)
	}
	if bytes.Contains(unreachableStdout, []byte("binary-secret")) || bytes.Contains(unreachableStderr, []byte("binary-secret")) {
		t.Fatal("binary database error exposed a password")
	}

	blocker := integrationConnect(t, databaseURL)
	if _, err := blocker.Exec(t.Context(), "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		t.Fatal("acquire binary cancellation blocker failed")
	}
	blockerReleased := false
	t.Cleanup(func() {
		if !blockerReleased {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = blocker.Exec(cleanupCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
		}
	})
	processCtx, cancelProcess := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelProcess()
	cmd := exec.CommandContext(processCtx, binary, "db", "upgrade", "--database-url-file="+urlFile, "--timeout=30s", "--output=json")
	cmd.Env = integrationCommandEnv(nil)
	var canceledStdout, canceledStderr bytes.Buffer
	cmd.Stdout = &canceledStdout
	cmd.Stderr = &canceledStderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary cancellation case failed: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	processWaited := false
	t.Cleanup(func() {
		if !processWaited && cmd.Process != nil {
			_ = cmd.Process.Kill()
			select {
			case <-waited:
			case <-time.After(2 * time.Second):
			}
		}
	})
	observer := integrationConnect(t, databaseURL)
	waitDeadline := time.Now().Add(3 * time.Second)
	lockWaitObserved := false
	for time.Now().Before(waitDeadline) {
		var count int
		if err := observer.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%pg_advisory_lock%'").Scan(&count); err != nil {
			t.Fatal("observe real binary advisory-lock wait failed")
		}
		if count > 0 {
			lockWaitObserved = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !lockWaitObserved {
		t.Fatal("real binary did not reach the advisory-lock wait")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to waiting binary failed: %v", err)
	}
	var waitErr error
	select {
	case waitErr = <-waited:
		processWaited = true
	case <-time.After(2 * time.Second):
		t.Fatal("real binary did not handle SIGTERM within the bound")
	}
	exitErr, isExitErr := errors.AsType[*exec.ExitError](waitErr)
	if !isExitErr || exitErr.ExitCode() != 6 {
		t.Fatalf("signal-cancelled binary exit = %v, want 6; stdout=%q stderr=%q", waitErr, canceledStdout.String(), canceledStderr.String())
	}
	canceled := decodeBinaryUpgrade(t, canceledStdout.Bytes())
	if canceled.Error == nil || canceled.Error.Code != "cancelled" || len(canceledStderr.Bytes()) != 0 {
		t.Fatalf("signal cancellation output=%q stderr=%q", canceledStdout.String(), canceledStderr.String())
	}
	if _, err := blocker.Exec(t.Context(), "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil {
		t.Fatal("release binary cancellation blocker failed")
	}
	blockerReleased = true
}

func integrationDatabase(t *testing.T) string {
	t.Helper()
	return testpostgres.Database(t)
}

func runAlmanac(t *testing.T, binary string, args, extraEnv []string) (int, []byte, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = integrationCommandEnv(extraEnv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stdout.Bytes(), stderr.Bytes()
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode(), stdout.Bytes(), stderr.Bytes()
	}
	t.Fatalf("run real Almanac executable failed (status unavailable)")
	return -1, nil, nil
}

func integrationCommandEnv(extra []string) []string {
	environ := make([]string, 0, len(os.Environ())+len(extra))
	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		if name != "ALMANAC_DATABASE_URL" && name != "ALMANAC_DATABASE_URL_FILE" {
			environ = append(environ, pair)
		}
	}
	return append(environ, extra...)
}

func protectedDatabaseURLFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal("write protected connection fixture failed")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal("protect connection fixture failed")
	}
	return path
}

func decodeBinaryUpgrade(t *testing.T, data []byte) struct {
	SchemaVersion         int    `json:"schema_version"`
	Operation             string `json:"operation"`
	DatabaseSchemaVersion int    `json:"database_schema_version"`
	Applied               int    `json:"applied"`
	Error                 *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
} {
	t.Helper()
	var result struct {
		SchemaVersion         int    `json:"schema_version"`
		Operation             string `json:"operation"`
		DatabaseSchemaVersion int    `json:"database_schema_version"`
		Applied               int    `json:"applied"`
		Error                 *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("real executable stdout is not one JSON document: %v; output=%q", err, data)
	}
	if result.SchemaVersion != 1 || result.Operation != "db.upgrade" {
		t.Fatalf("real executable returned unexpected document: %+v", result)
	}
	return result
}

func integrationConnect(t *testing.T, databaseURL string) *pgx.Conn {
	t.Helper()
	return testpostgres.Connect(t, databaseURL)
}

func closeIntegrationConn(conn *pgx.Conn) {
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Close(closeCtx)
}

func installationIdentity(t *testing.T, databaseURL string) (string, string, time.Time) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	var id, epoch string
	var createdAt time.Time
	if err := conn.QueryRow(t.Context(), "SELECT installation_id::text, credential_epoch::text, created_at FROM public.almanac_installation WHERE singleton").Scan(&id, &epoch, &createdAt); err != nil {
		t.Fatal("read installation identity failed")
	}
	return id, epoch, createdAt
}

func assertTableAbsent(t *testing.T, databaseURL, table string) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	var name *string
	if err := conn.QueryRow(t.Context(), "SELECT to_regclass($1)::text", "public."+table).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != nil {
		t.Fatalf("table %s unexpectedly exists", *name)
	}
}
