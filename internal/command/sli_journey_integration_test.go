//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	"github.com/google/uuid"
)

// TestIntegrationExecutableBrowserSLICollectionJourney creates an SLI through
// the real authenticated UI, then waits for the separate agent to query and
// persist the first authorized canonical interval in the isolated database.
func TestIntegrationExecutableBrowserSLICollectionJourney(t *testing.T) {
	verifySLIBrowserDiagnostics(t)
	started := time.Now()
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		db := testpostgres.Connect(t, databaseURL)
		rows, queryErr := db.Query(t.Context(), `SELECT conname FROM pg_constraint WHERE conrelid IN ('public.almanac_agent_configurations'::regclass, 'public.almanac_agent_execution_grants'::regclass) ORDER BY conname`)
		if queryErr == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					t.Logf("migration prerequisite constraint: %s", safeDiagnosticToken(name))
				}
			}
		}
		t.Fatalf("upgrade isolated SLI journey database failed: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve isolated SLI journey port failed")
	}
	address := listener.Addr().String()
	listener.Close()
	dir := t.TempDir()
	databaseFile := filepath.Join(dir, "database-url")
	initialPasswordFile := filepath.Join(dir, "initial-password")
	permanentPasswordFile := filepath.Join(dir, "permanent-password")
	certFile, keyFile := filepath.Join(dir, "backend-cert.pem"), filepath.Join(dir, "backend-key.pem")
	initialPassword := "SLI journey initial phrase 473!"
	permanentPassword := "SLI journey permanent phrase 825!"
	for path, value := range map[string]string{databaseFile: databaseURL, initialPasswordFile: initialPassword, permanentPasswordFile: permanentPassword} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal("write protected SLI journey input failed")
		}
	}
	certificate, privateKey, err := browserTestCertificate()
	if err != nil {
		t.Fatal("create SLI journey backend certificate failed")
	}
	if err := os.WriteFile(certFile, certificate, 0o644); err != nil {
		t.Fatal("write SLI journey backend certificate failed")
	}
	if err := os.WriteFile(keyFile, privateKey, 0o600); err != nil {
		t.Fatal("write SLI journey backend key failed")
	}
	sourceCA, sourceKey, err := datasourceTestCertificate()
	if err != nil {
		t.Fatal("create SLI journey Prometheus certificate failed")
	}
	sourceCertFile, sourceKeyFile := filepath.Join(dir, "prometheus-cert.pem"), filepath.Join(dir, "prometheus-key.pem")
	if err := os.WriteFile(sourceCertFile, sourceCA, 0o644); err != nil {
		t.Fatal("write SLI journey Prometheus certificate failed")
	}
	if err := os.WriteFile(sourceKeyFile, sourceKey, 0o600); err != nil {
		t.Fatal("write SLI journey Prometheus key failed")
	}
	sourceEndpoint := startVictoriaMetrics(t, dir, sourceCertFile, sourceKeyFile, sourceCA)
	binary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac"))
	if err != nil {
		t.Fatal("resolve candidate backend executable failed")
	}
	keysetFile := filepath.Join(dir, "source-keyset.json")
	keysetID := "00000000-0000-4000-8000-000000000022"
	provision := exec.Command(binary, "secrets", "provision-source-keyset", "--file="+keysetFile, "--keyset-id="+keysetID, "--output=json")
	provision.Stdout, provision.Stderr = io.Discard, io.Discard
	if err := provision.Run(); err != nil {
		t.Fatal("provision SLI journey source-secret keyset failed")
	}
	block, rest := pem.Decode(certificate)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("decode SLI journey backend certificate failed")
	}
	parsedCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse SLI journey backend certificate failed")
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsedCertificate)
	apiClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(apiClient.CloseIdleConnections)
	baseURL := "https://localhost:" + strings.Split(address, ":")[1]
	args := []string{"serve", "--components=backend,ui", "--listen=" + address, "--database-url-file=" + databaseFile,
		"--initial-admin-login=sli-owner", "--initial-admin-password-file=" + initialPasswordFile, "--public-origin=" + baseURL,
		"--tls-cert-file=" + certFile, "--tls-key-file=" + keyFile, "--source-secret-keyset-file=" + keysetFile,
		"--source-secret-keyset-id=" + keysetID, "--shutdown-timeout=5s"}
	process := exec.Command(binary, args...)
	logs := newRuntimeLog()
	process.Stdout, process.Stderr = io.Discard, logs
	if err := process.Start(); err != nil {
		t.Fatal("start isolated SLI journey backend executable failed")
	}
	processDone := make(chan struct{})
	var processErr error
	go func() { processErr = process.Wait(); close(processDone) }()
	t.Cleanup(func() {
		if process.Process == nil {
			return
		}
		_ = process.Process.Signal(syscall.SIGTERM)
		select {
		case <-processDone:
			if processErr != nil {
				t.Errorf("SLI journey backend did not shut down cleanly (failed=%t)", true)
			}
		case <-time.After(8 * time.Second):
			_ = process.Process.Kill()
			<-processDone
			t.Error("SLI journey backend exceeded its shutdown bound")
		}
	})
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case <-processDone:
			t.Fatalf("SLI journey backend exited before readiness (failed=%t)", processErr != nil)
		default:
		}
		response, requestErr := apiClient.Get(baseURL + "/livez")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("SLI journey backend did not become live")
	}
	setup := exec.CommandContext(t.Context(), binary, "setup", "--backend-url="+baseURL, "--initial-admin-login=sli-owner",
		"--initial-admin-password-file="+initialPasswordFile, "--new-password-file="+permanentPasswordFile, "--ca-file="+certFile, "--output=json")
	setup.Stdout, setup.Stderr = io.Discard, io.Discard
	if err := setup.Run(); err != nil {
		t.Fatal("complete isolated SLI journey administrator setup failed")
	}
	journey := runConnectedSLIAgentJourney(t, databaseURL, binary, dir, baseURL, certFile, permanentPassword)
	t.Logf("SLI journey stage=agent_ready elapsed=%s", time.Since(started).Round(time.Millisecond))
	datasourceID := createSLIJourneyDatasource(t, binary, dir, journey, sourceEndpoint, sourceCA)
	t.Logf("SLI journey stage=datasource_created elapsed=%s", time.Since(started).Round(time.Millisecond))
	sliID := checkSLIBrowserJourney(t, baseURL, certificate, "sli-owner", permanentPassword, journey.id, datasourceID)
	t.Logf("SLI journey stage=browser_observed elapsed=%s", time.Since(started).Round(time.Millisecond))
	waitDatasourceAgentConfiguration(t, func(args ...string) []byte {
		command := exec.CommandContext(t.Context(), binary, args...)
		output, runErr := command.Output()
		if runErr != nil {
			t.Fatalf("read assigned agent configuration using executable CLI failed (failed=%t)", true)
		}
		return output
	}, journey, databaseURL, func() string { return "none" })
	db := testpostgres.Connect(t, databaseURL)
	var grants int
	if err := db.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1", journey.id).Scan(&grants); err != nil || grants < 2 {
		t.Fatalf("agent execution grant count after observation=%d", grants)
	}
	verifyPersistedSLIJourneyObservation(t, databaseURL, sliID, journey.id)
	verifySLIMalformedBatchIsAtomic(t, apiClient, baseURL, databaseURL, filepath.Join(dir, "sli-agent-token"), sliID)
	if strings.Contains(logs.String(), initialPassword) || strings.Contains(logs.String(), permanentPassword) {
		t.Fatal("SLI journey backend logs contained an administrator credential")
	}
}

func verifySLIMalformedBatchIsAtomic(t *testing.T, client *http.Client, baseURL, databaseURL, tokenPath, sliID string) {
	t.Helper()
	db := testpostgres.Connect(t, databaseURL)
	var lease string
	var generation, revision, measurement int64
	var created, start, end time.Time
	if err := db.QueryRow(t.Context(), `SELECT lease_id, session_generation, configuration_revision, measurement_revision, agent_created_at, interval_start, interval_end FROM public.almanac_sli_coverage WHERE sli_id=$1 AND outcome='observed' ORDER BY interval_start DESC LIMIT 1`, sliID).Scan(&lease, &generation, &revision, &measurement, &created, &start, &end); err != nil {
		t.Fatal("read SLI batch fixture failed")
	}
	delivery := uuid.NewString()
	item := map[string]any{"delivery_id": delivery, "execution_id": uuid.NewString(), "created_at": created, "lease_id": lease,
		"configuration_revision": fmt.Sprint(revision), "session_generation": fmt.Sprint(generation), "sli_id": sliID,
		"measurement_revision": fmt.Sprint(measurement), "interval_start": start, "interval_end": end,
		"outcome": map[string]any{"state": "observed", "good_count": "99", "total_count": "100"}}
	body, err := json.Marshal(map[string]any{"items": []any{item, "malformed"}})
	if err != nil {
		t.Fatal("encode SLI batch fixture failed")
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal("read SLI agent credential failed")
	}
	defer clear(token)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/agent/v1/observations", bytes.NewReader(body))
	if err != nil {
		t.Fatal("create SLI batch request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("send SLI batch request failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed SLI batch status = %d", response.StatusCode)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_delivery_receipts WHERE delivery_id=$1`, delivery).Scan(&count); err != nil || count != 0 {
		t.Fatalf("malformed SLI batch committed first receipt: count=%d err=%v", count, err)
	}
	invalidID := uuid.NewString()
	invalid := make(map[string]any, len(item))
	for key, value := range item {
		invalid[key] = value
	}
	invalid["delivery_id"], invalid["execution_id"] = invalidID, uuid.NewString()
	invalid["outcome"] = map[string]any{"state": "observed", "good_count": "101", "total_count": "100"}
	body, err = json.Marshal(map[string]any{"items": []any{item, invalid}})
	if err != nil {
		t.Fatal("encode mixed SLI batch failed")
	}
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/agent/v1/observations", bytes.NewReader(body))
	if err != nil {
		t.Fatal("create mixed SLI batch failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	response, err = client.Do(req)
	if err != nil {
		t.Fatal("send mixed SLI batch failed")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	var mixed api.ObservationBatchResponse
	if readErr != nil || response.StatusCode != http.StatusOK || json.Unmarshal(data, &mixed) != nil || len(mixed.Items) != 2 || mixed.Items[0].DeliveryId.String() != delivery || mixed.Items[0].Status != api.Duplicate || mixed.Items[1].DeliveryId.String() != invalidID || mixed.Items[1].Status != api.Rejected {
		t.Fatal("mixed SLI batch did not return explicit durable outcomes")
	}
	var committedInvalid int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE delivery_id=$1), count(*) FILTER (WHERE delivery_id=$2) FROM public.almanac_sli_delivery_receipts`, delivery, invalidID).Scan(&count, &committedInvalid); err != nil || count != 1 || committedInvalid != 0 {
		t.Fatalf("mixed SLI batch receipt counts: valid=%d invalid=%d err=%v", count, committedInvalid, err)
	}
}

func runConnectedSLIAgentJourney(t *testing.T, databaseURL, binary, dir, baseURL, certFile, password string) connectedAgentJourney {
	t.Helper()
	profileDir := filepath.Join(dir, "sli-agent-profile")
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		t.Fatal("create SLI agent profile directory failed")
	}
	profile := filepath.Join(profileDir, "profile.json")
	passwordFile := filepath.Join(dir, "sli-agent-password")
	if err := os.WriteFile(passwordFile, []byte(password), 0o600); err != nil {
		t.Fatal("write protected SLI agent password failed")
	}
	login := exec.CommandContext(t.Context(), binary, "auth", "login", "--profile-file="+profile, "--backend-url="+baseURL, "--login=sli-owner", "--password-file="+passwordFile, "--ca-file="+certFile)
	login.Stdout, login.Stderr = io.Discard, io.Discard
	if err := login.Run(); err != nil {
		t.Fatal("authenticate SLI agent registration failed")
	}
	tokenPath := filepath.Join(dir, "sli-agent-token")
	create := exec.CommandContext(t.Context(), binary, "agents", "create", "--profile-file="+profile, "--name=sli-journey-agent", "--display-name=SLI Journey Agent", "--idempotency-key=sli-journey-agent-create", "--token-file="+tokenPath, "--output=json")
	create.Stderr = io.Discard
	output, err := create.Output()
	var created agentCLICreateDocument
	if err != nil || json.Unmarshal(output, &created) != nil || !validCLIUUID(created.Result.Agent.ID) {
		t.Fatal("register SLI journey agent failed")
	}
	agentBinary, err := filepath.Abs(filepath.Join("..", "..", "bin", "almanac-agent"))
	if err != nil {
		t.Fatal("resolve candidate agent executable failed")
	}
	agent := exec.Command(agentBinary, "run", "--backend-url="+baseURL, "--token-file="+tokenPath, "--ca-file="+certFile)
	agent.Stdout, agent.Stderr = io.Discard, io.Discard
	if err := agent.Start(); err != nil {
		t.Fatal("start separate SLI agent executable failed")
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- agent.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = agent.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			_ = agent.Process.Kill()
			<-done
			t.Error("SLI agent exceeded its shutdown bound")
		}
	})
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); {
		command := exec.CommandContext(t.Context(), binary, "agents", "get", "--profile-file="+profile, "--agent-id="+created.Result.Agent.ID, "--output=json")
		body, readErr := command.Output()
		var detail struct {
			Result struct {
				Agent api.Agent `json:"agent"`
			} `json:"result"`
		}
		if readErr == nil && json.Unmarshal(body, &detail) == nil && detail.Result.Agent.Runtime != nil &&
			detail.Result.Agent.Runtime.Reachability == api.AgentRuntimeStatusReachabilityConnected &&
			detail.Result.Agent.Runtime.AppliedConfigurationRevision == "1" && detail.Result.Agent.Runtime.DesiredConfigurationRevision == "1" {
			return connectedAgentJourney{id: created.Result.Agent.ID, credentialID: created.Result.Credential.ID, profile: profile, passwordPath: passwordFile, done: done, result: result}
		}
		select {
		case err := <-result:
			t.Fatalf("SLI agent executable exited before initial configuration (failed=%t)", err != nil)
		default:
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("SLI agent executable did not connect and renew its lease")
	return connectedAgentJourney{}
}

func createSLIJourneyDatasource(t *testing.T, binary, dir string, journey connectedAgentJourney, endpoint string, caPEM []byte) string {
	t.Helper()
	input := map[string]any{"name": "sli-journey-metrics", "display_name": "SLI Journey Metrics", "provider": "prometheus",
		"configuration_format": "prometheus-http-v1", "endpoint": endpoint, "tls": map[string]any{"insecure_skip_verify": false, "ca_pem": string(caPEM)},
		"authentication":           map[string]any{"mode": "none", "username": nil, "password_secret": map[string]any{"action": "clear"}, "token_secret": map[string]any{"action": "clear"}},
		"polling_interval_seconds": 15, "agent_id": journey.id}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal("encode SLI journey datasource failed")
	}
	path := filepath.Join(dir, "sli-journey-datasource.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal("write protected SLI journey datasource failed")
	}
	command := exec.CommandContext(t.Context(), binary, "datasources", "create", "--profile-file="+journey.profile, "--input-file="+path, "--idempotency-key=sli-journey-datasource-create", "--output=json")
	output, err := command.Output()
	var document struct {
		Result api.Datasource `json:"result"`
	}
	if err != nil || json.Unmarshal(output, &document) != nil || !validCLIUUID(datasourceUUID(document.Result.Id)) || document.Result.PollingIntervalSeconds != 15 {
		t.Fatal("create bounded 15-second SLI journey datasource failed")
	}
	return datasourceUUID(document.Result.Id)
}

func checkSLIBrowserJourney(t *testing.T, baseURL string, certificatePEM []byte, login, password, agentID, datasourceID string) string {
	t.Helper()
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Hostname() != "localhost" ||
		parsedURL.Path != "" && parsedURL.Path != "/" || login == "" || password == "" ||
		!validCLIUUID(agentID) || !validCLIUUID(datasourceID) || len(certificatePEM) > 16*1024 {
		t.Fatal("SLI browser journey fixture is invalid")
	}
	block, rest := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("decode SLI browser journey certificate failed")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(certificate.RawSubjectPublicKeyInfo) == 0 {
		t.Fatal("parse SLI browser journey certificate failed")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("resolve SLI browser journey repository failed")
	}
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "browser-sli-")
	if err != nil {
		t.Fatal("create SLI browser journey artifact directory failed")
	}
	spki := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	fixture := struct {
		URL          string `json:"url"`
		SPKI         string `json:"spki"`
		Login        string `json:"login"`
		Password     string `json:"password"`
		AgentID      string `json:"agent_id"`
		DatasourceID string `json:"datasource_id"`
		Artifacts    string `json:"artifacts"`
	}{
		URL: baseURL, SPKI: base64.StdEncoding.EncodeToString(spki[:]), Login: login, Password: password,
		AgentID: agentID, DatasourceID: datasourceID, Artifacts: artifacts,
	}
	input, err := json.Marshal(fixture)
	if err != nil || len(input) > 16*1024 {
		t.Fatal("encode SLI browser journey fixture failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 115*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "scripts/check-sli-browser.mjs")
	browser.Dir = filepath.Join(root, "web")
	browser.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	browser.Stdout, browser.Stderr = &output, &output
	err = browser.Run()
	if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) {
		t.Fatal("SLI browser journey output contained an administrator credential")
	}
	if err != nil {
		exitCode := -1
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exit.ExitCode()
		}
		t.Fatalf("SLI browser acceptance failed (exit=%d %s); inspect sanitized artifacts in %s", exitCode, safeSLIBrowserFailureMarker(output.String()), artifacts)
	}
	if !hasSLIBrowserPassMarker(output.String()) {
		t.Fatalf("SLI browser acceptance did not produce its exact completion marker; inspect sanitized artifacts in %s", artifacts)
	}
	manifest, err := os.ReadFile(filepath.Join(artifacts, "sli-journey.json"))
	if err != nil {
		t.Fatalf("read SLI browser journey evidence failed; inspect artifacts in %s", artifacts)
	}
	var evidence struct {
		Scenario     string `json:"scenario"`
		SLIID        string `json:"sli_id"`
		Outcome      string `json:"outcome"`
		GoodCount    string `json:"good_count"`
		TotalCount   string `json:"total_count"`
		Activation   string `json:"activation_state"`
		DatasourceID string `json:"datasource_id"`
		AgentID      string `json:"agent_id"`
		CreateStatus int    `json:"create_status"`
	}
	if err := json.Unmarshal(manifest, &evidence); err != nil || evidence.Scenario != "browser-create-agent-collect-readback" ||
		!validCLIUUID(evidence.SLIID) || evidence.Outcome != "observed" || evidence.GoodCount != "99" || evidence.TotalCount != "100" ||
		evidence.Activation != "active" || evidence.DatasourceID != datasourceID ||
		evidence.AgentID != agentID || evidence.CreateStatus != http.StatusCreated {
		t.Fatalf("SLI browser journey evidence did not match the accepted path; inspect artifacts in %s", artifacts)
	}
	return evidence.SLIID
}

func safeSLIBrowserFailureMarker(output string) string {
	var fields []string
	for line := range strings.SplitSeq(output, "\n") {
		candidate := strings.Fields(line)
		if len(candidate) == 7 && candidate[0] == "FAIL" && candidate[1] == "browser-sli-journey" {
			fields = candidate
			break
		}
	}
	if len(fields) != 7 || fields[0] != "FAIL" || fields[1] != "browser-sli-journey" {
		return "stage=none api_status=none request_id=none api_code=none class=other"
	}
	values := make(map[string]string, 5)
	for _, field := range fields[2:] {
		key, value, ok := strings.Cut(field, "=")
		if !ok || value == "" {
			return "stage=none api_status=none request_id=none api_code=none class=other"
		}
		values[key] = value
	}
	stage := safeDiagnosticToken(values["stage"])
	status := values["api_status"]
	if status != "none" {
		parsed := 0
		for _, digit := range status {
			if digit < '0' || digit > '9' {
				parsed = -1
				break
			}
			parsed = parsed*10 + int(digit-'0')
		}
		if parsed < 100 || parsed > 599 {
			status = "none"
		}
	}
	requestID := values["request_id"]
	if requestID != "none" && !safeBrowserRequestID(requestID) {
		requestID = "none"
	}
	code := values["api_code"]
	safeCodes := map[string]bool{"agent_unavailable": true, "conflict": true, "forbidden": true, "invalid_request": true,
		"measurement_conflict": true, "not_found": true, "observation_unavailable": true, "sli_unavailable": true,
		"source_secret_unavailable": true, "unauthenticated": true, "unauthorized": true, "unsupported_measurement": true}
	if code != "none" && !safeCodes[code] {
		code = "none"
	}
	class := values["class"]
	if class != "timeout" && class != "module" && class != "launch" && class != "other" {
		class = "other"
	}
	return "stage=" + stage + " api_status=" + status + " request_id=" + requestID + " api_code=" + code + " class=" + class
}

func hasSLIBrowserPassMarker(output string) bool {
	for line := range strings.SplitSeq(output, "\n") {
		if strings.TrimSpace(line) == "PASS browser-sli-journey" {
			return true
		}
	}
	return false
}

func verifySLIBrowserDiagnostics(t *testing.T) {
	t.Helper()
	output := "warning contains sensitive fixture data\nFAIL browser-sli-journey stage=browser_launch api_status=none request_id=none api_code=none class=launch\n"
	got := safeSLIBrowserFailureMarker(output)
	if got != "stage=browser_launch api_status=none request_id=none api_code=none class=launch" {
		t.Fatalf("safe failure marker = %q", got)
	}
	if !hasSLIBrowserPassMarker("warning\nPASS browser-sli-journey\n") || hasSLIBrowserPassMarker("PASS browser-sli-journey extra") {
		t.Fatal("pass marker was not matched by complete line")
	}
}

func verifyPersistedSLIJourneyObservation(t *testing.T, databaseURL, sliID, agentID string) {
	t.Helper()
	db := testpostgres.Connect(t, databaseURL)
	var good, total, outcome, persistedAgent string
	var start, end time.Time
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		err := db.QueryRow(t.Context(), `SELECT v.good_count::text, v.total_count::text, c.outcome, c.agent_id::text, c.interval_start, c.interval_end
			FROM public.almanac_sli_coverage c JOIN public.almanac_sli_observation_values v
			USING (interval_start, sli_id, measurement_revision) WHERE c.sli_id=$1 ORDER BY c.interval_start DESC LIMIT 1`, sliID).Scan(&good, &total, &outcome, &persistedAgent, &start, &end)
		if err == nil {
			if good != "99.000000000000" || total != "100.000000000000" || outcome != "observed" || persistedAgent != agentID || end.Sub(start) != 15*time.Second {
				t.Fatalf("persisted SLI journey observation differs: good=%s total=%s outcome=%s source_agent_matches=%t cadence=%s", good, total, safeDiagnosticToken(outcome), persistedAgent == agentID, end.Sub(start))
			}
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("browser-created SLI did not produce a persisted 99/100 canonical interval")
}
