package command

import (
	"bytes"
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

	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/maintenanceclient"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
)

func maintenanceCommand(parent context.Context, args []string, stdout, stderr io.Writer) int {
	operation, jsonOut := "maintenance", requestedJSON(args)
	fail := func(err error) int { return writeDatasourceFailure(stdout, stderr, operation, jsonOut, err) }
	if len(args) == 0 {
		return fail(datasourceInvalid("choose list, get, preview, impact, timeline, create, update, end, cancel or withdraw"))
	}
	in := maintenanceclient.Input{Action: args[0]}
	operation += " " + in.Action
	fs := flag.NewFlagSet(operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile-file", "", "protected authenticated profile")
	output := fs.String("output", "text", "text or json")
	timeout := fs.Duration("timeout", 30*time.Second, "request deadline, up to 2m")
	var inputFile, start, end string
	if in.Action != "preview" && in.Action != "impact" {
		fs.BoolVar(&in.Scoped, "scoped", false, "use service, individual SLO or all-services maintenance")
	}
	switch in.Action {
	case "list", "timeline", "preview", "impact":
		fs.IntVar(&in.Limit, "limit", 50, "page size")
		fs.StringVar(&in.Cursor, "cursor", "", "opaque page cursor")
		switch in.Action {
		case "preview":
			in.Scoped = true
			fs.StringVar(&inputFile, "input-file", "", "protected scope JSON input or -")
		case "impact":
			in.Scoped = true
			fs.StringVar(&in.ID, "maintenance-id", "", "window UUID")
			fs.StringVar(&in.Revision, "revision", "", "historical revision")
		case "timeline":
			fs.StringVar(&in.ID, "slo-id", "", "SLO UUID")
			fs.StringVar(&start, "start", "", "inclusive UTC RFC3339 bound")
			fs.StringVar(&end, "end", "", "exclusive UTC RFC3339 bound")
		}
	case "get":
		fs.StringVar(&in.ID, "maintenance-id", "", "window UUID")
		fs.StringVar(&in.Revision, "revision", "", "historical revision")
	case "create", "update", "end", "cancel", "withdraw":
		fs.StringVar(&in.Key, "idempotency-key", "", "caller-owned request key")
		fs.StringVar(&inputFile, "input-file", "", "protected JSON input or -")
		if in.Action != "create" {
			fs.StringVar(&in.ID, "maintenance-id", "", "window UUID")
			fs.StringVar(&in.ETag, "if-match", "", "current strong maintenance ETag")
		}
	default:
		return fail(datasourceInvalid("unsupported maintenance command"))
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *profile == "" ||
		(*output != "text" && *output != "json") || *timeout <= 0 || *timeout > 2*time.Minute {
		return fail(datasourceInvalid("invalid flags, profile, output or deadline"))
	}
	jsonOut = jsonOut || *output == "json"
	var err error
	if in.Action == "timeline" {
		in.Start, err = time.Parse(time.RFC3339, start)
		if err != nil {
			return fail(datasourceInvalid("start must be a UTC RFC3339 instant"))
		}
		in.End, err = time.Parse(time.RFC3339, end)
		if err != nil {
			return fail(datasourceInvalid("end must be a UTC RFC3339 instant"))
		}
	}
	if !maintenanceclient.ValidateOptions(in) {
		return fail(datasourceInvalid("invalid maintenance options"))
	}
	if in.Mutation() || in.Action == "preview" {
		if inputFile == "-" {
			in.Body, err = io.ReadAll(io.LimitReader(os.Stdin, maxDatasourceInputBytes+1))
		} else {
			in.Body, err = secretfile.Read(inputFile, maxDatasourceInputBytes)
		}
		defer clear(in.Body)
		if err != nil || len(in.Body) > maxDatasourceInputBytes || !maintenanceclient.ValidBody(in) {
			return fail(datasourceInvalid("provide protected JSON input with the fields required for this command"))
		}
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	locked, err := cliprofile.Open(ctx, *profile, false)
	if err != nil {
		return fail(datasourceProfileFailure(err))
	}
	defer locked.Close()
	record, exists, err := locked.Load()
	if err != nil || !exists {
		return fail(datasourceProfileFailure(cliprofile.ErrUnavailable))
	}
	session, err := setupclient.NewSession(setupclient.SessionOptions{
		BackendURL: record.BackendURL, CABundle: []byte(record.CAPEM), Session: record.Session,
	})
	if err != nil {
		return fail(maintenanceSetupFailure(err))
	}
	defer session.Close()
	ops, editor, identity, err := session.DatasourceContext(ctx)
	if session.Session() != record.Session {
		record.Session = session.Session()
		if err := locked.Save(record); err != nil {
			return fail(datasourceProfileFailure(err))
		}
	}
	if err != nil {
		return fail(maintenanceSetupFailure(err))
	}
	if in.Mutation() && identity.Role != "administrator" && identity.Role != "editor" {
		return fail(datasourceForbidden())
	}
	if in.Scoped {
		client, ok := ops.(maintenanceclient.ScopedOperations)
		if !ok {
			return fail(datasourceInvalid("scoped maintenance operations unavailable"))
		}
		result, err := maintenanceclient.DoScoped(ctx, client, editor, in)
		if err != nil {
			return fail(err)
		}
		if err := locked.Close(); err != nil {
			return fail(datasourceProfileFailure(err))
		}
		return writeScopedMaintenance(stdout, stderr, operation, jsonOut, result)
	}
	client, ok := ops.(maintenanceclient.Operations)
	if !ok {
		return fail(datasourceInvalid("maintenance operations unavailable"))
	}
	result, err := maintenanceclient.Do(ctx, client, editor, in)
	if err != nil {
		return fail(err)
	}
	if err := locked.Close(); err != nil {
		return fail(datasourceProfileFailure(err))
	}
	if jsonOut {
		var output any = result
		if result.Page != nil {
			output = result.Page
		}
		data, err := json.Marshal(output)
		if err != nil {
			return fail(err)
		}
		return writeDatasourceSuccess(stdout, stderr, true, operation, data)
	}

	if result.Page != nil {
		for _, item := range result.Page.Items {
			fmt.Fprintf(stdout, "%s\t%q\t%s\t%s\t%s\n", item.Id, item.Title,
				item.Start.Format(time.RFC3339), item.End.Format(time.RFC3339), item.Operation)
		}
		if result.Page.NextCursor != nil {
			fmt.Fprintf(stdout, "Next cursor: %s\n", *result.Page.NextCursor)
		}
		return 0
	}
	item := result.Window
	fmt.Fprintf(stdout, "%q (%s)\nUTC: %s to %s\nReason: %q\nRevision: %s (%s)\nETag: %s\n",
		item.Title, item.Id, item.Start.Format(time.RFC3339), item.End.Format(time.RFC3339),
		item.Reason, item.Revision, item.Operation, result.ETag)
	for _, id := range item.SloIds {
		fmt.Fprintf(stdout, "SLO: %s\n", id)
	}
	return 0
}

func writeScopedMaintenance(stdout, stderr io.Writer, operation string, jsonOut bool, result maintenanceclient.ScopedResult) int {
	if jsonOut {
		var output any = result
		switch {
		case result.Page != nil:
			output = result.Page
		case result.Impact != nil:
			output = result.Impact
		case result.Timeline != nil:
			output = result.Timeline
		}
		data, err := json.Marshal(output)
		if err != nil {
			return writeDatasourceFailure(stdout, stderr, operation, true, err)
		}
		return writeDatasourceSuccess(stdout, stderr, true, operation, data)
	}
	var output bytes.Buffer
	var cursor *string
	switch {
	case result.Page != nil:
		for _, item := range result.Page.Items {
			kind, _ := item.Scope.Discriminator()
			fmt.Fprintf(&output, "%s\t%q\t%s\t%s\t%s\t%s\n", item.Id, item.Title,
				item.Start.Format(time.RFC3339), item.End.Format(time.RFC3339), item.Operation, kind)
		}
		cursor = result.Page.NextCursor
	case result.Impact != nil:
		countContext := ""
		if operation == "maintenance impact" {
			countContext = "distinct SLOs, "
		}
		fmt.Fprintf(&output, "Affected SLOs: %s (%sinventory at %s)\n", result.Impact.Total, countContext, result.Impact.InventoryAt.Format(time.RFC3339Nano))
		if !result.Impact.Ready {
			fmt.Fprintln(&output, "Effective maintenance membership is being prepared.")
		}
		for _, item := range result.Impact.Items {
			fmt.Fprintf(&output, "%s\t%q\t%s\t%q", item.SloId, item.SloName, item.ServiceId, item.ServiceName)
			if item.Start != nil {
				fmt.Fprintf(&output, "\t%s\t%s", item.Start.Format(time.RFC3339Nano), item.End.Format(time.RFC3339Nano))
			}
			fmt.Fprintln(stdout)
		}
		cursor = result.Impact.NextCursor
	case result.Timeline != nil:
		if result.Timeline.Pending {
			fmt.Fprintln(&output, "Some effective maintenance intervals are being prepared.")
		}
		for _, item := range result.Timeline.Items {
			fmt.Fprintf(&output, "%s\t%q\t%s\t%s\trevision %s\n", item.Window.Id, item.Window.Title,
				item.Start.Format(time.RFC3339Nano), item.End.Format(time.RFC3339Nano), item.Window.Revision)
		}
		cursor = result.Timeline.NextCursor
	default:
		item := result.Window
		fmt.Fprintf(&output, "%q (%s)\nUTC: %s to %s\nReason: %q\nRevision: %s (%s)\nETag: %s\n",
			item.Title, item.Id, item.Start.Format(time.RFC3339), item.End.Format(time.RFC3339),
			item.Reason, item.Revision, item.Operation, result.ETag)
		kind, _ := item.Scope.Discriminator()
		switch kind {
		case "slos":
			scope, _ := item.Scope.AsMaintenanceSloScope()
			for _, id := range scope.SloIds {
				fmt.Fprintf(&output, "SLO: %s\n", id)
			}
		case "services":
			scope, _ := item.Scope.AsMaintenanceServiceScope()
			for _, id := range scope.ServiceIds {
				fmt.Fprintf(&output, "Service: %s\n", id)
			}
		case "all_services":
			fmt.Fprintln(&output, "Scope: All services")
		}
	}
	if cursor != nil {
		fmt.Fprintf(&output, "Next cursor: %s\n", *cursor)
	}
	if _, err := output.WriteTo(stdout); err != nil {
		fmt.Fprintln(stderr, operation+": output failed")
		return 1
	}
	return 0
}

func maintenanceSetupFailure(err error) error {
	if failure, ok := errors.AsType[*setupclient.Failure](err); ok {
		if failure.Code != "unauthenticated" {
			return failure
		}
		adjusted := *failure
		adjusted.Message = "sign in before managing maintenance"
		return &adjusted
	}
	return &datasourceCommandFailure{code: "management_unavailable", message: "maintenance management is temporarily unavailable"}
}
