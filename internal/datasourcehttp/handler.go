// Package datasourcehttp exposes redacted Prometheus datasource management.
package datasourcehttp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/google/uuid"
	"github.com/oapi-codegen/runtime/types"
)

const maxRequestBytes = 80 << 10

type Options struct {
	Now       func() time.Time
	Preflight func(context.Context) error
}

type Handler struct {
	databaseURL string
	secrets     *sourcesecret.Adapter
	auth        *authhttp.Handler
	now         func() time.Time
	preflight   func(context.Context) error
}

func New(databaseURL string, secrets *sourcesecret.Adapter, auth *authhttp.Handler, options Options) (*Handler, error) {
	if databaseURL == "" || auth == nil || options.Now == nil {
		return nil, errors.New("datasourcehttp: invalid options")
	}
	return &Handler{databaseURL: databaseURL, secrets: secrets, auth: auth, now: options.Now, preflight: options.Preflight}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	requestID, err := newRequestID()
	if err != nil {
		h.writeProblem(w, http.StatusServiceUnavailable, "datasource_unavailable", "")
		return
	}
	w.Header().Set("X-Request-ID", requestID)
	path := r.URL.EscapedPath()
	if path != r.URL.Path || strings.ContainsAny(path, "%\\") || strings.Contains(path, "/../") || strings.Contains(path, "/./") {
		h.writeProblem(w, http.StatusNotFound, "not_found", requestID)
		return
	}
	if h.preflight != nil {
		if err := h.preflight(r.Context()); err != nil {
			h.writeProblem(w, http.StatusServiceUnavailable, "datasource_unavailable", requestID)
			return
		}
	}
	authorized, err := h.auth.ValidateSessionRequest(r)
	if err != nil {
		h.writeAuthError(w, err, requestID)
		return
	}
	verifier, err := identity.SessionSecretVerifier(authorized.SessionSecret)
	if err != nil {
		h.writeProblem(w, http.StatusUnauthorized, "unauthorized", requestID)
		return
	}
	proof := storage.DatasourceProof{SessionVerifier: verifier}
	if strings.HasPrefix(path, "/api/v1/diagnostics/") || path == "/api/v1/datasources/connection-tests" || strings.Contains(path, "/query-previews") {
		h.writeProblem(w, http.StatusServiceUnavailable, "diagnostics_unavailable", requestID)
		return
	}
	if path == "/api/v1/providers" {
		h.providers(w, r, requestID)
		return
	}
	if path == "/api/v1/datasources" {
		h.collection(w, r, requestID, proof)
		return
	}
	const prefix = "/api/v1/datasources/"
	if !strings.HasPrefix(path, prefix) {
		h.writeProblem(w, http.StatusNotFound, "not_found", requestID)
		return
	}
	datasourceID := strings.TrimPrefix(path, prefix)
	if datasourceID == "" || strings.Contains(datasourceID, "/") {
		h.writeProblem(w, http.StatusNotFound, "not_found", requestID)
		return
	}
	h.detail(w, r, requestID, proof, datasourceID)
}

func (h *Handler) collection(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if hasBody(r) || hasMutationHeaders(r) || !onlyQueryKeys(query, "limit", "cursor") {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		limit := 0
		if values := query["limit"]; len(values) > 1 {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		} else if len(values) == 1 {
			limit, err = strconv.Atoi(values[0])
			if err != nil {
				h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
				return
			}
		}
		cursor := ""
		if values := query["cursor"]; len(values) > 1 {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		} else if len(values) == 1 {
			cursor = values[0]
		}
		page, err := storage.ListDatasources(r.Context(), h.databaseURL, proof, limit, cursor, h.now)
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		items := make([]api.Datasource, 0, len(page.Items))
		for _, record := range page.Items {
			items = append(items, datasourceDTO(record))
		}
		h.writeJSON(w, http.StatusOK, api.DatasourcePage{Items: items, NextCursor: page.NextCursor})
	case http.MethodPost:
		if r.URL.RawQuery != "" || r.URL.ForceQuery || hasConditionalHeaders(r) || !singlePrintableHeader(r.Header, "Idempotency-Key", 1, 128) || !jsonContent(r) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		body, err := readBody(r)
		if err != nil {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		var write api.DatasourceWrite
		if json.Unmarshal(body, &write, json.RejectUnknownMembers(true)) != nil {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		settings, password, token, valid := mapWrite(write)
		if !valid {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		created, err := storage.CreateDatasource(r.Context(), h.databaseURL, h.secrets, storage.CreateDatasourceInput{
			Proof: proof, Settings: settings, Password: password, Token: token,
			IdempotencyKey: r.Header.Get("Idempotency-Key"), RequestID: requestID, Now: h.now,
		})
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		w.Header().Set("ETag", datasourceETag(created.ID, created.Version))
		h.writeJSON(w, http.StatusCreated, datasourceDTO(created))
	default:
		w.Header().Set("Allow", "GET, POST")
		h.writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
	}
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request, requestID string, proof storage.DatasourceProof, datasourceID string) {
	if _, err := uuid.Parse(datasourceID); err != nil {
		h.writeProblem(w, http.StatusNotFound, "not_found", requestID)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.RawQuery != "" || r.URL.ForceQuery || hasBody(r) || hasConditionalHeaders(r) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		record, err := storage.GetDatasource(r.Context(), h.databaseURL, datasourceID, proof, h.now)
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		w.Header().Set("ETag", datasourceETag(record.ID, record.Version))
		h.writeJSON(w, http.StatusOK, datasourceDTO(record))
	case http.MethodPut:
		if r.URL.RawQuery != "" || r.URL.ForceQuery || !singleETag(r.Header.Values("If-Match"), datasourceID) || !jsonContent(r) {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		body, err := readBody(r)
		if err != nil {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		var write api.DatasourceWrite
		if json.Unmarshal(body, &write, json.RejectUnknownMembers(true)) != nil {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		settings, password, token, valid := mapWrite(write)
		if !valid {
			h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
			return
		}
		versionText := strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("If-Match"), "\"datasource:"+datasourceID+":"), "\"")
		version, _ := strconv.ParseInt(versionText, 10, 64)
		updated, err := storage.UpdateDatasource(r.Context(), h.databaseURL, h.secrets, storage.UpdateDatasourceInput{
			Proof: proof, DatasourceID: datasourceID, ExpectedVersion: version, Settings: settings,
			Password: password, Token: token, RequestID: requestID, Now: h.now,
		})
		if err != nil {
			h.writeStorageError(w, err, requestID)
			return
		}
		w.Header().Set("ETag", datasourceETag(updated.ID, updated.Version))
		h.writeJSON(w, http.StatusOK, datasourceDTO(updated))
	default:
		w.Header().Set("Allow", "GET, PUT")
		h.writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
	}
}

func (h *Handler) providers(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		h.writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", requestID)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !onlyQueryKeys(query, "agent_id") || len(query["agent_id"]) > 1 || hasBody(r) || hasConditionalHeaders(r) {
		h.writeProblem(w, http.StatusBadRequest, "invalid_request", requestID)
		return
	}
	availability := api.ProviderDescriptorAvailability("available")
	if len(query["agent_id"]) == 1 {
		availability = "unsupported"
	}
	h.writeJSON(w, http.StatusOK, api.ProviderPage{Items: []api.ProviderDescriptor{{Provider: "prometheus", ConfigurationFormat: "prometheus-http-v1", Operations: []string{"connection_test", "query_preview"}, Availability: availability}}})
}

func mapWrite(write api.DatasourceWrite) (datasource.Settings, datasource.SecretAction, datasource.SecretAction, bool) {
	if write.Tls.CaPem == nil || write.Authentication.Username == nil && write.Authentication.Mode == "basic" {
		return datasource.Settings{}, datasource.SecretAction{}, datasource.SecretAction{}, false
	}
	settings := datasource.Settings{Name: write.Name, DisplayName: write.DisplayName,
		Provider: string(write.Provider), ConfigurationFormat: string(write.ConfigurationFormat), Endpoint: write.Endpoint,
		TLS:                datasource.TLSSettings{InsecureSkipVerify: write.Tls.InsecureSkipVerify, CAPEM: *write.Tls.CaPem},
		AuthenticationMode: string(write.Authentication.Mode), PollingIntervalSeconds: write.PollingIntervalSeconds,
		SettlingDelaySeconds: datasource.PrometheusSettlingDelay, AgentID: write.AgentId.String()}
	if write.Authentication.Username != nil {
		settings.AuthenticationUser = *write.Authentication.Username
	}
	if settings.Validate() != nil {
		return datasource.Settings{}, datasource.SecretAction{}, datasource.SecretAction{}, false
	}
	return settings,
		datasource.SecretAction{Action: string(write.Authentication.PasswordSecret.Action), Value: optionalString(write.Authentication.PasswordSecret.Value)},
		datasource.SecretAction{Action: string(write.Authentication.TokenSecret.Action), Value: optionalString(write.Authentication.TokenSecret.Value)}, true
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func datasourceDTO(record storage.DatasourceRecord) api.Datasource {
	id, _ := uuid.Parse(record.ID)
	agentID, _ := uuid.Parse(record.Settings.AgentID)
	caPEM := record.Settings.TLS.CAPEM
	var username *string
	if record.Settings.AuthenticationUser != "" {
		value := record.Settings.AuthenticationUser
		username = &value
	}
	return api.Datasource{Id: types.UUID(id), AgentId: types.UUID(agentID), Name: record.Settings.Name, DisplayName: record.Settings.DisplayName,
		Version: strconv.FormatInt(record.Version, 10), Provider: api.DatasourceProvider(record.Settings.Provider),
		ConfigurationFormat: api.DatasourceConfigurationFormat(record.Settings.ConfigurationFormat), Endpoint: record.Settings.Endpoint,
		Tls: api.DatasourceTLS{CaPem: &caPEM, InsecureSkipVerify: record.Settings.TLS.InsecureSkipVerify},
		Authentication: api.DatasourceAuthentication{Mode: api.DatasourceAuthenticationMode(record.Settings.AuthenticationMode), Username: username,
			PasswordConfigured: record.PasswordConfigured, TokenConfigured: record.TokenConfigured},
		PollingIntervalSeconds: record.Settings.PollingIntervalSeconds, SettlingDelaySeconds: api.DatasourceSettlingDelaySeconds(record.Settings.SettlingDelaySeconds),
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}

func datasourceETag(id string, version int64) string {
	return `"datasource:` + id + `:` + strconv.FormatInt(version, 10) + `"`
}

func singleETag(values []string, id string) bool {
	if len(values) != 1 {
		return false
	}
	value := values[0]
	if strings.ContainsAny(value, "\r\n") || !strings.HasPrefix(value, `"datasource:`+id+":") || !strings.HasSuffix(value, `"`) {
		return false
	}
	version := strings.TrimSuffix(strings.TrimPrefix(value, "\"datasource:"+id+":"), "\"")
	number, err := strconv.ParseInt(version, 10, 64)
	return err == nil && number > 0 && strconv.FormatInt(number, 10) == version
}

func (h *Handler) writeStorageError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, storage.ErrInvalidSession):
		h.writeProblem(w, 401, "unauthorized", requestID)
	case errors.Is(err, storage.ErrAgentForbidden):
		h.writeProblem(w, 403, "forbidden", requestID)
	case errors.Is(err, storage.ErrDatasourceNotFound):
		h.writeProblem(w, 404, "not_found", requestID)
	case errors.Is(err, storage.ErrDatasourceConflict), errors.Is(err, storage.ErrAgentConflict):
		h.writeProblem(w, 409, "conflict", requestID)
	case errors.Is(err, storage.ErrAgentNotFound):
		h.writeProblem(w, 422, "invalid_agent", requestID)
	case errors.Is(err, storage.ErrSourceSecretUnavailable):
		h.writeProblem(w, 503, "source_secret_unavailable", requestID)
	case errors.Is(err, storage.ErrInvalidAgentInput), errors.Is(err, datasource.ErrInvalidSettings):
		h.writeProblem(w, 400, "invalid_request", requestID)
	default:
		h.writeProblem(w, 503, "datasource_unavailable", requestID)
	}
}

func (h *Handler) writeAuthError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, authhttp.ErrMissingSession), errors.Is(err, authhttp.ErrSessionRequest):
		h.writeProblem(w, 401, "unauthorized", requestID)
	case errors.Is(err, authhttp.ErrAuthorization):
		h.writeProblem(w, 400, "invalid_request", requestID)
	default:
		h.writeProblem(w, 403, "forbidden", requestID)
	}
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, value)
}

func (h *Handler) writeProblem(w http.ResponseWriter, status int, code, requestID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{}, Message: "Datasource request failed", RequestId: requestID})
}

func newRequestID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
func hasBody(r *http.Request) bool { return r.ContentLength > 0 || len(r.TransferEncoding) > 0 }
func hasConditionalHeaders(r *http.Request) bool {
	return len(r.Header.Values("If-Match")) > 0 || len(r.Header.Values("If-None-Match")) > 0
}
func hasMutationHeaders(r *http.Request) bool { return hasConditionalHeaders(r) }

func jsonContent(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	return err == nil && mediaType == "application/json" && (params["charset"] == "" || strings.EqualFold(params["charset"], "utf-8"))
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxRequestBytes {
		return nil, fmt.Errorf("datasource request body invalid")
	}
	return data, nil
}

func onlyQueryKeys(query url.Values, allowed ...string) bool {
	for key := range query {
		if !slices.Contains(allowed, key) {
			return false
		}
	}
	return true
}

func singlePrintableHeader(header http.Header, name string, min, max int) bool {
	values := header.Values(name)
	if len(values) != 1 || len(values[0]) < min || len(values[0]) > max {
		return false
	}
	for _, r := range values[0] {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
