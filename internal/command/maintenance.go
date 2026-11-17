package command

import (
	"context"
	"encoding/json/v2"
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
		return fail(datasourceInvalid("choose list, get, timeline, create, update, end, cancel or withdraw"))
	}
	in := maintenanceclient.Input{Action: args[0]}
	operation += " " + in.Action
	fs := flag.NewFlagSet(operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile-file", "", "protected authenticated profile")
	output := fs.String("output", "text", "text or json")
	timeout := fs.Duration("timeout", 30*time.Second, "request deadline, up to 2m")
	var inputFile, start, end string
	switch in.Action {
	case "list", "timeline":
		fs.IntVar(&in.Limit, "limit", 50, "page size")
		fs.StringVar(&in.Cursor, "cursor", "", "opaque page cursor")
		if in.Action == "timeline" {
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
	if in.Mutation() {
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
		return fail(datasourceSetupFailure(err))
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
		return fail(datasourceSetupFailure(err))
	}
	if in.Mutation() && identity.Role != "administrator" && identity.Role != "editor" {
		return fail(datasourceForbidden())
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
