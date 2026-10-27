// Package agenthttp exposes the bounded, cookie-authenticated agent management API.
package agenthttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/agentmanagement"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/storage"
	googleuuid "github.com/google/uuid"
)

const bodyLimit = 4096

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

type Options struct{ Preflight func(context.Context) error }
type Handler struct {
	service   *agentmanagement.Service
	auth      *authhttp.Handler
	preflight func(context.Context) error
}

func New(service *agentmanagement.Service, auth *authhttp.Handler, options Options) (*Handler, error) {
	if service == nil || auth == nil {
		return nil, errors.New("agenthttp: invalid options")
	}
	return &Handler{service: service, auth: auth, preflight: options.Preflight}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id, err := newRequestID()
	if err != nil {
		h.writeProblem(w, http.StatusServiceUnavailable, "management_unavailable", id)
		return
	}
	w.Header().Set("X-Request-ID", id)
	path := r.URL.EscapedPath()
	if path != r.URL.Path || strings.ContainsAny(path, "%\\") || strings.Contains(path, "/../") || strings.Contains(path, "/./") {
		h.writeProblem(w, 404, "not_found", id)
		return
	}
	route, ids, ok := matchRoute(path)
	if !ok {
		h.writeProblem(w, 404, "not_found", id)
		return
	}
	if route == "collection" {
		switch r.Method {
		case http.MethodGet:
			route = "list"
		case http.MethodPost:
			route = "create"
		default:
			w.Header().Set("Allow", "GET, POST")
			h.writeProblem(w, 405, "method_not_allowed", id)
			return
		}
	}
	wantMethod := http.MethodGet
	if route == "create" || route == "disable" || route == "rotate" || route == "revoke" {
		wantMethod = http.MethodPost
	}
	if r.Method != wantMethod {
		w.Header().Set("Allow", wantMethod)
		h.writeProblem(w, 405, "method_not_allowed", id)
		return
	}
	authorized, err := h.auth.ValidateSessionRequest(r)
	if err != nil {
		h.writeBoundaryError(w, err, id)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	if h.preflight != nil {
		if err := h.preflight(r.Context()); err != nil {
			h.writeProblem(w, 503, "management_unavailable", id)
			return
		}
	}
	if route == "list" || route == "credentials" {
		limit, cursor, valid := pageQuery(query, route == "credentials")
		if !valid || hasPositiveContent(r) || hasConditionalHeaders(r) {
			h.writeProblem(w, 400, "invalid_request", id)
			return
		}
		if route == "list" {
			page, err := h.service.List(r.Context(), agentmanagement.ListInput{SessionSecret: authorized.SessionSecret, Limit: limit, Cursor: cursor})
			if err != nil {
				h.writeReadError(w, err, id)
				return
			}
			h.writeJSON(w, 200, pageDTO(page))
			return
		}
		page, err := h.service.Credentials(r.Context(), agentmanagement.CredentialsInput{SessionSecret: authorized.SessionSecret, AgentID: ids[0], Limit: limit, Cursor: cursor})
		if err != nil {
			h.writeReadError(w, err, id)
			return
		}
		h.writeJSON(w, 200, credentialPageDTO(page))
		return
	}
	if route == "detail" {
		if r.URL.RawQuery != "" || r.URL.ForceQuery || hasPositiveContent(r) || hasConditionalHeaders(r) {
			h.writeProblem(w, 400, "invalid_request", id)
			return
		}
		value, err := h.service.Get(r.Context(), agentmanagement.GetInput{SessionSecret: authorized.SessionSecret, AgentID: ids[0]})
		if err != nil {
			h.writeError(w, err, id)
			return
		}
		dto := agentDTO(value)
		w.Header().Set("ETag", agentETag(value.ID, value.Version))
		h.writeJSON(w, 200, dto)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || !validIdempotencyKey(r.Header.Values("Idempotency-Key")) {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	version := int64(0)
	if route == "create" {
		if len(r.Header.Values("If-Match")) != 0 {
			h.writeProblem(w, 400, "invalid_request", id)
			return
		}
	} else {
		var valid bool
		version, valid = parseETag(r.Header.Values("If-Match"), ids[0])
		if !valid {
			h.writeProblem(w, 400, "invalid_request", id)
			return
		}
	}
	body, status := readBody(w, r)
	if status != 0 {
		code := map[int]string{408: "request_timeout", 413: "request_too_large", 415: "unsupported_media_type"}[status]
		if status == 503 {
			code = "management_unavailable"
		}
		if code == "" {
			code = "invalid_request"
		}
		h.writeProblem(w, status, code, id)
		return
	}
	if route == "create" {
		h.create(w, r, body, authorized.SessionSecret, id)
		return
	}
	if route == "rotate" {
		h.rotate(w, r, body, ids[0], version, authorized.SessionSecret, authorized.Source, id)
		return
	}
	if route == "disable" {
		h.disable(w, r, body, ids[0], version, authorized.SessionSecret, authorized.Source, id)
		return
	}
	h.revoke(w, r, body, ids[0], ids[1], version, authorized.SessionSecret, authorized.Source, id)
}

func matchRoute(path string) (string, []string, bool) {
	if path == "/api/v1/agents" {
		return "collection", nil, true
	}
	if !strings.HasPrefix(path, "/api/v1/agents/") {
		return "", nil, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/agents/"), "/")
	if len(parts) == 1 && canonicalID(parts[0]) {
		return "detail", parts, true
	}
	if len(parts) == 2 && canonicalID(parts[0]) && parts[1] == "credentials" {
		return "credentials", parts[:1], true
	}
	if len(parts) == 2 && canonicalID(parts[0]) && parts[1] == "disable" {
		return "disable", parts[:1], true
	}
	if len(parts) == 3 && canonicalID(parts[0]) && parts[1] == "credentials" && parts[2] == "rotate" {
		return "rotate", parts[:1], true
	}
	if len(parts) == 4 && canonicalID(parts[0]) && parts[1] == "credentials" && canonicalID(parts[2]) && parts[3] == "revoke" {
		return "revoke", []string{parts[0], parts[2]}, true
	}
	return "", nil, false
}

func canonicalID(value string) bool {
	id, err := googleuuid.Parse(value)
	return err == nil && id.String() == value
}

func pageQuery(values map[string][]string, credentials bool) (int, string, bool) {
	for key := range values {
		if key != "limit" && key != "cursor" {
			return 0, "", false
		}
	}
	limit := 50
	if raw, ok := values["limit"]; ok {
		if len(raw) != 1 || raw[0] == "" {
			return 0, "", false
		}
		n, err := strconv.Atoi(raw[0])
		if err != nil || strconv.Itoa(n) != raw[0] || n < 1 || n > 200 {
			return 0, "", false
		}
		limit = n
	}
	cursor := ""
	if raw, ok := values["cursor"]; ok {
		if len(raw) != 1 {
			return 0, "", false
		}
		cursor = raw[0]
	}
	return limit, cursor, true
}

func hasPositiveContent(r *http.Request) bool {
	return r.ContentLength > 0 || len(r.TransferEncoding) > 0 || len(r.Header.Values("Transfer-Encoding")) > 0
}
func hasConditionalHeaders(r *http.Request) bool {
	return len(r.Header.Values("If-Match")) > 0 || len(r.Header.Values("Idempotency-Key")) > 0
}
func validIdempotencyKey(values []string) bool {
	if len(values) != 1 || len(values[0]) < 1 || len(values[0]) > 128 {
		return false
	}
	for i := 0; i < len(values[0]); i++ {
		if values[0][i] < 33 || values[0][i] > 126 {
			return false
		}
	}
	return true
}
func parseETag(values []string, id string) (int64, bool) {
	if len(values) != 1 {
		return 0, false
	}
	value := values[0]
	prefix := "\"agent:" + id + ":"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, "\"") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(value, prefix), "\"")
	if raw == "" || raw[0] == '0' {
		return 0, false
	}
	for i := range raw {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	version, err := strconv.ParseInt(raw, 10, 64)
	return version, err == nil && version > 0
}
func agentETag(id string, version int64) string { return fmt.Sprintf("\"agent:%s:%d\"", id, version) }

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, int) {
	if len(r.Header.Values("Content-Encoding")) != 0 {
		return nil, 415
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return nil, 415
	}
	media, params, err := mime.ParseMediaType(values[0])
	if err != nil || media != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
		return nil, 415
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, 503
	}
	if r.ContentLength > bodyLimit {
		return nil, 413
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, 413
		}
		if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
			return nil, 408
		}
		return nil, 400
	}
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		return nil, 503
	}
	return data, 0
}

type createBody struct {
	Name            *string        `json:"name"`
	DisplayName     *string        `json:"display_name"`
	LifetimeSeconds jsontext.Value `json:"lifetime_seconds"`
}
type rotateBody struct {
	CurrentPassword *string        `json:"current_password"`
	LifetimeSeconds jsontext.Value `json:"lifetime_seconds"`
	OverlapSeconds  jsontext.Value `json:"overlap_seconds"`
}
type proofBody struct {
	CurrentPassword *string `json:"current_password"`
}

func decode(data []byte, target any) bool {
	if len(bytes.TrimSpace(data)) == 0 {
		return false
	}
	return json.Unmarshal(data, target, json.RejectUnknownMembers(true)) == nil
}

func seconds(value jsontext.Value, max int64, zeroDefault bool) (int64, bool) {
	if len(value) == 0 {
		return 0, true
	}
	if bytes.Equal(value, []byte("null")) {
		return 0, false
	}
	for i, digit := range value {
		if digit < '0' || digit > '9' || (i == 0 && digit == '0' && len(value) > 1) {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil || n < 0 || n > max || (!zeroDefault && n == 0) {
		return 0, false
	}
	return n, true
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request, data []byte, session, id string) {
	var body createBody
	if !decode(data, &body) || body.Name == nil || body.DisplayName == nil {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	lifetime, ok := seconds(body.LifetimeSeconds, 31536000, true)
	if !ok {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	result, err := h.service.Create(r.Context(), agentmanagement.CreateInput{SessionSecret: session, RequestID: id, IdempotencyKey: r.Header.Get("Idempotency-Key"), Name: *body.Name, DisplayName: *body.DisplayName, Lifetime: time.Duration(lifetime) * time.Second})
	if err != nil {
		h.writeError(w, err, id)
		return
	}
	base := api.AgentIssuance{Agent: agentDTO(result.Creation.Agent), Credential: creationCredentialDTO(result.Creation.Credential), Replayed: result.Creation.Replayed}
	if result.Creation.Replayed {
		dto := api.AgentIssuanceReplay{Agent: base.Agent, Credential: base.Credential, Replayed: true, SecretState: api.AgentIssuanceReplaySecretStateSecretUnavailable}
		h.writeJSON(w, 200, dto)
	} else {
		dto := api.AgentIssuanceFresh{Agent: base.Agent, Credential: base.Credential, Replayed: api.AgentIssuanceFreshReplayedFalse, SecretState: api.AgentIssuanceFreshSecretStateIssued, Token: result.Token}
		h.writeJSON(w, 201, dto)
	}
}
func (h *Handler) rotate(w http.ResponseWriter, r *http.Request, data []byte, agentID string, version int64, session string, source netip.Addr, id string) {
	var body rotateBody
	if !decode(data, &body) || body.CurrentPassword == nil {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	life, ok := seconds(body.LifetimeSeconds, 31536000, true)
	if !ok {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	over, ok := seconds(body.OverlapSeconds, 600, false)
	if len(body.OverlapSeconds) > 0 && bytes.Equal(body.OverlapSeconds, []byte("0")) {
		over, ok = 0, true
	}
	if !ok {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	var overlap *time.Duration
	if len(body.OverlapSeconds) > 0 {
		d := time.Duration(over) * time.Second
		overlap = &d
	}
	result, err := h.service.Rotate(r.Context(), agentmanagement.RotateInput{SessionSecret: session, CurrentPassword: *body.CurrentPassword, Source: source, RequestID: id, IdempotencyKey: r.Header.Get("Idempotency-Key"), AgentID: agentID, ExpectedVersion: version, Lifetime: time.Duration(life) * time.Second, Overlap: overlap})
	if err != nil {
		h.writeError(w, err, id)
		return
	}
	rotation := result.Rotation
	base := api.RotationIssuance{Agent: agentDTO(rotation.Agent), Credential: stateCredentialDTO(rotation.Credential), PreviousCredential: nullableCredentialDTO(rotation.PreviousCredential), Replayed: rotation.Replayed}
	var dto api.RotationIssuanceResponse
	if rotation.Replayed {
		variant := api.RotationIssuanceReplay{Agent: base.Agent, Credential: base.Credential, PreviousCredential: base.PreviousCredential, Replayed: api.RotationIssuanceReplayReplayedTrue, SecretState: api.RotationIssuanceReplaySecretStateSecretUnavailable}
		if err := dto.FromRotationIssuanceReplay(variant); err != nil {
			h.writeProblem(w, 503, "management_unavailable", id)
			return
		}
	} else {
		variant := api.RotationIssuanceFresh{Agent: base.Agent, Credential: base.Credential, PreviousCredential: base.PreviousCredential, Replayed: api.RotationIssuanceFreshReplayedFalse, SecretState: api.RotationIssuanceFreshSecretStateIssued, Token: result.Token}
		if err := dto.FromRotationIssuanceFresh(variant); err != nil {
			h.writeProblem(w, 503, "management_unavailable", id)
			return
		}
	}
	h.writeJSON(w, 200, dto)
}
func (h *Handler) disable(w http.ResponseWriter, r *http.Request, data []byte, agentID string, version int64, session string, source netip.Addr, id string) {
	var body proofBody
	if !decode(data, &body) || body.CurrentPassword == nil {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	result, err := h.service.Disable(r.Context(), agentmanagement.DisableInput{SessionSecret: session, CurrentPassword: *body.CurrentPassword, Source: source, RequestID: id, IdempotencyKey: r.Header.Get("Idempotency-Key"), AgentID: agentID, ExpectedVersion: version})
	if err != nil {
		h.writeError(w, err, id)
		return
	}
	h.writeJSON(w, 200, lifecycleDTO(result))
}
func (h *Handler) revoke(w http.ResponseWriter, r *http.Request, data []byte, agentID, credentialID string, version int64, session string, source netip.Addr, id string) {
	var body proofBody
	if !decode(data, &body) || body.CurrentPassword == nil {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	result, err := h.service.Revoke(r.Context(), agentmanagement.RevokeInput{SessionSecret: session, CurrentPassword: *body.CurrentPassword, Source: source, RequestID: id, IdempotencyKey: r.Header.Get("Idempotency-Key"), AgentID: agentID, CredentialID: credentialID, ExpectedVersion: version})
	if err != nil {
		h.writeError(w, err, id)
		return
	}
	h.writeJSON(w, 200, lifecycleDTO(result))
}

func agentDTO(v storage.AgentRecord) api.Agent {
	dto := api.Agent{Id: googleuuid.MustParse(v.ID), Name: v.Name, DisplayName: v.DisplayName, Enabled: v.Enabled, Version: strconv.FormatInt(v.Version, 10), CreatedAt: v.CreatedAt.UTC(), UpdatedAt: v.UpdatedAt.UTC()}
	if v.Runtime == nil {
		return dto
	}
	apiCapabilities := make([]api.AgentCapability, len(v.Runtime.Capabilities))
	for i, capability := range v.Runtime.Capabilities {
		apiCapabilities[i] = api.AgentCapability{Provider: capability.Provider, ConfigurationFormats: capability.ConfigurationFormats, Operations: capability.Operations}
	}
	dto.Runtime = &api.AgentRuntimeStatus{
		Reachability: api.AgentRuntimeStatusReachability(v.Runtime.Reachability),
		Protocol:     v.Runtime.Protocol, SoftwareVersion: v.Runtime.SoftwareVersion, Capabilities: apiCapabilities,
		LastHeartbeatAt:              v.Runtime.LastHeartbeatAt,
		DesiredConfigurationRevision: strconv.FormatInt(v.Runtime.DesiredConfigurationRevision, 10),
		AppliedConfigurationRevision: strconv.FormatInt(v.Runtime.AppliedConfigurationRevision, 10),
		SchedulerHealth:              api.AgentRuntimeStatusSchedulerHealth(v.Runtime.SchedulerHealth),
		CollectionProgress:           api.AgentRuntimeStatusCollectionProgress(v.Runtime.CollectionProgress),
		LastAttemptAt:                v.Runtime.LastAttemptAt, LastSuccessfulQueryAt: v.Runtime.LastSuccessfulQueryAt,
		NextExpectedExecutionAt: v.Runtime.NextExpectedExecutionAt,
		CollectionExplanation:   api.AgentRuntimeStatusCollectionExplanation(v.Runtime.CollectionExplanation),
	}
	return dto
}
func creationCredentialDTO(v storage.AgentCredentialRecord) api.AgentCredential {
	return api.AgentCredential{Id: googleuuid.MustParse(v.ID), AgentId: googleuuid.MustParse(v.AgentID), IssuedAt: v.IssuedAt.UTC(), ExpiresAt: v.ExpiresAt.UTC()}
}
func stateCredentialDTO(v storage.AgentCredentialStateRecord) api.AgentCredential {
	return api.AgentCredential{Id: googleuuid.MustParse(v.ID), AgentId: googleuuid.MustParse(v.AgentID), IssuedAt: v.IssuedAt.UTC(), ExpiresAt: v.ExpiresAt.UTC(), RevokedAt: utcPointer(v.RevokedAt), RetireAt: utcPointer(v.RetireAt)}
}
func utcPointer(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	t := v.UTC()
	return &t
}
func nullableCredentialDTO(v *storage.AgentCredentialStateRecord) *api.AgentCredential {
	if v == nil {
		return nil
	}
	d := stateCredentialDTO(*v)
	return &d
}
func pageDTO(v storage.AgentPage) api.AgentPage {
	items := make([]api.Agent, len(v.Items))
	for i, item := range v.Items {
		items[i] = agentDTO(item)
	}
	return api.AgentPage{Items: items, NextCursor: v.NextCursor}
}
func credentialPageDTO(v storage.AgentCredentialPage) api.CredentialPage {
	items := make([]api.AgentCredential, len(v.Items))
	for i, item := range v.Items {
		items[i] = stateCredentialDTO(item)
	}
	return api.CredentialPage{Items: items, NextCursor: v.NextCursor}
}
func lifecycleDTO(v storage.AgentLifecycleResult) api.LifecycleResult {
	count := strconv.FormatInt(v.RevokedCredentialCount, 10)
	return api.LifecycleResult{Agent: agentDTO(v.Agent), Credential: nullableCredentialDTO(v.Credential), Changed: v.Changed, RevokedCredentialCount: count, Replayed: v.Replayed}
}

func (h *Handler) writeBoundaryError(w http.ResponseWriter, err error, id string) {
	switch {
	case errors.Is(err, authhttp.ErrAuthorization):
		h.writeProblem(w, 401, "unauthenticated", id)
	case errors.Is(err, authhttp.ErrMissingSession):
		h.writeProblem(w, 401, "unauthenticated", id)
	case errors.Is(err, authhttp.ErrRequestOrigin):
		h.writeProblem(w, 403, "origin_rejected", id)
	case errors.Is(err, authhttp.ErrRequestTLS):
		h.writeProblem(w, 403, "secure_transport_required", id)
	case errors.Is(err, authhttp.ErrRequestCSRF):
		h.writeProblem(w, 403, "csrf_invalid", id)
	case errors.Is(err, authhttp.ErrRequestMethod):
		h.writeProblem(w, 405, "method_not_allowed", id)
	default:
		h.writeProblem(w, 400, "invalid_request", id)
	}
}
func (h *Handler) writeError(w http.ResponseWriter, err error, id string) {
	if limit, ok := errors.AsType[*authentication.RateLimitError](err); ok {
		retryAfter := max(1, int((limit.RetryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		h.writeProblem(w, 429, "rate_limited", id)
		return
	}
	switch {
	case errors.Is(err, authentication.ErrUnauthenticated), errors.Is(err, authentication.ErrAttemptExpired), errors.Is(err, storage.ErrInvalidSession):
		h.writeProblem(w, 401, "unauthenticated", id)
	case errors.Is(err, authentication.ErrForbidden), errors.Is(err, storage.ErrAgentForbidden):
		h.writeProblem(w, 403, "forbidden", id)
	case errors.Is(err, storage.ErrAgentNotFound):
		h.writeProblem(w, 404, "not_found", id)
	case errors.Is(err, storage.ErrAgentConflict):
		h.writeProblem(w, 409, "conflict", id)
	case errors.Is(err, storage.ErrInvalidAgentInput), errors.Is(err, authentication.ErrInvalidInput):
		h.writeProblem(w, 422, "invalid_agent_input", id)
	default:
		h.writeProblem(w, 503, "management_unavailable", id)
	}
}

func (h *Handler) writeReadError(w http.ResponseWriter, err error, id string) {
	if errors.Is(err, storage.ErrInvalidAgentInput) {
		h.writeProblem(w, 400, "invalid_request", id)
		return
	}
	h.writeError(w, err, id)
}
func (h *Handler) writeProblem(w http.ResponseWriter, status int, code, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, api.Problem{Code: code, Message: problemMessage(code), RequestId: id, FieldErrors: []struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}{}})
}
func problemMessage(code string) string {
	switch code {
	case "not_found":
		return "Resource not found"
	case "method_not_allowed":
		return "Method is not allowed"
	case "unauthenticated":
		return "Authentication is required"
	case "origin_rejected":
		return "Request origin is not allowed"
	case "secure_transport_required":
		return "A secure request is required"
	case "csrf_invalid":
		return "CSRF token is invalid"
	case "conflict":
		return "The requested change conflicts with current state"
	case "invalid_agent_input":
		return "Agent input is invalid"
	case "request_timeout":
		return "Request body timed out"
	case "request_too_large":
		return "Request body is too large"
	case "unsupported_media_type":
		return "Request media type is not supported"
	case "rate_limited":
		return "Too many requests"
	case "management_unavailable":
		return "Agent management is unavailable"
	default:
		return "Request is invalid"
	}
}
func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, value)
}
