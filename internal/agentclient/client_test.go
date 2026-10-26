package agentclient

import (
	"context"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/setupclient"
)

func TestAgentContextRejectsExpiredAndCancelledSessions(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"code":"unauthenticated","message":"do not expose this","request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`)
	}))
	defer server.Close()
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: server.URL, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), Session: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal("create session failed")
	}
	defer session.Close()
	_, err = New(t.Context(), session)
	var failure *setupclient.Failure
	if !errors.As(err, &failure) || failure.Kind != setupclient.KindAuthentication || failure.Code != "unauthenticated" || strings.Contains(failure.Message, "do not expose") {
		t.Fatalf("expired session was not safely rejected: %#v", err)
	}

	cancelled, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: "https://localhost", Session: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal("construct cancelled session failed")
	}
	defer cancelled.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = New(ctx, cancelled)
	if !errors.As(err, &failure) || failure.Kind != setupclient.KindCancelled {
		t.Fatalf("cancelled agent context was misclassified: %#v", err)
	}
}

func TestValidateIssuanceSeparatesFreshSecretFromReplay(t *testing.T) {
	fresh := `{"agent":{"id":"00000000-0000-0000-0000-000000000001","name":"east","display_name":"East","enabled":true,"version":"9223372036854775807","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z"},"credential":{"id":"00000000-0000-0000-0000-000000000002","agent_id":"00000000-0000-0000-0000-000000000001","issued_at":"2026-10-26T10:00:00Z","expires_at":"2026-10-27T10:00:00Z","revoked_at":null,"retire_at":null},"replayed":false,"secret_state":"issued","token":"secret-value"}`
	data, token, replay, err := validateIssuance(&http.Response{StatusCode: 201}, []byte(fresh), "", false)
	if err != nil || replay || token != "secret-value" || strings.Contains(string(data), token) || !strings.Contains(string(data), `"version":"9223372036854775807"`) {
		t.Fatalf("fresh issuance did not isolate its secret or preserve version: replay=%t token=%q data=%s err=%v", replay, token, data, err)
	}
	replayed := strings.Replace(strings.Replace(fresh, `"replayed":false`, `"replayed":true`, 1), `"secret_state":"issued","token":"secret-value"`, `"secret_state":"secret_unavailable"`, 1)
	data, token, replay, err = validateIssuance(&http.Response{StatusCode: 200}, []byte(replayed), "", false)
	if err != nil || !replay || token != "" || strings.Contains(string(data), "token") {
		t.Fatalf("replay exposed or required a token: replay=%t token=%q data=%s err=%v", replay, token, data, err)
	}
}

func TestValidateIssuanceRejectsContradictoryTokenDiscriminants(t *testing.T) {
	bad := `{"agent":{"id":"00000000-0000-0000-0000-000000000001","name":"east","display_name":"East","enabled":true,"version":"1","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z"},"credential":{"id":"00000000-0000-0000-0000-000000000002","agent_id":"00000000-0000-0000-0000-000000000001","issued_at":"2026-10-26T10:00:00Z","expires_at":"2026-10-27T10:00:00Z","revoked_at":null,"retire_at":null},"replayed":true,"secret_state":"secret_unavailable","token":"unexpected"}`
	if _, _, _, err := validateIssuance(&http.Response{StatusCode: 200}, []byte(bad), "", false); err == nil {
		t.Fatal("replayed response with token was accepted")
	}
	if _, _, _, err := validateIssuance(&http.Response{StatusCode: 201}, []byte(strings.ReplaceAll(bad, `"replayed":true`, `"replayed":false`)), "", false); err == nil {
		t.Fatal("fresh response with replay discriminant was accepted")
	}
	if _, _, _, err := validateIssuance(&http.Response{StatusCode: 200}, []byte(strings.Replace(bad, `"token":"unexpected"`, `"token":null`, 1)), "", false); err == nil {
		t.Fatal("replay with explicit null token was accepted")
	}
}

func TestDecimalEnforcesCanonicalSignedInt64(t *testing.T) {
	for _, value := range []string{"0", "1", "9223372036854775807"} {
		if !decimal(value, false) {
			t.Errorf("accepted-range decimal %q rejected", value)
		}
	}
	for _, value := range []string{"", "00", "+1", "-1", "9223372036854775808"} {
		if decimal(value, false) {
			t.Errorf("invalid decimal %q accepted", value)
		}
	}
	if decimal("0", true) {
		t.Fatal("zero accepted as positive version")
	}
	var duplicate map[string]any
	if json.Unmarshal([]byte(`{"x":1,"x":2}`), &duplicate) == nil {
		t.Fatal("strict JSON decoder accepted duplicate property")
	}
}

func TestMutationServerFailurePreservesSafeRetryMetadataAndUnknownOutcome(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": []string{"17"}}}
	body := []byte(`{"code":"management_unavailable","message":"database details","request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`)
	err := mutationProblem(response, body)
	failure, ok := errors.AsType[*Failure](err)
	if !ok || !failure.Unknown || failure.Code != "outcome_unknown" || failure.RequestID != "0123456789abcdef0123456789abcdef" || failure.RetryAfter != 17 || strings.Contains(failure.Message, "database details") {
		t.Fatalf("server failure lost safe metadata or disclosed message: %#v", err)
	}
	conflict := mutationProblem(&http.Response{StatusCode: http.StatusConflict, Header: make(http.Header)}, []byte(`{"code":"conflict","message":"stale","request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`))
	known, ok := errors.AsType[*Failure](conflict)
	if !ok || known.Unknown || known.Kind != setupclient.KindConflict {
		t.Fatalf("definitive conflict became uncertain: %#v", conflict)
	}
}

func TestResponseValidationRejectsNullStrings(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/context" {
			_, _ = fmt.Fprint(w, `{"csrf_token":"fresh-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":false}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[],"next_cursor":null}`)
	}))
	defer server.Close()
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: server.URL, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), Session: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal("create session failed")
	}
	defer session.Close()
	client, err := New(t.Context(), session)
	if err != nil {
		t.Fatal("create agent client failed")
	}
	if _, err := client.List(t.Context(), 50, ""); err == nil {
		t.Fatal("list accepted a null nonnullable next_cursor")
	}

	for _, body := range []string{
		`{"code":"conflict","message":null,"request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`,
		`{"code":"conflict","message":"stale","request_id":"0123456789abcdef0123456789abcdef","field_errors":[{"field":null,"code":"invalid","message":"bad"}]}`,
	} {
		err := mutationProblem(&http.Response{StatusCode: http.StatusConflict, Header: make(http.Header)}, []byte(body))
		failure, ok := errors.AsType[*Failure](err)
		if !ok || !failure.Unknown {
			t.Errorf("null Problem strings were not safely treated as unknown: %#v", err)
		}
	}
}

func TestGeneratedReadsValidatePaginationETagAndLargeVersions(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	const agent = `{"id":"` + id + `","name":"east","display_name":"East","enabled":true,"version":"9223372036854775807","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z"}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/context" {
			_, _ = fmt.Fprint(w, `{"csrf_token":"fresh-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":false}}`)
			return
		}
		if r.URL.Path == "/api/v1/agents" {
			if r.URL.Query().Get("limit") != "73" || r.URL.Query().Get("cursor") != "cursor-1" {
				t.Errorf("pagination query = %v", r.URL.Query())
			}
			_, _ = fmt.Fprintf(w, `{"items":[%s],"next_cursor":"next-1"}`, agent)
			return
		}
		if r.URL.Path == "/api/v1/agents/"+id {
			w.Header().Set("ETag", `"agent:`+id+`:9223372036854775807"`)
			_, _ = fmt.Fprint(w, agent)
			return
		}
		t.Errorf("unexpected request path %q", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: server.URL, CABundle: ca, Session: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal("create session failed")
	}
	defer session.Close()
	client, err := New(t.Context(), session)
	if err != nil {
		t.Fatal("create agent client failed")
	}
	page, err := client.List(t.Context(), 73, "cursor-1")
	if err != nil || !strings.Contains(string(page), "9223372036854775807") {
		t.Fatalf("list did not preserve page/version: %s err=%v", page, err)
	}
	var agentID api.AgentId
	if err := json.Unmarshal([]byte(`"`+id+`"`), &agentID); err != nil {
		t.Fatal("parse agent ID failed")
	}
	detail, etag, err := client.Get(t.Context(), agentID)
	if err != nil || !strings.Contains(string(detail), "9223372036854775807") || etag != `"agent:`+id+`:9223372036854775807"` {
		t.Fatalf("detail version or exact ETag changed: data=%s etag=%q err=%v", detail, etag, err)
	}
}

func TestGeneratedIssuanceSupportsFreshAndReplayStatusesAndExplicitZero(t *testing.T) {
	const agentID = "00000000-0000-0000-0000-000000000001"
	const initialCredential = "00000000-0000-0000-0000-000000000002"
	const rotatedCredential = "00000000-0000-0000-0000-000000000003"
	var creates, rotations int
	freshAgent := `"id":"` + agentID + `","name":"east","display_name":"East","enabled":true,"version":"2","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z"`
	credential := func(id string) string {
		return `"id":"` + id + `","agent_id":"` + agentID + `","issued_at":"2026-10-26T10:00:00Z","expires_at":"2026-10-27T10:00:00Z","revoked_at":null,"retire_at":null`
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/context":
			_, _ = fmt.Fprint(w, `{"csrf_token":"fresh-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":false}}`)
		case "/api/v1/agents":
			creates++
			if r.Header.Get("Idempotency-Key") != "create-key" || r.Header.Get("X-CSRF-Token") != "fresh-csrf" {
				t.Errorf("create headers were lost")
			}
			var body api.CreateAgentRequest
			if json.UnmarshalRead(r.Body, &body) != nil || body.LifetimeSeconds == nil || *body.LifetimeSeconds != 0 {
				t.Errorf("create omitted explicit zero lifetime")
			}
			if creates == 1 {
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprintf(w, `{"agent":{%s},"credential":{%s},"replayed":false,"secret_state":"issued","token":"fresh-create-secret","future_secret":"must-not-escape"}`, freshAgent, credential(initialCredential))
			} else {
				_, _ = fmt.Fprintf(w, `{"agent":{%s},"credential":{%s},"replayed":true,"secret_state":"secret_unavailable"}`, freshAgent, credential(initialCredential))
			}
		case "/api/v1/agents/" + agentID + "/credentials/rotate":
			rotations++
			if r.Header.Get("Idempotency-Key") != "rotate-key" || r.Header.Get("If-Match") != `"agent:`+agentID+`:1"` {
				t.Errorf("rotation headers were lost")
			}
			var body api.RotateAgentRequest
			if json.UnmarshalRead(r.Body, &body) != nil || body.LifetimeSeconds == nil || *body.LifetimeSeconds != 0 || body.OverlapSeconds == nil || *body.OverlapSeconds != 0 {
				t.Errorf("rotation omitted explicit zero timing values")
			}
			_, _ = fmt.Fprintf(w, `{"agent":{%s},"credential":{%s},"previous_credential":{%s},"replayed":%t,"secret_state":"%s"%s}`, freshAgent, credential(rotatedCredential), credential(initialCredential), rotations > 1, map[bool]string{true: "secret_unavailable", false: "issued"}[rotations > 1], map[bool]string{true: "", false: ",\"token\":\"fresh-rotation-secret\""}[rotations > 1])
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestAgentClient(t, server)
	zero := int32(0)
	created, token, replay, err := client.Create(t.Context(), api.CreateAgentRequest{Name: "east", DisplayName: "East", CurrentPassword: "proof", LifetimeSeconds: &zero}, "create-key")
	if err != nil || replay || token != "fresh-create-secret" || strings.Contains(string(created), "future_secret") || strings.Contains(string(created), token) {
		t.Fatalf("fresh create result was wrong: replay=%t err=%v", replay, err)
	}
	if _, token, replay, err = client.Create(t.Context(), api.CreateAgentRequest{Name: "east", DisplayName: "East", CurrentPassword: "proof", LifetimeSeconds: &zero}, "create-key"); err != nil || !replay || token != "" {
		t.Fatalf("create replay failed: replay=%t token=%q err=%v", replay, token, err)
	}
	var id api.AgentId
	if err = json.Unmarshal([]byte(`"`+agentID+`"`), &id); err != nil {
		t.Fatal("decode agent ID failed")
	}
	etag := `"agent:` + agentID + `:1"`
	rotation := api.RotateAgentRequest{CurrentPassword: "proof", LifetimeSeconds: &zero, OverlapSeconds: &zero}
	result, token, replay, err := client.Rotate(t.Context(), id, rotation, etag, "rotate-key")
	if err != nil || replay || token != "fresh-rotation-secret" || strings.Contains(string(result), token) {
		t.Fatalf("fresh rotation failed or exposed token in metadata: replay=%t err=%v result=%s", replay, err, result)
	}
	result, token, replay, err = client.Rotate(t.Context(), id, rotation, etag, "rotate-key")
	if err != nil || !replay || token != "" || strings.Contains(string(result), `"token"`) {
		t.Fatalf("rotation replay failed or included token: replay=%t token=%q err=%v", replay, token, err)
	}
	if creates != 2 || rotations != 2 {
		t.Fatalf("requests were retried or skipped: creates=%d rotations=%d", creates, rotations)
	}
}

func newTestAgentClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	session, err := setupclient.NewSession(setupclient.SessionOptions{BackendURL: server.URL, CABundle: ca, Session: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal("create session failed")
	}
	t.Cleanup(session.Close)
	client, err := New(t.Context(), session)
	if err != nil {
		t.Fatal("create agent client failed")
	}
	return client
}

func TestMutationsClassifyDocumentedRejectionsUnknownResponsesAndCancellation(t *testing.T) {
	const requestID = "0123456789abcdef0123456789abcdef"
	var posts int
	bodyStarted := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/context" {
			_, _ = fmt.Fprint(w, `{"csrf_token":"fresh-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":false}}`)
			return
		}
		posts++
		switch r.Header.Get("Idempotency-Key") {
		case "throttle":
			w.Header().Set("Retry-After", "25")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprintf(w, `{"code":"rate_limited","message":"secret server detail","request_id":%q,"field_errors":[]}`, requestID)
		case "server-error":
			w.Header().Set("Retry-After", "15")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, `{"code":"management_unavailable","message":"secret server detail","request_id":%q,"field_errors":[]}`, requestID)
		case "redirect":
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
		case "malformed-rejection":
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"code":"conflict","message":"missing request id"}`)
		case "malformed-success":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{`)
		case "body-loss":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{`)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			close(bodyStarted)
			<-r.Context().Done()
		default:
			t.Errorf("unexpected mutation key %q", r.Header.Get("Idempotency-Key"))
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	client := newTestAgentClient(t, server)
	input := api.CreateAgentRequest{Name: "east", DisplayName: "East", CurrentPassword: "proof"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := client.Create(ctx, input, "cancel-before-dispatch"); !isFailure(err, setupclient.KindCancelled, false) {
		t.Fatalf("pre-dispatch cancellation=%#v", err)
	}
	if posts != 0 {
		t.Fatal("cancelled operation was dispatched")
	}
	_, _, _, err := client.Create(t.Context(), input, "throttle")
	f := asAgentFailure(t, err)
	if f.Unknown || f.RetryAfter != 25 || f.RequestID != requestID || f.Code != "rate_limited" || strings.Contains(f.Message, "secret server detail") {
		t.Fatalf("throttling response classification=%#v", f)
	}
	_, _, _, err = client.Create(t.Context(), input, "server-error")
	f = asAgentFailure(t, err)
	if !f.Unknown || f.RequestID != requestID || f.RetryAfter != 15 {
		t.Fatalf("server failure lost uncertain metadata: %#v", f)
	}
	for _, key := range []string{"redirect", "malformed-rejection", "malformed-success"} {
		_, _, _, err = client.Create(t.Context(), input, key)
		f = asAgentFailure(t, err)
		if !f.Unknown {
			t.Errorf("%s response was treated as definitive: %#v", key, f)
		}
	}
	bodyCtx, bodyCancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, _, _, callErr := client.Create(bodyCtx, input, "body-loss"); done <- callErr }()
	select {
	case <-bodyStarted:
		bodyCancel()
	case <-time.After(3 * time.Second):
		bodyCancel()
		t.Fatal("mutation response body did not start")
	}
	select {
	case err = <-done:
		f = asAgentFailure(t, err)
		if !f.Unknown || f.Kind != setupclient.KindCancelled {
			t.Fatalf("cancelled body loss classification=%#v", f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("body read did not stop when its context was cancelled")
	}
	if posts != 6 {
		t.Fatalf("client retried or skipped mutations: requests=%d", posts)
	}
}

func asAgentFailure(t *testing.T, err error) *Failure {
	t.Helper()
	f, ok := errors.AsType[*Failure](err)
	if !ok {
		t.Fatalf("expected safe agent failure, got %#v", err)
	}
	return f
}
func isFailure(err error, kind setupclient.Kind, unknown bool) bool {
	f, ok := errors.AsType[*Failure](err)
	return ok && f.Kind == kind && f.Unknown == unknown
}
