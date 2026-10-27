package command

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/datasourceclient"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
)

const maxDatasourceInputBytes = 128 << 10

type datasourceDocument struct {
	SchemaVersion int            `json:"schema_version"`
	Operation     string         `json:"operation"`
	Result        jsontext.Value `json:"result,omitempty"`
	Error         *setupError    `json:"error,omitempty"`
}

func datasourceCommand(parent context.Context, args []string, stdout, stderr io.Writer) int {
	operation, rest := datasourceOperation(args)
	if operation == "" {
		return writeDatasourceFailure(stdout, stderr, "datasources", requestedJSON(args), datasourceInvalid("choose a supported datasource command"))
	}
	jsonOut := requestedJSON(rest)
	var profile, id, agentID, inputFile, etag, key, query, cursor, output string
	var limit int
	var timeout time.Duration
	var wait, cancelOnTimeout bool
	fs := flag.NewFlagSet(operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&profile, "profile-file", "", "protected profile")
	fs.StringVar(&id, "datasource-id", "", "canonical datasource UUID")
	fs.StringVar(&agentID, "agent-id", "", "canonical agent UUID")
	fs.StringVar(&inputFile, "input-file", "", "protected JSON input file, or - for stdin")
	fs.StringVar(&etag, "if-match", "", "exact strong datasource ETag")
	fs.StringVar(&key, "idempotency-key", "", "caller-owned operation key")
	fs.StringVar(&query, "query", "", "PromQL query to preview")
	fs.StringVar(&cursor, "cursor", "", "opaque page cursor")
	fs.IntVar(&limit, "limit", 50, "page size from 1 to 200")
	fs.DurationVar(&timeout, "timeout", 30*time.Second, "whole operation deadline")
	fs.BoolVar(&wait, "wait", false, "wait for a diagnostic result")
	fs.BoolVar(&cancelOnTimeout, "cancel-on-timeout", false, "request diagnostic cancellation when waiting reaches its deadline")
	fs.StringVar(&output, "output", "text", "text or json")
	if fs.Parse(rest) != nil || fs.NArg() != 0 {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("invalid datasource command flags"))
	}
	jsonOut = jsonOut || output == "json"
	if output != "text" && output != "json" {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("output must be text or json"))
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	allowed := map[string]bool{"profile-file": true, "timeout": true, "output": true}
	switch operation {
	case "datasources list":
		allowed["limit"], allowed["cursor"] = true, true
	case "datasources get":
		allowed["datasource-id"] = true
	case "datasources create":
		allowed["input-file"], allowed["idempotency-key"] = true, true
	case "datasources update":
		allowed["datasource-id"], allowed["input-file"], allowed["if-match"] = true, true, true
	case "datasources test":
		allowed["input-file"], allowed["datasource-id"], allowed["if-match"], allowed["idempotency-key"] = true, true, true, true
		allowed["wait"], allowed["cancel-on-timeout"] = true, true
	case "slis test":
		allowed["datasource-id"], allowed["query"], allowed["wait"], allowed["cancel-on-timeout"] = true, true, true, true
	case "providers list":
		allowed["agent-id"] = true
	}
	for name := range seen {
		if !allowed[name] {
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("a flag is not valid for this datasource operation"))
		}
	}
	if profile == "" || timeout <= 0 || timeout > 2*time.Minute {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("profile-file is required and timeout must be positive and at most 2m"))
	}
	if (operation == "datasources get" || operation == "datasources update" || operation == "slis test" || operation == "datasources test" && seen["datasource-id"]) && (!seen["datasource-id"] || !validCLIUUID(id)) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("datasource-id must be a canonical UUID"))
	}
	if operation == "datasources list" && (limit < 1 || limit > 200 || len(cursor) > 512 || !printable(cursor)) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("pagination options are invalid"))
	}
	if operation == "datasources create" && (!seen["input-file"] || inputFile == "" || !seen["idempotency-key"] || !validKey(key)) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("input-file and a valid idempotency-key are required"))
	}
	if operation == "datasources update" && (!seen["input-file"] || inputFile == "" || !seen["if-match"] || !validDatasourceETag(etag, id)) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("input-file and the exact datasource if-match ETag are required"))
	}
	if operation == "datasources test" {
		if err := validateDatasourceTestArgs(seen, id, inputFile, etag, key); err != nil {
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, err)
		}
	}
	if operation == "slis test" && (!seen["query"] || strings.TrimSpace(query) == "") {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("query is required"))
	}
	if operation == "providers list" && seen["agent-id"] && !validCLIUUID(agentID) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("agent-id must be a canonical UUID"))
	}
	if cancelOnTimeout && !wait {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("cancel-on-timeout requires --wait"))
	}
	signalCtx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, timeout)
	defer cancel()
	result, err := performDatasource(ctx, operation, profile, id, agentID, inputFile, etag, key, query, limit, cursor, wait, cancelOnTimeout, timeout)
	if err != nil {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, err)
	}
	return writeDatasourceSuccess(stdout, stderr, jsonOut, operation, result)
}

func datasourceOperation(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	if args[0] == "datasources" {
		if len(args) < 2 {
			return "", args
		}
		switch args[1] {
		case "list", "get", "create", "update", "test":
			return "datasources " + args[1], args[2:]
		}
	}
	if args[0] == "providers" && len(args) > 1 && args[1] == "list" {
		return "providers list", args[2:]
	}
	if args[0] == "slis" && len(args) > 1 && args[1] == "test" {
		return "slis test", args[2:]
	}
	return "", args
}

func performDatasource(parent context.Context, operation, profile, id, agentID, inputFile, etag, key, query string, limit int, cursor string, wait, cancelOnTimeout bool, timeout time.Duration) (jsontext.Value, error) {
	var input []byte
	if inputFile != "" {
		var err error
		if inputFile == "-" {
			input, err = io.ReadAll(io.LimitReader(os.Stdin, maxDatasourceInputBytes+1))
			if len(input) > maxDatasourceInputBytes {
				err = secretfile.ErrInvalid
			}
		} else {
			input, err = secretfile.Read(inputFile, maxDatasourceInputBytes)
		}
		if err != nil {
			return nil, datasourceInvalid("protected datasource input is invalid")
		}
	}
	locked, err := cliprofile.Open(parent, profile, false)
	if err != nil {
		return nil, datasourceProfileFailure(err)
	}
	defer locked.Close()
	record, exists, err := locked.Load()
	if err != nil || !exists {
		return nil, datasourceProfileFailure(cliprofile.ErrUnavailable)
	}
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: record.BackendURL, CABundle: []byte(record.CAPEM), Session: record.Session})
	if err != nil {
		return nil, datasourceSetupFailure(err)
	}
	defer session.Close()
	ops, editor, identity, err := session.DatasourceContext(parent)
	if session.Session() != record.Session {
		record.Session = session.Session()
		if saveErr := locked.Save(record); saveErr != nil {
			return nil, datasourceProfileFailure(saveErr)
		}
	}
	if err != nil {
		return nil, datasourceSetupFailure(err)
	}
	client := datasourceclient.New(ops, editor)
	var data []byte
	switch operation {
	case "datasources list":
		data, err = client.List(parent, limit, cursor)
	case "providers list":
		data, err = client.Providers(parent, agentID)
	case "datasources get":
		var tag string
		data, tag, err = client.Get(parent, id)
		if err == nil {
			data, err = json.Marshal(struct {
				Datasource jsontext.Value `json:"datasource"`
				ETag       string         `json:"etag"`
			}{jsontext.Value(data), tag})
		}
	case "datasources create", "datasources update":
		if identity.Role != "administrator" {
			return nil, datasourceForbidden()
		}
		var value api.DatasourceWrite
		if json.Unmarshal(input, &value) != nil || !validDatasourceWriteInput(input) {
			clear(input)
			return nil, datasourceInvalid("datasource JSON is invalid")
		}
		clear(input)
		if operation == "datasources create" {
			data, err = client.Create(parent, value, key)
		} else {
			data, err = client.Update(parent, id, etag, value)
		}
	case "datasources test":
		if identity.Role != "administrator" {
			return nil, datasourceForbidden()
		}
		if id != "" {
			data, err = client.TestSaved(parent, id, etag, key)
		} else {
			var value api.ConnectionTestRequest
			if json.Unmarshal(input, &value) != nil || !validConnectionTestInput(input) {
				clear(input)
				return nil, datasourceInvalid("connection test JSON is invalid")
			}
			data, err = client.Test(parent, value)
		}
		clear(input)
	case "slis test":
		if identity.Role != "administrator" && identity.Role != "editor" {
			return nil, datasourceForbidden()
		}
		data, err = client.Preview(parent, id, query)
	}
	if err != nil {
		return nil, datasourceClientFailure(err)
	}
	if operation == "slis test" || operation == "datasources test" {
		var diagnostic api.Diagnostic
		if json.Unmarshal(data, &diagnostic) != nil {
			return nil, datasourceClientFailure(errors.New("invalid diagnostic"))
		}
		if wait {
			deadline := time.Now().Add(timeout)
			if diagnostic.DeadlineAt.Before(deadline) {
				deadline = diagnostic.DeadlineAt
			}
			result, waitErr := waitForDatasourceDiagnosticResult(parent, diagnostic, deadline, cancelOnTimeout,
				func(ctx context.Context, id string) ([]byte, error) { return client.Diagnostic(ctx, id) },
				func(ctx context.Context, id string) { cancelDatasourceDiagnostic(ctx, client, id) })
			if waitErr != nil {
				return nil, waitErr
			}
			data = result
		}
	}
	if err := locked.Close(); err != nil {
		return nil, datasourceProfileFailure(err)
	}
	return jsontext.Value(data), nil
}

func waitForDatasourceDiagnosticResult(parent context.Context, diagnostic api.Diagnostic, deadline time.Time, cancelOnTimeout bool,
	poll func(context.Context, string) ([]byte, error), cancel func(context.Context, string)) (jsontext.Value, error) {
	final, err := waitForDatasourceDiagnostic(parent, diagnostic, deadline, cancelOnTimeout, poll, cancel)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(final)
	if err != nil {
		return nil, datasourceClientFailure(errors.New("invalid diagnostic"))
	}
	return jsontext.Value(data), nil
}

func waitForDatasourceDiagnostic(parent context.Context, diagnostic api.Diagnostic, deadline time.Time, cancelOnTimeout bool,
	poll func(context.Context, string) ([]byte, error), cancel func(context.Context, string)) (api.Diagnostic, error) {
	id := datasourceUUID(diagnostic.Id)
	for !datasourceTerminal(diagnostic.State) {
		deadlineExpired := errors.Is(parent.Err(), context.DeadlineExceeded) || !time.Now().Before(deadline)
		if deadlineExpired || parent.Err() != nil {
			if parent.Err() != nil && !errors.Is(parent.Err(), context.DeadlineExceeded) {
				return api.Diagnostic{}, &datasourceclient.Failure{Code: "request_canceled", Message: "waiting for the diagnostic was interrupted; the remote diagnostic may still be running"}
			}
			if cancelOnTimeout && deadlineExpired {
				cancel(parent, id)
				return api.Diagnostic{}, &datasourceclient.Failure{Code: "request_timeout", Message: "diagnostic did not finish before the CLI deadline; a cancellation was requested and its final state may still be pending"}
			}
			return api.Diagnostic{}, &datasourceclient.Failure{Code: "request_timeout", Message: "diagnostic did not finish before the CLI deadline; inspect the datasource and retry deliberately"}
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-parent.Done():
			timer.Stop()
			if errors.Is(parent.Err(), context.DeadlineExceeded) {
				message := "diagnostic polling reached its deadline; inspect its status before retrying"
				if cancelOnTimeout {
					cancel(parent, id)
					message = "diagnostic polling reached its deadline; a cancellation was requested and its final state may still be pending"
				}
				return api.Diagnostic{}, &datasourceclient.Failure{Code: "request_timeout", Message: message}
			}
			return api.Diagnostic{}, &datasourceclient.Failure{Code: "request_canceled", Message: "waiting for the diagnostic was interrupted; the remote diagnostic may still be running"}
		case <-timer.C:
		}
		data, err := poll(parent, id)
		if err != nil {
			if cancelOnTimeout && errors.Is(parent.Err(), context.DeadlineExceeded) {
				cancel(parent, id)
				return api.Diagnostic{}, &datasourceclient.Failure{Code: "request_timeout", Message: "diagnostic polling reached its deadline; a cancellation was requested and its final state may still be pending"}
			}
			return api.Diagnostic{}, datasourceClientFailure(err)
		}
		if json.Unmarshal(data, &diagnostic) != nil {
			return api.Diagnostic{}, datasourceClientFailure(errors.New("invalid diagnostic"))
		}
	}
	return diagnostic, nil
}

func cancelDatasourceDiagnostic(ctx context.Context, client *datasourceclient.Client, id string) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, _ = client.Cancel(cancelCtx, id)
}

func datasourceUUID(id any) string {
	data, _ := json.Marshal(id)
	var value string
	_ = json.Unmarshal(data, &value)
	return value
}

func validDatasourceWriteInput(data []byte) bool {
	var value map[string]jsontext.Value
	if json.Unmarshal(data, &value) != nil || !exactFields(value, "name", "display_name", "provider", "configuration_format", "endpoint", "tls", "authentication", "polling_interval_seconds", "agent_id") {
		return false
	}
	var tlsValue, authValue map[string]jsontext.Value
	if json.Unmarshal(value["tls"], &tlsValue) != nil || !exactFields(tlsValue, "insecure_skip_verify", "ca_pem") || json.Unmarshal(value["authentication"], &authValue) != nil || !exactFields(authValue, "mode", "username", "password_secret", "token_secret") {
		return false
	}
	return validSecretActionInput(authValue["password_secret"]) && validSecretActionInput(authValue["token_secret"])
}

func validConnectionTestInput(data []byte) bool {
	var value map[string]jsontext.Value
	if json.Unmarshal(data, &value) != nil || !exactFields(value, "provider", "configuration_format", "endpoint", "tls", "authentication", "agent_id") {
		return false
	}
	var tlsValue, authValue map[string]jsontext.Value
	if json.Unmarshal(value["tls"], &tlsValue) != nil || !exactFields(tlsValue, "insecure_skip_verify", "ca_pem") || json.Unmarshal(value["authentication"], &authValue) != nil || !exactFields(authValue, "mode", "username", "password_secret", "token_secret") {
		return false
	}
	return validSecretActionInput(authValue["password_secret"]) && validSecretActionInput(authValue["token_secret"])
}

func validSecretActionInput(data jsontext.Value) bool {
	var fields map[string]jsontext.Value
	var value api.SecretAction
	if json.Unmarshal(data, &value) != nil || !value.Action.Valid() || json.Unmarshal(data, &fields) != nil {
		return false
	}
	if value.Action == api.Replace {
		return exactFields(fields, "action", "value") && value.Value != nil && *value.Value != ""
	}
	return exactFields(fields, "action") && value.Value == nil
}

func exactFields(value map[string]jsontext.Value, names ...string) bool {
	if len(value) != len(names) {
		return false
	}
	for _, name := range names {
		if _, exists := value[name]; !exists {
			return false
		}
	}
	return true
}

func datasourceTerminal(state api.DiagnosticState) bool {
	return state == api.DiagnosticStateSucceeded || state == api.DiagnosticStateFailed || state == api.DiagnosticStateCanceled || state == api.DiagnosticStateTimedOut || state == api.DiagnosticStateUnknown
}
func validDatasourceETag(etag, id string) bool {
	prefix := "\"datasource:" + id + ":"
	if !strings.HasPrefix(etag, prefix) || !strings.HasSuffix(etag, "\"") {
		return false
	}
	version := strings.TrimSuffix(strings.TrimPrefix(etag, prefix), "\"")
	if !validPositiveDecimal(version) {
		return false
	}
	parsed, err := strconv.ParseInt(version, 10, 64)
	return err == nil && parsed > 0
}

func validateDatasourceTestArgs(seen map[string]bool, id, inputFile, etag, key string) error {
	if seen["datasource-id"] {
		if seen["input-file"] || !seen["if-match"] || !validDatasourceETag(etag, id) || !seen["idempotency-key"] || !validKey(key) {
			return datasourceInvalid("saved connection tests require datasource-id, its exact if-match ETag, and an idempotency-key; do not provide input-file")
		}
		return nil
	}
	if !seen["input-file"] || inputFile == "" || seen["if-match"] || seen["idempotency-key"] {
		return datasourceInvalid("unsaved connection tests require input-file and cannot use saved datasource version or idempotency flags")
	}
	return nil
}

func validPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func writeDatasourceSuccess(stdout, stderr io.Writer, jsonOut bool, operation string, result jsontext.Value) int {
	if jsonOut {
		if json.MarshalWrite(stdout, datasourceDocument{SchemaVersion: 1, Operation: operation, Result: result}) != nil || writeNewline(stdout) != nil {
			fmt.Fprintln(stderr, operation+": output failed")
			return 1
		}
		return 0
	}
	var value map[string]jsontext.Value
	if json.Unmarshal(result, &value) == nil {
		if raw, ok := value["items"]; ok {
			var items []map[string]jsontext.Value
			_ = json.Unmarshal(raw, &items)
			if len(items) == 0 {
				fmt.Fprintln(stdout, "No datasources or providers found.")
				return 0
			}
			for _, item := range items {
				var name, display, provider, availability, endpoint string
				_ = json.Unmarshal(item["name"], &name)
				_ = json.Unmarshal(item["display_name"], &display)
				_ = json.Unmarshal(item["provider"], &provider)
				_ = json.Unmarshal(item["availability"], &availability)
				_ = json.Unmarshal(item["endpoint"], &endpoint)
				fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", firstNonempty(display, name, provider), endpoint, provider, availability)
			}
			return 0
		}
		var state string
		if raw, ok := value["state"]; ok && json.Unmarshal(raw, &state) == nil {
			fmt.Fprintf(stdout, "Diagnostic status: %s\n", strings.ReplaceAll(state, "_", " "))
			return 0
		}
		if raw, ok := value["datasource"]; ok {
			result = jsontext.Value(raw)
		}
	}
	fmt.Fprintln(stdout, string(result))
	return 0
}
func writeDatasourceFailure(stdout, stderr io.Writer, operation string, jsonOut bool, err error) int {
	code, message := "management_unavailable", "datasource management is temporarily unavailable"
	requestID, backendCode, httpStatus := "", "", 0
	if failure, ok := errors.AsType[*datasourceclient.Failure](err); ok {
		code, message = failure.Code, failure.Message
		requestID, backendCode, httpStatus = failure.RequestID, failure.BackendCode, failure.Status
	}
	if failure, ok := errors.AsType[*setupclient.Failure](err); ok {
		code, message = failure.Code, failure.Message
		requestID = failure.RequestID
	}
	if failure, ok := errors.AsType[*datasourceCommandFailure](err); ok {
		code, message = failure.code, failure.message
	}
	exit := 1
	if code == "invalid_request" || code == "invalid_input" || code == "invalid_datasource_input" || code == "invalid_agent" || code == "configuration_too_large" {
		exit = 2
	} else if code == "unauthenticated" || code == "unauthorized" || code == "forbidden" {
		exit = 3
	} else if code == "not_found" {
		exit = 4
	} else if code == "conflict" {
		exit = 5
	} else if code == "request_timeout" || code == "request_canceled" {
		exit = 6
	}
	unknown := false
	if failure, ok := errors.AsType[*datasourceclient.Failure](err); ok {
		unknown = failure.Unknown
	}
	if jsonOut {
		doc := datasourceDocument{SchemaVersion: 1, Operation: operation, Error: &setupError{Code: code, BackendCode: backendCode, Message: message, OutcomeUnknown: unknown, SessionState: "unchecked", RequestID: requestID, HTTPStatus: httpStatus}}
		if json.MarshalWrite(stdout, doc) != nil || writeNewline(stdout) != nil {
			fmt.Fprintln(stderr, operation+": output failed")
			return 1
		}
		return exit
	}
	fmt.Fprintf(stderr, "%s: %s\n", operation, message)
	return exit
}

type datasourceCommandFailure struct{ code, message string }

func (f *datasourceCommandFailure) Error() string { return f.message }
func datasourceInvalid(message string) error {
	return &datasourceCommandFailure{code: "invalid_request", message: message}
}
func datasourceForbidden() error {
	return &datasourceCommandFailure{code: "forbidden", message: "the current role is not allowed to perform this operation"}
}
func datasourceProfileFailure(_ error) error {
	return &datasourceCommandFailure{code: "profile_unavailable", message: "the protected CLI profile is unavailable"}
}
func datasourceSetupFailure(err error) error {
	if failure, ok := errors.AsType[*setupclient.Failure](err); ok {
		return failure
	}
	return &datasourceCommandFailure{code: "management_unavailable", message: "datasource management is temporarily unavailable"}
}
func datasourceClientFailure(err error) error {
	if failure, ok := errors.AsType[*datasourceclient.Failure](err); ok {
		return failure
	}
	return &datasourceCommandFailure{code: "management_unavailable", message: "datasource management is temporarily unavailable"}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "—"
}
