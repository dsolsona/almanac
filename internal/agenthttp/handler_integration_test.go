//go:build integration

package agenthttp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentmanagement"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

func TestIntegrationTLSAgentManagementUsesSharedSessionHandler(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var clockMu sync.Mutex
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	authService, err := authentication.New(t.Context(), databaseURL, clock)
	if err != nil {
		t.Fatal(err)
	}
	initialPassword, permanentPassword := "HTTP agent initial password 157!", "HTTP agent permanent password 238!"
	passwordFile := filepath.Join(t.TempDir(), "initial-password")
	if err := os.WriteFile(passwordFile, []byte(initialPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := authService.EnsureInitialOwner(t.Context(), "http-agent-admin", passwordFile, "http-agent-bootstrap"); err != nil {
		t.Fatal(err)
	}
	source := netip.MustParseAddr("192.0.2.151")
	restricted, err := authService.Login(t.Context(), authentication.LoginInput{Login: "http-agent-admin", Password: initialPassword, Source: source, RequestID: "http-agent-login"})
	if err != nil {
		t.Fatal(err)
	}
	normal, err := authService.ChangePassword(t.Context(), authentication.ChangePasswordInput{SessionSecret: restricted.Secret, CurrentPassword: initialPassword, NewPassword: permanentPassword, Source: source, RequestID: "http-agent-password"})
	if err != nil {
		t.Fatal(err)
	}
	management, err := agentmanagement.New(databaseURL, authService, clock)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := authhttp.New(authService, authhttp.Options{PublicOrigin: "https://almanac.example", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	agents, err := New(management, auth, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, route := range []string{"/api/v1/auth/context", "/api/v1/auth/login", "/api/v1/auth/password", "/api/v1/auth/logout"} {
		mux.Handle(route, auth)
	}
	mux.Handle("/api/v1/agents", agents)
	mux.Handle("/api/v1/agents/", agents)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	callAs := func(sessionSecret, method, path, body string, origin bool, csrf string, headers map[string]string) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", "__Host-almanac-session="+sessionSecret)
		if origin {
			req.Header.Set("Origin", "https://almanac.example")
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data, resp.Header
	}
	call := func(method, path, body string, origin bool, csrf string, headers map[string]string) (int, []byte, http.Header) {
		return callAs(normal.Secret, method, path, body, origin, csrf, headers)
	}
	callMulti := func(method, path, body string, origin bool, csrf string, headers map[string][]string) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal("create multi-header request failed")
		}
		req.Header.Set("Cookie", "__Host-almanac-session="+normal.Secret)
		if origin {
			req.Header.Add("Origin", "https://almanac.example")
		}
		if csrf != "" {
			req.Header.Add("X-CSRF-Token", csrf)
		}
		for key, values := range headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("send multi-header request failed")
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal("read multi-header response failed")
		}
		return resp.StatusCode, data, resp.Header
	}
	status, body, _ := call(http.MethodPost, "/api/v1/auth/context", "{}", true, "", map[string]string{"Content-Type": "application/json"})
	if status != 200 {
		t.Fatalf("session context status=%d", status)
	}
	var context api.AuthResponse
	contextDecodeErr := json.Unmarshal(body, &context)
	if contextDecodeErr != nil || context.CsrfToken == "" {
		t.Fatalf("session context invalid (decode-error=%t, csrf-present=%t)", contextDecodeErr != nil, context.CsrfToken != "")
	}
	db := testpostgres.Connect(t, databaseURL)
	counts := func() (int, int, int) {
		t.Helper()
		var agentsCount, credentialsCount, receiptsCount int
		if err := db.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agents").Scan(&agentsCount); err != nil {
			t.Fatal("read agent count failed")
		}
		if err := db.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials").Scan(&credentialsCount); err != nil {
			t.Fatal("read credential count failed")
		}
		if err := db.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts").Scan(&receiptsCount); err != nil {
			t.Fatal("read receipt count failed")
		}
		return agentsCount, credentialsCount, receiptsCount
	}
	countSnapshot := func() [3]int { a, c, r := counts(); return [3]int{a, c, r} }
	assertNoMutation := func(before [3]int) {
		t.Helper()
		a, c, r := counts()
		if [3]int{a, c, r} != before {
			t.Fatalf("denied command changed persistent management counts (agents=%d credentials=%d receipts=%d)", a, c, r)
		}
	}
	baseline := [3]int{}
	if a, c, r := counts(); a != 0 || c != 0 || r != 0 {
		t.Fatalf("fresh fixture had management rows (agents=%d credentials=%d receipts=%d)", a, c, r)
	}
	adminVerifier, err := identity.SessionSecretVerifier(normal.Secret)
	if err != nil {
		t.Fatal("derive session verifier failed")
	}
	if _, err := db.Exec(t.Context(), "UPDATE public.almanac_sessions SET restricted=true WHERE verifier=$1", adminVerifier[:]); err != nil {
		t.Fatal("mark session restricted failed")
	}
	status, body, _ = call(http.MethodPost, "/api/v1/agents", `{"current_password":"`+permanentPassword+`","name":"restricted","display_name":"Restricted"}`, true, context.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "restricted-create"})
	assertProblem(t, status, body, 403, "forbidden")
	assertNoMutation(baseline)
	if _, err := db.Exec(t.Context(), "UPDATE public.almanac_sessions SET restricted=false WHERE verifier=$1", adminVerifier[:]); err != nil {
		t.Fatal("restore session restriction failed")
	}
	if _, err := db.Exec(t.Context(), "UPDATE public.almanac_users SET role='viewer' WHERE id=(SELECT user_id FROM public.almanac_sessions WHERE verifier=$1)", adminVerifier[:]); err != nil {
		t.Fatal("demote administrator fixture failed")
	}
	status, body, _ = call(http.MethodPost, "/api/v1/agents", `{"current_password":"`+permanentPassword+`","name":"viewer","display_name":"Viewer"}`, true, context.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "viewer-create"})
	assertProblem(t, status, body, 403, "forbidden")
	assertNoMutation(baseline)
	if _, err := db.Exec(t.Context(), "UPDATE public.almanac_users SET role='administrator' WHERE id=(SELECT user_id FROM public.almanac_sessions WHERE verifier=$1)", adminVerifier[:]); err != nil {
		t.Fatal("restore administrator role failed")
	}
	wrongPasswordHeaders := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "wrong-password-create"}
	status, body, _ = call(http.MethodPost, "/api/v1/agents", `{"current_password":"wrong password","name":"wrong-password","display_name":"Wrong Password"}`, true, context.CsrfToken, wrongPasswordHeaders)
	assertProblem(t, status, body, 401, "unauthenticated")
	assertNoMutation(baseline)
	revokedSession, err := authService.Login(t.Context(), authentication.LoginInput{Login: "http-agent-admin", Password: permanentPassword, Source: source, RequestID: "http-agent-revoked-login"})
	if err != nil {
		t.Fatal("create revoked-session fixture failed")
	}
	status, body, _ = callAs(revokedSession.Secret, http.MethodPost, "/api/v1/auth/context", "{}", true, "", map[string]string{"Content-Type": "application/json"})
	var revokedContext api.AuthResponse
	if status != 200 || json.Unmarshal(body, &revokedContext) != nil || revokedContext.CsrfToken == "" {
		t.Fatal("live revoked-session CSRF context unavailable")
	}
	revokedVerifier, err := identity.SessionSecretVerifier(revokedSession.Secret)
	if err != nil {
		t.Fatal("derive revoked session verifier failed")
	}
	if _, err := db.Exec(t.Context(), "DELETE FROM public.almanac_sessions WHERE verifier=$1", revokedVerifier[:]); err != nil {
		t.Fatal("revoke session fixture failed")
	}
	status, body, _ = callAs(revokedSession.Secret, http.MethodPost, "/api/v1/agents", `{"current_password":"`+permanentPassword+`","name":"revoked-session","display_name":"Revoked Session"}`, true, revokedContext.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "revoked-session-create"})
	assertProblem(t, status, body, 401, "unauthenticated")
	assertNoMutation(baseline)
	expiredSession, err := authService.Login(t.Context(), authentication.LoginInput{Login: "http-agent-admin", Password: permanentPassword, Source: source, RequestID: "http-agent-expired-login"})
	if err != nil {
		t.Fatal("create expired-session fixture failed")
	}
	status, body, _ = callAs(expiredSession.Secret, http.MethodPost, "/api/v1/auth/context", "{}", true, "", map[string]string{"Content-Type": "application/json"})
	var expiredContext api.AuthResponse
	if status != 200 || json.Unmarshal(body, &expiredContext) != nil || expiredContext.CsrfToken == "" {
		t.Fatal("live expired-session CSRF context unavailable")
	}
	expiredVerifier, err := identity.SessionSecretVerifier(expiredSession.Secret)
	if err != nil {
		t.Fatal("derive expired session verifier failed")
	}
	clockMu.Lock()
	expiredAt := now.Add(-365 * 24 * time.Hour)
	clockMu.Unlock()
	if _, err := db.Exec(t.Context(), "UPDATE public.almanac_sessions SET created_at=$2,last_seen_at=$2 WHERE verifier=$1", expiredVerifier[:], expiredAt); err != nil {
		t.Fatal("expire session fixture failed")
	}
	status, body, _ = callAs(expiredSession.Secret, http.MethodPost, "/api/v1/agents", `{"current_password":"`+permanentPassword+`","name":"expired-session","display_name":"Expired Session"}`, true, expiredContext.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "expired-session-create"})
	assertProblem(t, status, body, 401, "unauthenticated")
	assertNoMutation(baseline)
	invalidCreateHeaders := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "http-invalid-create"}
	for _, invalidBody := range []string{
		`{"current_password":"` + permanentPassword + `","name":"invalid","display_name":"Invalid","lifetime_seconds":null}`,
		`{"current_password":"` + permanentPassword + `","name":"invalid","name":"duplicate","display_name":"Invalid"}`,
		`{"current_password":"` + permanentPassword + `","name":"invalid","display_name":"Invalid","unknown":true}`,
		`{"current_password":"` + permanentPassword + `","name":"invalid","display_name":"Invalid","lifetime_seconds":1e2}`,
		`{"current_password":"` + permanentPassword + `","name":"invalid"}`,
		`{"current_password":null,"name":"invalid","display_name":"Invalid"}`,
		`{"current_password":"` + permanentPassword + `","name":7,"display_name":"Invalid"}`,
		`{"current_password":"` + permanentPassword + `","name":"invalid","display_name":"Invalid"} {}`,
	} {
		status, _, _ = call(http.MethodPost, "/api/v1/agents", invalidBody, true, context.CsrfToken, invalidCreateHeaders)
		if status != 400 {
			t.Fatalf("malformed create body returned status %d", status)
		}
	}
	status, _, _ = call(http.MethodPost, "/api/v1/agents", strings.Repeat("x", bodyLimit+1), true, context.CsrfToken, invalidCreateHeaders)
	if status != 413 {
		t.Fatalf("oversized create body returned status %d", status)
	}
	status, _, _ = call(http.MethodPost, "/api/v1/agents", `{}`, true, context.CsrfToken, map[string]string{"Content-Type": "text/plain", "Idempotency-Key": "unsupported-media"})
	if status != 415 {
		t.Fatalf("unsupported media type returned status %d", status)
	}
	status, _, _ = call(http.MethodPost, "/api/v1/agents", `{}`, true, context.CsrfToken, map[string]string{"Content-Type": "application/json", "Content-Encoding": "gzip", "Idempotency-Key": "unsupported-encoding"})
	if status != 415 {
		t.Fatalf("unsupported content encoding returned status %d", status)
	}
	status, _, _ = callMulti(http.MethodPost, "/api/v1/agents", `{}`, true, context.CsrfToken, map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"duplicate-one", "duplicate-two"}})
	if status != 400 {
		t.Fatalf("duplicate Idempotency-Key returned status %d", status)
	}
	for _, malformedQuery := range []string{"/api/v1/agents?limit=%zz", "/api/v1/agents?limit=1;cursor=x"} {
		status, _, _ = call(http.MethodGet, malformedQuery, "", false, "", nil)
		if status != 400 {
			t.Fatalf("malformed query returned status %d", status)
		}
	}
	status, _, _ = call(http.MethodPost, "/api/v1/agents?limit=%zz", `{}`, true, context.CsrfToken, invalidCreateHeaders)
	if status != 400 {
		t.Fatalf("malformed mutation query returned status %d", status)
	}
	status, _, _ = call(http.MethodHead, "/api/v1/agents", "", false, "", nil)
	if status != 405 {
		t.Fatalf("HEAD collection returned status %d", status)
	}
	chunkedRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/agents", strings.NewReader(""))
	if err != nil {
		t.Fatal("construct chunked GET failed")
	}
	chunkedRequest.Header.Set("Cookie", "__Host-almanac-session="+normal.Secret)
	chunkedRequest.ContentLength = -1
	chunkedRequest.TransferEncoding = []string{"chunked"}
	chunkedResponse, err := client.Do(chunkedRequest)
	if err != nil {
		t.Fatal("send chunked GET failed")
	}
	chunkedResponse.Body.Close()
	if chunkedResponse.StatusCode != 400 {
		t.Fatalf("chunked GET returned status %d", chunkedResponse.StatusCode)
	}
	rawConn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", server.Listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal("open slow-body TLS connection failed")
	}
	if err := rawConn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal("set slow-body client bound failed")
	}
	_, err = fmt.Fprintf(rawConn, "POST /api/v1/agents HTTP/1.1\r\nHost: %s\r\nOrigin: https://almanac.example\r\nCookie: __Host-almanac-session=%s\r\nX-CSRF-Token: %s\r\nContent-Type: application/json\r\nIdempotency-Key: slow-body-key\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n4\r\n{", strings.TrimPrefix(server.URL, "https://"), normal.Secret, context.CsrfToken)
	if err != nil {
		t.Fatal("write partial slow-body request failed")
	}
	slowResponse, err := http.ReadResponse(bufio.NewReader(rawConn), &http.Request{Method: http.MethodPost})
	_ = rawConn.Close()
	if err != nil {
		t.Fatal("read bounded slow-body response failed")
	}
	slowResponse.Body.Close()
	if slowResponse.StatusCode != 408 {
		t.Fatalf("slow request body returned status %d", slowResponse.StatusCode)
	}
	status, _, _ = call(http.MethodGet, "/api/v1/agents?limit=01", "", false, "", nil)
	if status != 400 {
		t.Fatalf("noncanonical limit returned status %d", status)
	}
	status, body, headers := call(http.MethodGet, "/api/v1/agents", "", false, "", nil)
	if status != 200 {
		t.Fatalf("same-origin GET without Origin status=%d", status)
	}
	var page api.AgentPage
	if err := json.Unmarshal(body, &page); err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("empty agent page was invalid (decode-error=%t, nil-items=%t, item-count=%d)", err != nil, page.Items == nil, len(page.Items))
	}
	if headers.Get("X-Request-ID") == "" || headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("managed response headers=%v", headers)
	}
	createHeaders := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "http-create-comma,key"}
	status, body, _ = call(http.MethodPost, "/api/v1/agents", `{"current_password":"`+permanentPassword+`","name":"http-agent","display_name":"HTTP Agent"}`, true, context.CsrfToken, createHeaders)
	if status != 201 {
		t.Fatalf("create status=%d", status)
	}
	var issuance map[string]any
	if err := json.Unmarshal(body, &issuance); err != nil {
		t.Fatal("fresh issuance JSON could not be decoded")
	}
	if issuance["token"] == nil || issuance["secret_state"] != "issued" {
		t.Fatalf("fresh issuance state invalid (token-present=%t, secret-state=%v)", issuance["token"] != nil, issuance["secret_state"])
	}
	createToken, tokenOK := issuance["token"].(string)
	credentialDTO, credentialOK := issuance["credential"].(map[string]any)
	if !tokenOK || !credentialOK {
		t.Fatal("fresh issuance fields had unexpected types")
	}
	credentialID, credentialIDOK := credentialDTO["id"].(string)
	if !credentialIDOK || verifyPersistedToken(t, databaseURL, createToken) != credentialID {
		t.Fatal("fresh create token did not match persisted credential")
	}
	afterCreate := countSnapshot()
	status, body, _ = call(http.MethodPost, "/api/v1/agents", `{"current_password":"wrong password","name":"http-agent","display_name":"HTTP Agent"}`, true, context.CsrfToken, createHeaders)
	assertProblem(t, status, body, 401, "unauthenticated")
	assertNoMutation(afterCreate)
	agent, ok := issuance["agent"].(map[string]any)
	if !ok {
		t.Fatal("fresh issuance agent field was not an object")
	}
	agentID, ok := agent["id"].(string)
	if !ok {
		t.Fatal("fresh issuance agent ID was not a string")
	}
	tag := `"agent:` + agentID + `:1"`
	status, body, _ = call(http.MethodPost, "/api/v1/agents", `{"current_password":"`+permanentPassword+`","name":"http-agent","display_name":"HTTP Agent"}`, true, context.CsrfToken, createHeaders)
	if status != 200 {
		t.Fatalf("creation replay status=%d", status)
	}
	issuance = map[string]any{}
	if err := json.Unmarshal(body, &issuance); err != nil {
		t.Fatal("replay JSON could not be decoded")
	}
	_, tokenPresent := issuance["token"]
	if tokenPresent || issuance["secret_state"] != "secret_unavailable" {
		t.Fatalf("replay state invalid (token-present=%t, secret-state=%v)", tokenPresent, issuance["secret_state"])
	}
	status, body, headers = call(http.MethodGet, "/api/v1/agents/"+agentID, "", false, "", nil)
	if status != 200 || headers.Get("ETag") != tag {
		t.Fatalf("detail status=%d etag-matched=%t", status, headers.Get("ETag") == tag)
	}
	for index, badTag := range []string{"W/" + tag, "*", tag + "," + tag, `"agent:` + agentID + `:01"`, `"agent:` + agentID + `:9223372036854775808"`} {
		status, body, _ = call(http.MethodPost, "/api/v1/agents/"+agentID+"/disable", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": fmt.Sprintf("bad-tag-%d", index), "If-Match": badTag})
		assertProblem(t, status, body, 400, "invalid_request")
	}
	status, body, _ = callMulti(http.MethodPost, "/api/v1/agents/"+agentID+"/disable", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, map[string][]string{"Content-Type": {"application/json"}, "Idempotency-Key": {"duplicate-etag"}, "If-Match": {tag, tag}})
	assertProblem(t, status, body, 400, "invalid_request")
	missingID := "00000000-0000-4000-8000-000000000001"
	status, body, _ = call(http.MethodPost, "/api/v1/agents/"+missingID+"/disable", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "missing-target", "If-Match": `"agent:00000000-0000-4000-8000-000000000001:1"`})
	assertProblem(t, status, body, 404, "not_found")
	status, body, _ = call(http.MethodPost, "/api/v1/agents/"+agentID+"/credentials/"+missingID+"/revoke", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "wrong-parent-credential", "If-Match": tag})
	assertProblem(t, status, body, 404, "not_found")
	status, body, _ = call(http.MethodGet, "/api/v1/agents/"+agentID+"/credentials", "", false, "", nil)
	var history api.CredentialPage
	if status != 200 || json.Unmarshal(body, &history) != nil || len(history.Items) != 1 || history.Items[0].RevokedAt != nil || history.Items[0].RetireAt != nil {
		t.Fatalf("initial credential history invalid (status=%d, decode-error=%t, items=%d)", status, json.Unmarshal(body, &history) != nil, len(history.Items))
	}
	rotateHeaders := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "http-rotate-1", "If-Match": tag}
	status, body, _ = call(http.MethodPost, "/api/v1/agents/"+agentID+"/credentials/rotate", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, rotateHeaders)
	var rotated api.RotationIssuance
	if status != 200 || json.Unmarshal(body, &rotated) != nil || rotated.Token == nil || *rotated.Token == "" || rotated.Replayed || rotated.SecretState != api.RotationIssuanceSecretStateIssued || rotated.Agent.Version != "2" || rotated.PreviousCredential == nil {
		t.Fatalf("rotation invalid (status=%d, decode-error=%t, token-present=%t, replayed=%t, version=%s, previous-present=%t)", status, json.Unmarshal(body, &rotated) != nil, rotated.Token != nil && *rotated.Token != "", rotated.Replayed, rotated.Agent.Version, rotated.PreviousCredential != nil)
	}
	if got := verifyPersistedToken(t, databaseURL, *rotated.Token); got != rotated.Credential.Id.String() {
		t.Fatal("fresh rotation token did not match persisted credential")
	}
	status, body, _ = call(http.MethodPost, "/api/v1/agents/"+agentID+"/disable", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "stale-version", "If-Match": tag})
	assertProblem(t, status, body, 409, "conflict")
	rotatedCredentialID := rotated.Credential.Id.String()
	revokeHeaders := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "http-revoke-1", "If-Match": `"agent:` + agentID + `:2"`}
	status, body, _ = call(http.MethodPost, "/api/v1/agents/"+agentID+"/credentials/"+rotatedCredentialID+"/revoke", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, revokeHeaders)
	var revoked api.LifecycleResult
	if status != 200 || json.Unmarshal(body, &revoked) != nil || !revoked.Changed || revoked.Agent.Version != "3" || revoked.Credential == nil || revoked.Credential.RevokedAt == nil {
		t.Fatalf("revoke invalid (status=%d, decode-error=%t, changed=%t, version=%s, credential-present=%t, revoked-at-present=%t)", status, json.Unmarshal(body, &revoked) != nil, revoked.Changed, revoked.Agent.Version, revoked.Credential != nil, revoked.Credential != nil && revoked.Credential.RevokedAt != nil)
	}
	disableHeaders := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "http-disable-1", "If-Match": `"agent:` + agentID + `:3"`}
	status, body, _ = call(http.MethodPost, "/api/v1/agents/"+agentID+"/disable", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, disableHeaders)
	var disabled api.LifecycleResult
	if status != 200 || json.Unmarshal(body, &disabled) != nil || !disabled.Changed || disabled.Agent.Enabled || disabled.Agent.Version != "4" {
		t.Fatalf("disable invalid (status=%d, decode-error=%t, changed=%t, enabled=%t, version=%s)", status, json.Unmarshal(body, &disabled) != nil, disabled.Changed, disabled.Agent.Enabled, disabled.Agent.Version)
	}
	disableAgainHeaders := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "http-disable-noop", "If-Match": `"agent:` + agentID + `:4"`}
	status, body, _ = call(http.MethodPost, "/api/v1/agents/"+agentID+"/disable", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, disableAgainHeaders)
	var noOp api.LifecycleResult
	if status != 200 || json.Unmarshal(body, &noOp) != nil || noOp.Changed || noOp.Agent.Version != "4" {
		t.Fatalf("disable no-op invalid (status=%d, decode-error=%t, changed=%t, version=%s)", status, json.Unmarshal(body, &noOp) != nil, noOp.Changed, noOp.Agent.Version)
	}
	status, body, _ = call(http.MethodPost, "/api/v1/agents/"+agentID+"/credentials/rotate", `{"current_password":"`+permanentPassword+`"}`, true, context.CsrfToken, rotateHeaders)
	var rotationReplay api.RotationIssuance
	var rotationReplayWire map[string]any
	decodeReplayErr := json.Unmarshal(body, &rotationReplay) != nil || json.Unmarshal(body, &rotationReplayWire) != nil
	_, replayTokenPresent := rotationReplayWire["token"]
	if status != 200 || decodeReplayErr || replayTokenPresent || rotationReplay.Token != nil || !rotationReplay.Replayed || rotationReplay.SecretState != api.RotationIssuanceSecretStateSecretUnavailable || rotationReplay.Agent.Version != "2" {
		t.Fatalf("historical rotation replay invalid (status=%d, decode-error=%t, token-member-present=%t, replayed=%t, version=%s)", status, decodeReplayErr, replayTokenPresent, rotationReplay.Replayed, rotationReplay.Agent.Version)
	}
	beforeLostResponse := countSnapshot()
	lostRequest, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/agents", strings.NewReader(`{"current_password":"`+permanentPassword+`","name":"lost-response","display_name":"Lost Response"}`))
	if err != nil {
		t.Fatal("create lost-response request failed")
	}
	lostRequest.Header.Set("Cookie", "__Host-almanac-session="+normal.Secret)
	lostRequest.Header.Set("Origin", "https://almanac.example")
	lostRequest.Header.Set("X-CSRF-Token", context.CsrfToken)
	lostRequest.Header.Set("Content-Type", "application/json")
	lostRequest.Header.Set("Idempotency-Key", "http-lost-response")
	lostResponse, err := client.Do(lostRequest)
	if err != nil {
		t.Fatal("send lost-response request failed")
	}
	if lostResponse.StatusCode != 201 {
		lostResponse.Body.Close()
		t.Fatalf("lost-response create status=%d", lostResponse.StatusCode)
	}
	_ = lostResponse.Body.Close()
	afterLostResponse := countSnapshot()
	if afterLostResponse[0] != beforeLostResponse[0]+1 || afterLostResponse[1] != beforeLostResponse[1]+1 || afterLostResponse[2] != beforeLostResponse[2]+1 {
		t.Fatalf("lost-response command did not persist exactly once (agent-delta=%d credential-delta=%d receipt-delta=%d)", afterLostResponse[0]-beforeLostResponse[0], afterLostResponse[1]-beforeLostResponse[1], afterLostResponse[2]-beforeLostResponse[2])
	}
	status, body, _ = call(http.MethodPost, "/api/v1/agents", `{"current_password":"`+permanentPassword+`","name":"lost-response","display_name":"Lost Response"}`, true, context.CsrfToken, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "http-lost-response"})
	if status != 200 {
		t.Fatalf("lost-response replay status=%d", status)
	}
	var lostReplay map[string]any
	if json.Unmarshal(body, &lostReplay) != nil {
		t.Fatal("lost-response replay JSON decode failed")
	}
	if _, present := lostReplay["token"]; present {
		t.Fatal("lost-response replay disclosed token")
	}
	if after := countSnapshot(); after != afterLostResponse {
		t.Fatalf("lost-response replay mutated management counts (agents=%d credentials=%d receipts=%d)", after[0], after[1], after[2])
	}
	status, body, _ = call(http.MethodGet, "/api/v1/agents/"+agentID+"/credentials", "", false, "", nil)
	if status != 200 || json.Unmarshal(body, &history) != nil || len(history.Items) != 2 {
		t.Fatalf("final credential history invalid (status=%d, decode-error=%t, items=%d)", status, json.Unmarshal(body, &history) != nil, len(history.Items))
	}
}

func verifyPersistedToken(t *testing.T, databaseURL, token string) string {
	t.Helper()
	credentialID, verifier, err := identity.AgentTokenVerifier(token)
	if err != nil {
		t.Fatal("issued credential token failed verifier parsing")
	}
	conn := testpostgres.Connect(t, databaseURL)
	var stored []byte
	if err := conn.QueryRow(t.Context(), "SELECT verifier FROM public.almanac_agent_credentials WHERE id=$1", credentialID).Scan(&stored); err != nil {
		t.Fatal("persisted credential verifier missing")
	}
	if !bytes.Equal(stored, verifier[:]) {
		t.Fatal("persisted credential verifier did not match issued token")
	}
	return credentialID
}

func assertProblem(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
	t.Helper()
	var problem api.Problem
	if status != wantStatus || json.Unmarshal(body, &problem) != nil || problem.Code != wantCode {
		t.Fatalf("safe problem mapping mismatch (status=%d, code=%q)", status, problem.Code)
	}
	var fields map[string]any
	if json.Unmarshal(body, &fields) != nil {
		t.Fatal("problem JSON could not be inspected")
	}
	if _, exists := fields["token"]; exists {
		t.Fatal("problem response contained a token member")
	}
}
