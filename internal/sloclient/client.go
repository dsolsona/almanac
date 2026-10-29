// Package sloclient provides authenticated service and objective operations.
package sloclient

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
)

type Operations interface {
	ListServices(context.Context, *api.ListServicesParams, ...api.RequestEditorFn) (*http.Response, error)
	CreateService(context.Context, *api.CreateServiceParams, api.CreateServiceJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetService(context.Context, api.ServiceId, ...api.RequestEditorFn) (*http.Response, error)
	PatchServiceMetadata(context.Context, api.ServiceId, *api.PatchServiceMetadataParams, api.PatchServiceMetadataJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	ListSlos(context.Context, *api.ListSlosParams, ...api.RequestEditorFn) (*http.Response, error)
	CreateSlo(context.Context, *api.CreateSloParams, api.CreateSloJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetSlo(context.Context, api.SloId, ...api.RequestEditorFn) (*http.Response, error)
	PatchSloMetadata(context.Context, api.SloId, *api.PatchSloMetadataParams, api.PatchSloMetadataJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	EvaluateSlo(context.Context, api.SloId, *api.EvaluateSloParams, ...api.RequestEditorFn) (*http.Response, error)
}
type Client struct {
	ops    Operations
	editor api.RequestEditorFn
}

func New(ops Operations, editor api.RequestEditorFn) *Client { return &Client{ops, editor} }

type Input struct {
	Kind, Action, ID, Cursor, ServiceID, Key, ETag string
	Limit                                          int
	Body                                           []byte
	At, TimelineStart, TimelineEnd                 *time.Time
}

func (c *Client) Do(ctx context.Context, in Input) ([]byte, string, error) {
	var response *http.Response
	var err error
	var id api.SloId
	if in.ID != "" {
		if json.Unmarshal([]byte(strconv.Quote(in.ID)), &id) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
	}
	limit := api.PageLimit(in.Limit)
	cursor := api.PageCursor(in.Cursor)
	var cursorPtr *api.PageCursor
	if in.Cursor != "" {
		cursorPtr = &cursor
	}
	editor := c.editor
	if in.Action == "create" || in.Action == "update" {
		editor = func(ctx context.Context, r *http.Request) error {
			if err := c.editor(ctx, r); err != nil {
				return err
			}
			r.GetBody = nil
			return nil
		}
	}
	switch in.Kind + " " + in.Action {
	case "services list":
		response, err = c.ops.ListServices(ctx, &api.ListServicesParams{Limit: &limit, Cursor: cursorPtr}, editor)
	case "services get":
		response, err = c.ops.GetService(ctx, id, editor)
	case "services create":
		var body api.ServiceCreateRequest
		if json.Unmarshal(in.Body, &body) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
		response, err = c.ops.CreateService(ctx, &api.CreateServiceParams{IdempotencyKey: api.IdempotencyKey(in.Key)}, body, editor)
	case "services update":
		var body api.ResourceMetadataWrite
		if json.Unmarshal(in.Body, &body) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
		response, err = c.ops.PatchServiceMetadata(ctx, id, &api.PatchServiceMetadataParams{IfMatch: api.ResourceIfMatch(in.ETag)}, body, editor)
	case "slos list":
		params := &api.ListSlosParams{Limit: &limit, Cursor: cursorPtr}
		if in.ServiceID != "" {
			var serviceID api.ServiceId
			if json.Unmarshal([]byte(strconv.Quote(in.ServiceID)), &serviceID) != nil {
				return nil, "", failure("invalid_request", 0, false)
			}
			params.ServiceId = &serviceID
		}
		response, err = c.ops.ListSlos(ctx, params, editor)
	case "slos get":
		response, err = c.ops.GetSlo(ctx, id, editor)
	case "slos create":
		var body api.SloCreateRequest
		if json.Unmarshal(in.Body, &body) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
		response, err = c.ops.CreateSlo(ctx, &api.CreateSloParams{IdempotencyKey: api.IdempotencyKey(in.Key)}, body, editor)
	case "slos update":
		var body api.ResourceMetadataWrite
		if json.Unmarshal(in.Body, &body) != nil {
			return nil, "", failure("invalid_request", 0, false)
		}
		response, err = c.ops.PatchSloMetadata(ctx, id, &api.PatchSloMetadataParams{IfMatch: api.ResourceIfMatch(in.ETag)}, body, editor)
	case "slos evaluate":
		response, err = c.ops.EvaluateSlo(ctx, id, &api.EvaluateSloParams{At: in.At, TimelineStart: in.TimelineStart, TimelineEnd: in.TimelineEnd}, editor)
	default:
		return nil, "", failure("invalid_request", 0, false)
	}
	mutation := in.Action == "create" || in.Action == "update"
	if err != nil || response == nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, "", failure("management_unavailable", 0, mutation)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil || len(data) > 4<<20 {
		return nil, "", failure("invalid_response", 0, mutation)
	}
	expected := http.StatusOK
	if in.Action == "create" {
		expected = http.StatusCreated
	}
	if response.StatusCode != expected {
		var problem struct {
			Code        string `json:"code"`
			RequestID   string `json:"request_id"`
			FieldErrors []struct {
				Field string `json:"field"`
				Code  string `json:"code"`
			} `json:"field_errors"`
		}
		_ = json.Unmarshal(data, &problem)
		code := problem.Code
		switch code {
		case "invalid_request", "unauthenticated", "unauthorized", "forbidden", "not_found", "conflict", "request_timeout", "request_canceled":
		default:
			code = "management_unavailable"
		}
		f := failure(code, response.StatusCode, mutation && response.StatusCode >= 500)
		if !f.Unknown && len(problem.FieldErrors) <= 16 {
			for _, item := range problem.FieldErrors {
				if guidance := fieldGuidance[item.Field+":"+item.Code]; guidance != "" {
					f.Message = guidance
					break
				}
			}
		}
		if requestIDPattern.MatchString(problem.RequestID) {
			f.RequestID = problem.RequestID
		}
		return nil, "", f
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || object == nil || !validResponse(data, in.Kind, in.Action) {
		return nil, "", failure("invalid_response", 0, mutation)
	}
	if in.Action == "get" || mutation {
		resourceID, okID := object["id"].(string)
		version, okVersion := object["version"].(string)
		kind := "slo"
		if in.Kind == "services" {
			kind = "service"
		}
		tag := response.Header.Get("ETag")
		if !okID || !okVersion || tag != `"`+kind+":"+resourceID+":"+version+`"` || in.ID != "" && resourceID != in.ID {
			return nil, "", failure("invalid_response", 0, mutation)
		}
		return data, tag, nil
	}
	return data, "", nil
}

var decimalPattern = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)
var requestIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
var fieldGuidance = map[string]string{
	"target:target_invalid":                          "target must be at least 0 and less than 1",
	"rolling_window_seconds:window_invalid":          "window must be positive, within the supported limit and aligned to the SLI cadence",
	"evaluation_valid_from:historical_start_invalid": "historical start must be aligned, retained, within SLI validity and no later than activation; refresh the eligible range",
	"timeline_start:timeline_range_invalid":          "both timeline bounds must align, lie within the objective window and cover at most 1500 intervals",
	"at:evaluation_time_invalid":                     "evaluation time must be a valid UTC microsecond timestamp that is not in the future",
}

func fields(data []byte, names ...string) bool {
	var value map[string]jsontext.Value
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	for _, name := range names {
		if _, ok := value[name]; !ok {
			return false
		}
	}
	return true
}
func validDecimal(value *string) bool {
	return value == nil || len(*value) <= 128 && decimalPattern.MatchString(*value)
}
func validResponse(data []byte, kind, action string) bool {
	if action == "list" {
		var page struct {
			Items      []jsontext.Value `json:"items"`
			NextCursor string           `json:"next_cursor"`
		}
		if !fields(data, "items", "next_cursor") || json.Unmarshal(data, &page) != nil || page.Items == nil || len(page.Items) > 200 || len(page.NextCursor) > 512 {
			return false
		}
		for _, item := range page.Items {
			if !validResponse(item, kind, "get") {
				return false
			}
		}
		return true
	}
	if action != "evaluate" {
		if kind == "services" {
			var value api.Service
			return fields(data, "id", "name", "display_name", "version", "created_at", "updated_at") && json.Unmarshal(data, &value) == nil && value.Name != "" && value.DisplayName != "" && value.Version != "" && !value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero()
		}
		var value api.Slo
		return fields(data, "id", "name", "display_name", "version", "definition_revision", "service_id", "service_display_name", "sli_id", "sli_display_name", "target", "rolling_window_seconds", "cadence_seconds", "activated_at", "evaluation_valid_from", "historical_requested_by", "historical_requested_at", "created_at", "updated_at") && json.Unmarshal(data, &value) == nil && value.Name != "" && value.DisplayName != "" && value.Version != "" && value.DefinitionRevision != "" && value.ServiceDisplayName != "" && value.SliDisplayName != "" && validDecimal(&value.Target) && value.CadenceSeconds > 0 && value.RollingWindowSeconds > 0 && !value.ActivatedAt.IsZero()
	}
	var value api.SloEvaluation
	var raw struct {
		Quality     jsontext.Value `json:"quality"`
		Performance jsontext.Value `json:"performance"`
		Timeline    jsontext.Value `json:"timeline"`
	}
	if !fields(data, "evaluation_id", "requested_at", "effective_end", "calculation_version", "snapshot_kind", "objective", "window", "quality", "performance", "timeline") || json.Unmarshal(data, &raw) != nil || json.Unmarshal(data, &value) != nil ||
		!fields(raw.Quality, "state", "expected_intervals", "observed_intervals", "known_empty_intervals", "missing_intervals", "zero_traffic_intervals", "expired_intervals", "coverage", "first_observation_at", "last_observation_at", "latest_receipt_at", "freshness") ||
		!fields(raw.Performance, "state", "observed_state", "provisional", "reason", "good", "total", "bad", "attainment", "allowed_budget", "remaining_budget", "consumed_fraction", "remaining_fraction", "burn", "budget_unit") ||
		!fields(raw.Timeline, "start", "end", "cadence_seconds", "max_intervals", "items") {
		return false
	}
	q, p, t := value.Quality, value.Performance, value.Timeline
	if value.SnapshotKind != "current_evaluation" || value.RequestedAt.IsZero() || value.EffectiveEnd.IsZero() || value.Objective.Method != "occurrences" || value.Window.End.IsZero() || t.Start.IsZero() || t.End.IsZero() || t.CadenceSeconds <= 0 || t.MaxIntervals != 1500 || t.Items == nil || len(t.Items) > 1500 || q.ExpectedIntervals < 0 || q.ObservedIntervals < 0 || q.KnownEmptyIntervals < 0 || q.MissingIntervals < 0 || q.ZeroTrafficIntervals < 0 || q.ExpiredIntervals < 0 || !validDecimal(q.Coverage) || p.BudgetUnit != "events" {
		return false
	}
	for _, d := range []*string{p.Good, p.Total, p.Bad, p.Attainment, p.AllowedBudget, p.RemainingBudget, p.ConsumedFraction, p.RemainingFraction, p.Burn} {
		if !validDecimal(d) {
			return false
		}
	}
	for _, item := range t.Items {
		if item.Start.IsZero() || item.End.IsZero() || !validDecimal(item.Good) || !validDecimal(item.Total) || !validDecimal(item.Attainment) || !validDecimal(item.Burn) {
			return false
		}
		switch item.State {
		case "observed", "zero_traffic", "known_empty", "missing", "expired", "not_applicable":
		default:
			return false
		}
	}
	return true
}
func failure(code string, status int, unknown bool) *datasourceclient.Failure {
	message := "The request could not be completed."
	if unknown {
		code = "outcome_unknown"
		message = "The command may have completed. Reconcile the record before retrying the same request."
	}
	return &datasourceclient.Failure{Code: code, Message: message, Status: status, Unknown: unknown}
}
