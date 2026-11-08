package command

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
)

type userOptions struct {
	group, operation, profile, id, input, etag, key, linkFile, passwordFile, output, query, status, role, cursor string
	limit                                                                                                        int
	timeout                                                                                                      time.Duration
	confirm                                                                                                      bool
}

func usersCommand(parent context.Context, group string, args []string, stdout, stderr io.Writer) int {
	options := userOptions{group: group}
	op := group
	jsonOut := requestedJSON(args)
	fail := func(err error) int {
		failure, ok := errors.AsType[*setupclient.Failure](err)
		if !ok {
			failure = authProfileFailure(err, false)
		}
		return writeAuthFailure(stdout, stderr, jsonOut, op, failure)
	}
	invalid := func(message string) int {
		return fail(authLocalFailure("invalid_request", message, setupclient.KindInvalid))
	}
	operations := []string{"list", "get", "create", "update", "disable", "enable", "delete", "reset-password", "cancel-password-reset", "replace-setup-link"}
	if group == "account" {
		operations = []string{"get", "update", "password", "redeem"}
	}
	if len(args) == 0 || !slices.Contains(operations, args[0]) {
		return invalid("choose a supported " + group + " operation")
	}
	options.operation = args[0]
	op += " " + options.operation
	fs := flag.NewFlagSet(op, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&options.profile, "profile-file", "", "protected session profile")
	fs.StringVar(&options.output, "output", "text", "text or json")
	fs.DurationVar(&options.timeout, "timeout", 30*time.Second, "whole command deadline")
	mutation := !slices.Contains([]string{"list", "get", "password", "redeem"}, options.operation)
	if group == "users" && options.operation != "list" && options.operation != "create" {
		fs.StringVar(&options.id, "user-id", "", "canonical user UUID")
	}
	if mutation {
		fs.StringVar(&options.key, "idempotency-key", "", "caller-owned operation key")
		if options.operation != "create" {
			fs.StringVar(&options.etag, "if-match", "", "exact user ETag")
		}
		if options.operation == "create" || options.operation == "update" {
			fs.StringVar(&options.input, "input-file", "", "protected JSON input")
		}
		if options.operation == "create" || options.operation == "reset-password" || options.operation == "replace-setup-link" {
			fs.StringVar(&options.linkFile, "link-file", "", "new protected one-time link file")
		}
		if options.operation != "create" && options.operation != "update" {
			fs.BoolVar(&options.confirm, "confirm", false, "confirm access change")
		}
	}
	if options.operation == "list" {
		fs.IntVar(&options.limit, "limit", 50, "1 through 200 rows")
		fs.StringVar(&options.cursor, "cursor", "", "opaque cursor from the previous page")
		fs.StringVar(&options.query, "query", "", "search full name or email")
		fs.StringVar(&options.status, "status", "", "active, pending, disabled or resetting")
		fs.StringVar(&options.role, "role", "", "viewer, editor or administrator")
	}
	if options.operation == "password" || options.operation == "redeem" {
		fs.StringVar(&options.passwordFile, "password-file", "", "protected new password file")
	}
	if options.operation == "redeem" {
		fs.StringVar(&options.linkFile, "link-file", "", "protected setup or reset link file")
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return invalid("invalid account or user command flags")
	}
	jsonOut = jsonOut || options.output == "json"
	if options.profile == "" || options.timeout <= 0 || options.timeout > 2*time.Minute || !slices.Contains([]string{"text", "json"}, options.output) {
		return invalid("profile-file, valid output and a timeout of at most 2m are required")
	}
	if group == "users" && !slices.Contains([]string{"create", "list"}, options.operation) && !validCLIUUID(options.id) {
		return invalid("user-id must be a canonical UUID")
	}
	if options.operation == "list" && (options.limit < 1 || options.limit > 200 || len(options.cursor) > 2048 || len(options.query) > 1024 || !slices.Contains([]string{"", "active", "pending", "disabled", "resetting"}, options.status) || !slices.Contains([]string{"", "viewer", "editor", "administrator"}, options.role)) {
		return invalid("invalid user pagination or filters")
	}
	if mutation {
		if !validKey(options.key) {
			return invalid("a valid idempotency-key is required")
		}
		if options.operation != "create" && !validUserETag(options.etag, options.id) {
			return invalid("the exact user if-match ETag is required")
		}
		if (options.operation == "create" || options.operation == "update") && options.input == "" {
			return invalid("input-file is required")
		}
		if slices.Contains([]string{"create", "reset-password", "replace-setup-link"}, options.operation) && options.linkFile == "" {
			return invalid("a new protected link-file is required before issuing a one-time link")
		}
		if options.operation != "create" && options.operation != "update" && !options.confirm {
			return invalid("access changes require --confirm")
		}
	}
	if (options.operation == "password" || options.operation == "redeem") && options.passwordFile == "" || options.operation == "redeem" && options.linkFile == "" {
		return invalid("protected password-file and, for redemption, link-file are required")
	}
	signalCtx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, options.timeout)
	defer cancel()
	result, err := performUser(ctx, options)
	if err != nil {
		return fail(err)
	}
	if jsonOut {
		if json.MarshalWrite(stdout, datasourceDocument{SchemaVersion: 1, Operation: op, Result: result}) != nil || writeNewline(stdout) != nil {
			return authOutputFailure(stderr)
		}
	} else if _, err := fmt.Fprintf(stdout, "%s completed\n%s\n", op, result); err != nil {
		return authOutputFailure(stderr)
	}
	return 0
}

func validUserETag(tag, id string) bool {
	value, ok := strings.CutPrefix(tag, `"user-`)
	if !ok || len(value) < 39 || value[36] != '-' || !validCLIUUID(value[:36]) || id != "" && value[:36] != id {
		return false
	}
	version, ok := strings.CutSuffix(value[37:], `"`)
	n, err := strconv.ParseInt(version, 10, 64)
	return ok && err == nil && n > 0 && strconv.FormatInt(n, 10) == version
}

func performUser(ctx context.Context, o userOptions) (jsontext.Value, error) {
	var sink *secretfile.TokenFile
	if slices.Contains([]string{"create", "reset-password", "replace-setup-link"}, o.operation) {
		var err error
		sink, err = secretfile.Reserve(ctx, o.linkFile)
		if err != nil {
			return nil, authLocalFailure("invalid_request", "link-file must be a new file in a protected directory", setupclient.KindInvalid)
		}
		defer sink.Close()
	}
	var input api.UserWriteRequest
	if o.input != "" {
		data, err := secretfile.Read(o.input, 16<<10)
		if err != nil {
			return nil, authLocalFailure("invalid_request", "protected input-file could not be read", setupclient.KindInvalid)
		}
		defer clear(data)
		if o.group == "account" {
			var profile api.AccountUpdateRequest
			err = json.Unmarshal(data, &profile, json.RejectUnknownMembers(true))
			input.FullName = profile.FullName
		} else {
			err = json.Unmarshal(data, &input, json.RejectUnknownMembers(true))
		}
		if err != nil || input.FullName == "" {
			return nil, authLocalFailure("invalid_request", "input JSON must contain the supported profile fields", setupclient.KindInvalid)
		}
	}
	locked, err := cliprofile.Open(ctx, o.profile, false)
	if err != nil {
		return nil, authProfileFailure(err, false)
	}
	defer locked.Close()
	record, exists, err := locked.Load()
	if err != nil || !exists {
		return nil, authProfileFailure(cliprofile.ErrUnavailable, false)
	}
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: record.BackendURL, CABundle: []byte(record.CAPEM), Session: record.Session})
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var result any
	switch o.operation {
	case "list":
		params := api.ListUsersParams{Limit: &o.limit, Cursor: &o.cursor, Q: &o.query, Status: new(api.ListUsersParamsStatus(o.status)), Role: new(api.UserRole(o.role))}
		result, err = session.Users(ctx, params)
	case "get":
		var user api.ManagedUser
		var tag string
		user, tag, err = session.User(ctx, o.id)
		result = struct {
			User api.ManagedUser `json:"user"`
			ETag string          `json:"etag"`
		}{user, tag}
	case "password", "redeem":
		password, readErr := secretfile.Read(o.passwordFile, 512)
		if readErr != nil {
			return nil, authLocalFailure("invalid_request", "protected password-file could not be read", setupclient.KindInvalid)
		}
		defer clear(password)
		if o.operation == "password" {
			err = session.ChangePassword(ctx, string(password))
		} else {
			link, readErr := secretfile.Read(o.linkFile, 4096)
			if readErr != nil {
				return nil, authLocalFailure("invalid_request", "protected link-file could not be read", setupclient.KindInvalid)
			}
			defer clear(link)
			parsed, parseErr := url.Parse(strings.TrimSpace(string(link)))
			if parseErr != nil || parsed.Scheme+"://"+parsed.Host != record.BackendURL || parsed.Path != "/activate" || parsed.RawQuery != "" || parsed.User != nil {
				return nil, authLocalFailure("invalid_request", "link must belong to this profile's backend", setupclient.KindInvalid)
			}
			err = session.RedeemUser(ctx, parsed.Fragment, string(password))
		}
		result = struct {
			Completed bool `json:"completed"`
		}{err == nil}
	default:
		operation := o.operation
		if o.group == "account" {
			operation = "profile"
		}
		var value api.UserCommandResult
		value, err = session.ChangeUser(ctx, operation, o.id, o.etag, o.key, input)
		if err == nil && value.Link != nil {
			if sink == nil || sink.Complete([]byte(*value.Link+"\n")) != nil {
				return nil, &setupclient.Failure{Kind: setupclient.KindRemote, Code: "secret_output_unavailable", Message: "the command completed but its one-time link could not be saved; inspect the destination and issue a replacement if needed", OutcomeUnknown: true}
			}
		}
		value.Link = nil
		result = value
	}
	if session.Session() != record.Session {
		record.Session = session.Session()
		if saveErr := locked.Save(record); saveErr != nil {
			return nil, authProfileFailure(saveErr, true)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := locked.Close(); err != nil {
		return nil, authProfileFailure(err, false)
	}
	data, err := json.Marshal(result)
	return jsontext.Value(data), err
}
