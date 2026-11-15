//go:build integration

package alerthttp

import (
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/agentmanagement"
	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/authhttp"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

type alertHTTPFixture struct {
	databaseURL, baseURL, secret, csrf, sloID string
	verifier                                  [32]byte
	client                                    *http.Client
}

func newAlertHTTPFixture(t *testing.T) alertHTTPFixture {
	t.Helper()
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	clock := func() time.Time { return now }
	authService, err := authentication.New(t.Context(), databaseURL, clock)
	if err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(t.TempDir(), "initial-password")
	initial := "Alert HTTP fixture initial password 239!"
	if err := os.WriteFile(passwordFile, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := authService.EnsureInitialOwner(t.Context(), "alert-http-owner", passwordFile, "alert-http-bootstrap"); err != nil {
		t.Fatal(err)
	}
	source := netip.MustParseAddr("192.0.2.154")
	restricted, err := authService.Login(t.Context(), authentication.LoginInput{Login: "alert-http-owner", Password: initial, Source: source, RequestID: "alert-http-login"})
	if err != nil {
		t.Fatal(err)
	}
	normal, err := authService.ChangePassword(t.Context(), authentication.ChangePasswordInput{SessionSecret: restricted.Secret, NewPassword: "Alert HTTP fixture permanent password 893!", Source: source, RequestID: "alert-http-password"})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := identity.SessionSecretVerifier(normal.Secret)
	if err != nil {
		t.Fatal("derive fixture session verifier")
	}
	proof := storage.DatasourceProof{SessionVerifier: verifier}
	management, err := agentmanagement.New(databaseURL, authService, clock)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := management.Create(t.Context(), agentmanagement.CreateInput{SessionSecret: normal.Secret, Name: "alert-http-agent", DisplayName: "Alert HTTP agent", IdempotencyKey: "alert-http-agent", RequestID: "alert-http-agent"})
	if err != nil {
		t.Fatal(err)
	}
	credentialID, credentialVerifier, err := identity.AgentTokenVerifier(agent.Token)
	if err != nil {
		t.Fatal("derive fixture agent verifier")
	}
	hello := &storage.AgentRuntimeHello{Protocol: agentprotocol.ProtocolV2, SoftwareVersion: "integration-test", Capabilities: []byte(`[{"provider":"prometheus","configuration_formats":["prometheus-http-v1"],"operations":["collect","connection_test","query_preview"]}]`)}
	if _, err := storage.AcquireAgentOwnershipWithHello(t.Context(), databaseURL, credentialID, credentialVerifier, sha256.Sum256([]byte("alert-http-fixture-resume")), hello, clock); err != nil {
		t.Fatal(err)
	}
	settings := datasource.DefaultSettings("alert-http-source", "Alert HTTP source", "https://metrics.example.test", agent.Creation.Agent.ID)
	connection, err := storage.CreateDatasource(t.Context(), databaseURL, nil, storage.CreateDatasourceInput{Proof: proof, Settings: settings, Password: datasource.SecretAction{Action: "clear"}, Token: datasource.SecretAction{Action: "clear"}, IdempotencyKey: "alert-http-source", RequestID: "alert-http-source", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := storage.CreateSli(t.Context(), databaseURL, storage.CreateSliInput{Proof: proof, Name: "alert-http-sli", DisplayName: "Alert HTTP SLI", DatasourceID: connection.ID, MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total", IdempotencyKey: "alert-http-sli", RequestID: "alert-http-sli", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	service, _, err := storage.CreateObjective(t.Context(), databaseURL, storage.CreateObjectiveInput{Proof: proof, Name: "alert-http-service", DisplayName: "Alert HTTP service", IdempotencyKey: "alert-http-service", RequestID: "alert-http-service", Now: clock}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, objective, err := storage.CreateObjective(t.Context(), databaseURL, storage.CreateObjectiveInput{Proof: proof, Name: "alert-http-slo", DisplayName: "Alert HTTP SLO", ServiceID: service.ID, SliID: measurement.ID, Target: "0.999", RollingWindowSeconds: 30 * 86400, IdempotencyKey: "alert-http-slo", RequestID: "alert-http-slo", Now: clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := storage.OpenRuntime(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	auth, err := authhttp.New(authService, authhttp.Options{PublicOrigin: "https://almanac.example", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(databaseURL, auth, Options{Runtime: runtime, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/auth/context", auth)
	mux.Handle("/api/v1/", handler)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := server.Client() // Trusts this fixture's certificate; TLS verification remains enabled.
	client.Timeout = 10 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	f := alertHTTPFixture{databaseURL: databaseURL, baseURL: server.URL, secret: normal.Secret, sloID: objective.ID, verifier: verifier, client: client}
	body, _ := f.call(t, http.MethodPost, "/api/v1/auth/context", "{}", "", "", http.StatusOK)
	var context api.AuthResponse
	if err := json.Unmarshal(body, &context); err != nil || context.CsrfToken == "" {
		t.Fatal("authenticated CSRF context unavailable")
	}
	f.csrf = context.CsrfToken
	return f
}

func (f alertHTTPFixture) call(t *testing.T, method, path, body, key, etag string, want int) ([]byte, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: f.secret})
	req.Header.Set("Origin", "https://almanac.example")
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		if f.csrf != "" {
			req.Header.Set("X-CSRF-Token", f.csrf)
		}
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d", method, path, response.StatusCode, want)
	}
	return data, response.Header
}

func decodeAlertHTTPCommand(t *testing.T, body []byte) api.AlertCommandResult {
	t.Helper()
	var result api.AlertCommandResult
	if err := json.Unmarshal(body, &result); err != nil || result.Policy == nil || result.RequestId == "" {
		t.Fatalf("invalid policy command result: decode=%v", err)
	}
	return result
}

func TestIntegrationAlertHTTPPolicyJourney(t *testing.T) {
	t.Parallel()
	f := newAlertHTTPFixture(t)
	conditions := `[{"id":"fast","short_window_seconds":300,"long_window_seconds":3600,"threshold":"1"},{"id":"sustained","short_window_seconds":1800,"long_window_seconds":21600,"threshold":"2"},{"id":"third","short_window_seconds":600,"long_window_seconds":7200,"threshold":"3"}]`
	create := fmt.Sprintf(`{"name":"Checkout alert","slo_id":%q,"conditions":%s}`, f.sloID, conditions)
	body, headers := f.call(t, http.MethodPost, "/api/v1/alert-policies", create, "create-checkout", "", 201)
	original := decodeAlertHTTPCommand(t, body)
	policy := original.Policy
	path := "/api/v1/alert-policies/" + policy.Id.String()
	originalETag := headers.Get("ETag")
	if policy.Version != "1" || policy.EvaluationRevision != "1" || !policy.Enabled || policy.Severity != "medium" || len(policy.Conditions) != 3 || policy.Conditions[2].Threshold != "3" || originalETag != fmt.Sprintf(`"alert-policy:%s:1"`, policy.Id) {
		t.Fatal("creation lost defaults, exact third condition or initial identity")
	}
	body, headers = f.call(t, http.MethodGet, path, "", "", "", 200)
	var read api.AlertPolicy
	if json.Unmarshal(body, &read) != nil || read.Id != policy.Id || read.Version != "1" || headers.Get("ETag") != originalETag {
		t.Fatal("policy read disagrees with committed create")
	}
	body, _ = f.call(t, http.MethodGet, "/api/v1/alert-policies?limit=20", "", "", "", 200)
	var page api.AlertPolicyPage
	if json.Unmarshal(body, &page) != nil || page.Total != "1" || len(page.Items) != 1 || page.Items[0].Policy.Id != policy.Id || page.Items[0].OpenOccurrenceId != nil {
		t.Fatal("policy without an occurrence was not listed correctly")
	}
	body, _ = f.call(t, http.MethodPost, "/api/v1/alert-policies", create, "create-checkout", "", 201)
	replay := decodeAlertHTTPCommand(t, body)
	if replay.RequestId != original.RequestId || replay.Policy.Id != policy.Id || replay.Policy.Version != "1" {
		t.Fatal("create replay did not preserve original command result")
	}
	body, _ = f.call(t, http.MethodGet, "/api/v1/alert-commands?operation=alert-policy.create&idempotency_key=create-checkout", "", "", "", 200)
	var lookup api.AlertCommandLookup
	if json.Unmarshal(body, &lookup) != nil || lookup.Status != api.AlertCommandLookupStatusCommitted || lookup.Result == nil || lookup.Result.RequestId != original.RequestId {
		t.Fatal("actor command lookup failed to reconcile committed creation")
	}
	f.call(t, http.MethodPost, "/api/v1/alert-policies", strings.Replace(create, "Checkout alert", "Different body", 1), "create-checkout", "", 409)
	metadata := `{"name":"Checkout renamed","severity":"high"}`
	body, headers = f.call(t, http.MethodPatch, path, metadata, "metadata", originalETag, 200)
	meta := decodeAlertHTTPCommand(t, body)
	if meta.Policy.Version != "2" || meta.Policy.EvaluationRevision != "1" || meta.Policy.Name != "Checkout renamed" || meta.Policy.Severity != "high" {
		t.Fatal("metadata change changed evaluation meaning or failed to persist")
	}
	f.call(t, http.MethodPatch, path, metadata, "stale-version", originalETag, 409)
	semantic := `{"conditions":` + strings.Replace(conditions, `"threshold":"3"`, `"threshold":"4"`, 1) + `,"reason":"Adjust third condition"}`
	body, headers = f.call(t, http.MethodPatch, path, semantic, "semantic", headers.Get("ETag"), 200)
	changed := decodeAlertHTTPCommand(t, body)
	if changed.Policy.Version != "3" || changed.Policy.EvaluationRevision != "2" || changed.Policy.Conditions[2].Threshold != "4" {
		t.Fatal("semantic update did not start a new evaluation revision")
	}
	body, headers = f.call(t, http.MethodPost, path+"/disable", `{"reason":"Pause policy"}`, "disable", headers.Get("ETag"), 200)
	disabled := decodeAlertHTTPCommand(t, body)
	if disabled.Policy.Enabled || disabled.Policy.Version != "4" || disabled.Policy.EvaluationRevision != "2" {
		t.Fatal("disable did not preserve revision and disable configuration")
	}
	body, _ = f.call(t, http.MethodPost, path+"/enable", `{}`, "enable", headers.Get("ETag"), 200)
	enabled := decodeAlertHTTPCommand(t, body)
	if !enabled.Policy.Enabled || enabled.Policy.Version != "5" || enabled.Policy.EvaluationRevision != "3" {
		t.Fatal("enable did not start a fresh evaluation revision")
	}
	// A replay precedes current-version validation and returns its old result,
	// without reverting the current configuration.
	body, _ = f.call(t, http.MethodPatch, path, metadata, "metadata", originalETag, 200)
	replayedMeta := decodeAlertHTTPCommand(t, body)
	if replayedMeta.RequestId != meta.RequestId || replayedMeta.Policy.Version != "2" {
		t.Fatal("metadata replay lost its original result")
	}
	body, _ = f.call(t, http.MethodGet, path, "", "", "", 200)
	if json.Unmarshal(body, &read) != nil || read.Version != "5" || read.EvaluationRevision != "3" {
		t.Fatal("replay mutated current configuration")
	}
	savedDisabled := strings.Replace(create, `"name":"Checkout alert"`, `"name":"Saved disabled","enabled":false`, 1)
	body, _ = f.call(t, http.MethodPost, "/api/v1/alert-policies", savedDisabled, "saved-disabled", "", 201)
	saved := decodeAlertHTTPCommand(t, body)
	if saved.Policy.Enabled || saved.Policy.Version != "1" || saved.Policy.EvaluationRevision != "1" {
		t.Fatal("initial disabled configuration was implemented as a second mutation")
	}
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"null object", `null`, 400},
		{"null field", strings.Replace(create, `"name":"Checkout alert"`, `"name":null`, 1), 400},
		{"unknown field", strings.TrimSuffix(create, "}") + `,"unknown":true}`, 400},
		{"duplicate key", strings.Replace(create, `"name":"Checkout alert"`, `"name":"A","name":"B"`, 1), 400},
		{"oversize", `{"name":"` + strings.Repeat("x", 64<<10) + `"}`, 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, _ := f.call(t, http.MethodPost, "/api/v1/alert-policies", test.body, "invalid-body", "", test.status)
			var problem api.Problem
			if json.Unmarshal(body, &problem) != nil || problem.Code != "invalid_request" || problem.RequestId == "" {
				t.Fatal("invalid body did not return a safe actionable Problem")
			}
		})
	}
	f.call(t, http.MethodGet, "/api/v1/alert-policies?limit=1&limit=2", "", "", "", 400)
	db := testpostgres.Connect(t, f.databaseURL)
	if _, err := db.Exec(t.Context(), `UPDATE almanac_users SET role='viewer' WHERE id=(SELECT user_id FROM almanac_sessions WHERE verifier=$1)`, f.verifier[:]); err != nil {
		t.Fatal(err)
	}
	body, _ = f.call(t, http.MethodGet, "/api/v1/alert-policies", "", "", "", 200)
	if json.Unmarshal(body, &page) != nil || page.Total != "2" {
		t.Fatal("viewer cannot read policies or rejected requests changed policy count")
	}
	f.call(t, http.MethodPost, "/api/v1/alert-policies", strings.Replace(create, "Checkout alert", "Viewer mutation", 1), "viewer-create", "", 403)
	var count, occurrences int
	if err := db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_policies),(SELECT count(*) FROM almanac_alert_occurrences)`).Scan(&count, &occurrences); err != nil || count != 2 || occurrences != 0 {
		t.Fatalf("management fabricated an occurrence or denied mutation committed: policies=%d occurrences=%d error=%v", count, occurrences, err)
	}
}
