package command

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
	"github.com/dsolsona/almanac/internal/sloclient"
)

func objectivesCommand(parent context.Context, kind string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeDatasourceFailure(stdout, stderr, kind, requestedJSON(args), datasourceInvalid("choose a supported command"))
	}
	action := args[0]
	operation := kind + " " + action
	rest := args[1:]
	jsonOut := requestedJSON(rest)
	valid := kind == "services" && (action == "list" || action == "get" || action == "create" || action == "update") || kind == "slos" && (action == "list" || action == "get" || action == "create" || action == "update" || action == "evaluate" || action == "overview" || action == "operational")
	if !valid {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("choose a supported command"))
	}
	var profile, id, serviceID, inputFile, etag, key, cursor, output, at, start, end string
	var query, urgency, operationalStart, operationalEnd string
	var limit, burnWindowSeconds int
	var timeout time.Duration
	fs := flag.NewFlagSet(operation, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&profile, "profile-file", "", "protected profile")
	fs.StringVar(&id, kind[:len(kind)-1]+"-id", "", "resource ID")
	if kind == "slos" {
		fs.StringVar(&serviceID, "service-id", "", "service filter")
	}
	fs.StringVar(&inputFile, "input-file", "", "protected JSON input or -")
	fs.StringVar(&etag, "if-match", "", "resource ETag")
	fs.StringVar(&key, "idempotency-key", "", "create key")
	fs.StringVar(&cursor, "cursor", "", "page cursor")
	fs.IntVar(&limit, "limit", 50, "page size")
	fs.StringVar(&output, "output", "text", "text or json")
	fs.DurationVar(&timeout, "timeout", 30*time.Second, "deadline")
	fs.StringVar(&at, "at", "", "evaluation time")
	fs.StringVar(&start, "timeline-start", "", "timeline start")
	fs.StringVar(&end, "timeline-end", "", "timeline end")
	fs.StringVar(&query, "q", "", "literal name search")
	fs.StringVar(&urgency, "urgency", "", "needs_attention, watch or healthy")
	fs.StringVar(&operationalStart, "start", "", "operational history range start")
	fs.StringVar(&operationalEnd, "end", "", "operational history range end")
	fs.IntVar(&burnWindowSeconds, "burn-window-seconds", 3600, "selected operational burn window")
	// Services already uses service-id as its resource ID.
	if kind == "services" {
		_ = serviceID
	}
	if fs.Parse(rest) != nil || fs.NArg() != 0 {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("invalid command flags"))
	}
	jsonOut = jsonOut || output == "json"
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	allowed := map[string]bool{"profile-file": true, "timeout": true, "output": true}
	idFlag := kind[:len(kind)-1] + "-id"
	if action == "get" || action == "update" || action == "evaluate" {
		allowed[idFlag] = true
	}
	if action == "list" {
		allowed["limit"], allowed["cursor"] = true, true
		if kind == "slos" {
			allowed["service-id"] = true
		}
	}
	if action == "create" {
		allowed["input-file"], allowed["idempotency-key"] = true, true
	}
	if action == "update" {
		allowed["input-file"], allowed["if-match"] = true, true
	}
	if action == "evaluate" {
		allowed["at"], allowed["timeline-start"], allowed["timeline-end"] = true, true, true
	}
	if action == "overview" {
		allowed["q"], allowed["urgency"], allowed["service-id"] = true, true, true
		allowed["limit"], allowed["cursor"] = true, true
	}
	if action == "operational" {
		allowed[idFlag], allowed["at"], allowed["start"], allowed["end"], allowed["burn-window-seconds"] = true, true, true, true, true
	}
	for k := range seen {
		if !allowed[k] {
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("a flag is not valid for this operation"))
		}
	}
	if output != "json" && output != "text" || profile == "" || timeout <= 0 || timeout > 2*time.Minute || limit < 1 || limit > 200 || len(cursor) > 512 || !printable(cursor) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("invalid output, profile, deadline or pagination"))
	}
	if (action == "get" || action == "update" || action == "evaluate" || action == "operational") && !validCLIUUID(id) || serviceID != "" && !validCLIUUID(serviceID) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("resource identifier must be a canonical UUID"))
	}
	if action == "overview" && (!utf8.ValidString(query) || utf8.RuneCountInString(query) > 128 || urgency != "" && urgency != "needs_attention" && urgency != "watch" && urgency != "healthy") {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("overview search or urgency filter is invalid"))
	}
	if action == "operational" && burnWindowSeconds != 3600 && burnWindowSeconds != 21600 {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("burn window must be 3600 or 21600 seconds"))
	}
	if action == "create" && (inputFile == "" || !validKey(key)) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("input-file and idempotency-key are required"))
	}
	if action == "update" && (inputFile == "" || !validObjectiveETag(etag, kind[:len(kind)-1], id)) {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("input-file and exact if-match ETag are required"))
	}
	if (start == "") != (end == "") {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("both timeline bounds are required"))
	}
	parse := func(value string) (*time.Time, error) {
		if value == "" {
			return nil, nil
		}
		v, e := time.Parse(time.RFC3339Nano, value)
		if e != nil {
			return nil, e
		}
		return &v, nil
	}
	atTime, e1 := parse(at)
	startTime, e2 := parse(start)
	endTime, e3 := parse(end)
	if e1 != nil || e2 != nil || e3 != nil {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("times must be RFC3339 instants"))
	}
	var operationalStartTime, operationalEndTime *time.Time
	if action == "operational" {
		var ok bool
		atTime, ok = parseOperationalTime(at)
		if !ok {
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("operational times must be UTC RFC3339 instants with microsecond precision"))
		}
		operationalStartTime, ok = parseOperationalTime(operationalStart)
		if !ok {
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("operational times must be UTC RFC3339 instants with microsecond precision"))
		}
		operationalEndTime, ok = parseOperationalTime(operationalEnd)
		if !ok || !validOperationalRange(operationalStartTime, operationalEndTime) {
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("provide both operational range bounds, aligned, positive and no longer than 365 days"))
		}
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var input []byte
	var err error
	if inputFile != "" {
		if inputFile == "-" {
			input, err = io.ReadAll(io.LimitReader(os.Stdin, maxDatasourceInputBytes+1))
			if len(input) > maxDatasourceInputBytes {
				err = secretfile.ErrInvalid
			}
		} else {
			input, err = secretfile.Read(inputFile, maxDatasourceInputBytes)
		}
		if err != nil || !validObjectiveInput(input, kind, action) {
			clear(input)
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("input JSON has missing or unsupported fields"))
		}
		defer clear(input)
	}
	locked, err := cliprofile.Open(ctx, profile, false)
	if err != nil {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceProfileFailure(err))
	}
	defer locked.Close()
	record, exists, err := locked.Load()
	if err != nil || !exists {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceProfileFailure(cliprofile.ErrUnavailable))
	}
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: record.BackendURL, CABundle: []byte(record.CAPEM), Session: record.Session})
	if err != nil {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceSetupFailure(err))
	}
	defer session.Close()
	ops, editor, identity, err := session.DatasourceContext(ctx)
	if session.Session() != record.Session {
		record.Session = session.Session()
		if saveErr := locked.Save(record); saveErr != nil {
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceProfileFailure(saveErr))
		}
	}
	if err != nil {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceSetupFailure(err))
	}
	if (action == "create" || action == "update") && identity.Role != "administrator" && identity.Role != "editor" {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceForbidden())
	}
	objectiveOps, ok := ops.(sloclient.Operations)
	if !ok {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("objective operations unavailable"))
	}
	data, tag, err := sloclient.New(objectiveOps, editor).Do(ctx, sloclient.Input{Kind: kind, Action: action, ID: id, ServiceID: serviceID, Cursor: cursor, Limit: limit, Key: key, ETag: etag, Body: input, At: atTime, TimelineStart: startTime, TimelineEnd: endTime, Query: query, Urgency: urgency, Start: operationalStartTime, End: operationalEndTime, BurnWindowSeconds: burnWindowSeconds})
	if err != nil {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, err)
	}
	if tag != "" {
		data, err = json.Marshal(map[string]any{kind[:len(kind)-1]: jsontext.Value(data), "etag": tag})
		if err != nil {
			return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceInvalid("invalid response"))
		}
	}
	if err = locked.Close(); err != nil {
		return writeDatasourceFailure(stdout, stderr, operation, jsonOut, datasourceProfileFailure(err))
	}
	if jsonOut {
		return writeDatasourceSuccess(stdout, stderr, true, operation, data)
	}
	var page struct {
		Items []struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	if action == "list" && json.Unmarshal(data, &page) == nil {
		if len(page.Items) == 0 {
			fmt.Fprintln(stdout, "No records found.")
		}
		for _, item := range page.Items {
			fmt.Fprintf(stdout, "%s\t%s\n", item.DisplayName, item.Name)
		}
		if page.NextCursor != "" {
			fmt.Fprintf(stdout, "Next cursor: %s\n", page.NextCursor)
		}
		return 0
	}
	if action == "overview" {
		var overview api.SloOperationalOverviewPage
		if json.Unmarshal(data, &overview) == nil {
			if len(overview.Items) == 0 {
				fmt.Fprintln(stdout, "No SLOs match these filters.")
			}
			fmt.Fprintln(stdout, "SLO\tUrgency\tRemaining events\tBurn 1 h\tBurn 6 h\tExhaustion")
			for _, item := range overview.Items {
				performance := item.Current.Performance
				fmt.Fprintf(stdout, "%s\t%s (%s)\t%s\t%s\t%s\t%s\n", item.Slo.DisplayName, item.Urgency, item.LeadingReason.Code, objectiveValue(performance.RemainingBudget), burnSummary(item.Burn1h), burnSummary(item.Burn6h), objectiveExhaustion(item.Exhaustion))
				fmt.Fprintf(stdout, "  %s · %s · %s target over %d seconds · sparkline %d samples\n", item.Slo.ServiceDisplayName, item.Slo.SliDisplayName, item.Slo.Target, item.Slo.RollingWindowSeconds, len(item.BudgetSparkline))
			}
			if overview.NextCursor != "" {
				fmt.Fprintf(stdout, "Next cursor: %s\n", overview.NextCursor)
			}
			return 0
		}
	}
	if action == "operational" {
		var detail api.SloOperationalDetail
		if json.Unmarshal(data, &detail) == nil {
			p, q := detail.Current.Performance, detail.Current.Quality
			selectedBurn := detail.Burn1h.Performance.Burn
			if burnWindowSeconds == 21600 {
				selectedBurn = detail.Burn6h.Performance.Burn
			}
			fmt.Fprintf(stdout, "%s · %s · %s\n", detail.Objective.DisplayName, detail.Urgency, detail.LeadingReason.Code)
			fmt.Fprintf(stdout, "Performance: %s%s · attainment: %s · remaining: %s events (fraction %s)\n", p.State, provisionalSuffix(p.Provisional), objectiveValue(p.Attainment), objectiveValue(p.RemainingBudget), objectiveValue(p.RemainingFraction))
			fmt.Fprintf(stdout, "Burn 1 h: %s · Burn 6 h: %s · selected %d h: %s\n", burnSummary(detail.Burn1h), burnSummary(detail.Burn6h), burnWindowSeconds/3600, objectiveBurn(selectedBurn))
			fmt.Fprintf(stdout, "Coverage: %s · observed %d, missing %d, expired %d of %d intervals · freshness %s\n", objectiveValue(q.Coverage), q.ObservedIntervals, q.MissingIntervals, q.ExpiredIntervals, q.ExpectedIntervals, q.Freshness)
			fmt.Fprintf(stdout, "Exhaustion: %s · history %s to %s · %d attainment, %d budget and %d burn samples\n", objectiveExhaustion(detail.Exhaustion), detail.Range.EffectiveStart.Format(time.RFC3339Nano), detail.Range.EffectiveEnd.Format(time.RFC3339Nano), len(detail.AttainmentSeries.Samples), len(detail.BudgetSeries.Samples), len(detail.BurnSeries.Samples))
			fmt.Fprintf(stdout, "Service: %s · SLI: %s · datasource: %s\n", detail.Objective.ServiceDisplayName, detail.Context.SliDisplayName, detail.Context.DatasourceDisplayName)
			return 0
		}
	}
	if action == "evaluate" {
		var evaluation api.SloEvaluation
		if json.Unmarshal(data, &evaluation) == nil {
			p, q := evaluation.Performance, evaluation.Quality
			fmt.Fprintf(stdout, "Window: %s – %s (end excluded)\n", evaluation.Window.EffectiveStart.Format(time.RFC3339Nano), evaluation.Window.End.Format(time.RFC3339Nano))
			fmt.Fprintf(stdout, "Measured success: %s; remaining budget: %s events; burn: %s; coverage: %s\n", objectiveValue(p.Attainment), objectiveValue(p.RemainingBudget), objectiveValue(p.Burn), objectiveValue(q.Coverage))
			fmt.Fprintf(stdout, "Performance: %s; provisional: %t; observed: %d, missing: %d, expired: %d intervals\n", p.State, p.Provisional, q.ObservedIntervals, q.MissingIntervals, q.ExpiredIntervals)
			return 0
		}
	}
	if tag != "" {
		var envelope map[string]jsontext.Value
		if json.Unmarshal(data, &envelope) == nil {
			var record struct {
				Name        string `json:"name"`
				DisplayName string `json:"display_name"`
				ID          string `json:"id"`
			}
			if json.Unmarshal(envelope[kind[:len(kind)-1]], &record) == nil {
				fmt.Fprintf(stdout, "%s (%s)\nID: %s\nETag: %s\n", record.DisplayName, record.Name, record.ID, tag)
				return 0
			}
		}
	}
	_, err = fmt.Fprintln(stdout, string(data))
	if err != nil {
		fmt.Fprintln(stderr, operation+": output failed")
		return 1
	}
	return 0
}

func parseOperationalTime(value string) (*time.Time, bool) {
	if value == "" {
		return nil, true
	}
	if !strings.HasSuffix(value, "Z") {
		return nil, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Nanosecond()%1_000 != 0 {
		return nil, false
	}
	return &parsed, true
}

func validOperationalRange(start, end *time.Time) bool {
	if (start == nil) != (end == nil) {
		return false
	}
	if start == nil {
		return true
	}
	rangeLength := end.Sub(*start)
	return rangeLength > 0 && rangeLength <= 365*24*time.Hour
}

func objectiveValue(value *string) string {
	if value == nil {
		return "—"
	}
	return *value
}

func objectiveBurn(value *string) string {
	if value == nil {
		return "unavailable"
	}
	return *value + "×"
}

func burnSummary(value api.SloBurnValue) string {
	if value.Performance.Burn != nil {
		return objectiveBurn(value.Performance.Burn)
	}
	if value.Quality.Reason != nil && *value.Quality.Reason != "" {
		return "unavailable (" + *value.Quality.Reason + ")"
	}
	return "unavailable (" + string(value.Quality.State) + ")"
}

func objectiveExhaustion(value api.SloExhaustionEstimate) string {
	switch value.State {
	case "exhausted":
		return "exhausted"
	case "estimated":
		if value.HoursUntilExhaustion != nil {
			return *value.HoursUntilExhaustion + " h at observed 1 h bad-event rate"
		}
	case "unavailable":
		if value.Reason != nil {
			return *value.Reason
		}
	}
	return "no finite estimate"
}

func provisionalSuffix(provisional bool) string {
	if provisional {
		return " (observed only)"
	}
	return ""
}
func validObjectiveETag(tag, kind, id string) bool {
	prefix := `"` + kind + ":" + id + ":"
	if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, `"`) {
		return false
	}
	version := strings.TrimSuffix(strings.TrimPrefix(tag, prefix), `"`)
	v, err := strconv.ParseInt(version, 10, 64)
	return validPositiveDecimal(version) && err == nil && v > 0
}
func validObjectiveInput(data []byte, kind, action string) bool {
	var fields map[string]jsontext.Value
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	for _, value := range fields {
		if string(value) == "null" {
			return false
		}
	}
	if action == "update" {
		return exactFields(fields, "display_name")
	}
	if kind == "services" {
		return exactFields(fields, "name", "display_name")
	}
	if _, ok := fields["evaluation_valid_from"]; ok {
		return exactFields(fields, "name", "display_name", "service_id", "sli_id", "target", "rolling_window_seconds", "evaluation_valid_from")
	}
	return exactFields(fields, "name", "display_name", "service_id", "sli_id", "target", "rolling_window_seconds")
}
