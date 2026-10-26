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

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
)

type authDocument struct {
	SchemaVersion int               `json:"schema_version"`
	Operation     string            `json:"operation"`
	SessionState  string            `json:"session_state"`
	Identity      *api.SafeIdentity `json:"identity"`
}

type authOptions struct {
	operation, profile, backend, login, passwordFile, caFile, output string
	timeout                                                          time.Duration
	caProvided                                                       bool
}

func authCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	jsonOut := requestedJSON(args)
	op := "auth"
	fail := func(f *setupclient.Failure) int { return writeAuthFailure(stdout, stderr, jsonOut, op, f) }
	invalid := func(message string) int {
		return fail(&setupclient.Failure{Kind: setupclient.KindInvalid, Code: "invalid_request", Message: message, SessionState: "unchecked"})
	}
	if len(args) == 0 || (args[0] != "login" && args[0] != "context" && args[0] != "logout") {
		return invalid("choose auth login, context or logout")
	}
	op += " " + args[0]
	options := authOptions{operation: args[0]}
	fs := flag.NewFlagSet(op, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&options.profile, "profile-file", "", "protected profile file")
	fs.StringVar(&options.output, "output", "text", "text or json")
	fs.DurationVar(&options.timeout, "timeout", 30*time.Second, "whole authentication deadline")
	if options.operation == "login" {
		fs.StringVar(&options.backend, "backend-url", "", "canonical HTTPS backend origin")
		fs.StringVar(&options.login, "login", "", "user login")
		fs.StringVar(&options.passwordFile, "password-file", "", "protected password file")
		fs.StringVar(&options.caFile, "ca-file", "", "optional PEM CA bundle")
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return invalid("invalid authentication flags")
	}
	jsonOut = jsonOut || options.output == "json"
	if options.output != "text" && options.output != "json" {
		return invalid("output must be text or json")
	}
	if options.profile == "" || options.timeout <= 0 || options.timeout > 2*time.Minute {
		return invalid("profile-file is required and timeout must be positive and at most 2m")
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "ca-file" {
			options.caProvided = true
		}
	})
	if options.operation == "login" && (options.backend == "" || options.login == "" || len(options.login) > 256 || !utf8.ValidString(options.login) || options.passwordFile == "" || (options.caProvided && options.caFile == "")) {
		return invalid("login requires a backend origin, valid login and protected password file")
	}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	opCtx, cancel := context.WithTimeout(signalCtx, options.timeout)
	defer cancel()
	result, failure := performAuth(opCtx, options)
	if failure != nil {
		return fail(failure)
	}
	result.Operation = op
	if jsonOut {
		if json.MarshalWrite(stdout, result) != nil {
			return authOutputFailure(stderr)
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return authOutputFailure(stderr)
		}
	} else {
		message := "authentication session: " + result.SessionState
		if result.SessionState == "restricted" {
			message += "; complete administrator setup before management commands"
		}
		if _, err := fmt.Fprintln(stdout, message); err != nil {
			return authOutputFailure(stderr)
		}
	}
	return 0
}

func performAuth(ctx context.Context, options authOptions) (authDocument, *setupclient.Failure) {
	var password, ca []byte
	if options.operation == "login" {
		var err error
		password, err = secretfile.Read(options.passwordFile, 512)
		if err != nil || len(password) == 0 || !utf8.Valid(password) {
			return authDocument{}, authLocalFailure("invalid_password_file", "password file is invalid", setupclient.KindInvalid)
		}
		if options.caProvided {
			ca, err = secretfile.ReadCertificate(options.caFile)
			if err != nil || len(ca) == 0 {
				return authDocument{}, authLocalFailure("invalid_ca_file", "CA bundle file is invalid", setupclient.KindInvalid)
			}
		}
	}
	locked, err := cliprofile.Open(ctx, options.profile, options.operation == "login")
	if err != nil {
		return authDocument{}, authProfileFailure(err, false)
	}
	defer locked.Close()
	record, exists, err := locked.Load()
	if err != nil {
		return authDocument{}, authProfileFailure(err, false)
	}
	if !exists {
		if options.operation != "login" {
			return authDocument{}, authLocalFailure("profile_unavailable", "profile is unavailable", setupclient.KindInvalid)
		}
		record = cliprofile.Record{SchemaVersion: 1, BackendURL: options.backend, CAPEM: string(ca)}
	} else if options.operation == "login" {
		// An existing profile pins both origin and trust. Omitted --ca-file uses
		// that pin; an explicit different bundle never silently changes it.
		if record.BackendURL != options.backend || (options.caProvided && record.CAPEM != string(ca)) {
			return authDocument{}, authLocalFailure("profile_binding_conflict", "profile origin or CA binding differs; select a different profile explicitly", setupclient.KindConflict)
		}
	}
	client, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: record.BackendURL, CABundle: []byte(record.CAPEM), Session: record.Session})
	if err != nil {
		return authDocument{}, authClientFailure(err)
	}
	defer client.Close()
	// Establish the protected destination before sending a login. Later saves
	// remain fallible and cannot turn remote success into reported CLI success.
	if !exists {
		if err := locked.Save(record); err != nil {
			return authDocument{}, authProfileFailure(err, false)
		}
	}
	var identity *api.SafeIdentity
	switch options.operation {
	case "login":
		identity, err = client.Login(ctx, options.login, string(password))
	case "context":
		identity, err = client.Context(ctx)
	case "logout":
		err = client.Logout(ctx)
	}
	mutationPossible := options.operation != "context" && err == nil
	if failure, ok := errors.AsType[*setupclient.Failure](err); ok && failure.OutcomeUnknown {
		mutationPossible = true
	}
	if record.Session != client.Session() {
		record.Session = client.Session()
		if saveErr := locked.Save(record); saveErr != nil {
			return authDocument{}, authProfileFailure(saveErr, mutationPossible)
		}
	}
	if err != nil {
		return authDocument{}, authClientFailure(err)
	}
	if err := locked.Close(); err != nil {
		return authDocument{}, authProfileFailure(err, options.operation != "context")
	}
	state := "anonymous"
	if identity != nil {
		state = "authenticated"
		if identity.Restricted {
			state = "restricted"
		}
	}
	return authDocument{SchemaVersion: 1, SessionState: state, Identity: identity}, nil
}

func authLocalFailure(code, message string, kind setupclient.Kind) *setupclient.Failure {
	return &setupclient.Failure{Kind: kind, Code: code, Message: message, SessionState: "unchecked"}
}

func authProfileFailure(err error, remoteMutation bool) *setupclient.Failure {
	f := authLocalFailure("profile_unavailable", "protected profile could not be read or saved", setupclient.KindRemote)
	if errors.Is(err, cliprofile.ErrInvalid) {
		f.Kind = setupclient.KindInvalid
		f.Code = "invalid_profile"
		f.Message = "profile file or its directory is invalid"
	}
	if errors.Is(err, cliprofile.ErrUnsupported) {
		f.Kind = setupclient.KindInvalid
		f.Code = "profile_unsupported"
		f.Message = "protected profiles are unsupported on this platform"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		f.Kind = setupclient.KindCancelled
		f.Code = "cancelled"
		f.Message = "authentication was cancelled or timed out"
	}
	if errors.Is(err, cliprofile.ErrCommitUncertain) {
		f.OutcomeUnknown = true
		f.Message = "local profile write could not be confirmed; inspect the profile before continuing"
	}
	if remoteMutation {
		f.OutcomeUnknown = true
		f.Message = "profile persistence could not be confirmed; authentication may have completed; inspect the profile and session before retrying"
	}
	f.SessionState = "unavailable"
	return f
}

func authClientFailure(err error) *setupclient.Failure {
	if f, ok := errors.AsType[*setupclient.Failure](err); ok {
		return f
	}
	return authLocalFailure("authentication_failed", "authentication failed", setupclient.KindRemote)
}

func writeAuthFailure(stdout, stderr io.Writer, jsonOut bool, operation string, f *setupclient.Failure) int {
	exit := 1
	switch f.Kind {
	case setupclient.KindInvalid:
		exit = 2
	case setupclient.KindAuthentication:
		exit = 3
	case setupclient.KindConflict:
		exit = 5
	case setupclient.KindCancelled:
		exit = 6
	}
	if jsonOut {
		doc := setupErrorDocument{SchemaVersion: 1, Operation: operation, Error: setupError{Code: f.Code, Message: f.Message, OutcomeUnknown: f.OutcomeUnknown, SessionState: f.SessionState, RequestID: f.RequestID, RetryAfterSeconds: f.RetryAfterSeconds}}
		if json.MarshalWrite(stdout, doc) != nil {
			return authOutputFailure(stderr)
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return authOutputFailure(stderr)
		}
	} else if _, err := fmt.Fprintln(stderr, operation+":", f.Message); err != nil {
		return authOutputFailure(stderr)
	}
	return exit
}

func authOutputFailure(stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, "auth: output failed")
	return 1
}
