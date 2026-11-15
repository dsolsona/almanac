// Package alertclient implements alert management only through the generated HTTP API.
package alertclient

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/datasourceclient"
)

type Operations interface {
	GetAlertSeries(context.Context, api.AlertOccurrenceId, *api.GetAlertSeriesParams, ...api.RequestEditorFn) (*http.Response, error)
	ListAlertPolicies(context.Context, *api.ListAlertPoliciesParams, ...api.RequestEditorFn) (*http.Response, error)
	CreateAlertPolicy(context.Context, *api.CreateAlertPolicyParams, api.CreateAlertPolicyJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetAlertPolicy(context.Context, api.AlertPolicyId, ...api.RequestEditorFn) (*http.Response, error)
	UpdateAlertPolicy(context.Context, api.AlertPolicyId, *api.UpdateAlertPolicyParams, api.UpdateAlertPolicyJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	EnableAlertPolicy(context.Context, api.AlertPolicyId, *api.EnableAlertPolicyParams, api.EnableAlertPolicyJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	DisableAlertPolicy(context.Context, api.AlertPolicyId, *api.DisableAlertPolicyParams, api.DisableAlertPolicyJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	RetryAlertPolicy(context.Context, api.AlertPolicyId, *api.RetryAlertPolicyParams, api.RetryAlertPolicyJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	ListAlertRanges(context.Context, api.AlertPolicyId, *api.ListAlertRangesParams, ...api.RequestEditorFn) (*http.Response, error)
	ListAlerts(context.Context, *api.ListAlertsParams, ...api.RequestEditorFn) (*http.Response, error)
	GetAlert(context.Context, api.AlertOccurrenceId, ...api.RequestEditorFn) (*http.Response, error)
	GetAlertEvidence(context.Context, api.AlertOccurrenceId, *api.GetAlertEvidenceParams, ...api.RequestEditorFn) (*http.Response, error)
	ListAlertTransitions(context.Context, api.AlertOccurrenceId, *api.ListAlertTransitionsParams, ...api.RequestEditorFn) (*http.Response, error)
	AcknowledgeAlert(context.Context, api.AlertOccurrenceId, *api.AcknowledgeAlertParams, api.AcknowledgeAlertJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
	GetAlertProcessing(context.Context, *api.GetAlertProcessingParams, ...api.RequestEditorFn) (*http.Response, error)
	GetAlertCommand(context.Context, *api.GetAlertCommandParams, ...api.RequestEditorFn) (*http.Response, error)
}

type Client struct {
	ops    Operations
	editor api.RequestEditorFn
}

func New(ops Operations, editor api.RequestEditorFn) *Client { return &Client{ops, editor} }

type Input struct {
	ConditionID, Start, End, Evidence, MinimumDuration                                       string
	Kind, Action, ID, SLOID, ServiceID, Cursor, Query, Scope, State, Severity, GroupBy, Sort string
	Revision, Selection, Operation, Key, ETag                                                string
	Enabled                                                                                  *bool
	Limit                                                                                    int
	Body                                                                                     []byte
}

func (in Input) Mutation() bool {
	return in.Kind == "alert-policies" && contains(in.Action, "create", "update", "enable", "disable", "retry") || in.Kind == "alerts" && in.Action == "acknowledge"
}
func (in Input) CommandOperation() string {
	if in.Action == "acknowledge" {
		return "alert.acknowledge"
	}
	return "alert-policy." + in.Action
}

func (c *Client) Do(ctx context.Context, in Input) ([]byte, string, error) {
	if err := Validate(in); err != nil {
		return nil, "", err
	}
	if in.Limit == 0 {
		in.Limit = 20
		if in.Action == "series" {
			in.Limit = 120
		}
	}
	if in.Mutation() && len(in.Body) == 0 {
		in.Body = []byte(`{}`)
	}
	editor := func(ctx context.Context, r *http.Request) error {
		if c.editor != nil {
			if err := c.editor(ctx, r); err != nil {
				return err
			}
		}
		if in.Mutation() {
			r.GetBody = nil
		}
		return nil
	}
	var id, slo, service api.AlertPolicyId
	if in.ID != "" {
		_ = json.Unmarshal([]byte(strconv.Quote(in.ID)), &id)
	}
	if in.SLOID != "" {
		_ = json.Unmarshal([]byte(strconv.Quote(in.SLOID)), &slo)
	}
	if in.ServiceID != "" {
		_ = json.Unmarshal([]byte(strconv.Quote(in.ServiceID)), &service)
	}
	var cursor *string
	if in.Cursor != "" {
		cursor = &in.Cursor
	}
	var response *http.Response
	var err error
	switch in.Kind + " " + in.Action {
	case "alert-policies list":
		p := &api.ListAlertPoliciesParams{Limit: &in.Limit, Cursor: cursor, Enabled: in.Enabled}
		if in.SLOID != "" {
			p.SloId = &slo
		}
		if in.Severity != "" {
			p.Severity = new(api.AlertSeverity(in.Severity))
		}
		if in.Query != "" {
			p.Q = &in.Query
		}
		response, err = c.ops.ListAlertPolicies(ctx, p, editor)
	case "alert-policies get":
		response, err = c.ops.GetAlertPolicy(ctx, id, editor)
	case "alert-policies create":
		var body api.AlertPolicyWrite
		_ = json.Unmarshal(in.Body, &body)
		response, err = c.ops.CreateAlertPolicy(ctx, &api.CreateAlertPolicyParams{IdempotencyKey: in.Key}, body, editor)
	case "alert-policies update":
		var body api.AlertPolicyPatch
		_ = json.Unmarshal(in.Body, &body)
		response, err = c.ops.UpdateAlertPolicy(ctx, id, &api.UpdateAlertPolicyParams{IdempotencyKey: in.Key, IfMatch: in.ETag}, body, editor)
	case "alert-policies enable", "alert-policies disable", "alert-policies retry":
		var body api.AlertPolicyCommand
		_ = json.Unmarshal(in.Body, &body)
		switch in.Action {
		case "enable":
			response, err = c.ops.EnableAlertPolicy(ctx, id, &api.EnableAlertPolicyParams{IdempotencyKey: in.Key, IfMatch: in.ETag}, body, editor)
		case "disable":
			response, err = c.ops.DisableAlertPolicy(ctx, id, &api.DisableAlertPolicyParams{IdempotencyKey: in.Key, IfMatch: in.ETag}, body, editor)
		case "retry":
			response, err = c.ops.RetryAlertPolicy(ctx, id, &api.RetryAlertPolicyParams{IdempotencyKey: in.Key, IfMatch: in.ETag}, api.RetryAlertPolicyJSONRequestBody{}, editor)
		}
	case "alert-policies ranges":
		response, err = c.ops.ListAlertRanges(ctx, id, &api.ListAlertRangesParams{Revision: in.Revision, Limit: &in.Limit, Cursor: cursor}, editor)
	case "alerts list":
		p := &api.ListAlertsParams{Limit: &in.Limit, Cursor: cursor}
		if in.Evidence != "" {
			p.Evidence = new(api.ListAlertsParamsEvidence(in.Evidence))
		}
		if in.MinimumDuration != "" {
			value, _ := strconv.ParseInt(in.MinimumDuration, 10, 64)
			p.MinimumDurationSeconds = &value
		}
		if in.SLOID != "" {
			p.SloId = &slo
		}
		if in.ServiceID != "" {
			p.ServiceId = &service
		}
		if in.Query != "" {
			p.Q = &in.Query
		}
		if in.Scope != "" {
			p.Scope = new(api.ListAlertsParamsScope(in.Scope))
		}
		if in.GroupBy != "" {
			p.GroupBy = new(api.ListAlertsParamsGroupBy(in.GroupBy))
		}
		if in.Sort != "" {
			p.Sort = new(api.ListAlertsParamsSort(in.Sort))
		}
		if in.State != "" {
			states := []api.AlertLifecycle{}
			for v := range strings.SplitSeq(in.State, ",") {
				states = append(states, api.AlertLifecycle(v))
			}
			p.State = &states
		}
		if in.Severity != "" {
			values := []api.AlertSeverity{}
			for v := range strings.SplitSeq(in.Severity, ",") {
				values = append(values, api.AlertSeverity(v))
			}
			p.Severity = &values
		}
		response, err = c.ops.ListAlerts(ctx, p, editor)
	case "alerts get":
		response, err = c.ops.GetAlert(ctx, id, editor)
	case "alerts evidence":
		p := &api.GetAlertEvidenceParams{}
		if in.Selection != "" {
			p.Selection = new(api.GetAlertEvidenceParamsSelection(in.Selection))
		}
		response, err = c.ops.GetAlertEvidence(ctx, id, p, editor)
	case "alerts series":
		p := &api.GetAlertSeriesParams{ConditionId: in.ConditionID, Limit: &in.Limit}
		if in.Start != "" {
			value, _ := time.Parse(time.RFC3339Nano, in.Start)
			p.Start = &value
		}
		if in.End != "" {
			value, _ := time.Parse(time.RFC3339Nano, in.End)
			p.End = &value
		}
		response, err = c.ops.GetAlertSeries(ctx, id, p, editor)
	case "alerts transitions":
		response, err = c.ops.ListAlertTransitions(ctx, id, &api.ListAlertTransitionsParams{Limit: &in.Limit, Cursor: cursor}, editor)
	case "alerts acknowledge":
		response, err = c.ops.AcknowledgeAlert(ctx, id, &api.AcknowledgeAlertParams{IdempotencyKey: in.Key, IfMatch: in.ETag}, api.AlertAcknowledge{}, editor)
	case "alerts processing":
		response, err = c.ops.GetAlertProcessing(ctx, &api.GetAlertProcessingParams{PolicyId: id}, editor)
	case "alerts command-result":
		response, err = c.ops.GetAlertCommand(ctx, &api.GetAlertCommandParams{Operation: api.AlertOperation(in.Operation), IdempotencyKey: in.Key}, editor)
	}
	if err != nil || response == nil || response.Body == nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, "", failure(in, "management_unavailable", 0, in.Mutation())
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, "", failure(in, "invalid_response", response.StatusCode, in.Mutation())
	}
	expected := http.StatusOK
	if in.Action == "create" {
		expected = http.StatusCreated
	}
	if response.StatusCode != expected {
		f := failure(in, "management_unavailable", response.StatusCode, in.Mutation() && (response.StatusCode < 400 || response.StatusCode >= 500))
		var problem api.Problem
		if json.Unmarshal(data, &problem) == nil {
			if contains(problem.Code, "invalid_request", "unauthorized", "forbidden", "not_found", "version_conflict", "command_body_conflict", "command_expired", "name_conflict", "request_timeout", "request_canceled", "unavailable") && !f.Unknown {
				f.Code = problem.Code
				switch problem.Code {
				case "version_conflict":
					f.Message = "The resource changed. Read its current state and ETag before deciding how to proceed."
				case "name_conflict":
					f.Message = "An alert policy already uses that name. Choose another name."
				case "command_body_conflict":
					f.Message = "This idempotency key belongs to a different request. Reconcile the original command before proceeding."
				case "command_expired":
					f.Message = "The saved command result has expired. Inspect the current resource and history before deciding whether to submit another command."
				}
				for _, field := range problem.FieldErrors {
					if field.Field == "confirm_non_30_day_presets" && field.Code == "preset_confirmation_required" {
						f.Message = "These presets use a 30-day reference period. Confirm them explicitly for this SLO period or choose custom conditions."
						break
					}
				}
			}
			if validRequestID(problem.RequestId) {
				f.RequestID = problem.RequestId
			}
		}
		return nil, "", f
	}
	tag := response.Header.Get("ETag")
	if !validResponse(data, in, tag) {
		return nil, "", failure(in, "invalid_response", response.StatusCode, in.Mutation())
	}
	return data, tag, nil
}

func failure(in Input, code string, status int, unknown bool) *datasourceclient.Failure {
	message := "The alert request could not be completed."
	if code == "invalid_request" {
		message = "Check the operation, identifiers, flags and JSON input."
	}
	if unknown {
		code = "outcome_unknown"
		message = "The command may have completed. Use alerts command-result with the same profile. Original operation: " + in.CommandOperation() + "; original idempotency key: " + strconv.Quote(in.Key) + ". Do not submit a new key."
	}
	return &datasourceclient.Failure{Code: code, Message: message, Status: status, Unknown: unknown}
}
