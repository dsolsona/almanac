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
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
)

type setupDocument struct {
	SchemaVersion  int    `json:"schema_version"`
	Operation      string `json:"operation"`
	Completed      bool   `json:"completed"`
	SessionCleanup string `json:"session_cleanup"`
}
type setupErrorDocument struct {
	SchemaVersion int        `json:"schema_version"`
	Operation     string     `json:"operation"`
	Error         setupError `json:"error"`
}
type setupError struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	OutcomeUnknown    bool   `json:"outcome_unknown"`
	SessionState      string `json:"session_state"`
	RequestID         string `json:"request_id,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitzero"`
}

func setup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	jsonHint := requestedJSON(args)
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	backendURL := fs.String("backend-url", "", "canonical HTTPS backend origin")
	login := fs.String("initial-admin-login", "", "initial administrator login")
	initialFile := fs.String("initial-admin-password-file", "", "protected initial password file")
	newFile := fs.String("new-password-file", "", "protected new password file")
	caFile := fs.String("ca-file", "", "optional PEM CA bundle")
	timeout := fs.Duration("timeout", 30*time.Second, "whole setup deadline")
	output := fs.String("output", "text", "result format: text or json")
	if err := fs.Parse(args); err != nil {
		return writeSetupError(stdout, stderr, jsonHint, 2, "invalid_request", "invalid flags", false, "unchecked", "", 0)
	}
	jsonOut := jsonHint || *output == "json"
	if fs.NArg() != 0 {
		return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_request", "positional arguments are not supported", false, "unchecked", "", 0)
	}
	if *output != "text" && *output != "json" {
		return writeSetupError(stdout, stderr, jsonHint, 2, "invalid_request", "output must be text or json", false, "unchecked", "", 0)
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	for _, name := range []string{"backend-url", "initial-admin-login", "initial-admin-password-file", "new-password-file"} {
		if !seen[name] {
			return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_request", "required setup flags are missing", false, "unchecked", "", 0)
		}
	}
	if *timeout <= 0 || *timeout > 2*time.Minute {
		return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_configuration", "timeout must be positive and at most 2m", false, "unchecked", "", 0)
	}
	if len(*login) == 0 || len(*login) > 256 || !utf8.ValidString(*login) {
		return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_login", "initial administrator login is invalid", false, "unchecked", "", 0)
	}
	initial, err := secretfile.Read(*initialFile, 512)
	if err != nil {
		return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_password_file", "password file is invalid", false, "unchecked", "", 0)
	}
	newPassword, err := secretfile.Read(*newFile, 512)
	if err != nil {
		return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_password_file", "password file is invalid", false, "unchecked", "", 0)
	}
	if !utf8.Valid(initial) || !utf8.Valid(newPassword) {
		return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_password", "password file must contain UTF-8 text", false, "unchecked", "", 0)
	}
	if err := identity.ValidateNewPassword(string(newPassword)); err != nil || string(initial) == string(newPassword) {
		return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_password", "new password is invalid or unchanged", false, "unchecked", "", 0)
	}
	var ca []byte
	if seen["ca-file"] && *caFile == "" {
		return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_ca_file", "CA bundle file is invalid", false, "unchecked", "", 0)
	}
	if *caFile != "" {
		ca, err = secretfile.ReadCertificate(*caFile)
		if err != nil {
			return writeSetupError(stdout, stderr, jsonOut, 2, "invalid_ca_file", "CA bundle file is invalid", false, "unchecked", "", 0)
		}
	}
	client, err := setupclient.New(setupclient.Options{BackendURL: *backendURL, Login: *login, CABundle: ca})
	if err != nil {
		return writeSetupFailure(stdout, stderr, jsonOut, err)
	}
	defer client.Close()
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	opCtx, cancel := context.WithTimeout(signalCtx, *timeout)
	defer cancel()
	result, err := client.Complete(opCtx, string(initial), string(newPassword))
	if err != nil {
		return writeSetupFailure(stdout, stderr, jsonOut, err)
	}
	cleanupDiagnosticFailed := false
	if result.Completed && result.SessionCleanup == "unknown" {
		if _, err := fmt.Fprintln(stderr, "setup: password change completed; session cleanup could not be confirmed"); err != nil {
			writeSetupOutputFailure(stderr)
			cleanupDiagnosticFailed = true
		}
	}
	if jsonOut {
		doc := setupDocument{SchemaVersion: 1, Operation: "setup", Completed: result.Completed, SessionCleanup: result.SessionCleanup}
		if err := json.MarshalWrite(stdout, doc); err != nil {
			writeSetupOutputFailure(stderr)
			return 1
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			writeSetupOutputFailure(stderr)
			return 1
		}
		if cleanupDiagnosticFailed {
			return 1
		}
		return 0
	}
	_, err = fmt.Fprintln(stdout, "administrator setup completed")
	if err != nil {
		writeSetupOutputFailure(stderr)
		return 1
	}
	if cleanupDiagnosticFailed {
		return 1
	}
	return 0
}

func writeSetupFailure(stdout, stderr io.Writer, jsonOut bool, err error) int {
	f, ok := errors.AsType[*setupclient.Failure](err)
	if !ok {
		return writeSetupError(stdout, stderr, jsonOut, 1, "setup_failed", "setup failed", false, "unchecked", "", 0)
	}
	code := 1
	switch f.Kind {
	case setupclient.KindInvalid:
		code = 2
	case setupclient.KindAuthentication:
		code = 3
	case setupclient.KindConflict:
		code = 5
	case setupclient.KindCancelled:
		code = 6
	}
	return writeSetupError(stdout, stderr, jsonOut, code, f.Code, f.Message, f.OutcomeUnknown, f.SessionState, f.RequestID, f.RetryAfterSeconds)
}
func writeSetupError(stdout, stderr io.Writer, jsonOut bool, exit int, code, message string, unknown bool, state, requestID string, retry int) int {
	if jsonOut {
		doc := setupErrorDocument{SchemaVersion: 1, Operation: "setup", Error: setupError{Code: code, Message: message, OutcomeUnknown: unknown, SessionState: state, RequestID: requestID, RetryAfterSeconds: retry}}
		if err := json.MarshalWrite(stdout, doc); err != nil {
			writeSetupOutputFailure(stderr)
			return 1
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			writeSetupOutputFailure(stderr)
			return 1
		}
		return exit
	}
	if _, err := fmt.Fprintln(stderr, "setup:", message); err != nil {
		writeSetupOutputFailure(stderr)
		return 1
	}
	return exit
}
func writeSetupOutputFailure(stderr io.Writer) { _, _ = fmt.Fprintln(stderr, "setup: output failed") }
