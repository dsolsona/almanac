// Package agentclient implements the bounded, authenticated agent-management API.
package agentclient

import (
	"context"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/setupclient"
)

const maxBody = 512 << 10

type Client struct {
	ops    setupclient.AgentOperations
	editor api.RequestEditorFn
}
type Failure struct {
	Kind                     setupclient.Kind
	Code, Message, RequestID string
	RetryAfter               int
	Unknown                  bool
}

func (f *Failure) Error() string { return f.Message }

func New(ctx context.Context, session *setupclient.SessionClient) (*Client, error) {
	ops, editor, err := session.AgentContext(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{ops: ops, editor: editor}, nil
}

func (c *Client) editorWith(key, etag string) api.RequestEditorFn {
	return func(ctx context.Context, r *http.Request) error {
		if err := c.editor(ctx, r); err != nil {
			return err
		}
		// Mutations must never be replayed by net/http after a reused-connection
		// failure. Keep the idempotency header for caller-visible receipts, but
		// remove GetBody so Transport cannot reconstruct and resend the request.
		r.GetBody = nil
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if etag != "" {
			r.Header.Set("If-Match", etag)
		}
		return nil
	}
}

func (c *Client) List(ctx context.Context, limit int, cursor string) ([]byte, error) {
	if !validPageOptions(limit, cursor) {
		return nil, invalidRequest()
	}
	params := &api.GetAgentsParams{Limit: &limit}
	if cursor != "" {
		params.Cursor = &cursor
	}
	r, e := c.ops.GetAgents(ctx, params, c.editor)
	if e != nil {
		return nil, network(ctx, false)
	}
	b, e := read(r)
	if e != nil {
		return nil, network(ctx, false)
	}
	if r.StatusCode != 200 {
		return nil, problem(r, b)
	}
	var page api.AgentPage
	if json.Unmarshal(b, &page) != nil || !hasFields(b, "items", "next_cursor") || !strings.HasPrefix(strings.TrimSpace(string(rawField(b, "items"))), "[") || !rawString(rawField(b, "next_cursor")) || !validPage(page) || !validAgentItems(b) {
		return nil, protocol(false)
	}
	return json.Marshal(page)
}
func (c *Client) Get(ctx context.Context, id api.AgentId) ([]byte, string, error) {
	if !uuid.MatchString(uuidText(id)) {
		return nil, "", invalidRequest()
	}
	r, e := c.ops.GetAgent(ctx, id, c.editor)
	if e != nil {
		return nil, "", network(ctx, false)
	}
	b, e := read(r)
	if e != nil {
		return nil, "", network(ctx, false)
	}
	if r.StatusCode != 200 {
		return nil, "", problem(r, b)
	}
	var a api.Agent
	if json.Unmarshal(b, &a) != nil || !validAgentRaw(b, a) || uuidText(a.Id) != uuidText(id) {
		return nil, "", protocol(false)
	}
	etag := r.Header.Get("ETag")
	if etag != `"agent:`+uuidText(id)+":"+a.Version+`"` {
		return nil, "", protocol(false)
	}
	data, err := json.Marshal(a)
	return data, etag, err
}
func (c *Client) Credentials(ctx context.Context, id api.AgentId, limit int, cursor string) ([]byte, error) {
	if !uuid.MatchString(uuidText(id)) || !validPageOptions(limit, cursor) {
		return nil, invalidRequest()
	}
	p := &api.GetAgentCredentialsParams{Limit: &limit}
	if cursor != "" {
		p.Cursor = &cursor
	}
	r, e := c.ops.GetAgentCredentials(ctx, id, p, c.editor)
	if e != nil {
		return nil, network(ctx, false)
	}
	b, e := read(r)
	if e != nil {
		return nil, network(ctx, false)
	}
	if r.StatusCode != 200 {
		return nil, problem(r, b)
	}
	var page api.CredentialPage
	if json.Unmarshal(b, &page) != nil || !hasFields(b, "items", "next_cursor") || !strings.HasPrefix(strings.TrimSpace(string(rawField(b, "items"))), "[") || !rawString(rawField(b, "next_cursor")) || len(page.Items) > 200 || !validCursor(page.NextCursor) || !validCredentialItems(b) {
		return nil, protocol(false)
	}
	for _, v := range page.Items {
		if !validCredential(v) || uuidText(v.AgentId) != uuidText(id) {
			return nil, protocol(false)
		}
	}
	return json.Marshal(page)
}
func (c *Client) Create(ctx context.Context, body api.CreateAgentRequest, key string) ([]byte, string, bool, error) {
	if !validKey(key) {
		return nil, "", false, invalidRequest()
	}
	if ctx.Err() != nil {
		return nil, "", false, network(ctx, false)
	}
	r, e := c.ops.PostAgents(ctx, &api.PostAgentsParams{IdempotencyKey: key}, body, c.editorWith(key, ""))
	if e != nil {
		return nil, "", true, network(ctx, true)
	}
	b, e := read(r)
	if e != nil {
		return nil, "", true, bodyReadFailure(ctx, true)
	}
	return validateIssuance(r, b, "", false)
}
func (c *Client) Rotate(ctx context.Context, id api.AgentId, body api.RotateAgentRequest, etag, key string) ([]byte, string, bool, error) {
	if !uuid.MatchString(uuidText(id)) || !validKey(key) || !validETag(etag, uuidText(id)) {
		return nil, "", false, invalidRequest()
	}
	if ctx.Err() != nil {
		return nil, "", false, network(ctx, false)
	}
	r, e := c.ops.PostAgentCredentialRotate(ctx, id, &api.PostAgentCredentialRotateParams{IfMatch: etag, IdempotencyKey: key}, body, c.editorWith(key, etag))
	if e != nil {
		return nil, "", true, network(ctx, true)
	}
	b, e := read(r)
	if e != nil {
		return nil, "", true, bodyReadFailure(ctx, true)
	}
	return validateIssuance(r, b, uuidText(id), true)
}
func (c *Client) Revoke(ctx context.Context, id, credential api.AgentId, body api.PasswordProofRequest, etag, key string) ([]byte, error) {
	if !uuid.MatchString(uuidText(id)) || !uuid.MatchString(uuidText(credential)) || !validKey(key) || !validETag(etag, uuidText(id)) {
		return nil, invalidRequest()
	}
	if ctx.Err() != nil {
		return nil, network(ctx, false)
	}
	r, e := c.ops.PostAgentCredentialRevoke(ctx, id, credential, &api.PostAgentCredentialRevokeParams{IfMatch: etag, IdempotencyKey: key}, body, c.editorWith(key, etag))
	if e != nil {
		return nil, network(ctx, true)
	}
	b, e := read(r)
	if e != nil {
		return nil, bodyReadFailure(ctx, true)
	}
	if r.StatusCode != 200 {
		return nil, mutationProblem(r, b)
	}
	var v api.LifecycleResult
	if json.Unmarshal(b, &v) != nil || !validLifecycleRaw(b, v) || uuidText(v.Agent.Id) != uuidText(id) || v.Credential == nil || !validCredentialRawField(b, "credential", *v.Credential) || uuidText(v.Credential.AgentId) != uuidText(id) || uuidText(v.Credential.Id) != uuidText(credential) || v.Credential.RevokedAt == nil || !decimal(v.RevokedCredentialCount, false) || v.Changed && v.RevokedCredentialCount != "1" || !v.Changed && v.RevokedCredentialCount != "0" {
		return nil, protocol(true)
	}
	return json.Marshal(v)
}
func (c *Client) Disable(ctx context.Context, id api.AgentId, body api.PasswordProofRequest, etag, key string) ([]byte, error) {
	if !uuid.MatchString(uuidText(id)) || !validKey(key) || !validETag(etag, uuidText(id)) {
		return nil, invalidRequest()
	}
	if ctx.Err() != nil {
		return nil, network(ctx, false)
	}
	r, e := c.ops.PostAgentDisable(ctx, id, &api.PostAgentDisableParams{IfMatch: etag, IdempotencyKey: key}, body, c.editorWith(key, etag))
	if e != nil {
		return nil, network(ctx, true)
	}
	b, e := read(r)
	if e != nil {
		return nil, bodyReadFailure(ctx, true)
	}
	if r.StatusCode != 200 {
		return nil, mutationProblem(r, b)
	}
	var v api.LifecycleResult
	if json.Unmarshal(b, &v) != nil || !validLifecycleRaw(b, v) || uuidText(v.Agent.Id) != uuidText(id) || v.Credential != nil || !jsonNullField(b, "credential") || v.Agent.Enabled || !decimal(v.RevokedCredentialCount, false) || !v.Changed && v.RevokedCredentialCount != "0" {
		return nil, protocol(true)
	}
	return json.Marshal(v)
}

func validateIssuance(r *http.Response, b []byte, id string, rotation bool) ([]byte, string, bool, error) {
	if r.StatusCode != 200 && r.StatusCode != 201 {
		return nil, "", false, mutationProblem(r, b)
	}
	var fields map[string]jsontext.Value
	if json.Unmarshal(b, &fields) != nil {
		return nil, "", false, protocol(true)
	}
	var common struct {
		Agent       api.Agent            `json:"agent"`
		Credential  api.AgentCredential  `json:"credential"`
		Replayed    bool                 `json:"replayed"`
		SecretState string               `json:"secret_state"`
		Token       *string              `json:"token"`
		Previous    *api.AgentCredential `json:"previous_credential"`
	}
	if json.Unmarshal(b, &common) != nil || !hasFields(b, "agent", "credential", "replayed", "secret_state") || !rawBool(rawField(b, "replayed")) || !rawString(rawField(b, "secret_state")) || !validAgentRawField(b, "agent", common.Agent) || !validCredentialRawField(b, "credential", common.Credential) || uuidText(common.Credential.AgentId) != uuidText(common.Agent.Id) || id != "" && uuidText(common.Agent.Id) != id {
		return nil, "", false, protocol(true)
	}
	if rotation {
		if _, ok := fields["previous_credential"]; !ok {
			return nil, "", false, protocol(true)
		}
		if common.Previous == nil && !jsonNullField(b, "previous_credential") {
			return nil, "", false, protocol(true)
		}
		if common.Previous != nil && (!validCredentialRawField(b, "previous_credential", *common.Previous) || uuidText(common.Previous.AgentId) != uuidText(common.Agent.Id) || uuidText(common.Previous.Id) == uuidText(common.Credential.Id)) {
			return nil, "", false, protocol(true)
		}
	}
	if common.Replayed {
		_, hasToken := fields["token"]
		if r.StatusCode != 200 || common.SecretState != "secret_unavailable" || common.Token != nil || hasToken {
			return nil, "", true, protocol(true)
		}
	} else {
		expectedStatus := http.StatusCreated
		if rotation {
			expectedStatus = http.StatusOK
		}
		if !rawString(fields["token"]) || r.StatusCode != expectedStatus || common.SecretState != "issued" || common.Token == nil || *common.Token == "" {
			return nil, "", false, protocol(true)
		}
	}
	var safe []byte
	var e error
	if rotation {
		safe, e = json.Marshal(struct {
			Agent       api.Agent            `json:"agent"`
			Credential  api.AgentCredential  `json:"credential"`
			Previous    *api.AgentCredential `json:"previous_credential"`
			Replayed    bool                 `json:"replayed"`
			SecretState string               `json:"secret_state"`
		}{common.Agent, common.Credential, common.Previous, common.Replayed, common.SecretState})
	} else {
		safe, e = json.Marshal(struct {
			Agent       api.Agent           `json:"agent"`
			Credential  api.AgentCredential `json:"credential"`
			Replayed    bool                `json:"replayed"`
			SecretState string              `json:"secret_state"`
		}{common.Agent, common.Credential, common.Replayed, common.SecretState})
	}
	if e != nil {
		return nil, "", false, protocol(true)
	}
	token := ""
	if common.Token != nil {
		token = *common.Token
	}
	return safe, token, common.Replayed, nil
}

func read(r *http.Response) ([]byte, error) {
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if e != nil || len(b) > maxBody {
		return nil, errors.New("body unavailable")
	}
	return b, nil
}
func problem(r *http.Response, b []byte) error {
	f, documented := decodeProblem(r, b)
	if !documented {
		return protocol(false)
	}
	return f
}

func mutationProblem(r *http.Response, b []byte) error {
	f, documented := decodeProblem(r, b)
	if documented && r.StatusCode != http.StatusRequestTimeout && r.StatusCode < http.StatusInternalServerError {
		return f
	}
	if f == nil {
		f = unknownProblem(r, b)
	}
	if f == nil {
		return protocol(true)
	}
	f.Unknown = true
	if r.StatusCode == http.StatusRequestTimeout {
		f.Kind = setupclient.KindCancelled
		f.Code = "request_timeout"
		f.Message = "agent request timed out; the operation outcome is unknown"
	} else {
		f.Kind = setupclient.KindRemote
		f.Code = "outcome_unknown"
		f.Message = "agent operation outcome is unknown; inspect agent state before deciding what to do next"
	}
	return f
}

func decodeProblem(r *http.Response, b []byte) (*Failure, bool) {
	var fields map[string]jsontext.Value
	if json.Unmarshal(b, &fields) != nil || !rawString(fields["code"]) || !rawString(fields["message"]) || !rawString(fields["request_id"]) || !requestID(stringValue(fields["request_id"])) || !fieldErrorsValid(fields["field_errors"]) {
		return nil, false
	}
	var p api.Problem
	if json.Unmarshal(b, &p) != nil {
		return nil, false
	}
	code := p.Code
	f := &Failure{Kind: setupclient.KindRemote, Message: "agent management request failed", RequestID: p.RequestId}
	switch r.StatusCode {
	case 400:
		if code != "invalid_request" {
			return f, false
		}
		f.Kind = setupclient.KindInvalid
		f.Code = code
		f.Message = "agent request parameters are invalid"
	case 401:
		if code != "unauthenticated" {
			return f, false
		}
		f.Kind = setupclient.KindAuthentication
		f.Code = code
		f.Message = "sign in before managing agents"
	case 403:
		if code != "forbidden" && code != "origin_rejected" && code != "secure_transport_required" && code != "csrf_invalid" {
			return f, false
		}
		f.Kind = setupclient.KindAuthentication
		f.Code = "forbidden"
		f.Message = "an unrestricted administrator session is required"
	case 404:
		if code != "not_found" {
			return f, false
		}
		f.Kind = setupclient.KindNotFound
		f.Code = code
		f.Message = "the requested agent was not found"
	case 408:
		if code != "request_timeout" {
			return f, false
		}
		f.Kind = setupclient.KindCancelled
		f.Code = code
		f.Message = "agent request timed out"
	case 409:
		if code != "conflict" {
			return f, false
		}
		f.Kind = setupclient.KindConflict
		f.Code = code
		f.Message = "agent state changed; refresh it before trying again"
	case 413:
		if code != "request_too_large" {
			return f, false
		}
		f.Kind = setupclient.KindInvalid
		f.Code = code
		f.Message = "agent request is too large"
	case 415:
		if code != "unsupported_media_type" {
			return f, false
		}
		f.Kind = setupclient.KindInvalid
		f.Code = code
		f.Message = "agent request format is unsupported"
	case 422:
		if code != "invalid_agent_input" {
			return f, false
		}
		f.Kind = setupclient.KindInvalid
		f.Code = code
		f.Message = "agent details were rejected"
	case 429:
		if code != "rate_limited" {
			return f, false
		}
		f.Code = code
		f.Message = "too many agent requests; wait before retrying"
	case 503:
		if code != "management_unavailable" {
			return f, false
		}
		f.Code = code
		f.Message = "agent management is temporarily unavailable"
	default:
		return f, false
	}
	if r.StatusCode == 429 || r.StatusCode == 503 || r.StatusCode == 408 {
		if n, e := strconv.Atoi(r.Header.Get("Retry-After")); e == nil && n > 0 && n <= 86400 {
			f.RetryAfter = n
		}
	}
	return f, true
}

func unknownProblem(r *http.Response, b []byte) *Failure {
	var fields map[string]jsontext.Value
	if json.Unmarshal(b, &fields) != nil || !rawString(fields["code"]) || !rawString(fields["message"]) || !rawString(fields["request_id"]) || !requestID(stringValue(fields["request_id"])) || !fieldErrorsValid(fields["field_errors"]) {
		return nil
	}
	f := &Failure{Kind: setupclient.KindRemote, Code: "outcome_unknown", Message: "agent operation outcome is unknown; inspect agent state before deciding what to do next", RequestID: stringValue(fields["request_id"]), Unknown: true}
	if n, e := strconv.Atoi(r.Header.Get("Retry-After")); e == nil && n > 0 && n <= 86400 {
		f.RetryAfter = n
	}
	return f
}
func stringValue(v jsontext.Value) string { var s string; _ = json.Unmarshal(v, &s); return s }
func fieldErrorsValid(v jsontext.Value) bool {
	if !strings.HasPrefix(strings.TrimSpace(string(v)), "[") {
		return false
	}
	var items []map[string]jsontext.Value
	if json.Unmarshal(v, &items) != nil {
		return false
	}
	for _, item := range items {
		if !rawString(item["field"]) || !rawString(item["code"]) || !rawString(item["message"]) {
			return false
		}
	}
	return true
}
func network(ctx context.Context, unknown bool) error {
	if ctx.Err() != nil {
		message := "agent operation was cancelled or timed out"
		if unknown {
			message = "agent operation was cancelled or timed out; the mutation may have completed; inspect agent state before deciding what to do next"
		}
		return &Failure{Kind: setupclient.KindCancelled, Code: "cancelled", Message: message, Unknown: unknown}
	}
	if unknown {
		return &Failure{Kind: setupclient.KindRemote, Code: "outcome_unknown", Message: "agent operation outcome is unknown; inspect agent state before deciding what to do next", Unknown: true}
	}
	return &Failure{Kind: setupclient.KindRemote, Code: "connection_failed", Message: "agent management could not be reached"}
}
func bodyReadFailure(ctx context.Context, mutation bool) error {
	if ctx.Err() != nil {
		return network(ctx, mutation)
	}
	if mutation {
		return protocol(true)
	}
	return protocol(false)
}
func protocol(unknown bool) error {
	if unknown {
		return &Failure{Kind: setupclient.KindRemote, Code: "outcome_unknown", Message: "agent operation outcome is unknown; inspect agent state before deciding what to do next", Unknown: true}
	}
	return &Failure{Kind: setupclient.KindRemote, Code: "invalid_response", Message: "agent service returned an invalid response"}
}

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var dec = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

const maxInt64 = "9223372036854775807"

func decimal(s string, positive bool) bool {
	if !dec.MatchString(s) || positive && s == "0" {
		return false
	}
	return len(s) < len(maxInt64) || len(s) == len(maxInt64) && strings.Compare(s, maxInt64) <= 0
}
func validPageOptions(limit int, cursor string) bool {
	return limit >= 1 && limit <= 200 && validCursor(cursor)
}
func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range []byte(key) {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}
func validETag(tag, id string) bool {
	prefix := "\"agent:" + id + ":"
	if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, "\"") {
		return false
	}
	version := strings.TrimSuffix(strings.TrimPrefix(tag, prefix), "\"")
	return decimal(version, true)
}
func invalidRequest() error {
	return &Failure{Kind: setupclient.KindInvalid, Code: "invalid_request", Message: "agent command parameters are invalid"}
}
func validAgent(a api.Agent) bool {
	return uuid.MatchString(uuidText(a.Id)) && len(a.Name) > 0 && len(a.DisplayName) > 0 && decimal(a.Version, true) && !a.CreatedAt.IsZero() && !a.UpdatedAt.IsZero()
}
func validCredential(c api.AgentCredential) bool {
	return uuid.MatchString(uuidText(c.Id)) && uuid.MatchString(uuidText(c.AgentId)) && !c.IssuedAt.IsZero() && !c.ExpiresAt.IsZero() && (c.RevokedAt == nil || !c.RevokedAt.IsZero()) && (c.RetireAt == nil || !c.RetireAt.IsZero())
}
func uuidText(id api.AgentId) string {
	b, _ := json.Marshal(id)
	return strings.Trim(string(b), `"`)
}
func validPage(p api.AgentPage) bool {
	if len(p.Items) > 200 || !validCursor(p.NextCursor) {
		return false
	}
	for _, a := range p.Items {
		if !validAgent(a) {
			return false
		}
	}
	return true
}
func hasFields(raw []byte, names ...string) bool {
	var fields map[string]jsontext.Value
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}
func rawField(raw []byte, name string) jsontext.Value {
	var fields map[string]jsontext.Value
	_ = json.Unmarshal(raw, &fields)
	return fields[name]
}
func jsonNullField(raw []byte, name string) bool { return string(rawField(raw, name)) == "null" }
func rawString(value jsontext.Value) bool {
	var s string
	return len(value) >= 2 && value[0] == '"' && json.Unmarshal(value, &s) == nil
}
func rawBool(value jsontext.Value) bool { return string(value) == "true" || string(value) == "false" }
func validAgentRaw(raw []byte, a api.Agent) bool {
	return hasFields(raw, "id", "name", "display_name", "enabled", "version", "created_at", "updated_at") && rawString(rawField(raw, "id")) && rawString(rawField(raw, "name")) && rawString(rawField(raw, "display_name")) && rawBool(rawField(raw, "enabled")) && rawString(rawField(raw, "version")) && rawString(rawField(raw, "created_at")) && rawString(rawField(raw, "updated_at")) && validAgent(a)
}
func validAgentRawField(raw []byte, name string, a api.Agent) bool {
	return validAgentRaw(rawField(raw, name), a)
}
func validCredentialRaw(raw []byte, c api.AgentCredential) bool {
	return hasFields(raw, "id", "agent_id", "issued_at", "expires_at", "revoked_at", "retire_at") && rawString(rawField(raw, "id")) && rawString(rawField(raw, "agent_id")) && rawString(rawField(raw, "issued_at")) && rawString(rawField(raw, "expires_at")) && nullableString(rawField(raw, "revoked_at")) && nullableString(rawField(raw, "retire_at")) && validCredential(c)
}
func nullableString(v jsontext.Value) bool { return string(v) == "null" || rawString(v) }
func validCredentialRawField(raw []byte, name string, c api.AgentCredential) bool {
	return validCredentialRaw(rawField(raw, name), c)
}
func validLifecycleRaw(raw []byte, v api.LifecycleResult) bool {
	return hasFields(raw, "agent", "changed", "credential", "replayed", "revoked_credential_count") && rawBool(rawField(raw, "changed")) && rawBool(rawField(raw, "replayed")) && rawString(rawField(raw, "revoked_credential_count")) && validAgentRawField(raw, "agent", v.Agent) && (v.Credential == nil && jsonNullField(raw, "credential") || v.Credential != nil && validCredentialRawField(raw, "credential", *v.Credential))
}
func validAgentItems(raw []byte) bool {
	var page struct {
		Items []jsontext.Value `json:"items"`
	}
	if json.Unmarshal(raw, &page) != nil || string(rawField(raw, "items")) == "null" || !strings.HasPrefix(strings.TrimSpace(string(rawField(raw, "items"))), "[") {
		return false
	}
	for _, item := range page.Items {
		var a api.Agent
		if json.Unmarshal(item, &a) != nil || !validAgentRaw(item, a) {
			return false
		}
	}
	return true
}
func validCredentialItems(raw []byte) bool {
	var page struct {
		Items []jsontext.Value `json:"items"`
	}
	if json.Unmarshal(raw, &page) != nil || string(rawField(raw, "items")) == "null" || !strings.HasPrefix(strings.TrimSpace(string(rawField(raw, "items"))), "[") {
		return false
	}
	for _, item := range page.Items {
		var c api.AgentCredential
		if json.Unmarshal(item, &c) != nil || !validCredentialRaw(item, c) {
			return false
		}
	}
	return true
}
func validCursor(s string) bool {
	if len(s) > 512 {
		return false
	}
	for _, b := range []byte(s) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}
func requestID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}
