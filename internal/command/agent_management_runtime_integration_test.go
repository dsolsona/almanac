//go:build integration

package command

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
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
	args := []string{"serve", "--components=backend,ui", "--listen=" + address, "--database-url-file=" + databaseFile, "--initial-admin-login=agent-owner", "--initial-admin-password-file=" + passwordFile, "--public-origin=" + baseURL, "--tls-cert-file=" + certFile, "--tls-key-file=" + keyFile, "--shutdown-timeout=5s"}
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
			if readErr == nil && response.StatusCode == http.StatusOK && json.Unmarshal(body, &detail) == nil && grants >= 4 && detail.Runtime != nil && detail.Runtime.Reachability == api.AgentRuntimeStatusReachabilityConnected && detail.Runtime.AppliedConfigurationRevision == "1" {
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
