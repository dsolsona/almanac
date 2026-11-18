package maintenanceclient

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strconv"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
	openapi "github.com/oapi-codegen/runtime/types"
)

type ScopedOperations interface {
	ListSloScopedMaintenance(context.Context, api.SloId, *api.ListSloScopedMaintenanceParams, ...api.RequestEditorFn) (*http.Response, error)
	ListScopedMaintenance(context.Context, *api.ListScopedMaintenanceParams, ...api.RequestEditorFn) (*http.Response, error)
	GetScopedMaintenance(context.Context, openapi.UUID, *api.GetScopedMaintenanceParams, ...api.RequestEditorFn) (*http.Response, error)
	PreviewMaintenanceScopeWithBody(context.Context, *api.PreviewMaintenanceScopeParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	ReadMaintenanceImpact(context.Context, openapi.UUID, *api.ReadMaintenanceImpactParams, ...api.RequestEditorFn) (*http.Response, error)
	CreateScopedMaintenanceWithBody(context.Context, *api.CreateScopedMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	EditScopedMaintenanceWithBody(context.Context, openapi.UUID, *api.EditScopedMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	EndScopedMaintenanceWithBody(context.Context, openapi.UUID, *api.EndScopedMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	CancelScopedMaintenanceWithBody(context.Context, openapi.UUID, *api.CancelScopedMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
	WithdrawScopedMaintenanceWithBody(context.Context, openapi.UUID, *api.WithdrawScopedMaintenanceParams, string, io.Reader, ...api.RequestEditorFn) (*http.Response, error)
}

type ScopedResult struct {
	Window   *api.ScopedMaintenanceWindow       `json:"maintenance,omitempty"`
	ETag     string                             `json:"etag,omitempty"`
	Page     *api.ScopedMaintenancePage         `json:"-"`
	Impact   *api.MaintenanceImpactPage         `json:"-"`
	Timeline *api.ScopedMaintenanceTimelinePage `json:"-"`
}

func DoScoped(ctx context.Context, ops ScopedOperations, auth api.RequestEditorFn, in Input) (ScopedResult, error) {
	var result ScopedResult
	if !in.Scoped || !ValidateOptions(in) || !ValidBody(in) {
		return result, &datasourceclient.Failure{Code: "invalid_request", Message: "Invalid maintenance input."}
	}
	id, _ := validID(in.ID)
	limit := new(api.PageLimit(in.Limit))
	var cursor *api.PageCursor
	if in.Cursor != "" {
		cursor = new(api.PageCursor(in.Cursor))
	}
	var revision *string
	if in.Revision != "" {
		revision = &in.Revision
	}
	editor := maintenanceEditor(auth, in.Mutation())
	var response *http.Response
	var err error
	body := bytes.NewReader(in.Body)
	key := api.IdempotencyKey(in.Key)
	const content = "application/json"
	switch in.Action {
	case "list":
		response, err = ops.ListScopedMaintenance(ctx, &api.ListScopedMaintenanceParams{Limit: limit, Cursor: cursor}, editor)
	case "timeline":
		response, err = ops.ListSloScopedMaintenance(ctx, id, &api.ListSloScopedMaintenanceParams{Limit: limit, Cursor: cursor, Start: in.Start, End: in.End}, editor)
	case "get":
		response, err = ops.GetScopedMaintenance(ctx, id, &api.GetScopedMaintenanceParams{Revision: revision}, editor)
	case "preview":
		response, err = ops.PreviewMaintenanceScopeWithBody(ctx, &api.PreviewMaintenanceScopeParams{Limit: limit, Cursor: cursor}, content, body, editor)
	case "impact":
		response, err = ops.ReadMaintenanceImpact(ctx, id, &api.ReadMaintenanceImpactParams{Limit: limit, Cursor: cursor, Revision: revision}, editor)
	case "create":
		response, err = ops.CreateScopedMaintenanceWithBody(ctx, &api.CreateScopedMaintenanceParams{IdempotencyKey: key}, content, body, editor)
	case "update":
		response, err = ops.EditScopedMaintenanceWithBody(ctx, id, &api.EditScopedMaintenanceParams{IfMatch: in.ETag, IdempotencyKey: key}, content, body, editor)
	case "end":
		response, err = ops.EndScopedMaintenanceWithBody(ctx, id, &api.EndScopedMaintenanceParams{IfMatch: in.ETag, IdempotencyKey: key}, content, body, editor)
	case "cancel":
		response, err = ops.CancelScopedMaintenanceWithBody(ctx, id, &api.CancelScopedMaintenanceParams{IfMatch: in.ETag, IdempotencyKey: key}, content, body, editor)
	case "withdraw":
		response, err = ops.WithdrawScopedMaintenanceWithBody(ctx, id, &api.WithdrawScopedMaintenanceParams{IfMatch: in.ETag, IdempotencyKey: key}, content, body, editor)
	}
	data, err := maintenanceResponse(ctx, in, response, err)
	if err != nil {
		return result, err
	}
	switch in.Action {
	case "list":
		var page api.ScopedMaintenancePage
		if json.Unmarshal(data, &page, json.RejectUnknownMembers(true)) != nil ||
			page.Items == nil || len(page.Items) > in.Limit || !validCursor(page.NextCursor) {
			return result, responseFailure(ctx, false, response.StatusCode)
		}
		for _, item := range page.Items {
			if !validScopedWindow(item) {
				return result, responseFailure(ctx, false, response.StatusCode)
			}
		}
		result.Page = &page
	case "timeline":
		var page api.ScopedMaintenanceTimelinePage
		if json.Unmarshal(data, &page, json.RejectUnknownMembers(true)) != nil ||
			page.Items == nil || len(page.Items) > in.Limit || !validCursor(page.NextCursor) {
			return result, responseFailure(ctx, false, response.StatusCode)
		}
		for _, interval := range page.Items {
			if !validScopedWindow(interval.Window) || !validTimelineTime(interval.Start) || !validTimelineTime(interval.End) ||
				!interval.Start.Before(interval.End) || !interval.Start.Before(in.End) || !in.Start.Before(interval.End) {
				return result, responseFailure(ctx, false, response.StatusCode)
			}
		}
		result.Timeline = &page
	case "preview", "impact":
		var page api.MaintenanceImpactPage
		if json.Unmarshal(data, &page, json.RejectUnknownMembers(true)) != nil || !validImpact(page, in) {
			return result, responseFailure(ctx, false, response.StatusCode)
		}
		result.Impact = &page
	default:
		var item api.ScopedMaintenanceWindow
		etag := response.Header.Get("ETag")
		if json.Unmarshal(data, &item, json.RejectUnknownMembers(true)) != nil || !validScopedWindow(item) ||
			etag != tag(item.Id.String(), item.Revision) || in.ID != "" && id != item.Id ||
			in.Revision != "" && in.Revision != item.Revision {
			return result, responseFailure(ctx, in.Mutation(), response.StatusCode)
		}
		result.Window, result.ETag = &item, etag
	}
	return result, nil
}

func validCursor(cursor *string) bool {
	return cursor == nil || visibleASCII(*cursor, 512)
}

func validScopedWindow(item api.ScopedMaintenanceWindow) bool {
	scope, err := item.Scope.MarshalJSON()
	return err == nil && item.Id != openapi.UUID{} && validRevision(item.Revision) && item.Operation.Valid() &&
		validTime(item.Start) && validTime(item.End) && item.Start.Before(item.End) && validSelector(scope)
}

func validImpact(page api.MaintenanceImpactPage, in Input) bool {
	count, err := strconv.ParseInt(page.Total, 10, 64)
	if err != nil || count < int64(len(page.Items)) || strconv.FormatInt(count, 10) != page.Total ||
		page.Items == nil || len(page.Items) > in.Limit || !validCursor(page.NextCursor) || !validTimelineTime(page.InventoryAt) {
		return false
	}
	for _, member := range page.Items {
		if member.SloId == (openapi.UUID{}) || member.ServiceId == (openapi.UUID{}) ||
			member.SloName == "" || member.ServiceName == "" || (member.Start == nil) != (member.End == nil) {
			return false
		}
		if member.Start != nil && (in.Action == "preview" || !validTimelineTime(*member.Start) ||
			!validTimelineTime(*member.End) || !member.Start.Before(*member.End)) {
			return false
		}
	}
	return true
}

func validSelector(body []byte) bool {
	_, valid := api.ParseMaintenanceScope(body)
	return valid
}
