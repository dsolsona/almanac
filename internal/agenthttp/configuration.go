package agenthttp

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/agentaccess"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authhttp"
)

type ConfigurationHandler struct {
	access *agentaccess.Service
	auth   *authhttp.Handler
	clock  func() time.Time
}

func NewConfigurationHandler(access *agentaccess.Service, auth *authhttp.Handler, clock func() time.Time) (*ConfigurationHandler, error) {
	if access == nil || auth == nil || clock == nil {
		return nil, errors.New("agenthttp: invalid configuration handler")
	}
	return &ConfigurationHandler{access: access, auth: auth, clock: clock}, nil
}

func (h *ConfigurationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id, err := newRequestID()
	if err != nil {
		writeConfigurationProblem(w, http.StatusServiceUnavailable, "agent_configuration_unavailable", "")
		return
	}
	w.Header().Set("X-Request-ID", id)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeConfigurationProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", id)
		return
	}
	if h.auth.ValidateTLSRequest(r) != nil || r.URL.RawQuery != "" || r.URL.ForceQuery || hasPositiveContent(r) || len(r.Header.Values("Cookie")) != 0 {
		writeConfigurationProblem(w, http.StatusBadRequest, "invalid_request", id)
		return
	}
	authorization := r.Header.Values("Authorization")
	if len(authorization) != 1 || len(authorization[0]) > 2048 {
		writeConfigurationProblem(w, http.StatusUnauthorized, "unauthorized", id)
		return
	}
	scheme, token, ok := strings.Cut(authorization[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		writeConfigurationProblem(w, http.StatusUnauthorized, "unauthorized", id)
		return
	}
	configuration, err := h.access.Configuration(r.Context(), token)
	if errors.Is(err, agentaccess.ErrInvalidCredential) {
		writeConfigurationProblem(w, http.StatusUnauthorized, "unauthorized", id)
		return
	}
	if err != nil {
		writeConfigurationProblem(w, http.StatusServiceUnavailable, "agent_configuration_unavailable", id)
		return
	}
	etag := `"agent-config:` + configuration.AgentID + `:` + strconv.FormatInt(configuration.Revision, 10) + `"`
	if values := r.Header.Values("If-None-Match"); len(values) != 0 {
		if len(values) != 1 || (values[0] != etag && values[0] != "*") {
			writeConfigurationProblem(w, http.StatusBadRequest, "invalid_request", id)
			return
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(configuration.Snapshot)
}

func writeConfigurationProblem(w http.ResponseWriter, status int, code, id string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{}, Message: "Agent configuration request failed", RequestId: id})
}

var _ http.Handler = (*ConfigurationHandler)(nil)
