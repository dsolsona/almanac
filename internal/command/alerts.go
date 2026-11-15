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
	"syscall"
	"time"

	"github.com/dsolsona/almanac/internal/alertclient"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/datasourceclient"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
)

// Preserve alert-specific API codes while retaining the existing CLI conflict
// exit status. Expired command results remain explicit remote failures (exit 1),
// distinct from an unknown command or an uncertain mutation outcome.
func writeAlertFailure(stdout, stderr io.Writer, operation string, jsonOut bool, err error) int {
	f, ok := errors.AsType[*datasourceclient.Failure](err)
	if !ok || f.Unknown || (f.Code != "version_conflict" && f.Code != "name_conflict" && f.Code != "command_body_conflict") {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, err)
	}
	if jsonOut {
		doc := datasourceDocument{SchemaVersion: 1, Operation: operation, Error: &setupError{Code: f.Code, BackendCode: f.BackendCode, Message: f.Message, RequestID: f.RequestID, HTTPStatus: f.Status, SessionState: "unchecked"}}
		if json.MarshalWrite(stdout, doc) != nil || writeNewline(stdout) != nil {
			fmt.Fprintln(stderr, operation+": output failed")
			return 1
		}
	} else if _, err := fmt.Fprintf(stderr, "%s: %s\n", operation, f.Message); err != nil {
		return 1
	}
	return 5
}

func alertsCommand(parent context.Context, kind string, args []string, stdout, stderr io.Writer) int {
	jsonOut := requestedJSON(args)
	operation := kind
	invalid := func(message string) int {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, datasourceInvalid(message))
	}
	if len(args) == 0 {
		return invalid("choose a supported alert command")
	}
	action := args[0]
	operation += " " + action
	in := alertclient.Input{Kind: kind, Action: action}
	var profile, inputFile, output, policyID, alertID, enabled string
	var timeout time.Duration
	fs := flag.NewFlagSet(operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&profile, "profile-file", "", "protected authenticated profile")
	fs.StringVar(&policyID, "policy-id", "", "alert policy UUID")
	fs.StringVar(&alertID, "alert-id", "", "alert occurrence UUID")
	fs.StringVar(&in.SLOID, "slo-id", "", "SLO filter UUID")
	fs.StringVar(&in.ServiceID, "service-id", "", "service filter UUID")
	fs.StringVar(&inputFile, "input-file", "", "protected JSON input or -")
	fs.StringVar(&in.Key, "idempotency-key", "", "original command key")
	fs.StringVar(&in.ETag, "if-match", "", "exact strong resource ETag")
	fs.StringVar(&in.Operation, "operation", "", "original operation for receipt lookup")
	fs.StringVar(&in.Cursor, "cursor", "", "opaque pagination cursor")
	defaultLimit := 20
	if action == "series" {
		defaultLimit = 120
	}
	fs.IntVar(&in.Limit, "limit", defaultLimit, "page size 1..200; series 2..120")
	fs.StringVar(&in.ConditionID, "condition-id", "", "immutable condition ID for series")
	fs.StringVar(&in.Start, "start", "", "inclusive UTC series start")
	fs.StringVar(&in.End, "end", "", "inclusive UTC series end")
	fs.StringVar(&in.Evidence, "evidence", "", "matching, unavailable or waiting evidence")
	fs.StringVar(&in.MinimumDuration, "minimum-duration-seconds", "", "minimum effective duration in exact integer seconds")
	fs.StringVar(&in.Query, "q", "", "literal name search")
	fs.StringVar(&in.Scope, "scope", "", "active, history or all")
	fs.StringVar(&in.State, "state", "", "comma-separated lifecycle states")
	fs.StringVar(&in.Severity, "severity", "", "severity filter (CSV for occurrences)")
	fs.StringVar(&in.GroupBy, "group-by", "", "none or slo")
	fs.StringVar(&in.Sort, "sort", "", "newest or severity")
	fs.StringVar(&enabled, "enabled", "", "true or false policy filter")
	fs.StringVar(&in.Revision, "revision", "", "exact evaluation revision for range history")
	fs.StringVar(&in.Selection, "selection", "", "firing or latest evidence")
	fs.StringVar(&output, "output", "text", "text or json")
	fs.DurationVar(&timeout, "timeout", 30*time.Second, "request deadline (maximum 2m)")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return invalid("invalid command flags")
	}
	jsonOut = jsonOut || output == "json"
	allowed := map[string]bool{"profile-file": true, "output": true, "timeout": true}
	allow := func(names ...string) {
		for _, n := range names {
			allowed[n] = true
		}
	}
	switch operation {
	case "alert-policies list":
		allow("limit", "cursor", "slo-id", "severity", "enabled", "q")
	case "alert-policies create":
		allow("input-file", "idempotency-key")
	case "alert-policies get":
		allow("policy-id")
	case "alert-policies update", "alert-policies enable", "alert-policies disable", "alert-policies retry":
		allow("policy-id", "if-match", "idempotency-key", "input-file")
	case "alert-policies ranges":
		allow("policy-id", "revision", "limit", "cursor")
	case "alerts list":
		allow("limit", "cursor", "slo-id", "service-id", "q", "scope", "state", "severity", "group-by", "sort", "evidence", "minimum-duration-seconds")
	case "alerts get":
		allow("alert-id")
	case "alerts evidence":
		allow("alert-id", "selection")
	case "alerts series":
		allow("alert-id", "condition-id", "start", "end", "limit")
	case "alerts transitions":
		allow("alert-id", "limit", "cursor")
	case "alerts acknowledge":
		allow("alert-id", "if-match", "idempotency-key", "input-file")
	case "alerts processing":
		allow("policy-id")
	case "alerts command-result":
		allow("operation", "idempotency-key")
	default:
		return invalid("choose a supported alert command")
	}
	badFlag := false
	fs.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] || (f.Name == "evidence" || f.Name == "minimum-duration-seconds" || f.Name == "start" || f.Name == "end") && f.Value.String() == "" {
			badFlag = true
		}
	})
	if badFlag {
		return invalid("a flag is not valid for this operation")
	}
	if profile == "" || output != "text" && output != "json" || timeout <= 0 || timeout > 2*time.Minute || in.Limit < 1 || in.Limit > 200 {
		return invalid("invalid profile, output, page size or deadline")
	}
	in.ID = policyID
	if kind == "alerts" && action != "processing" {
		in.ID = alertID
	}
	if enabled != "" {
		if enabled != "true" && enabled != "false" {
			return invalid("enabled must be true or false")
		}
		in.Enabled = new(enabled == "true")
	}
	if (action == "create" || action == "update") && inputFile == "" {
		return invalid("input-file is required")
	}
	// Structural and operation flags are rejected before even reading input/profile.
	if err := alertclient.ValidateOptions(in); err != nil {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, err)
	}

	if inputFile != "" {
		var err error
		if inputFile == "-" {
			in.Body, err = io.ReadAll(io.LimitReader(os.Stdin, alertclient.MaxInputBytes+1))
			if len(in.Body) > alertclient.MaxInputBytes {
				err = secretfile.ErrInvalid
			}
		} else {
			in.Body, err = secretfile.Read(inputFile, alertclient.MaxInputBytes)
		}
		if err != nil {
			clear(in.Body)
			return invalid("could not read protected bounded input")
		}
		defer clear(in.Body)
	}
	if err := alertclient.Validate(in); err != nil {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, err)
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	locked, err := cliprofile.Open(ctx, profile, false)
	if err != nil {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, datasourceProfileFailure(err))
	}
	defer locked.Close()
	record, exists, err := locked.Load()
	if err != nil || !exists {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, datasourceProfileFailure(cliprofile.ErrUnavailable))
	}
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: record.BackendURL, CABundle: []byte(record.CAPEM), Session: record.Session})
	if err != nil {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, datasourceSetupFailure(err))
	}
	defer session.Close()
	ops, editor, identity, err := session.DatasourceContext(ctx)
	if session.Session() != record.Session {
		record.Session = session.Session()
		if saveErr := locked.Save(record); saveErr != nil {
			return writeAlertFailure(stdout, stderr, operation, jsonOut, datasourceProfileFailure(saveErr))
		}
	}
	if err != nil {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, datasourceSetupFailure(err))
	}
	if in.Mutation() && identity.Role != "administrator" && identity.Role != "editor" {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, datasourceForbidden())
	}
	alertOps, ok := ops.(alertclient.Operations)
	if !ok {
		return invalid("alert operations unavailable")
	}
	data, tag, err := alertclient.New(alertOps, editor).Do(ctx, in)
	if err != nil {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, err)
	}
	if err := locked.Close(); err != nil {
		return writeAlertFailure(stdout, stderr, operation, jsonOut, datasourceProfileFailure(err))
	}
	if jsonOut {
		if tag != "" {
			var result map[string]jsontext.Value
			if action == "get" {
				name := "policy"
				if kind == "alerts" {
					name = "alert"
				}
				result = map[string]jsontext.Value{name: data}
			} else if err = json.Unmarshal(data, &result); err != nil {
				return invalid("invalid response")
			}
			result["etag"], err = json.Marshal(tag)
			if err != nil {
				return invalid("invalid response")
			}
			data, err = json.Marshal(result)
			if err != nil {
				return invalid("invalid response")
			}
		}
		return writeDatasourceSuccess(stdout, stderr, true, operation, data)
	}
	return writeAlertText(stdout, stderr, in, data, tag)
}

func writeAlertText(stdout, stderr io.Writer, in alertclient.Input, data []byte, tag string) int {
	// Pretty JSON is the complete text view for immutable evidence/history. Typed
	// summaries for the common list and mutation paths keep the actual exact values.
	var summary string
	switch in.Kind + " " + in.Action {
	case "alert-policies list":
		var p api.AlertPolicyPage
		_ = json.Unmarshal(data, &p)
		summary = "Policies: " + p.Total + "\n"
		for _, v := range p.Items {
			summary += v.Policy.Id.String() + "\t" + strconv.Quote(v.Policy.Name) + "\t" + string(v.Policy.Severity) + "\tenabled=" + strconv.FormatBool(v.Policy.Enabled) + "\tversion=" + v.Policy.Version + "\n"
		}
		if p.NextCursor != nil {
			summary += "Next cursor: " + *p.NextCursor + "\n"
		}
	case "alerts list":
		var p api.AlertPage
		_ = json.Unmarshal(data, &p)
		summary = "Alerts: " + p.Total + "\n"
		for _, group := range p.Groups {
			summary += "SLO " + strconv.Quote(group.SloName) + ": " + group.Total + " alerts\n"
		}
		for _, v := range p.Items {
			summary += v.Alert.Id.String() + "\t" + strconv.Quote(v.PolicyName) + "\t" + string(v.Alert.State) + "\t" + string(v.Evaluation.Truth) + "\tversion=" + v.Alert.Version + "\n"
		}
		if p.NextCursor != nil {
			summary += "Next cursor: " + *p.NextCursor + "\n"
		}
	case "alerts command-result":
		var v api.AlertCommandLookup
		_ = json.Unmarshal(data, &v)
		summary = "Command result: " + string(v.Status) + "\n"
		if v.Result != nil {
			summary += "Original operation: " + string(v.Result.Operation) + " · Request ID: " + v.Result.RequestId + "\n"
			value, err := json.Marshal(v.Result)
			if err != nil {
				return 1
			}
			pretty := jsontext.Value(value)
			if err := pretty.Indent(); err != nil {
				return 1
			}
			summary += string(pretty) + "\n"
		}
	}
	if summary == "" {
		value := jsontext.Value(data)
		if err := value.Indent(); err != nil {
			return 1
		}
		summary = string(value) + "\n"
	}
	if tag != "" {
		summary += "ETag: " + tag + "\n"
	}
	if _, err := io.WriteString(stdout, summary); err != nil {
		fmt.Fprintln(stderr, "alerts: output failed")
		return 1
	}
	return 0
}
