package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dsolsona/almanac/internal/storage"
)

const maxDatabaseURLBytes = 16 << 10

type dbUpgradeJSON struct {
	SchemaVersion         int    `json:"schema_version"`
	Operation             string `json:"operation"`
	DatabaseSchemaVersion int    `json:"database_schema_version"`
	Applied               int    `json:"applied"`
}

type dbUpgradeErrorJSON struct {
	SchemaVersion int                `json:"schema_version"`
	Operation     string             `json:"operation"`
	Error         dbUpgradeErrorBody `json:"error"`
}

type dbUpgradeErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func db(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "db: expected subcommand upgrade")
		return 2
	}
	if args[0] != "upgrade" {
		return writeUpgradeError(stdout, stderr, requestedJSON(args), 2, "invalid_request", "unsupported database subcommand")
	}
	return dbUpgrade(ctx, args[1:], stdout, stderr)
}

func dbUpgrade(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	jsonRequested := requestedJSON(args)
	fs := flag.NewFlagSet("db upgrade", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	databaseURLFile := fs.String("database-url-file", "", "protected file containing a PostgreSQL URL")
	timeout := fs.Duration("timeout", 30*time.Second, "database operation deadline")
	output := fs.String("output", "text", "result format: text or json")
	if err := fs.Parse(args); err != nil {
		return writeUpgradeError(stdout, stderr, jsonRequested, 2, "invalid_request", "invalid flags")
	}
	if fs.NArg() != 0 {
		return writeUpgradeError(stdout, stderr, *output == "json" || jsonRequested, 2, "invalid_request", "positional arguments are not supported")
	}
	if *timeout <= 0 || *timeout > 2*time.Minute {
		return writeUpgradeError(stdout, stderr, *output == "json", 2, "invalid_configuration", "timeout must be positive and at most 2m")
	}
	if *output != "text" && *output != "json" {
		return writeUpgradeError(stdout, stderr, jsonRequested, 2, "invalid_request", "output must be text or json")
	}
	explicitFile := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "database-url-file" {
			explicitFile = true
		}
	})
	databaseURL, err := resolveDatabaseURL(explicitFile, *databaseURLFile, os.Environ())
	if err != nil {
		return writeUpgradeError(stdout, stderr, *output == "json", 2, "invalid_configuration", "database URL configuration is invalid")
	}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	opCtx, cancel := context.WithTimeout(signalCtx, *timeout)
	defer cancel()
	result, err := storage.Upgrade(opCtx, databaseURL)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrCancelled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return writeUpgradeError(stdout, stderr, *output == "json", 6, "cancelled", "database upgrade was cancelled or timed out")
		case errors.Is(err, storage.ErrNewerSchema), errors.Is(err, storage.ErrInconsistentHistory):
			return writeUpgradeError(stdout, stderr, *output == "json", 1, "schema_incompatible", "database schema is incompatible with this binary")
		default:
			return writeUpgradeError(stdout, stderr, *output == "json", 1, "database_failure", "database upgrade failed")
		}
	}
	return writeUpgradeResult(stdout, stderr, *output == "json", result)
}

func writeUpgradeResult(stdout, stderr io.Writer, outputJSON bool, result storage.UpgradeResult) int {
	if outputJSON {
		response := dbUpgradeJSON{SchemaVersion: 1, Operation: "db.upgrade", DatabaseSchemaVersion: result.DatabaseSchemaVersion, Applied: result.Applied}
		if err := json.MarshalWrite(stdout, response); err != nil {
			writeOutputFailure(stderr)
			return 1
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			writeOutputFailure(stderr)
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprintf(stdout, "database schema version %d; applied %d migrations\n", result.DatabaseSchemaVersion, result.Applied); err != nil {
		writeOutputFailure(stderr)
		return 1
	}
	return 0
}

func writeOutputFailure(stderr io.Writer) {
	_, _ = fmt.Fprintln(stderr, "db upgrade: output failed")
}

func writeUpgradeError(stdout, stderr io.Writer, outputJSON bool, code int, errorCode, message string) int {
	if outputJSON {
		response := dbUpgradeErrorJSON{
			SchemaVersion: 1,
			Operation:     "db.upgrade",
			Error:         dbUpgradeErrorBody{Code: errorCode, Message: message},
		}
		if err := json.MarshalWrite(stdout, response); err != nil {
			fmt.Fprintln(stderr, "db upgrade: output failed")
			return 1
		}
		fmt.Fprintln(stdout)
		return code
	}
	fmt.Fprintln(stderr, "db upgrade:", message)
	return code
}

func requestedJSON(args []string) bool {
	for i, arg := range args {
		if arg == "--output=json" || arg == "--output" && i+1 < len(args) && args[i+1] == "json" {
			return true
		}
	}
	return false
}

func resolveDatabaseURL(explicitFile bool, explicitPath string, environ []string) (string, error) {
	if explicitFile {
		return readDatabaseURLFile(explicitPath)
	}
	if value, ok := environmentValue(environ, "ALMANAC_DATABASE_URL_FILE"); ok {
		return readDatabaseURLFile(value)
	}
	value, ok := environmentValue(environ, "ALMANAC_DATABASE_URL")
	if !ok || value == "" {
		return "", errors.New("database URL is required")
	}
	if _, err := storage.ParseDatabaseURL(value); err != nil {
		return "", errors.New("database URL is invalid")
	}
	return value, nil
}

func environmentValue(environ []string, name string) (string, bool) {
	var value string
	found := false
	for _, pair := range environ {
		key, candidate, ok := strings.Cut(pair, "=")
		if ok && key == name {
			value = candidate
			found = true
		}
	}
	return value, found
}

func readDatabaseURLFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("database URL file path is empty")
	}
	file, err := openProtectedURLFile(path)
	if err != nil {
		return "", errors.New("database URL file is invalid")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("database URL file must be regular and protected")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxDatabaseURLBytes+1))
	if err != nil || len(data) > maxDatabaseURLBytes {
		return "", errors.New("database URL file exceeds 16 KiB or cannot be read")
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("database URL file is empty")
	}
	if _, err := storage.ParseDatabaseURL(value); err != nil {
		return "", errors.New("database URL file does not contain a valid PostgreSQL URL")
	}
	return value, nil
}
