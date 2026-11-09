//go:build integration

package command

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
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
func TestIntegrationExecutableSLICollectionJourney(t *testing.T) {
	t.Parallel()
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
	sliID := createSLIJourneySLI(t, binary, dir, journey, databaseURL, datasourceID)
	t.Logf("SLI journey stage=observed elapsed=%s", time.Since(started).Round(time.Millisecond))
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
	if err := db.QueryRow(t.Context(), `SELECT observation.lease_id,observation.session_generation,observation.configuration_revision,
		observation.measurement_revision,observation.agent_created_at,observation.interval_start,
		observation.interval_start+source.polling_interval_seconds*interval '1 second' AS interval_end
		FROM public.almanac_sli_observations observation
		JOIN public.almanac_sli_measurements measurement ON measurement.sli_id=observation.sli_id AND measurement.revision=observation.measurement_revision
		JOIN public.almanac_datasources source ON source.id=measurement.datasource_id
		WHERE observation.sli_id=$1 AND observation.outcome='observed' ORDER BY observation.interval_start DESC LIMIT 1`, sliID).Scan(&lease, &generation, &revision, &measurement, &created, &start, &end); err != nil {
		t.Fatal("read SLI batch fixture failed")
	}
	delivery := uuid.NewString()
	item := map[string]any{"delivery_id": delivery, "execution_id": uuid.NewString(), "created_at": created.UTC(), "lease_id": lease,
		"configuration_revision": fmt.Sprint(revision), "session_generation": fmt.Sprint(generation), "sli_id": sliID,
		"measurement_revision": fmt.Sprint(measurement), "interval_start": start.UTC(), "interval_end": end.UTC(),
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
		outcomes := make([]string, 0, len(mixed.Items))
		for _, item := range mixed.Items {
			code := ""
			if item.ErrorCode != nil {
				code = *item.ErrorCode
			}
			outcomes = append(outcomes, string(item.Status)+"/"+code)
		}
		t.Fatalf("mixed SLI batch did not return explicit durable outcomes: HTTP=%d items=%v", response.StatusCode, outcomes)
	}
	var committedInvalid int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE delivery_id=$1), count(*) FILTER (WHERE delivery_id=$2) FROM public.almanac_sli_delivery_receipts`, delivery, invalidID).Scan(&count, &committedInvalid); err != nil || count != 1 || committedInvalid != 0 {
		t.Fatalf("mixed SLI batch receipt counts: valid=%d invalid=%d err=%v", count, committedInvalid, err)
	}
}

func runConnectedSLIAgentJourney(t *testing.T, databaseURL, binary, dir, baseURL, certFile, password string) connectedAgentJourney {
	t.Helper()
	return runNamedConnectedSLIAgentJourney(t, databaseURL, binary, dir, baseURL, certFile, password, "sli-journey-agent")
}

func runNamedConnectedSLIAgentJourney(t *testing.T, databaseURL, binary, dir, baseURL, certFile, password, name string) connectedAgentJourney {
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
	create := exec.CommandContext(t.Context(), binary, "agents", "create", "--profile-file="+profile, "--name="+name, "--display-name=SLI Journey Agent", "--idempotency-key="+name+"-create", "--token-file="+tokenPath, "--output=json")
	create.Stderr = io.Discard
	output, err := create.Output()
	var created agentCLICreateDocument
	if err != nil || json.Unmarshal(output, &created) != nil || !validCLIUUID(created.Result.Agent.ID) {
		t.Fatal("register SLI journey agent failed")
	}
	agentBinary, err := filepath.Abs(filepath.Join(filepath.Dir(binary), "almanac-agent"))
	if err != nil {
		t.Fatal("resolve candidate agent executable failed")
	}
	done, result := startSLIJourneyAgent(t, agentBinary, tokenPath, baseURL, certFile)
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

func startSLIJourneyAgent(t *testing.T, binary, tokenPath, baseURL, certFile string) (<-chan struct{}, <-chan error) {
	t.Helper()
	agent := exec.Command(binary, "run", "--backend-url="+baseURL, "--token-file="+tokenPath, "--ca-file="+certFile)
	agent.Stdout, agent.Stderr = io.Discard, io.Discard
	if err := agent.Start(); err != nil {
		t.Fatal("start separate SLI agent executable failed")
	}
	done, result := make(chan struct{}), make(chan error, 1)
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
	return done, result
}

func createSLIJourneyDatasource(t *testing.T, binary, dir string, journey connectedAgentJourney, endpoint string, caPEM []byte) string {
	t.Helper()
	return createNamedSLIJourneyDatasource(t, binary, dir, journey, endpoint, caPEM, "sli-journey-metrics")
}

func createNamedSLIJourneyDatasource(t *testing.T, binary, dir string, journey connectedAgentJourney, endpoint string, caPEM []byte, name string) string {
	t.Helper()
	return createSLIJourneyDatasourceWithCadence(t, binary, dir, journey, endpoint, caPEM, name, 15)
}

func createSLIJourneyDatasourceWithCadence(t *testing.T, binary, dir string, journey connectedAgentJourney, endpoint string, caPEM []byte, name string, cadence int) string {
	t.Helper()
	input := map[string]any{"name": name, "display_name": "SLI Journey Metrics", "provider": "prometheus",
		"configuration_format": "prometheus-http-v1", "endpoint": endpoint, "tls": map[string]any{"insecure_skip_verify": false, "ca_pem": string(caPEM)},
		"authentication":           map[string]any{"mode": "none", "username": nil, "password_secret": map[string]any{"action": "clear"}, "token_secret": map[string]any{"action": "clear"}},
		"polling_interval_seconds": cadence, "agent_id": journey.id}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal("encode SLI journey datasource failed")
	}
	path := filepath.Join(dir, "sli-journey-datasource.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal("write protected SLI journey datasource failed")
	}
	command := exec.CommandContext(t.Context(), binary, "datasources", "create", "--profile-file="+journey.profile, "--input-file="+path, "--idempotency-key="+name+"-create", "--output=json")
	output, err := command.Output()
	var document struct {
		Result api.Datasource `json:"result"`
	}
	if err != nil || json.Unmarshal(output, &document) != nil || !validCLIUUID(datasourceUUID(document.Result.Id)) || document.Result.PollingIntervalSeconds != cadence {
		t.Fatal("create bounded SLI journey datasource failed")
	}
	return datasourceUUID(document.Result.Id)
}

// createSLIJourneySLI creates the journey's 99/100 SLI through the executable CLI and waits until the
// assigned agent has collected its first interval. The UI path is covered by web/e2e (ADR-0033).
func createSLIJourneySLI(t *testing.T, binary, dir string, journey connectedAgentJourney, databaseURL, datasourceID string) string {
	t.Helper()
	input, err := json.Marshal(map[string]any{"name": "request-success", "display_name": "Request success", "datasource_id": datasourceID,
		"measurement_kind": "event_counts", "component_mode": "interval_count", "good_query": "vector(99)", "total_query": "vector(100)"})
	if err != nil {
		t.Fatal("encode SLI journey SLI failed")
	}
	path := filepath.Join(dir, "sli-journey-sli.json")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal("write protected SLI journey SLI failed")
	}
	output, err := exec.CommandContext(t.Context(), binary, "slis", "create", "--profile-file="+journey.profile, "--input-file="+path,
		"--idempotency-key=request-success-create", "--output=json").Output()
	var document struct {
		Result struct {
			SLI struct {
				ID string `json:"id"`
			} `json:"sli"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(output, &document) != nil || !validCLIUUID(document.Result.SLI.ID) {
		t.Fatal("create SLI journey SLI through the CLI failed")
	}
	db := testpostgres.Connect(t, databaseURL)
	// Settling is fixed at 60 s and the cadence is 15 s, so the first interval lands within about 90 s.
	for deadline := time.Now().Add(150 * time.Second); time.Now().Before(deadline); {
		var observed int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_observations WHERE sli_id=$1 AND outcome='observed'`, document.Result.SLI.ID).Scan(&observed); err == nil && observed > 0 {
			return document.Result.SLI.ID
		}
		select {
		case <-t.Context().Done():
			t.Fatal("SLI journey observation wait cancelled")
		case <-time.After(time.Second):
		}
	}
	t.Fatal("CLI-created SLI produced no collected interval within 150 s")
	return ""
}

func verifyPersistedSLIJourneyObservation(t *testing.T, databaseURL, sliID, agentID string) {
	t.Helper()
	db := testpostgres.Connect(t, databaseURL)
	var good, total, outcome, persistedAgent string
	var start, end time.Time
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		err := db.QueryRow(t.Context(), `SELECT c.good_count::text,c.total_count::text,c.outcome,c.agent_id::text,c.interval_start,
			c.interval_start+source.polling_interval_seconds*interval '1 second' AS interval_end
			FROM public.almanac_sli_observations c
			JOIN public.almanac_sli_measurements measurement ON measurement.sli_id=c.sli_id AND measurement.revision=c.measurement_revision
			JOIN public.almanac_datasources source ON source.id=measurement.datasource_id
			WHERE c.sli_id=$1 AND c.outcome='observed' ORDER BY c.interval_start DESC LIMIT 1`, sliID).Scan(&good, &total, &outcome, &persistedAgent, &start, &end)
		if err == nil {
			if good != "99.000000000000" || total != "100.000000000000" || outcome != "observed" || persistedAgent != agentID || end.Sub(start) != 15*time.Second {
				t.Fatalf("persisted SLI journey observation differs: good=%s total=%s outcome=%s source_agent_matches=%t cadence=%s", good, total, safeDiagnosticToken(outcome), persistedAgent == agentID, end.Sub(start))
			}
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("SLI journey SLI did not produce a persisted 99/100 canonical interval")
}
