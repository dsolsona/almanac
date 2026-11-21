package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/datasourceclient"
	"github.com/dsolsona/almanac/internal/setupclient"
)

func backfillCommand(parent context.Context, args []string, stdout, stderr io.Writer) int {
	operation := "backfills"
	jsonOut := requestedJSON(args)
	fail := func(err error) int { return writeDatasourceFailure(stdout, stderr, operation, jsonOut, err) }
	if len(args) == 0 {
		return fail(datasourceInvalid("choose list, preview, request or cancel"))
	}
	action := args[0]
	operation += " " + action
	fs := flag.NewFlagSet(operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile-file", "", "protected authenticated profile")
	sliID := fs.String("sli-id", "", "SLI UUID")
	output := fs.String("output", "text", "text or json")
	timeout := fs.Duration("timeout", 30*time.Second, "request deadline")
	var start, end, key, job string
	var retryExhausted bool
	switch action {
	case "list":
	case "preview", "request":
		fs.StringVar(&start, "start", "", "inclusive UTC RFC3339 bound")
		fs.StringVar(&end, "end", "", "exclusive UTC RFC3339 bound")
		fs.BoolVar(&retryExhausted, "retry-exhausted", false, "include previously failed intervals (administrator only)")
		fs.StringVar(&key, "idempotency-key", "", "caller-owned request key")
	case "cancel":
		fs.StringVar(&job, "job-id", "", "backfill job UUID")
	default:
		return fail(datasourceInvalid("unsupported backfill command"))
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *profile == "" || !validCLIUUID(*sliID) ||
		(*output != "text" && *output != "json") || *timeout <= 0 || *timeout > 2*time.Minute {
		return fail(datasourceInvalid("provide a profile, SLI UUID, valid output and positive deadline"))
	}
	var input api.SliBackfillRequest
	if action == "preview" || action == "request" {
		input.RetryExhausted = new(retryExhausted)
		var startErr, endErr error
		input.Start, startErr = time.Parse(time.RFC3339, start)
		input.End, endErr = time.Parse(time.RFC3339, end)
		if startErr != nil || endErr != nil || !input.Start.Before(input.End) || !validKey(key) {
			return fail(datasourceInvalid("provide aligned UTC start and end dates and an idempotency key"))
		}
	}
	if action == "cancel" && !validCLIUUID(job) {
		return fail(datasourceInvalid("provide a canonical job UUID"))
	}

	signals, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signals, *timeout)
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
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: record.BackendURL, CABundle: []byte(record.CAPEM), Session: record.Session})
	if err != nil {
		return fail(maintenanceSetupFailure(err))
	}
	defer session.Close()
	ops, editor, _, err := session.DatasourceContext(ctx)
	if session.Session() != record.Session {
		record.Session = session.Session()
		if err := locked.Save(record); err != nil {
			return fail(datasourceProfileFailure(err))
		}
	}
	if err != nil {
		return fail(maintenanceSetupFailure(err))
	}
	result, err := datasourceclient.New(ops, editor).Backfill(ctx, action, *sliID, job, key, input)
	if err != nil {
		return fail(err)
	}
	if jsonOut {
		return writeDatasourceSuccess(stdout, stderr, true, operation, result)
	}
	if _, err := fmt.Fprintln(stdout, string(result)); err != nil {
		return 1
	}
	return 0
}
