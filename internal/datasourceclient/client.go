// Package datasourceclient implements the bounded authenticated CLI surface for
// datasource configuration and remote diagnostics.
package datasourceclient

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/setupclient"
	"github.com/dsolsona/almanac/internal/sli"
)

const (
	maxBody                   = 512 << 10
	maxDiagnosticResponseBody = (1 << 20) + (1 << 18)
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var diagnosticCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

type Failure struct {
	Code        string
	BackendCode string
	Message     string
	Status      int
	RequestID   string
	Unknown     bool
}

func (f *Failure) Error() string { return f.Message }

type Client struct {
	ops    setupclient.DatasourceOperations
	editor api.RequestEditorFn
}

func New(ops setupclient.DatasourceOperations, editor api.RequestEditorFn) *Client {
	return &Client{ops: ops, editor: editor}
}

func (c *Client) List(ctx context.Context, limit int, cursor string) ([]byte, error) {
	if limit < 1 || limit > 200 || len(cursor) > 512 {
		return nil, invalid()
	}
	p := &api.GetDatasourcesParams{Limit: &limit}
	if cursor != "" {
		p.Cursor = &cursor
	}
	r, err := c.ops.GetDatasources(ctx, p, c.editor)
	return c.read(r, err, http.StatusOK, func(data []byte) bool {
		var value api.DatasourcePage
		return json.Unmarshal(data, &value) == nil && len(value.Items) <= 200 && validDatasourcePage(data, value)
	})
}

func (c *Client) ListSLIs(ctx context.Context, limit int, cursor string) ([]byte, error) {
	if limit < 1 || limit > 200 || len(cursor) > 512 {
		return nil, invalid()
	}
	p := &api.ListSlisParams{Limit: pageLimit(limit)}
	if cursor != "" {
		p.Cursor = pageCursor(cursor)
	}
	r, err := c.ops.ListSlis(ctx, p, c.editor)
	return c.read(r, err, http.StatusOK, validSliPage)
}

func (c *Client) GetSLI(ctx context.Context, rawID string) ([]byte, string, error) {
	id, err := parseSliID(rawID)
	if err != nil {
		return nil, "", err
	}
	r, err := c.ops.GetSli(ctx, id, c.editor)
	data, err := c.read(r, err, http.StatusOK, validSli)
	if err != nil {
		return nil, "", err
	}
	var value api.Sli
	if json.Unmarshal(data, &value) != nil || uuidText(value.Id) != rawID {
		return nil, "", protocol()
	}
	etag := r.Header.Get("ETag")
	if etag != sliETag(rawID, value.Version) {
		return nil, "", protocol()
	}
	return data, etag, nil
}

func (c *Client) CreateSLI(ctx context.Context, value api.SliCreateRequest, key string) ([]byte, string, error) {
	if !validKey(key) {
		return nil, "", invalid()
	}
	params := &api.CreateSliParams{IdempotencyKey: api.IdempotencyKey(key)}
	r, err := c.ops.CreateSli(ctx, params, value, c.mutationEditor(key, ""))
	data, err := c.read(r, err, http.StatusCreated, validSli)
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) {
		failure.Unknown = true
		failure.Code = "outcome_unknown"
		failure.Message = "SLI creation outcome is unknown; repeat the exact command with the same idempotency key and protected input file"
	}
	if err != nil {
		return nil, "", err
	}
	var created api.Sli
	if json.Unmarshal(data, &created) != nil {
		return nil, "", protocol()
	}
	etag := r.Header.Get("ETag")
	if etag != sliETag(uuidText(created.Id), created.Version) {
		return nil, "", &Failure{Code: "outcome_unknown", Message: "SLI creation succeeded but its resource version could not be confirmed; inspect the SLI list before continuing", Unknown: true}
	}
	return data, etag, nil
}

func (c *Client) UpdateSLIMetadata(ctx context.Context, rawID, etag string, value api.SliMetadataWrite) ([]byte, string, error) {
	id, err := parseSliID(rawID)
	if err != nil {
		return nil, "", err
	}
	if sliETagVersion(etag, rawID) == "" {
		return nil, "", invalid()
	}
	params := &api.PatchSliMetadataParams{IfMatch: api.SliIfMatch(etag)}
	r, err := c.ops.PatchSliMetadata(ctx, id, params, value, c.mutationEditor("", etag))
	data, err := c.read(r, err, http.StatusOK, validSli)
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) {
		failure.Unknown = true
		failure.Code = "outcome_unknown"
		failure.Message = "SLI metadata update outcome is unknown; retrieve the SLI and reconcile its current version before retrying"
	}
	if err != nil {
		return nil, "", err
	}
	var updated api.Sli
	if json.Unmarshal(data, &updated) != nil {
		return nil, "", protocol()
	}
	newETag := r.Header.Get("ETag")
	if newETag != sliETag(rawID, updated.Version) {
		return nil, "", &Failure{Code: "outcome_unknown", Message: "SLI metadata update may have completed but its version could not be confirmed; retrieve the SLI before retrying", Unknown: true}
	}
	return data, newETag, nil
}

func (c *Client) SLIObservations(ctx context.Context, rawID string, limit int, cursor string) ([]byte, error) {
	id, err := parseSliID(rawID)
	if err != nil || limit < 1 || limit > 200 || len(cursor) > 512 {
		return nil, invalid()
	}
	p := &api.ListSliObservationsParams{Limit: pageLimit(limit)}
	if cursor != "" {
		p.Cursor = pageCursor(cursor)
	}
	r, err := c.ops.ListSliObservations(ctx, id, p, c.editor)
	return c.read(r, err, http.StatusOK, validSliObservationPage)
}

func (c *Client) Providers(ctx context.Context, agentID string) ([]byte, error) {
	var params *api.GetProvidersParams
	if agentID != "" {
		id, err := parseID(agentID)
		if err != nil {
			return nil, err
		}
		params = &api.GetProvidersParams{AgentId: &id}
	}
	r, err := c.ops.GetProviders(ctx, params, c.editor)
	return c.read(r, err, http.StatusOK, func(data []byte) bool {
		var value api.ProviderPage
		var fields map[string]jsontext.Value
		var raw struct {
			Items []jsontext.Value `json:"items"`
		}
		if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "items") || json.Unmarshal(data, &raw) != nil ||
			json.Unmarshal(data, &value) != nil || len(value.Items) > 256 || len(raw.Items) != len(value.Items) {
			return false
		}
		for index, descriptor := range value.Items {
			var descriptorFields map[string]jsontext.Value
			if json.Unmarshal(raw.Items[index], &descriptorFields) != nil || !exactFields(descriptorFields, "applied_configuration_revision", "availability", "configuration_failure_code", "configuration_failure_revision", "configuration_format", "desired_configuration_revision", "operations", "provider") {
				return false
			}
			if descriptor.Provider != api.ProviderDescriptorProviderPrometheus || descriptor.ConfigurationFormat != api.ProviderDescriptorConfigurationFormatPrometheusHttpV1 || !descriptor.Availability.Valid() || len(descriptor.Operations) > 16 {
				return false
			}
			for _, operation := range descriptor.Operations {
				if operation != "connection_test" && operation != "query_preview" {
					return false
				}
			}
		}
		return true
	})
}

func (c *Client) Get(ctx context.Context, rawID string) ([]byte, string, error) {
	id, err := parseID(rawID)
	if err != nil {
		return nil, "", err
	}
	r, err := c.ops.GetDatasource(ctx, id, c.editor)
	data, err := c.read(r, err, http.StatusOK, validDatasource)
	if err != nil {
		return nil, "", err
	}
	var value api.Datasource
	if json.Unmarshal(data, &value) != nil || uuidText(value.Id) != rawID {
		return nil, "", protocol()
	}
	etag := r.Header.Get("ETag")
	if etag != `"datasource:`+rawID+":"+value.Version+`"` {
		return nil, "", protocol()
	}
	return data, etag, nil
}

func (c *Client) Create(ctx context.Context, value api.DatasourceWrite, key string) ([]byte, error) {
	if !validKey(key) {
		return nil, invalid()
	}
	params := &api.PostDatasourcesParams{IdempotencyKey: api.IdempotencyKey(key)}
	r, err := c.ops.PostDatasources(ctx, params, value, c.mutationEditor(key, ""))
	data, err := c.read(r, err, http.StatusCreated, validDatasource)
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) {
		failure.Unknown = true
		failure.Code = "outcome_unknown"
		failure.Message = "create outcome is unknown; repeat the exact command with the same idempotency key and protected input file"
	}
	if err == nil && r.Header.Get("ETag") != `"datasource:`+mustDatasourceID(data)+":"+mustDatasourceVersion(data)+`"` {
		return nil, &Failure{Code: "outcome_unknown", Message: "create succeeded but its resource version could not be confirmed; inspect the datasource list before continuing", Unknown: true}
	}
	return data, err
}

func (c *Client) Update(ctx context.Context, rawID, etag string, value api.DatasourceWrite) ([]byte, error) {
	id, err := parseID(rawID)
	if err != nil {
		return nil, err
	}
	if etag != `"datasource:`+rawID+":"+etagVersion(etag, rawID)+`"` || etagVersion(etag, rawID) == "" {
		return nil, invalid()
	}
	params := &api.PutDatasourceParams{IfMatch: api.DatasourceIfMatch(etag)}
	r, err := c.ops.PutDatasource(ctx, id, params, value, c.mutationEditor("", etag))
	data, err := c.read(r, err, http.StatusOK, validDatasource)
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) {
		failure.Unknown = true
		failure.Code = "outcome_unknown"
		failure.Message = "update outcome is unknown; retrieve the datasource and reconcile its current version before retrying"
	}
	if err == nil && r.Header.Get("ETag") != `"datasource:`+rawID+":"+mustDatasourceVersion(data)+`"` {
		return nil, &Failure{Code: "outcome_unknown", Message: "update may have completed but its resource version could not be confirmed; retrieve the datasource and reconcile its current version", Unknown: true}
	}
	return data, err
}

func (c *Client) Test(ctx context.Context, value api.ConnectionTestRequest) ([]byte, error) {
	r, err := c.ops.PostDatasourceConnectionTest(ctx, value, c.mutationEditor("", ""))
	data, err := c.read(r, err, http.StatusAccepted, validDiagnostic)
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) && !definiteNoDiagnostic(failure.Code) {
		failure.Unknown = true
		failure.Code = "outcome_unknown"
		failure.Message = "diagnostic outcome is unknown; it may still be running, so check agent and diagnostic status before starting another test"
	}
	if err != nil {
		return nil, err
	}
	return safeDiagnostic(data)
}

func (c *Client) TestSaved(ctx context.Context, rawID, etag, key string) ([]byte, error) {
	id, err := parseID(rawID)
	if err != nil {
		return nil, err
	}
	if etagVersion(etag, rawID) == "" || !validKey(key) {
		return nil, invalid()
	}
	params := &api.PostSavedDatasourceConnectionTestParams{IfMatch: api.DatasourceIfMatch(etag), IdempotencyKey: api.IdempotencyKey(key)}
	r, err := c.ops.PostSavedDatasourceConnectionTest(ctx, id, params, api.EmptyAuthRequest{}, c.mutationEditor(key, etag))
	data, err := c.read(r, err, http.StatusAccepted, validDiagnostic)
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) && !definiteNoDiagnostic(failure.Code) {
		failure.Unknown = true
		failure.Code = "outcome_unknown"
		failure.Message = "diagnostic outcome is unknown; it may still be running, so check agent and diagnostic status before starting another test"
	}
	if err != nil {
		return nil, err
	}
	return safeDiagnostic(data)
}

func (c *Client) Preview(ctx context.Context, rawID, query string) ([]byte, error) {
	id, err := parseID(rawID)
	if err != nil {
		return nil, err
	}
	if query == "" || len(query) > 4096 {
		return nil, invalid()
	}
	r, err := c.ops.PostDatasourceQueryPreview(ctx, id, api.QueryPreviewRequest{Query: query}, c.mutationEditor("", ""))
	data, err := c.read(r, err, http.StatusAccepted, validDiagnostic)
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) && !definiteNoDiagnostic(failure.Code) {
		failure.Unknown = true
		failure.Code = "outcome_unknown"
		failure.Message = "preview outcome is unknown; it may still be running, so check agent and diagnostic status before starting another preview"
	}
	if err != nil {
		return nil, err
	}
	return safeDiagnostic(data)
}

func definiteNoDiagnostic(code string) bool {
	switch code {
	case "configuration_pending", "configuration_failed", "source_secret_unavailable", "invalid_agent", "datasource_unavailable", "agent_unavailable", "diagnostic_unavailable", "unsupported", "configuration_too_large":
		return true
	default:
		return false
	}
}

func (c *Client) Diagnostic(ctx context.Context, rawID string) ([]byte, error) {
	id, err := parseID(rawID)
	if err != nil {
		return nil, err
	}
	r, err := c.ops.GetDiagnostic(ctx, id, c.editor)
	data, err := c.readLimit(r, err, http.StatusOK, validDiagnostic, maxDiagnosticResponseBody)
	if err != nil {
		return nil, err
	}
	return safeDiagnostic(data)
}

func (c *Client) Cancel(ctx context.Context, rawID string) ([]byte, error) {
	id, err := parseID(rawID)
	if err != nil {
		return nil, err
	}
	r, err := c.ops.PostDiagnosticCancel(ctx, id, api.EmptyAuthRequest{}, c.mutationEditor("", ""))
	data, err := c.readLimit(r, err, http.StatusOK, validDiagnostic, maxDiagnosticResponseBody)
	if err != nil {
		return nil, err
	}
	return safeDiagnostic(data)
}

func (c *Client) mutationEditor(key, etag string) api.RequestEditorFn {
	return func(ctx context.Context, request *http.Request) error {
		if err := c.editor(ctx, request); err != nil {
			return err
		}
		request.GetBody = nil
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		if etag != "" {
			request.Header.Set("If-Match", etag)
		}
		return nil
	}
}

func (c *Client) read(response *http.Response, err error, expected int, validate func([]byte) bool) ([]byte, error) {
	return c.readLimit(response, err, expected, validate, maxBody)
}

func (c *Client) readLimit(response *http.Response, err error, expected int, validate func([]byte) bool, limit int64) ([]byte, error) {
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, network(ctxCanceled(err))
	}
	if response == nil {
		return nil, protocol()
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if readErr != nil || int64(len(data)) > limit {
		return nil, protocol()
	}
	if response.StatusCode != expected {
		var problem struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(data, &problem) != nil || problem.Code == "" {
			return nil, remote()
		}
		return nil, problemFailure(response.StatusCode, problem.Code, problem.RequestID)
	}
	if !validate(data) {
		return nil, protocol()
	}
	return data, nil
}

func validDatasource(data []byte) bool {
	var value api.Datasource
	var fields map[string]jsontext.Value
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "id", "name", "display_name", "version", "provider", "configuration_format", "endpoint", "tls", "authentication", "polling_interval_seconds", "settling_delay_seconds", "agent_id", "created_at", "updated_at") || json.Unmarshal(data, &value) != nil {
		return false
	}
	var tlsFields, authFields map[string]jsontext.Value
	if json.Unmarshal(fields["tls"], &tlsFields) != nil || !exactFields(tlsFields, "insecure_skip_verify", "ca_pem") || json.Unmarshal(fields["authentication"], &authFields) != nil || !exactFields(authFields, "mode", "username", "password_configured", "token_configured") {
		return false
	}
	if !uuidPattern.MatchString(uuidText(value.Id)) || !uuidPattern.MatchString(uuidText(value.AgentId)) || !positiveDecimal(value.Version) || value.Name == "" || value.DisplayName == "" || value.PollingIntervalSeconds < 15 || value.SettlingDelaySeconds != 60 || value.Provider != api.DatasourceProviderPrometheus || value.ConfigurationFormat != api.DatasourceConfigurationFormatPrometheusHttpV1 || !value.Authentication.Mode.Valid() || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() || value.Tls.CaPem != nil && len(*value.Tls.CaPem) > 64<<10 {
		return false
	}
	if _, err := strconv.ParseInt(value.Version, 10, 64); err != nil {
		return false
	}
	endpoint, err := url.Parse(value.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" || strings.Contains(value.Endpoint, "#") {
		return false
	}
	return true
}

func validSli(data []byte) bool {
	var value api.Sli
	var fields map[string]jsontext.Value
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields,
		"activation_state", "agent_id", "agent_reachability", "collection_progress", "component_mode", "created_at",
		"datasource_id", "datasource_version", "display_name", "effective_from", "freshness", "good_query", "id",
		"last_attempt_at", "last_observation_at", "latest_observation", "measurement_kind", "measurement_revision",
		"name", "next_expected_execution_at", "polling_interval_seconds", "settling_delay_seconds", "total_query", "updated_at", "version") || json.Unmarshal(data, &value) != nil {
		return false
	}
	if !uuidPattern.MatchString(uuidText(value.Id)) || !uuidPattern.MatchString(uuidText(value.DatasourceId)) || !uuidPattern.MatchString(uuidText(value.AgentId)) ||
		!positiveDecimal(value.Version) || !positiveDecimal(value.MeasurementRevision) || !positiveDecimal(value.DatasourceVersion) ||
		value.MeasurementKind != "event_counts" || value.ComponentMode != "interval_count" || value.Name == "" || value.DisplayName == "" ||
		value.PollingIntervalSeconds < 15 || value.SettlingDelaySeconds != 60 || value.EffectiveFrom.IsZero() || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() ||
		!value.ActivationState.Valid() || !value.AgentReachability.Valid() || !value.CollectionProgress.Valid() || !value.Freshness.Valid() {
		return false
	}
	if fields["latest_observation"].Kind() != jsontext.KindNull && !validSliObservation(fields["latest_observation"]) {
		return false
	}
	return true
}

func validSliPage(data []byte) bool {
	var value api.SliPage
	var fields map[string]jsontext.Value
	var raw struct {
		Items []jsontext.Value `json:"items"`
	}
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "items", "next_cursor") ||
		json.Unmarshal(data, &raw) != nil || json.Unmarshal(data, &value) != nil || len(raw.Items) != len(value.Items) || len(value.Items) > 200 || len(value.NextCursor) > 512 {
		return false
	}
	for _, item := range raw.Items {
		if !validSli(item) {
			return false
		}
	}
	return true
}

func validSliObservationPage(data []byte) bool {
	var value api.SliObservationPage
	var fields map[string]jsontext.Value
	var raw struct {
		Items []jsontext.Value `json:"items"`
	}
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "items", "next_cursor", "earliest_queryable_interval_start", "inspected_start", "inspected_end", "missing_intervals") ||
		json.Unmarshal(data, &raw) != nil || json.Unmarshal(data, &value) != nil || len(raw.Items) != len(value.Items) || len(value.Items) > 200 || len(value.NextCursor) > 512 {
		return false
	}
	if value.EarliestQueryableIntervalStart.IsZero() || len(value.MissingIntervals) > 200 || (value.InspectedStart == nil) != (value.InspectedEnd == nil) {
		return false
	}
	if value.InspectedStart != nil && !value.InspectedStart.Before(*value.InspectedEnd) {
		return false
	}
	for _, interval := range value.MissingIntervals {
		if interval.IntervalStart.IsZero() || !interval.IntervalStart.Before(interval.IntervalEnd) {
			return false
		}
	}
	for _, item := range raw.Items {
		if !validSliObservation(item) {
			return false
		}
	}
	return true
}

func validSliObservation(data []byte) bool {
	var value api.SliObservation
	var fields map[string]jsontext.Value
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "error_code", "good_count", "interval_end", "interval_start", "measurement_revision", "missing_components", "observation_id", "outcome", "recorded_at", "total_count") || json.Unmarshal(data, &value) != nil ||
		!uuidPattern.MatchString(uuidText(value.ObservationId)) || !positiveDecimal(value.MeasurementRevision) || value.IntervalStart.IsZero() || value.IntervalEnd.IsZero() || value.RecordedAt.IsZero() || !value.Outcome.Valid() {
		return false
	}
	for _, component := range value.MissingComponents {
		if !component.Valid() {
			return false
		}
	}
	if value.ErrorCode != nil && !value.ErrorCode.Valid() {
		return false
	}
	switch value.Outcome {
	case api.SliObservationOutcomeObserved:
		if value.GoodCount == nil || value.TotalCount == nil || value.ErrorCode != nil || len(value.MissingComponents) != 0 {
			return false
		}
		_, _, err := sli.ValidateCounts(*value.GoodCount, *value.TotalCount)
		return err == nil
	case api.SliObservationOutcomeCompleteEmpty:
		return value.GoodCount == nil && value.TotalCount == nil && value.ErrorCode == nil && len(value.MissingComponents) == 0
	case api.SliObservationOutcomeIncomplete:
		return value.GoodCount == nil && value.TotalCount == nil && value.ErrorCode == nil && len(value.MissingComponents) > 0
	case api.SliObservationOutcomeFailed:
		return value.GoodCount == nil && value.TotalCount == nil && value.ErrorCode != nil && len(value.MissingComponents) == 0
	default:
		return false
	}
}

func pageLimit(value int) *api.PageLimit {
	converted := api.PageLimit(value)
	return &converted
}

func pageCursor(value string) *api.PageCursor {
	converted := api.PageCursor(value)
	return &converted
}

func parseSliID(raw string) (api.SliId, error) {
	if !uuidPattern.MatchString(raw) {
		return api.SliId{}, invalid()
	}
	var id api.SliId
	if json.Unmarshal([]byte(strconv.Quote(raw)), &id) != nil {
		return api.SliId{}, invalid()
	}
	return id, nil
}

func sliETag(id, version string) string { return `"sli:` + id + ":" + version + `"` }

func sliETagVersion(etag, id string) string {
	prefix := `"sli:` + id + ":"
	if !strings.HasPrefix(etag, prefix) || !strings.HasSuffix(etag, `"`) {
		return ""
	}
	version := strings.TrimSuffix(strings.TrimPrefix(etag, prefix), `"`)
	if !positiveDecimal(version) {
		return ""
	}
	if _, err := strconv.ParseInt(version, 10, 64); err != nil {
		return ""
	}
	return version
}

func validDatasourcePage(data []byte, page api.DatasourcePage) bool {
	var fields map[string]jsontext.Value
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "items", "next_cursor") || len(page.Items) > 200 || len(page.NextCursor) > 512 {
		return false
	}
	var raw struct {
		Items []jsontext.Value `json:"items"`
	}
	if json.Unmarshal(data, &raw) != nil || len(raw.Items) != len(page.Items) {
		return false
	}
	for _, value := range raw.Items {
		if !validDatasource(value) {
			return false
		}
	}
	return true
}

func validDiagnostic(data []byte) bool {
	var value api.Diagnostic
	var fields map[string]jsontext.Value
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "id", "state", "operation", "created_at", "deadline_at", "result_class", "result", "error_code", "message") || json.Unmarshal(data, &value) != nil || !uuidPattern.MatchString(uuidText(value.Id)) || !value.State.Valid() {
		return false
	}
	if value.Operation != api.ConnectionTest && value.Operation != api.QueryPreview || value.CreatedAt.IsZero() || value.DeadlineAt.IsZero() {
		return false
	}
	if value.ResultClass != nil && !value.ResultClass.Valid() {
		return false
	}
	if fields["result"].Kind() != jsontext.KindNull && !validDiagnosticResult(fields["result"], uuidText(value.Id)) {
		return false
	}
	return true
}

func validDiagnosticResult(data jsontext.Value, diagnosticID string) bool {
	var result agentprotocol.DiagnosticResult
	if json.Unmarshal(data, &result, json.RejectUnknownMembers(true)) != nil || result.DiagnosticID != diagnosticID || result.State != "succeeded" || result.ValidateHTTPS() != nil {
		return false
	}
	return true
}

func safeDiagnostic(data []byte) ([]byte, error) {
	var value api.Diagnostic
	if json.Unmarshal(data, &value) != nil {
		return nil, protocol()
	}
	knownCodes := map[string]bool{"authentication_failed": true, "network_failed": true, "tls_failed": true, "query_failed": true, "request_timeout": true, "assignment_revoked": true, "rate_limited": true, "unsupported": true, "diagnostic_failed": true}
	if value.ErrorCode != nil && (!diagnosticCodePattern.MatchString(string(*value.ErrorCode)) || !knownCodes[string(*value.ErrorCode)]) {
		value.ErrorCode = new(api.DiagnosticErrorCode("diagnostic_failed"))
	}
	if value.ErrorCode != nil {
		safeMessage := "The diagnostic failed. Check agent connectivity and datasource settings."
		value.Message = &safeMessage
	} else if value.State == api.DiagnosticStateSucceeded {
		safeMessage := "The diagnostic completed."
		value.Message = &safeMessage
	} else {
		value.Message = nil
	}
	return json.Marshal(value)
}

func exactFields(value map[string]jsontext.Value, names ...string) bool {
	if len(value) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := value[name]; !ok {
			return false
		}
	}
	return true
}

func parseID(raw string) (api.DatasourceId, error) {
	if !uuidPattern.MatchString(raw) {
		return api.DatasourceId{}, invalid()
	}
	var id api.DatasourceId
	if json.Unmarshal([]byte(strconv.Quote(raw)), &id) != nil {
		return api.DatasourceId{}, invalid()
	}
	return id, nil
}
func validKey(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\t ")
}
func etagVersion(etag, id string) string {
	prefix := `"datasource:` + id + `:`
	if !strings.HasPrefix(etag, prefix) || !strings.HasSuffix(etag, `"`) {
		return ""
	}
	version := strings.TrimSuffix(strings.TrimPrefix(etag, prefix), `"`)
	if !positiveDecimal(version) {
		return ""
	}
	if _, err := strconv.ParseInt(version, 10, 64); err != nil {
		return ""
	}
	return version
}
func positiveDecimal(version string) bool {
	if version == "" || version[0] == '0' {
		return false
	}
	for _, char := range version {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
func mustDatasourceID(data []byte) string {
	var value api.Datasource
	_ = json.Unmarshal(data, &value)
	return uuidText(value.Id)
}
func mustDatasourceVersion(data []byte) string {
	var value api.Datasource
	_ = json.Unmarshal(data, &value)
	return value.Version
}
func uuidText(value api.DatasourceId) string {
	data, _ := json.Marshal(value)
	var result string
	_ = json.Unmarshal(data, &result)
	return result
}
func ctxCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
func invalid() *Failure {
	return &Failure{Code: "invalid_request", Message: "datasource command input is invalid"}
}
func protocol() *Failure {
	return &Failure{Code: "invalid_response", Message: "the backend returned an invalid or unsafe datasource response"}
}
func remote() *Failure {
	return &Failure{Code: "management_unavailable", Message: "datasource management is temporarily unavailable"}
}
func network(canceled bool) *Failure {
	if canceled {
		return &Failure{Code: "request_canceled", Message: "the datasource request was canceled"}
	}
	return remote()
}
func problemFailure(status int, code, requestID string) *Failure {
	messages := map[string]string{"unauthorized": "sign in before managing datasources", "unauthenticated": "sign in before managing datasources", "forbidden": "the current role is not allowed to perform this operation", "not_found": "the requested datasource, SLI or diagnostic was not found", "conflict": "the resource changed; retrieve it and review the current settings", "measurement_conflict": "the datasource cadence or agent assignment cannot change while an SLI uses it", "invalid_request": "request input is invalid", "invalid_datasource_input": "datasource settings were rejected", "configuration_pending": "the agent is applying datasource settings; no diagnostic was started, so wait for readiness and retry", "configuration_failed": "the assigned agent could not apply its datasource configuration", "source_secret_unavailable": "the stored datasource credential is unavailable; update credentials before retrying", "invalid_agent": "the assigned agent is invalid or unavailable", "datasource_unavailable": "the datasource is unavailable for this operation", "unsupported": "the assigned agent does not support this diagnostic", "unsupported_measurement": "this measurement kind or component mode is not supported by the current SLI collector", "diagnostic_unavailable": "the assigned agent cannot run this diagnostic", "agent_unavailable": "the assigned agent is unavailable for this operation", "request_timeout": "the datasource request timed out", "configuration_too_large": "the datasource configuration exceeds the agent assignment limits", "sli_unavailable": "SLI management is temporarily unavailable", "observation_unavailable": "observation ingestion is temporarily unavailable", "management_unavailable": "management is temporarily unavailable"}
	message, known := messages[code]
	backendCode := ""
	if !known || !diagnosticCodePattern.MatchString(code) {
		code = "management_unavailable"
		message = messages[code]
	} else {
		backendCode = code
	}
	if !validRequestID(requestID) {
		requestID = ""
	}
	return &Failure{Code: code, BackendCode: backendCode, Message: message, Status: status, RequestID: requestID}
}

func validRequestID(value string) bool {
	if len(value) == 36 {
		for i, r := range value {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if r != '-' {
					return false
				}
				continue
			}
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return true
	}
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
