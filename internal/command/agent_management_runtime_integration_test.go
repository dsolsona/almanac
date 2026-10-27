//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

func TestIntegrationExecutableAgentManagementRouteAndSessionLifecycle(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade isolated database failed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve runtime port failed")
	}
	address := listener.Addr().String()
	_ = listener.Close()
	dir := t.TempDir()
	databaseFile, passwordFile := filepath.Join(dir, "database-url"), filepath.Join(dir, "initial-password")
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	initialPassword, permanentPassword := "Runtime agent initial password 346!", "Runtime agent permanent password 571!"
	for path, value := range map[string][]byte{databaseFile: []byte(databaseURL), passwordFile: []byte(initialPassword)} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal("write protected runtime file failed")
		}
	}
	certificate, key, err := browserTestCertificate()
	if err != nil {
		t.Fatal("create runtime TLS certificate failed")
	}
	if err := os.WriteFile(certFile, certificate, 0o644); err != nil {
		t.Fatal("write runtime certificate failed")
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal("write runtime key failed")
	}
	sourceCA, sourceKey, err := datasourceTestCertificate()
	if err != nil {
		t.Fatal("create VictoriaMetrics TLS certificate failed")
	}
	sourceCertFile, sourceKeyFile := filepath.Join(dir, "source-cert.pem"), filepath.Join(dir, "source-key.pem")
	if err := os.WriteFile(sourceCertFile, sourceCA, 0o644); err != nil {
		t.Fatal("write VictoriaMetrics certificate failed")
	}
	if err := os.WriteFile(sourceKeyFile, sourceKey, 0o600); err != nil {
		t.Fatal("write VictoriaMetrics key failed")
	}
	sourceEndpoint := startVictoriaMetrics(t, dir, sourceCertFile, sourceKeyFile, sourceCA)
	sourceKeysetFile := filepath.Join(dir, "source-secret-keyset.json")
	sourceKeysetID := "00000000-0000-4000-8000-000000000020"
	block, _ := pem.Decode(certificate)
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse runtime certificate failed")
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("create cookie jar failed")
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	baseURL := "https://localhost:" + strings.Split(address, ":")[1]
	binary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac"))
	if err != nil {
		t.Fatal("resolve executable failed")
	}
	provision := exec.Command(binary, "secrets", "provision-source-keyset", "--file="+sourceKeysetFile, "--keyset-id="+sourceKeysetID, "--output=json")
	provision.Stdout, provision.Stderr = io.Discard, io.Discard
	if err := provision.Run(); err != nil {
		t.Fatal("provision source-secret keyset failed")
	}
	args := []string{"serve", "--components=backend,ui", "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=agent-owner", "--initial-admin-password-file=" + passwordFile, "--public-origin=" + baseURL, "--tls-cert-file=" + certFile, "--tls-key-file=" + keyFile, "--source-secret-keyset-file=" + sourceKeysetFile, "--source-secret-keyset-id=" + sourceKeysetID, "--shutdown-timeout=5s"}
	process := exec.Command(binary, args...)
	stderr := newRuntimeLog()
	process.Stderr = stderr
	process.Stdout = io.Discard
	if err := process.Start(); err != nil {
		t.Fatal("start backend executable failed")
	}
	wait := make(chan error, 1)
	done := make(chan struct{})
	go func(proc *exec.Cmd, result chan<- error, finished chan<- struct{}) {
		result <- proc.Wait()
		close(finished)
	}(process, wait, done)
	t.Cleanup(func() {
		_ = process.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("backend did not exit")
		}
	})
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-wait:
			t.Fatalf("backend exited before readiness (failed=%t)", err != nil)
		default:
		}
		response, err := client.Get(baseURL + "/livez")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		_ = process.Process.Kill()
		<-wait
		t.Fatal("backend did not become live")
	}
	callAuth := func(path, body, csrf string) (int, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal("create auth request failed")
		}
		request.Header.Set("Origin", baseURL)
		request.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("send auth request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal("read auth response failed")
		}
		return response.StatusCode, data
	}
	status, body := callAuth("/api/v1/auth/context", `{}`, "")
	var authContext api.AuthResponse
	if status != 200 || json.Unmarshal(body, &authContext) != nil || authContext.Session != nil || authContext.CsrfToken == "" {
		t.Fatal("initial auth context unavailable")
	}
	status, body = callAuth("/api/v1/auth/login", `{"login":"agent-owner","password":"`+initialPassword+`"}`, authContext.CsrfToken)
	var session api.AuthResponse
	if status != 200 || json.Unmarshal(body, &session) != nil || session.Session == nil || !session.Session.Restricted {
		t.Fatal("initial owner session was not restricted")
	}
	managementGET := func() (int, []byte, http.Header) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/agents", nil)
		if err != nil {
			t.Fatal("create management request failed")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("send management request failed")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal("read management response failed")
		}
		return response.StatusCode, body, response.Header
	}
	status, body, headers := managementGET()
	if status != 403 || !strings.Contains(string(body), `"forbidden"`) || headers.Get("X-Request-ID") == "" {
		t.Fatalf("restricted management GET status=%d body=%s headers=%v", status, body, headers)
	}
	runRestrictedAgentCLI(t, binary, dir, baseURL, passwordFile, certFile)
	status, body = callAuth("/api/v1/auth/context", `{}`, "")
	if status != 200 || json.Unmarshal(body, &authContext) != nil {
		t.Fatal("restricted session context failed")
	}
	status, body = callAuth("/api/v1/auth/password", `{"current_password":"`+initialPassword+`","new_password":"`+permanentPassword+`"}`, authContext.CsrfToken)
	if status != 200 || json.Unmarshal(body, &session) != nil || session.Session == nil || session.Session.Restricted {
		t.Fatal("owner password rotation did not complete")
	}
	status, body, headers = managementGET()
	if status != 200 {
		t.Fatalf("normal administrator GET status=%d body=%s", status, body)
	}
	var page api.AgentPage
	if err := json.Unmarshal(body, &page); err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("initial agent page=%s err=%v", body, err)
	}
	if headers.Get("X-Request-ID") == "" || headers.Get("Cache-Control") != "no-store" || headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("management response headers=%v", headers)
	}
	journey := runConnectedAgentJourney(t, databaseURL, binary, dir, baseURL, certFile, permanentPassword, client)
	assertDatasourceDiagnosticJourney(t, databaseURL, binary, dir, baseURL, certFile, sourceEndpoint, sourceCA, permanentPassword, journey, stderr)
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal backend for reconnect journey failed")
	}
	if err := <-wait; err != nil {
		t.Fatal("first backend shutdown for reconnect journey failed")
	}
	stderr = newRuntimeLog()
	process = exec.Command(binary, args...)
	process.Stderr, process.Stdout = stderr, io.Discard
	if err := process.Start(); err != nil {
		t.Fatal("restart backend for reconnect journey failed")
	}
	wait = make(chan error, 1)
	done = make(chan struct{})
	go func(proc *exec.Cmd, result chan<- error, finished chan<- struct{}) {
		result <- proc.Wait()
		close(finished)
	}(process, wait, done)
	restarted := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get(baseURL + "/livez")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				restarted = true
				break
			}
		}
		select {
		case err := <-wait:
			t.Fatalf("restarted backend exited before readiness (failed=%t)", err != nil)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !restarted {
		t.Fatal("restarted backend did not become live")
	}
	waitForAgentReconnect(t, databaseURL, client, baseURL, journey)
	revokeConnectedAgentCredential(t, binary, journey, client, baseURL)
	if cliAccepted := runAgentManagementCLIAcceptance(t, databaseURL, binary, dir, baseURL, certFile, permanentPassword); cliAccepted {
		status, body, _ = managementGET()
		if status != http.StatusOK || json.Unmarshal(body, &page) != nil || len(page.Items) != 2 || !containsAgentName(page.Items, "cli-agent") || !containsAgentName(page.Items, "connected-agent") {
			t.Fatal("API did not observe agent created by executable CLI")
		}
	}
	malformedPath, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/agents/%2e%2e/readyz", nil)
	if err != nil {
		t.Fatal("create malformed management path request failed")
	}
	malformedResponse, err := client.Do(malformedPath)
	if err != nil {
		t.Fatal("send malformed management path request failed")
	}
	malformedResponse.Body.Close()
	if malformedResponse.StatusCode != http.StatusNotFound || malformedResponse.Header.Get("Cache-Control") != "no-store" || malformedResponse.Header.Get("X-Content-Type-Options") != "nosniff" || malformedResponse.Header.Get("X-Request-ID") == "" {
		t.Fatalf("malformed management path boundary status=%d headers=%v", malformedResponse.StatusCode, malformedResponse.Header)
	}
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal backend shutdown failed")
	}
	select {
	case err := <-wait:
		if err != nil {
			logs := stderr.String()
			t.Fatalf("backend shutdown failed: exit=%d shutdown-failed=%t deadline-exceeded=%t ownership-lost=%t cleanup-failed=%t", process.ProcessState.ExitCode(), strings.Contains(logs, "backend shutdown failed"), strings.Contains(logs, `"deadline_exceeded":true`), strings.Contains(logs, "backend ownership lost"), strings.Contains(logs, "backend ownership cleanup failed"))
		}
	case <-time.After(10 * time.Second):
		_ = process.Process.Kill()
		<-wait
		t.Fatal("backend shutdown timed out")
	}
}

func containsAgentName(items []api.Agent, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

type connectedAgentJourney struct {
	id           string
	credentialID string
	token        string
	profile      string
	passwordPath string
	done         <-chan struct{}
	result       <-chan error
	artifacts    string
}

func runConnectedAgentJourney(t *testing.T, databaseURL, backendBinary, dir, baseURL, certFile, password string, client *http.Client) connectedAgentJourney {
	t.Helper()
	profileDir := filepath.Join(dir, "connected-agent-profile")
	if err := os.Mkdir(profileDir, 0700); err != nil {
		t.Fatal("create connected-agent profile directory failed")
	}
	passwordPath := filepath.Join(dir, "connected-agent-password")
	if err := os.WriteFile(passwordPath, []byte(password), 0600); err != nil {
		t.Fatal("write protected connected-agent password failed")
	}
	profile := filepath.Join(profileDir, "profile.json")
	login := exec.CommandContext(t.Context(), backendBinary, "auth", "login", "--profile-file="+profile, "--backend-url="+baseURL, "--login=agent-owner", "--password-file="+passwordPath, "--ca-file="+certFile)
	login.Stdout, login.Stderr = io.Discard, io.Discard
	if err := login.Run(); err != nil {
		t.Fatal("authenticate connected-agent registration failed")
	}
	tokenPath := filepath.Join(dir, "connected-agent-token")
	create := exec.CommandContext(t.Context(), backendBinary, "agents", "create", "--profile-file="+profile, "--name=connected-agent", "--display-name=Connected Agent", "--idempotency-key=connected-agent-create", "--token-file="+tokenPath, "--output=json")
	create.Stderr = io.Discard
	createOutput, err := create.Output()
	if err != nil {
		t.Fatal("register connected-agent credential failed")
	}
	var created agentCLICreateDocument
	if err := json.Unmarshal(createOutput, &created); err != nil || created.Result.Agent.ID == "" {
		t.Fatal("connected-agent registration response was invalid")
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal("read protected connected-agent token failed")
	}
	token := strings.TrimSpace(string(tokenBytes))
	assertAgentWebSocketBoundaries(t, baseURL, token, client)
	assertIncompatibleAgentAttempt(t, baseURL, token, databaseURL, created.Result.Agent.ID, client, "0", 86, "incompatible_protocol", api.AgentRuntimeStatusReachabilityIncompatible)
	agentBinary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac-agent"))
	if err != nil {
		t.Fatal("resolve separate agent executable failed")
	}
	agent := exec.Command(agentBinary, "run", "--backend-url="+baseURL, "--token-file="+tokenPath, "--ca-file="+certFile)
	agent.Stdout, agent.Stderr = io.Discard, io.Discard
	if err := agent.Start(); err != nil {
		t.Fatal("start separate agent executable failed")
	}
	agentDone := make(chan struct{})
	agentResult := make(chan error, 1)
	go func() { agentResult <- agent.Wait(); close(agentDone) }()
	t.Cleanup(func() {
		if agent.Process != nil {
			_ = agent.Process.Signal(syscall.SIGTERM)
			select {
			case <-agentDone:
			case <-time.After(7 * time.Second):
				_ = agent.Process.Kill()
				<-agentDone
				t.Error("separate agent did not shut down within bound")
			}
		}
	})
	db := testpostgres.Connect(t, databaseURL)
	for deadline := time.Now().Add(35 * time.Second); time.Now().Before(deadline); {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/agents/"+created.Result.Agent.ID, nil)
		if err != nil {
			t.Fatal("create connected-agent status request failed")
		}
		response, err := client.Do(request)
		if err == nil {
			var detail api.Agent
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && json.Unmarshal(body, &detail) == nil && detail.Runtime != nil && detail.Runtime.Reachability == api.AgentRuntimeStatusReachabilityConnected && detail.Runtime.AppliedConfigurationRevision == "1" && detail.Runtime.DesiredConfigurationRevision == "1" && detail.Runtime.LastHeartbeatAt != nil {
				var grants int
				if err := db.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1", created.Result.Agent.ID).Scan(&grants); err != nil {
					t.Fatal("read connected-agent heartbeat grants failed")
				}
				if grants >= 3 && detail.Runtime.SchedulerHealth == api.Ready && detail.Runtime.CollectionProgress == api.AgentRuntimeStatusCollectionProgressIdle {
					certificate, err := os.ReadFile(certFile)
					if err != nil {
						t.Fatal("read public backend certificate for browser check failed")
					}
					artifacts := assertConnectedAgentBrowser(t, baseURL, certificate, "agent-owner", password, created.Result.Agent.ID)
					assertCopiedAgentCannotReplaceOwner(t, agentBinary, baseURL, token, certFile, databaseURL, created.Result.Agent.ID, client)
					return connectedAgentJourney{id: created.Result.Agent.ID, credentialID: created.Result.Credential.ID, token: token, profile: profile, passwordPath: passwordPath, done: agentDone, result: agentResult, artifacts: artifacts}
				}
			}
		}
		select {
		case <-agentDone:
			err := <-agentResult
			t.Fatalf("separate agent exited before two heartbeat renewals (failed=%t)", err != nil)
		default:
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("separate agent did not apply configuration and persist two heartbeat renewals")
	return connectedAgentJourney{}
}

func waitForAgentReconnect(t *testing.T, databaseURL string, client *http.Client, baseURL string, journey connectedAgentJourney) {
	t.Helper()
	db := testpostgres.Connect(t, databaseURL)
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); {
		var grants int
		if err := db.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1", journey.id).Scan(&grants); err != nil {
			t.Fatal("read reconnected agent grants failed")
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/agents/"+journey.id, nil)
		if err != nil {
			t.Fatal("create reconnected agent status request failed")
		}
		response, err := client.Do(request)
		if err == nil {
			var detail api.Agent
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && json.Unmarshal(body, &detail) == nil && grants >= 4 && detail.Runtime != nil && detail.Runtime.Reachability == api.AgentRuntimeStatusReachabilityConnected && detail.Runtime.DesiredConfigurationRevision != "" && detail.Runtime.AppliedConfigurationRevision == detail.Runtime.DesiredConfigurationRevision {
				select {
				case <-journey.done:
					err := <-journey.result
					t.Fatalf("agent process exited during backend reconnect (failed=%t)", err != nil)
				default:
				}
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("same agent process did not resume ownership and renew after backend restart")
}

func assertDatasourceDiagnosticJourney(t *testing.T, databaseURL, binary, dir, baseURL, certFile, endpoint string, caPEM []byte, password string, journey connectedAgentJourney, backendLogs *runtimeLog) {
	t.Helper()
	inputPath := filepath.Join(dir, "datasource-input.json")
	writeInput := func(value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil || os.WriteFile(inputPath, data, 0o600) != nil {
			t.Fatal("write protected datasource CLI input failed")
		}
	}
	agentEvidence := "agent_generation=unknown reachability=unknown desired=unknown applied=unknown config_failure=none registry_present=unknown"
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, args...)
		output, err := command.Output()
		if err != nil {
			var exitError *exec.ExitError
			if bytes.Contains(output, []byte(password)) || bytes.Contains(output, caPEM) || errors.As(err, &exitError) && bytes.Contains(exitError.Stderr, []byte(password)) {
				t.Fatal("datasource CLI acceptance output leaked fixture material")
			}
			evidence := datasourceFailureEvidence(strings.Join(args[:2], " "), output, err)
			evidence += " " + agentEvidence + " " + datasourceRegistryEvidence(backendLogs, output)
			t.Fatalf("datasource CLI acceptance failed: %s", evidence)
		}
		return output
	}
	common := map[string]any{
		"endpoint": endpoint,
		"tls":      map[string]any{"insecure_skip_verify": false, "ca_pem": string(caPEM)},
		"authentication": map[string]any{"mode": "none", "username": nil,
			"password_secret": map[string]any{"action": "clear"}, "token_secret": map[string]any{"action": "clear"}},
	}
	write := map[string]any{
		"name": "integration-prometheus", "display_name": "Integration Prometheus", "provider": "prometheus",
		"configuration_format": "prometheus-http-v1", "endpoint": endpoint,
		"tls":            map[string]any{"insecure_skip_verify": false, "ca_pem": string(caPEM)},
		"authentication": common["authentication"], "polling_interval_seconds": 60, "agent_id": journey.id,
	}
	writeInput(write)
	createdOutput := run("datasources", "create", "--profile-file="+journey.profile, "--input-file="+inputPath,
		"--idempotency-key=integration-prometheus-create", "--output=json")
	var createdEnvelope struct {
		Result struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"result"`
	}
	if json.Unmarshal(createdOutput, &createdEnvelope) != nil || !validCLIUUID(createdEnvelope.Result.ID) || createdEnvelope.Result.Version != "1" {
		t.Fatal("datasource CLI create response was invalid")
	}
	datasourceID := createdEnvelope.Result.ID
	agentEvidence = waitDatasourceAgentConfiguration(t, run, journey, databaseURL)
	listOutput := run("datasources", "list", "--profile-file="+journey.profile, "--output=json")
	if !bytes.Contains(listOutput, []byte(datasourceID)) {
		t.Fatal("datasource CLI list omitted the source")
	}
	getOutput := run("datasources", "get", "--profile-file="+journey.profile, "--datasource-id="+datasourceID, "--output=json")
	if !bytes.Contains(getOutput, []byte(datasourceID)) || bytes.Contains(getOutput, []byte("password_secret")) || bytes.Contains(getOutput, []byte("token_secret")) {
		t.Fatal("datasource CLI get was not redacted")
	}
	writeInput(map[string]any{
		"provider": "prometheus", "configuration_format": "prometheus-http-v1", "endpoint": endpoint,
		"tls": common["tls"], "authentication": common["authentication"], "agent_id": journey.id,
	})
	connectionOutput := run("datasources", "test", "--profile-file="+journey.profile, "--input-file="+inputPath, "--wait", "--output=json")
	assertDiagnosticJSON(t, databaseURL, "datasources.test", connectionOutput, "succeeded", "")
	queryOutput := run("slis", "test", "--profile-file="+journey.profile, "--datasource-id="+datasourceID, "--query=vector(1)", "--wait", "--output=json")
	assertDiagnosticJSON(t, databaseURL, "slis.test", queryOutput, "succeeded", `"value":"1"`)
	db := testpostgres.Connect(t, databaseURL)
	var observationTable *string
	if err := db.QueryRow(t.Context(), `SELECT to_regclass('public.almanac_observations')::text`).Scan(&observationTable); err != nil {
		t.Fatal("check diagnostic observation isolation failed")
	}
	if observationTable != nil {
		t.Fatal("diagnostic journey requires separate future observation ingestion storage")
	}
	certificate, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal("read backend certificate for datasource browser failed")
	}
	checkDatasourceBrowser(t, baseURL, certificate, "agent-owner", password, journey.id, datasourceID, "vector(1)", string(caPEM))
}

func datasourceFailureEvidence(stage string, output []byte, err error) string {
	var response struct {
		Error struct {
			Code        string `json:"code"`
			BackendCode string `json:"backend_code"`
			RequestID   string `json:"request_id"`
			Status      int    `json:"http_status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(output, &response)
	code := safeDiagnosticToken(response.Error.Code)
	backendCode := safeDiagnosticToken(response.Error.BackendCode)
	requestID := safeDiagnosticToken(response.Error.RequestID)
	status := response.Error.Status
	if status < 0 || status > 599 {
		status = 0
	}
	processClass := "other"
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		processClass = "exit"
	} else if errors.Is(err, context.DeadlineExceeded) {
		processClass = "timeout"
	} else if errors.Is(err, exec.ErrNotFound) {
		processClass = "not_found"
	}
	return fmt.Sprintf("stage=%s code=%s backend_code=%s status=%d request_id=%s process=%s", stage, code, backendCode, status, requestID, processClass)
}

func safeDiagnosticToken(value string) string {
	if value == "" || len(value) > 64 {
		return "none"
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "none"
		}
	}
	return value
}

func assertDiagnosticJSON(t *testing.T, databaseURL, stage string, output []byte, state, resultFragment string) {
	t.Helper()
	var envelope struct {
		Result jsontext.Value `json:"result"`
	}
	if json.Unmarshal(output, &envelope) != nil {
		t.Fatal("datasource CLI diagnostic response was invalid JSON")
	}
	var diagnostic struct {
		ID          string  `json:"id"`
		State       string  `json:"state"`
		ErrorCode   *string `json:"error_code"`
		ResultClass *string `json:"result_class"`
	}
	if json.Unmarshal(envelope.Result, &diagnostic) != nil || diagnostic.State != state || resultFragment != "" && !bytes.Contains(envelope.Result, []byte(resultFragment)) {
		code, class := "none", "none"
		if diagnostic.ErrorCode != nil {
			code = safeDiagnosticToken(*diagnostic.ErrorCode)
		}
		if diagnostic.ResultClass != nil {
			class = safeDiagnosticToken(*diagnostic.ResultClass)
		}
		agentCode := "none"
		if validCLIUUID(diagnostic.ID) {
			db := testpostgres.Connect(t, databaseURL)
			var storedCode string
			if err := db.QueryRow(t.Context(), `SELECT COALESCE(error_code, '') FROM public.almanac_diagnostics WHERE id=$1`, diagnostic.ID).Scan(&storedCode); err == nil {
				agentCode = safeDiagnosticToken(storedCode)
			}
		}
		t.Fatalf("datasource CLI diagnostic failed: stage=%s diagnostic_id=%s state=%s error_code=%s agent_error_code=%s result_class=%s", stage,
			safeDiagnosticToken(diagnostic.ID), safeDiagnosticToken(diagnostic.State), code, agentCode, class)
	}
}

func waitDatasourceAgentConfiguration(t *testing.T, run func(...string) []byte, journey connectedAgentJourney, databaseURL string) string {
	t.Helper()
	db := testpostgres.Connect(t, databaseURL)
	var generation int64
	if err := db.QueryRow(t.Context(), "SELECT generation FROM public.almanac_agent_runtime_status WHERE agent_id=$1", journey.id).Scan(&generation); err != nil {
		t.Fatal("read datasource agent generation failed")
	}
	lastEvidence := "agent_generation=unknown reachability=unknown latest_revision=unknown desired=unknown applied=unknown assigned_sources=unknown config_failure=none agent_process=unknown"
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		var latestRevision int64
		var assignedSources int
		if err := db.QueryRow(t.Context(), `SELECT COALESCE((SELECT max(revision) FROM public.almanac_agent_configurations WHERE agent_id=$1),0),
			(SELECT count(*) FROM public.almanac_datasources WHERE agent_id=$1)`, journey.id).Scan(&latestRevision, &assignedSources); err != nil {
			t.Fatal("read datasource assignment revision failed")
		}
		processState := "running"
		select {
		case <-journey.done:
			err, ok := <-journey.result
			processState = "exited"
			if exitError, matched := errors.AsType[*exec.ExitError](err); ok && matched && exitError.ExitCode() != 0 {
				processState = "exit_failure"
			}
		default:
		}
		output := run("agents", "get", "--profile-file="+journey.profile, "--agent-id="+journey.id, "--output=json")
		var document struct {
			Result struct {
				Agent api.Agent `json:"agent"`
			} `json:"result"`
		}
		if json.Unmarshal(output, &document) == nil && document.Result.Agent.Runtime != nil {
			runtime := document.Result.Agent.Runtime
			failure := "none"
			if runtime.ConfigurationFailureCode != nil {
				failure = safeDiagnosticToken(string(*runtime.ConfigurationFailureCode))
			}
			lastEvidence = fmt.Sprintf("agent_generation=%d reachability=%s latest_revision=%d desired=%s applied=%s assigned_sources=%d config_failure=%s agent_process=%s", generation,
				safeDiagnosticToken(string(runtime.Reachability)), latestRevision, safeDiagnosticToken(runtime.DesiredConfigurationRevision),
				safeDiagnosticToken(runtime.AppliedConfigurationRevision), assignedSources, failure, processState)
			if processState != "running" {
				t.Fatalf("assigned agent process exited during datasource configuration: %s", lastEvidence)
			}
			if latestRevision > 1 && assignedSources > 0 && runtime.Reachability == api.AgentRuntimeStatusReachabilityConnected && runtime.DesiredConfigurationRevision == strconv.FormatInt(latestRevision, 10) && runtime.AppliedConfigurationRevision == strconv.FormatInt(latestRevision, 10) && runtime.ConfigurationFailureCode == nil {
				return lastEvidence
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("assigned agent did not apply the datasource configuration: %s", lastEvidence)
	return lastEvidence
}

func datasourceRegistryEvidence(logs *runtimeLog, output []byte) string {
	var document struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if logs == nil || json.Unmarshal(output, &document) != nil || !validDatasourceRequestID(document.Error.RequestID) {
		return "registry_present=unknown"
	}
	text := logs.String()
	needle := "request_id=" + document.Error.RequestID
	if strings.Contains(text, needle+" stage=session_lookup agent_session_present=false") {
		return "registry_present=false"
	}
	if strings.Contains(text, needle+" stage=storage_begin agent_session_present=true") || strings.Contains(text, needle+" stage=websocket_dispatch agent_session_present=true") {
		return "registry_present=true"
	}
	return "registry_present=unknown"
}

func validDatasourceRequestID(value string) bool {
	if len(value) != 32 && len(value) != 36 {
		return false
	}
	index := 0
	for _, r := range value {
		if len(value) == 36 && (index == 8 || index == 13 || index == 18 || index == 23) {
			if r != '-' {
				return false
			}
		} else if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
		index++
	}
	return true
}

func datasourceTestCertificate() ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(20261027), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
		DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), nil
}

func startVictoriaMetrics(t *testing.T, dir, certificatePath, keyPath string, certificatePEM []byte) string {
	t.Helper()
	digest := sha256.Sum256([]byte(dir))
	name := "almanac-f2-t20-" + hex.EncodeToString(digest[:6])
	image := "victoriametrics/victoria-metrics:v1.155.0@sha256:f60dc268b54ecf87f6e09d740c2b732838aa9193090cc741417c9f6386a130df"
	start := exec.CommandContext(t.Context(), "docker", "run", "--detach", "--rm", "--name", name,
		"--publish", "127.0.0.1::8428/tcp",
		"--mount", "type=bind,src="+certificatePath+",dst=/tls/cert.pem,readonly",
		"--mount", "type=bind,src="+keyPath+",dst=/tls/key.pem,readonly",
		image, "-tls", "-tlsCertFile=/tls/cert.pem", "-tlsKeyFile=/tls/key.pem")
	container, err := start.Output()
	if err != nil || strings.TrimSpace(string(container)) == "" {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && len(exitError.Stderr) != 0 {
			message := strings.TrimSpace(string(exitError.Stderr))
			if len(message) > 512 {
				message = message[:512]
			}
			t.Fatalf("start pinned isolated VictoriaMetrics fixture failed: %s", message)
		}
		t.Fatalf("start pinned isolated VictoriaMetrics fixture failed: %v", err)
	}
	containerID := strings.TrimSpace(string(container))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stop := exec.CommandContext(ctx, "docker", "stop", containerID)
		stop.Stdout, stop.Stderr = io.Discard, io.Discard
		if err := stop.Run(); err != nil {
			t.Error("stop isolated VictoriaMetrics fixture failed")
		}
	})
	portCommand := exec.CommandContext(t.Context(), "docker", "port", containerID, "8428/tcp")
	portOutput, err := portCommand.Output()
	if err != nil {
		t.Fatal("read isolated VictoriaMetrics port failed")
	}
	_, port, err := net.SplitHostPort(strings.TrimSpace(string(portOutput)))
	if err != nil {
		t.Fatal("parse isolated VictoriaMetrics port failed")
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal("isolated VictoriaMetrics port is invalid")
	}
	endpoint := "https://localhost:" + port
	block, _ := pem.Decode(certificatePEM)
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse VictoriaMetrics fixture CA failed")
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get(endpoint + "/-/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return endpoint
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("isolated VictoriaMetrics fixture did not become ready over verified HTTPS")
	return ""
}
