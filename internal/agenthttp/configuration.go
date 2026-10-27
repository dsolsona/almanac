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
	if configuration.Pending {
		writeConfigurationProblem(w, http.StatusServiceUnavailable, "agent_configuration_unavailable", id)
		return
	}
	etag := `"agent-config:` + configuration.AgentID + `:` + strconv.FormatInt(configuration.Revision, 10) + `"`
	if values := r.Header.Values("If-None-Match"); len(values) != 0 {
		matched, valid := ifNoneMatch(values, etag)
		if !valid {
			writeConfigurationProblem(w, http.StatusBadRequest, "invalid_request", id)
			return
		}
		if matched {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(configuration.Snapshot)
}

func ifNoneMatch(values []string, current string) (matched, valid bool) {
	if len(values) == 0 {
		return false, false
	}
	value := strings.Join(values, ",")
	i := 0
	skipWhitespace := func() {
		for i < len(value) && (value[i] == ' ' || value[i] == '\t') {
			i++
		}
	}
	skipWhitespace()
	if i < len(value) && value[i] == '*' {
		i++
		skipWhitespace()
		if i != len(value) {
			return false, false
		}
		return true, true
	}
	if i == len(value) {
		return false, false
	}
	for {
		if strings.HasPrefix(value[i:], "W/") {
			i += 2
		}
		if i >= len(value) || value[i] != '"' {
			return false, false
		}
		start := i
		i++
		for i < len(value) && value[i] != '"' {
			c := value[i]
			if c != 0x21 && (c < 0x23 || c > 0x7e) && c < 0x80 {
				return false, false
			}
			i++
		}
		if i == len(value) {
			return false, false
		}
		i++
		candidate := value[start:i]
		if candidate == current {
			matched = true
		}
		skipWhitespace()
		if i == len(value) {
			return matched, true
		}
		if value[i] != ',' {
			return false, false
		}
		i++
		skipWhitespace()
		if i == len(value) {
			return false, false
		}
	}
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
