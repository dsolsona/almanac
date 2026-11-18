// Package maintenanceclient implements bounded, authenticated maintenance commands.
package maintenanceclient

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
	openapi "github.com/oapi-codegen/runtime/types"
)

type Operations interface {
	ListMaintenance(context.Context, *api.ListMaintenanceParams, ...api.RequestEditorFn) (*http.Response, error)
	GetMaintenance(context.Context, openapi.UUID, *api.GetMaintenanceParams, ...api.RequestEditorFn) (*http.Response, error)
	ListSloMaintenance(context.Context, api.SloId, *api.ListSloMaintenanceParams, ...api.RequestEditorFn) (*http.Response, error)
	CreateMaintenanceWithBody(context.Context, *api.CreateMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	EditMaintenanceWithBody(context.Context, openapi.UUID, *api.EditMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	EndMaintenanceWithBody(context.Context, openapi.UUID, *api.EndMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	CancelMaintenanceWithBody(context.Context, openapi.UUID, *api.CancelMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	WithdrawMaintenanceWithBody(context.Context, openapi.UUID, *api.WithdrawMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
}

type Input struct {
	Action, ID, Revision, Cursor, Key, ETag string
	Limit                                   int
	Start, End                              time.Time
	Body                                    []byte
}

func (in Input) Mutation() bool {
	switch in.Action {
	case "create", "update", "end", "cancel", "withdraw":
		return true
	}
	return false
}

func validID(raw string) (openapi.UUID, bool) {
	id, err := uuid.Parse(raw)
	return openapi.UUID(id), err == nil && id != uuid.Nil() && id.String() == raw
}

func validRevision(raw string) bool {
	n, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == raw
}

func tag(id, revision string) string {
	return `"maintenance:` + id + ":" + revision + `"`
}

func validTime(at time.Time) bool {
	return validTimelineTime(at) && at.Nanosecond() == 0
}

func validTimelineTime(at time.Time) bool {
	_, offset := at.Zone()
	return !at.IsZero() && at.Year() >= 1970 && at.Year() <= 9999 && offset == 0 && at.Nanosecond()%1000 == 0
}

func visibleASCII(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for _, b := range []byte(value) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

func ValidateOptions(in Input) bool {
	_, hasID := validID(in.ID)
	page := in.Limit >= 1 && in.Limit <= 200 && (in.Cursor == "" || visibleASCII(in.Cursor, 512))
	switch in.Action {
	case "list":
		return page
	case "get":
		return hasID && (in.Revision == "" || validRevision(in.Revision))
	case "timeline":
		return hasID && page && validTimelineTime(in.Start) && validTimelineTime(in.End) && in.Start.Before(in.End)
	case "create":
		return visibleASCII(in.Key, 128)
	case "update", "end", "cancel", "withdraw":
		revision, ok := strings.CutPrefix(in.ETag, `"maintenance:`+in.ID+":")
		revision, closed := strings.CutSuffix(revision, `"`)
		return hasID && ok && closed && validRevision(revision) && visibleASCII(in.Key, 128)
	}
	return false
}

// Validate the envelope before opening a profile and pass the exact body through
// so canonical ID validation and replay identity stay with the API.
func ValidBody(in Input) bool {
	if !in.Mutation() {
		return len(in.Body) == 0
	}
	var fields struct {
		Title  *string    `json:"title"`
		Reason *string    `json:"reason"`
		Start  *time.Time `json:"start"`
		End    *time.Time `json:"end"`
		SloIDs *[]string  `json:"slo_ids"`
	}
	if json.Unmarshal(in.Body, &fields, json.RejectUnknownMembers(true)) != nil ||
		fields.Reason == nil || strings.TrimSpace(*fields.Reason) == "" {
		return false
	}
	var keys map[string]any
	if json.Unmarshal(in.Body, &keys) != nil {
		return false
	}
	switch in.Action {
	case "create", "update":
		return len(keys) == 5 && fields.Title != nil && strings.TrimSpace(*fields.Title) != "" &&
			fields.Start != nil && fields.End != nil && fields.SloIDs != nil &&
			len(*fields.SloIDs) >= 1 && len(*fields.SloIDs) <= 100
	case "end":
		return len(keys) == 2 && fields.End != nil
	default:
		return len(keys) == 1
	}
}

type Result struct {
	Window *api.MaintenanceWindow `json:"maintenance,omitempty"`
	ETag   string                 `json:"etag,omitempty"`
	Page   *api.MaintenancePage   `json:"-"`
}

func Do(ctx context.Context, ops Operations, auth api.RequestEditorFn, in Input) (Result, error) {
	var result Result
	if !ValidateOptions(in) || !ValidBody(in) {
		return result, &datasourceclient.Failure{Code: "invalid_request", Message: "Invalid maintenance input."}
	}
	id, _ := validID(in.ID)
	limit := new(api.PageLimit(in.Limit))
	var cursor *api.PageCursor
	if in.Cursor != "" {
		cursor = new(api.PageCursor(in.Cursor))
	}
	editor := auth
	if in.Mutation() {
		editor = func(ctx context.Context, r *http.Request) error {
			if err := auth(ctx, r); err != nil {
				return err
			}
			r.GetBody = nil // Explicitly reconcile an uncertain write before replay.
			return nil
		}
	}
	var response *http.Response
	var err error
	body := bytes.NewReader(in.Body)
	key := api.IdempotencyKey(in.Key)
	const content = "application/json"
	switch in.Action {
	case "list":
		response, err = ops.ListMaintenance(ctx, &api.ListMaintenanceParams{Limit: limit, Cursor: cursor}, editor)
	case "timeline":
		response, err = ops.ListSloMaintenance(ctx, id, &api.ListSloMaintenanceParams{Limit: limit, Cursor: cursor, Start: in.Start, End: in.End}, editor)
	case "get":
		params := &api.GetMaintenanceParams{}
		if in.Revision != "" {
			params.Revision = &in.Revision
		}
		response, err = ops.GetMaintenance(ctx, id, params, editor)
	case "create":
		response, err = ops.CreateMaintenanceWithBody(ctx, &api.CreateMaintenanceParams{IdempotencyKey: key}, content, body, editor)
	case "update":
		response, err = ops.EditMaintenanceWithBody(ctx, id, &api.EditMaintenanceParams{IfMatch: in.ETag, IdempotencyKey: key}, content, body, editor)
	case "end":
		response, err = ops.EndMaintenanceWithBody(ctx, id, &api.EndMaintenanceParams{IfMatch: in.ETag, IdempotencyKey: key}, content, body, editor)
	case "cancel":
		response, err = ops.CancelMaintenanceWithBody(ctx, id, &api.CancelMaintenanceParams{IfMatch: in.ETag, IdempotencyKey: key}, content, body, editor)
	case "withdraw":
		response, err = ops.WithdrawMaintenanceWithBody(ctx, id, &api.WithdrawMaintenanceParams{IfMatch: in.ETag, IdempotencyKey: key}, content, body, editor)
	}
	if err != nil || response == nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return result, responseFailure(ctx, in.Mutation(), 0)
	}
	defer response.Body.Close()
	const maxResponse = 4 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return result, responseFailure(ctx, in.Mutation(), response.StatusCode)
	}
	expected := http.StatusOK
	if in.Action == "create" {
		expected = http.StatusCreated
	}
	if response.StatusCode != expected {
		return result, problemFailure(ctx, in.Mutation(), response.StatusCode, data)
	}
	if in.Action == "list" || in.Action == "timeline" {
		var page api.MaintenancePage
		if json.Unmarshal(data, &page) != nil || page.Items == nil || len(page.Items) > in.Limit ||
			page.NextCursor != nil && !visibleASCII(*page.NextCursor, 512) {
			return result, responseFailure(ctx, false, response.StatusCode)
		}
		for _, record := range page.Items {
			if !validWindow(record) {
				return result, responseFailure(ctx, false, response.StatusCode)
			}
		}
		result.Page = &page
		return result, nil
	}
	var record api.MaintenanceWindow
	etag := response.Header.Get("ETag")
	if json.Unmarshal(data, &record) != nil || !validWindow(record) ||
		etag != tag(record.Id.String(), record.Revision) ||
		in.ID != "" && id != record.Id || in.Revision != "" && in.Revision != record.Revision {
		return result, responseFailure(ctx, in.Mutation(), response.StatusCode)
	}
	result.Window, result.ETag = &record, etag
	return result, nil
}

func validWindow(record api.MaintenanceWindow) bool {
	return record.Id != openapi.UUID{} && validRevision(record.Revision) && record.Operation.Valid() &&
		validTime(record.Start) && validTime(record.End) && record.Start.Before(record.End) &&
		len(record.SloIds) > 0 && len(record.SloIds) <= 100
}

func responseFailure(ctx context.Context, mutation bool, status int) *datasourceclient.Failure {
	failure := &datasourceclient.Failure{Code: "management_unavailable", Message: "Maintenance request failed.", Status: status}
	if mutation {
		failure.Code, failure.Message, failure.Unknown = "outcome_unknown", "The request may have completed. Inspect the window before repeating the same request and key.", true
	} else if ctx.Err() == context.DeadlineExceeded {
		failure.Code, failure.Message = "request_timeout", "The maintenance request timed out."
	} else if ctx.Err() == context.Canceled {
		failure.Code, failure.Message = "request_canceled", "The maintenance request was cancelled."
	}
	return failure
}

func problemFailure(ctx context.Context, mutation bool, status int, data []byte) error {
	failure := responseFailure(ctx, mutation && status >= 500, status)
	switch status {
	case 400, 422:
		failure.Code, failure.Message = "invalid_request", "Check the maintenance request fields and bounds."
	case 401:
		failure.Code, failure.Message = "unauthorized", "Sign in again before continuing."
	case 403:
		failure.Code, failure.Message = "forbidden", "This account cannot perform that maintenance action."
	case 404:
		failure.Code, failure.Message = "not_found", "Maintenance or the affected SLO was not found."
	case 409:
		failure.Code, failure.Message = "conflict", "Read the latest maintenance revision or check whether this key was already used."
	}
	var problem api.Problem
	if json.Unmarshal(data, &problem) == nil {
		switch {
		case status == 400 && problem.Code == "maintenance_alignment":
			failure.BackendCode = problem.Code
			failure.Message = "Choose bounds aligned with all affected SLO observation grids."
			if problem.SuggestedStart != nil && problem.SuggestedEnd != nil && validTime(*problem.SuggestedStart) && validTime(*problem.SuggestedEnd) {
				failure.Message += " Suggested bounds: " + problem.SuggestedStart.Format(time.RFC3339) + " to " + problem.SuggestedEnd.Format(time.RFC3339) + "."
			}
		case status == 409 && problem.Code == "maintenance_conflict":
			failure.BackendCode = problem.Code
			if problem.CurrentRevision != nil && validRevision(*problem.CurrentRevision) {
				failure.Message += " Current revision is " + *problem.CurrentRevision + "."
			}
		}
	}
	return failure
}
