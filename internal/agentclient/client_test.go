package agentclient

import (
	"context"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	data, token, replay, err := validateIssuance(t.Context(), &http.Response{StatusCode: 201}, []byte(fresh), "", false)
	if err != nil || replay || token != "secret-value" || strings.Contains(string(data), token) || !strings.Contains(string(data), `"version":"9223372036854775807"`) {
		t.Fatalf("fresh issuance did not isolate its secret or preserve version: replay=%t token=%q data=%s err=%v", replay, token, data, err)
	}
	replayed := strings.Replace(strings.Replace(fresh, `"replayed":false`, `"replayed":true`, 1), `"secret_state":"issued","token":"secret-value"`, `"secret_state":"secret_unavailable"`, 1)
	data, token, replay, err = validateIssuance(t.Context(), &http.Response{StatusCode: 200}, []byte(replayed), "", false)
	if err != nil || !replay || token != "" || strings.Contains(string(data), "token") {
		t.Fatalf("replay exposed or required a token: replay=%t token=%q data=%s err=%v", replay, token, data, err)
	}
}

func TestValidateIssuanceRejectsContradictoryTokenDiscriminants(t *testing.T) {
	bad := `{"agent":{"id":"00000000-0000-0000-0000-000000000001","name":"east","display_name":"East","enabled":true,"version":"1","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z"},"credential":{"id":"00000000-0000-0000-0000-000000000002","agent_id":"00000000-0000-0000-0000-000000000001","issued_at":"2026-10-26T10:00:00Z","expires_at":"2026-10-27T10:00:00Z","revoked_at":null,"retire_at":null},"replayed":true,"secret_state":"secret_unavailable","token":"unexpected"}`
	if _, _, _, err := validateIssuance(t.Context(), &http.Response{StatusCode: 200}, []byte(bad), "", false); err == nil {
		t.Fatal("replayed response with token was accepted")
	}
	if _, _, _, err := validateIssuance(t.Context(), &http.Response{StatusCode: 201}, []byte(strings.ReplaceAll(bad, `"replayed":true`, `"replayed":false`)), "", false); err == nil {
		t.Fatal("fresh response with replay discriminant was accepted")
	}
	if _, _, _, err := validateIssuance(t.Context(), &http.Response{StatusCode: 200}, []byte(strings.Replace(bad, `"token":"unexpected"`, `"token":null`, 1)), "", false); err == nil {
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
	err := mutationProblem(t.Context(), response, body)
	failure, ok := errors.AsType[*Failure](err)
	if !ok || !failure.Unknown || failure.Code != "outcome_unknown" || failure.RequestID != "0123456789abcdef0123456789abcdef" || failure.RetryAfter != 17 || strings.Contains(failure.Message, "database details") {
		t.Fatalf("server failure lost safe metadata or disclosed message: %#v", err)
	}
	conflict := mutationProblem(t.Context(), &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header)}, []byte(`{"code":"conflict","message":"stale","request_id":"0123456789abcdef0123456789abcdef","field_errors":[]}`))
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
		err := mutationProblem(t.Context(), &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header)}, []byte(body))
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

func TestGetValidatesOptionalRuntimeAndPreservesExactRevisions(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	const base = `{"id":"` + id + `","name":"east","display_name":"East","enabled":true,"version":"1","created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:00Z"}`
	validRuntime := `{"reachability":"connected","protocol":"almanac-agent/1","software_version":"1.2.3","capabilities":[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["live_query"]}],"last_heartbeat_at":"2026-10-26T10:00:00.123456Z","desired_configuration_revision":"9223372036854775807","applied_configuration_revision":"0","scheduler_health":"ready","collection_progress":"running","configuration_failure_code":null,"configuration_failure_revision":null,"last_attempt_at":null,"last_successful_query_at":null,"next_expected_execution_at":null,"collection_explanation":"no_assignment"}`
	responseBody := base
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/context" {
			_, _ = fmt.Fprint(w, `{"csrf_token":"fresh-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":false}}`)
			return
		}
		if r.URL.Path == "/api/v1/agents/"+id {
			w.Header().Set("ETag", `"agent:`+id+`:1"`)
			_, _ = fmt.Fprint(w, responseBody)
			return
		}
		t.Errorf("unexpected request path %q", r.URL.Path)
		http.NotFound(w, r)
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
	var agentID api.AgentId
	if err := json.Unmarshal([]byte(`"`+id+`"`), &agentID); err != nil {
		t.Fatal("parse agent ID failed")
	}
	responseBody = strings.TrimSuffix(base, "}") + `,"runtime":` + validRuntime + `}`
	detail, _, err := client.Get(t.Context(), agentID)
	if err != nil || !strings.Contains(string(detail), `"desired_configuration_revision":"9223372036854775807"`) || !strings.Contains(string(detail), `"applied_configuration_revision":"0"`) {
		t.Fatalf("valid runtime revisions did not remain exact: data=%s err=%v", detail, err)
	}
	failureRuntime := strings.Replace(validRuntime, `"configuration_failure_code":null,"configuration_failure_revision":null`,
		`"configuration_failure_code":"apply_failed","configuration_failure_revision":"2"`, 1)
	responseBody = strings.TrimSuffix(base, "}") + `,"runtime":` + failureRuntime + `}`
	if _, _, err := client.Get(t.Context(), agentID); err != nil {
		t.Fatalf("Get() rejected valid configuration failure detail: %v", err)
	}
	capability := `{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["live_query"]}`
	formats := strings.TrimSuffix(strings.Repeat(`"format",`, 63), ",") + `,"format"`
	operations := strings.TrimSuffix(strings.Repeat(`"operation",`, 15), ",") + `,"operation"`
	boundaryRuntime := strings.Replace(validRuntime, `"capabilities":[`+capability+`]`, `"capabilities":[`+strings.TrimSuffix(strings.Repeat(capability+`,`, 255), ",")+`,`+capability+`]`, 1)
	boundaryRuntime = strings.Replace(boundaryRuntime, `"configuration_formats":["prometheus-http-v1"]`, `"configuration_formats":[`+formats+`]`, 1)
	boundaryRuntime = strings.Replace(boundaryRuntime, `"operations":["live_query"]`, `"operations":[`+operations+`]`, 1)
	responseBody = strings.TrimSuffix(base, "}") + `,"runtime":` + boundaryRuntime + `}`
	if _, _, err := client.Get(t.Context(), agentID); err != nil {
		t.Fatalf("Get() rejected maximum supported runtime capability lists: %v", err)
	}

	invalidRuntimes := []string{
		strings.Replace(validRuntime, `"connected"`, `"online"`, 1),
		strings.Replace(validRuntime, `"ready"`, `"healthy"`, 1),
		strings.Replace(validRuntime, `"running"`, `"complete"`, 1),
		strings.Replace(validRuntime, `"no_assignment"`, `"none"`, 1),
		strings.Replace(validRuntime, `"desired_configuration_revision":"9223372036854775807"`, `"desired_configuration_revision":7`, 1),
		strings.Replace(validRuntime, `"desired_configuration_revision":"9223372036854775807"`, `"desired_configuration_revision":"0"`, 1),
		strings.Replace(validRuntime, `"applied_configuration_revision":"0"`, `"applied_configuration_revision":"00"`, 1),
		strings.Replace(validRuntime, `"last_heartbeat_at":"2026-10-26T10:00:00.123456Z"`, `"last_heartbeat_at":"not-a-date"`, 1),
		strings.Replace(validRuntime, `"capabilities":[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["live_query"]}]`, `"capabilities":null`, 1),
		strings.Replace(validRuntime, `,"scheduler_health":"ready"`, "", 1),
		strings.Replace(validRuntime, `,"configuration_failure_revision":null`, "", 1),
		strings.Replace(validRuntime, `"configuration_failure_code":null`, `"configuration_failure_code":"source_password"`, 1),
		strings.Replace(validRuntime, `"configuration_failure_revision":null`, `"configuration_failure_revision":"2"`, 1),
		strings.Replace(validRuntime, `"provider":"prometheus"`, `"provider":""`, 1),
		strings.Replace(validRuntime, `"configuration_formats":["prometheus-http-v1"]`, `"configuration_formats":null`, 1),
		strings.Replace(validRuntime, `"configuration_formats":["prometheus-http-v1"]`, `"configuration_formats":[`+strings.TrimSuffix(strings.Repeat(`"format",`, 64), ",")+`,"format"]`, 1),
		strings.Replace(validRuntime, `"operations":["live_query"]`, `"operations":[`+strings.TrimSuffix(strings.Repeat(`"operation",`, 16), ",")+`,"operation"]`, 1),
		strings.Replace(validRuntime, `"capabilities":[`+capability+`]`, `"capabilities":[`+strings.TrimSuffix(strings.Repeat(capability+`,`, 256), ",")+`,`+capability+`]`, 1),
	}
	for i, runtime := range invalidRuntimes {
		responseBody = strings.TrimSuffix(base, "}") + `,"runtime":` + runtime + `}`
		if _, _, err := client.Get(t.Context(), agentID); err == nil {
			t.Errorf("Get() accepted malformed runtime case %d", i)
		}
	}
	for _, neverConnected := range []string{base, strings.TrimSuffix(base, "}") + `,"runtime":null}`} {
		responseBody = neverConnected
		if _, _, err := client.Get(t.Context(), agentID); err != nil {
			t.Errorf("Get() rejected absent runtime: %v", err)
		}
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
	created, token, replay, err := client.Create(t.Context(), api.CreateAgentRequest{Name: "east", DisplayName: "East", LifetimeSeconds: &zero}, "create-key")
	if err != nil || replay || token != "fresh-create-secret" || strings.Contains(string(created), "future_secret") || strings.Contains(string(created), token) {
		t.Fatalf("fresh create result was wrong: replay=%t err=%v", replay, err)
	}
	if _, token, replay, err = client.Create(t.Context(), api.CreateAgentRequest{Name: "east", DisplayName: "East", LifetimeSeconds: &zero}, "create-key"); err != nil || !replay || token != "" {
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

func TestLostResponseOnReusedTLSConnectionDoesNotReplayIssuance(t *testing.T) {
	for _, mutation := range []string{"create", "rotate"} {
		t.Run(mutation, func(t *testing.T) {
			var attempts atomic.Int32
			var warmMu sync.Mutex
			var warmConnection string
			committed := make(chan string, 2)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v1/auth/context":
					warmMu.Lock()
					warmConnection = r.RemoteAddr
					warmMu.Unlock()
					_, _ = fmt.Fprint(w, `{"csrf_token":"fresh-csrf","session":{"user_id":"u-1","login":"admin","role":"administrator","restricted":false}}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
					_, _ = fmt.Fprint(w, `{"items":[],"next_cursor":""}`)
				case r.Method == http.MethodPost:
					attempt := attempts.Load() + 1
					warmMu.Lock()
					warmed := warmConnection
					warmMu.Unlock()
					if attempt == 1 && r.RemoteAddr != warmed {
						t.Errorf("%s did not reuse the warmed TLS connection", mutation)
					}
					wantKey := "create-once"
					if mutation == "rotate" {
						wantKey = "rotate-once"
					}
					if r.Header.Get("Idempotency-Key") != wantKey {
						t.Errorf("%s request idempotency key was not preserved", mutation)
					}
					body, err := io.ReadAll(r.Body)
					if err != nil || len(body) < 8<<20 {
						t.Errorf("read committed %s request body failed", mutation)
					}
					attempts.Add(1)
					committed <- r.RemoteAddr
					hijacker, ok := w.(http.Hijacker)
					if !ok {
						t.Error("TLS response writer does not support connection close")
						return
					}
					conn, _, err := hijacker.Hijack()
					if err != nil {
						t.Errorf("hijack committed %s connection failed", mutation)
						return
					}
					if tlsConn, ok := conn.(interface{ NetConn() net.Conn }); ok {
						if tcpConn, ok := tlsConn.NetConn().(*net.TCPConn); ok {
							_ = tcpConn.SetLinger(0)
						}
					}
					_ = conn.Close()
				default:
					t.Errorf("unexpected request for %s: %s %s", mutation, r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			server.EnableHTTP2 = false
			server.StartTLS()
			defer server.Close()

			client := newTestAgentClient(t, server)
			if _, err := client.List(t.Context(), 50, ""); err != nil {
				t.Fatal("warm reused TLS connection failed")
			}
			var err error
			switch mutation {
			case "create":
				_, _, _, err = client.Create(t.Context(), api.CreateAgentRequest{Name: strings.Repeat("a", 8<<20), DisplayName: "East"}, "create-once")
			case "rotate":
				var id api.AgentId
				if decodeErr := json.Unmarshal([]byte(`"00000000-0000-0000-0000-000000000001"`), &id); decodeErr != nil {
					t.Fatal("decode fixture agent ID failed")
				}
				_, _, _, err = client.Rotate(t.Context(), id, api.RotateAgentRequest{CurrentPassword: strings.Repeat("p", 8<<20)}, `"agent:00000000-0000-0000-0000-000000000001:1"`, "rotate-once")
			}
			failure := asAgentFailure(t, err)
			if !failure.Unknown || failure.Code != "outcome_unknown" {
				t.Fatal("connection loss after request dispatch was not classified as unknown")
			}
			select {
			case addr := <-committed:
				warmMu.Lock()
				warmed := warmConnection
				warmMu.Unlock()
				if addr != warmed {
					t.Fatal("server did not commit on the reused TLS connection")
				}
			default:
				t.Fatal("server did not read and commit the mutation before dropping its response")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("%s issuance was replayed after connection loss: attempts=%d", mutation, got)
			}
		})
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

type postAgentsOperationsStub struct {
	setupclient.AgentOperations
	post func(context.Context, *api.PostAgentsParams, api.PostAgentsJSONRequestBody, ...api.RequestEditorFn) (*http.Response, error)
}

func (s postAgentsOperationsStub) PostAgents(ctx context.Context, params *api.PostAgentsParams, body api.PostAgentsJSONRequestBody, editors ...api.RequestEditorFn) (*http.Response, error) {
	return s.post(ctx, params, body, editors...)
}

func TestCreateDistinguishesCancellationFromCleanBodyLoss(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cancel   bool
		wantKind setupclient.Kind
		wantCode string
	}{
		{name: "explicit cancellation", cancel: true, wantKind: setupclient.KindCancelled, wantCode: "cancelled"},
		{name: "body loss", wantKind: setupclient.KindRemote, wantCode: "outcome_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{})
			release := make(chan struct{})
			client := &Client{
				ops: postAgentsOperationsStub{post: func(requestCtx context.Context, _ *api.PostAgentsParams, _ api.PostAgentsJSONRequestBody, _ ...api.RequestEditorFn) (*http.Response, error) {
					reader, writer := io.Pipe()
					go func() {
						_, _ = io.WriteString(writer, "{")
						close(started)
						if tc.cancel {
							<-requestCtx.Done()
						} else {
							<-release
						}
						_ = writer.Close()
					}()
					return &http.Response{StatusCode: http.StatusCreated, Body: reader}, nil
				}},
				editor: func(context.Context, *http.Request) error { return nil },
			}
			done := make(chan error, 1)
			go func() {
				_, _, _, err := client.Create(ctx, api.CreateAgentRequest{Name: "east", DisplayName: "East"}, "response-body")
				done <- err
			}()
			<-started
			if tc.cancel {
				cancel()
			} else {
				close(release)
			}
			failure := asAgentFailure(t, <-done)
			if failure.Kind != tc.wantKind || failure.Code != tc.wantCode || !failure.Unknown {
				t.Fatalf("response body classification=%#v; want kind=%q code=%q unknown=true", failure, tc.wantKind, tc.wantCode)
			}
		})
	}
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
	input := api.CreateAgentRequest{Name: "east", DisplayName: "East"}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := client.Create(ctx, input, "cancel-before-dispatch"); !isFailure(err, setupclient.KindCancelled, false) {
		t.Fatalf("pre-dispatch cancellation=%#v", err)
	} else if f := asAgentFailure(t, err); f.Unknown || strings.Contains(f.Message, "may have completed") {
		t.Fatalf("pre-dispatch cancellation incorrectly warned of an issued mutation: %#v", f)
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
		if !f.Unknown || f.Kind != setupclient.KindCancelled || !strings.Contains(f.Message, "may have completed") {
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
