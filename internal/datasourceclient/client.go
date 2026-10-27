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

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/setupclient"
)

const maxBody = 512 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var diagnosticCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

type Failure struct {
	Code    string
	Message string
	Status  int
	Unknown bool
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
		if json.Unmarshal(data, &value) != nil || !hasFields(data, "items") || len(value.Items) > 256 {
			return false
		}
		for _, descriptor := range value.Items {
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
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) {
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
	if failure, ok := errors.AsType[*Failure](err); ok && (failure.Status == 0 || failure.Status >= 500) {
		failure.Unknown = true
		failure.Code = "outcome_unknown"
		failure.Message = "preview outcome is unknown; it may still be running, so check agent and diagnostic status before starting another preview"
	}
	if err != nil {
		return nil, err
	}
	return safeDiagnostic(data)
}

func (c *Client) Diagnostic(ctx context.Context, rawID string) ([]byte, error) {
	id, err := parseID(rawID)
	if err != nil {
		return nil, err
	}
	r, err := c.ops.GetDiagnostic(ctx, id, c.editor)
	data, err := c.read(r, err, http.StatusOK, validDiagnostic)
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
	data, err := c.read(r, err, http.StatusOK, validDiagnostic)
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
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if readErr != nil || len(data) > maxBody {
		return nil, protocol()
	}
	if response.StatusCode != expected {
		var problem struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(data, &problem) != nil || problem.Code == "" {
			return nil, remote()
		}
		return nil, problemFailure(response.StatusCode, problem.Code)
	}
	if strings.Contains(string(data), `"password_secret"`) || strings.Contains(string(data), `"token_secret"`) || strings.Contains(string(data), `"password"`) || strings.Contains(string(data), `"token"`) {
		return nil, protocol()
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
	return !hasSecretValue(data) && !hasPlainSecretKey(data)
}

func validDatasourcePage(data []byte, page api.DatasourcePage) bool {
	if !hasFields(data, "items", "next_cursor") || len(page.Items) > 200 || len(page.NextCursor) > 512 {
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
	return !hasSecretValue(data) && !hasPlainSecretKey(data)
}

func validDiagnostic(data []byte) bool {
	var value api.Diagnostic
	var fields map[string]jsontext.Value
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "id", "state", "operation", "created_at", "deadline_at", "result_class", "result", "error_code", "message") || json.Unmarshal(data, &value) != nil || !uuidPattern.MatchString(uuidText(value.Id)) || !value.State.Valid() {
		return false
	}
	if value.Operation != api.DiagnosticOperationConnectionTest && value.Operation != api.DiagnosticOperationQueryPreview || value.CreatedAt.IsZero() || value.DeadlineAt.IsZero() {
		return false
	}
	if value.ResultClass != nil && !value.ResultClass.Valid() {
		return false
	}
	return !hasSecretValue(data) && !hasPlainSecretKey(data)
}

func safeDiagnostic(data []byte) ([]byte, error) {
	var value api.Diagnostic
	if json.Unmarshal(data, &value) != nil {
		return nil, protocol()
	}
	knownCodes := map[string]bool{"authentication_failed": true, "network_failed": true, "tls_failed": true, "query_failed": true, "rate_limited": true, "unsupported": true, "diagnostic_failed": true}
	if value.ErrorCode != nil && (!diagnosticCodePattern.MatchString(*value.ErrorCode) || !knownCodes[*value.ErrorCode]) {
		safeCode := "diagnostic_failed"
		value.ErrorCode = &safeCode
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

func hasSecretValue(data []byte) bool {
	return strings.Contains(string(data), `"password_secret"`) || strings.Contains(string(data), `"token_secret"`)
}
func hasPlainSecretKey(data []byte) bool {
	return strings.Contains(string(data), `"password"`) || strings.Contains(string(data), `"token"`)
}
func hasFields(data []byte, names ...string) bool {
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
	prefix := `"datasource:` + id + `:"`
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
func problemFailure(status int, code string) *Failure {
	messages := map[string]string{"unauthenticated": "sign in before managing datasources", "forbidden": "the current role is not allowed to perform this operation", "not_found": "the requested datasource or diagnostic was not found", "conflict": "the datasource changed; retrieve it and review the current settings", "invalid_datasource_input": "datasource settings were rejected", "diagnostic_unavailable": "the assigned agent cannot run this diagnostic", "request_timeout": "the datasource request timed out", "management_unavailable": "datasource management is temporarily unavailable"}
	message, known := messages[code]
	if !known || !diagnosticCodePattern.MatchString(code) {
		code = "management_unavailable"
		message = messages[code]
	}
	return &Failure{Code: code, Message: message, Status: status}
}
