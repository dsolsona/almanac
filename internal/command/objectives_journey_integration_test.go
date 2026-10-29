//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
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
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

func TestIntegrationExecutableFirstObjectiveJourney(t *testing.T) {
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
	verifyPersistedSLIJourneyObservation(t, databaseURL, sliID, journey.id)
	runObjectiveCLIJourney(t, binary, dir, journey.profile, sliID, databaseURL, baseURL, apiClient)
	runObjectiveBrowserJourney(t, baseURL, certificate, "sli-owner", permanentPassword, sliID)
	if strings.Contains(logs.String(), initialPassword) || strings.Contains(logs.String(), permanentPassword) {
		t.Fatal("backend logs contained a credential")
	}
}

type objectiveJourneyDocument struct {
	SchemaVersion int                       `json:"schema_version"`
	Result        map[string]jsontext.Value `json:"result"`
	Error         *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func runObjectiveCLIJourney(t *testing.T, binary, dir, profile, sliID, databaseURL, baseURL string, httpClient *http.Client) {
	t.Helper()
	run := func(expected int, args ...string) objectiveJourneyDocument {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append(args, "--profile-file="+profile, "--output=json")...)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		code := 0
		if err != nil {
			if exit, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exit.ExitCode()
			} else {
				t.Fatalf("objective CLI launch failed: %v", err)
			}
		}
		var document objectiveJourneyDocument
		if code != expected || json.Unmarshal(stdout.Bytes(), &document) != nil || document.SchemaVersion != 1 || stderr.Len() != 0 {
			t.Fatalf("objective CLI %s %s exit=%d expected=%d; invalid envelope or stderr", args[0], args[1], code, expected)
		}
		return document
	}
	writeInput := func(name string, value any) string {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if os.WriteFile(path, encoded, 0o600) != nil {
			t.Fatal("write objective input failed")
		}
		return path
	}
	serviceInput := writeInput("objective-service.json", map[string]any{"name": "cli-checkout", "display_name": "CLI Checkout"})
	created := run(0, "services", "create", "--input-file="+serviceInput, "--idempotency-key=objective-service-create")
	var service api.Service
	var serviceTag string
	if json.Unmarshal(created.Result["service"], &service) != nil || json.Unmarshal(created.Result["etag"], &serviceTag) != nil || service.Name != "cli-checkout" || service.Version != "1" {
		t.Fatal("invalid created service")
	}
	serviceID := service.Id.String()
	replay := run(0, "services", "create", "--input-file="+serviceInput, "--idempotency-key=objective-service-create")
	var replayService api.Service
	if json.Unmarshal(replay.Result["service"], &replayService) != nil || replayService.Id != service.Id {
		t.Fatal("service idempotency replay changed identity")
	}
	list := run(0, "services", "list")
	var services []api.Service
	if json.Unmarshal(list.Result["items"], &services) != nil || !containsObjectiveService(services, serviceID) {
		t.Fatal("service list omitted created record")
	}
	detail := run(0, "services", "get", "--service-id="+serviceID)
	var fetched api.Service
	if json.Unmarshal(detail.Result["service"], &fetched) != nil || fetched.Id != service.Id {
		t.Fatal("service get mismatch")
	}
	serviceUpdate := writeInput("objective-service-update.json", map[string]string{"display_name": "CLI Checkout Updated"})
	updated := run(0, "services", "update", "--service-id="+serviceID, "--input-file="+serviceUpdate, "--if-match="+serviceTag)
	if json.Unmarshal(updated.Result["service"], &fetched) != nil || fetched.DisplayName != "CLI Checkout Updated" {
		t.Fatal("service metadata update failed")
	}
	conflict := run(5, "services", "update", "--service-id="+serviceID, "--input-file="+serviceUpdate, "--if-match="+serviceTag)
	if conflict.Error == nil || conflict.Error.Code != "conflict" {
		t.Fatal("stale service ETag was not rejected")
	}
	db := testpostgres.Connect(t, databaseURL)
	var start, end time.Time
	if err := db.QueryRow(t.Context(), `SELECT interval_start, interval_end FROM public.almanac_sli_coverage WHERE sli_id=$1 AND outcome='observed' ORDER BY interval_start DESC LIMIT 1`, sliID).Scan(&start, &end); err != nil {
		t.Fatal("observed SLI interval unavailable")
	}
	sloInput := writeInput("objective-slo.json", map[string]any{"name": "cli-availability", "display_name": "CLI Availability", "service_id": serviceID, "sli_id": sliID, "target": "0.999", "rolling_window_seconds": 2592000, "evaluation_valid_from": start.UTC().Format(time.RFC3339Nano)})
	sloCreated := run(0, "slos", "create", "--input-file="+sloInput, "--idempotency-key=objective-slo-create")
	var objective api.Slo
	var sloTag string
	if json.Unmarshal(sloCreated.Result["slo"], &objective) != nil || json.Unmarshal(sloCreated.Result["etag"], &sloTag) != nil || objective.Name != "cli-availability" || objective.Target != "0.999" {
		t.Fatal("invalid created objective")
	}
	sloID := objective.Id.String()
	sloReplay := run(0, "slos", "create", "--input-file="+sloInput, "--idempotency-key=objective-slo-create")
	var replaySLO api.Slo
	if json.Unmarshal(sloReplay.Result["slo"], &replaySLO) != nil || replaySLO.Id != objective.Id {
		t.Fatal("SLO replay changed identity")
	}
	sloList := run(0, "slos", "list", "--service-id="+serviceID)
	var objectives []api.Slo
	if json.Unmarshal(sloList.Result["items"], &objectives) != nil || len(objectives) != 1 || objectives[0].Id != objective.Id {
		t.Fatal("service-filtered list mismatch")
	}
	sloDetail := run(0, "slos", "get", "--slo-id="+sloID)
	var fetchedSLO api.Slo
	if json.Unmarshal(sloDetail.Result["slo"], &fetchedSLO) != nil || fetchedSLO.Id != objective.Id {
		t.Fatal("SLO get mismatch")
	}
	sloUpdate := writeInput("objective-slo-update.json", map[string]string{"display_name": "CLI Availability Updated"})
	changed := run(0, "slos", "update", "--slo-id="+sloID, "--input-file="+sloUpdate, "--if-match="+sloTag)
	if json.Unmarshal(changed.Result["slo"], &fetchedSLO) != nil || fetchedSLO.DisplayName != "CLI Availability Updated" {
		t.Fatal("SLO metadata update failed")
	}
	conflict = run(5, "slos", "update", "--slo-id="+sloID, "--input-file="+sloUpdate, "--if-match="+sloTag)
	if conflict.Error == nil || conflict.Error.Code != "conflict" {
		t.Fatal("stale SLO ETag was not rejected")
	}
	at := end.UTC().Format(time.RFC3339Nano)
	evaluation := run(0, "slos", "evaluate", "--slo-id="+sloID, "--at="+at)
	encoded, err := json.Marshal(evaluation.Result)
	if err != nil {
		t.Fatal(err)
	}
	var cli api.SloEvaluation
	if json.Unmarshal(encoded, &cli) != nil || cli.Performance.Good == nil || cli.Performance.Total == nil || cli.Performance.Attainment == nil || cli.Performance.Burn == nil || !sameObjectiveDecimal(*cli.Performance.Good, "99") || !sameObjectiveDecimal(*cli.Performance.Total, "100") || !sameObjectiveDecimal(*cli.Performance.Attainment, "0.99") || !sameObjectiveDecimal(*cli.Performance.Burn, "10") {
		t.Fatal("objective numerical evaluation did not match the real 99/100 interval")
	}
	locked, err := cliprofile.Open(t.Context(), profile, false)
	if err != nil {
		t.Fatal("open protected CLI profile failed")
	}
	record, exists, err := locked.Load()
	if err != nil || !exists {
		t.Fatal("load protected CLI profile failed")
	}
	if err = locked.Close(); err != nil {
		t.Fatal("close protected CLI profile failed")
	}
	verifyObjectiveHTTPBoundaries(t, httpClient, baseURL, record.Session, serviceID, sliID, databaseURL)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/slos/"+sloID+"/evaluation?at="+url.QueryEscape(at), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: record.Session})
	response, err := httpClient.Do(req)
	if err != nil {
		t.Fatal("evaluation API read failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	var direct api.SloEvaluation
	if err != nil || response.StatusCode != 200 || json.Unmarshal(body, &direct) != nil || direct.Performance.Good == nil || direct.Performance.Total == nil || direct.Performance.Attainment == nil || direct.Performance.Burn == nil || *direct.Performance.Good != *cli.Performance.Good || *direct.Performance.Total != *cli.Performance.Total || *direct.Performance.Attainment != *cli.Performance.Attainment || *direct.Performance.Burn != *cli.Performance.Burn || direct.Quality.ExpectedIntervals != cli.Quality.ExpectedIntervals {
		t.Fatal("CLI and API evaluation differ")
	}
}
func containsObjectiveService(values []api.Service, id string) bool {
	for _, value := range values {
		if value.Id.String() == id {
			return true
		}
	}
	return false
}
func sameObjectiveDecimal(value, want string) bool {
	a, ok := new(big.Rat).SetString(value)
	if !ok {
		return false
	}
	b, ok := new(big.Rat).SetString(want)
	return ok && a.Cmp(b) == 0
}

func runObjectiveBrowserJourney(t *testing.T, baseURL string, certificatePEM []byte, login, password, sliID string) {
	t.Helper()
	block, rest := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 || !validCLIUUID(sliID) {
		t.Fatal("invalid objective browser fixture")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("invalid objective browser certificate")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "browser-sli-objective-")
	if err != nil {
		t.Fatal("create objective artifact directory failed")
	}
	hash := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	fixture := struct {
		URL       string `json:"url"`
		SPKI      string `json:"spki"`
		Login     string `json:"login"`
		Password  string `json:"password"`
		SliID     string `json:"sli_id"`
		Artifacts string `json:"artifacts"`
	}{baseURL, base64.StdEncoding.EncodeToString(hash[:]), login, password, sliID, artifacts}
	input, err := json.Marshal(fixture)
	if err != nil || len(input) > 16*1024 {
		t.Fatal("encode objective browser fixture failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 125*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "scripts/check-slo-browser.mjs")
	browser.Dir = filepath.Join(root, "web")
	browser.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	browser.Stdout = &output
	browser.Stderr = &output
	err = browser.Run()
	if strings.Contains(output.String(), password) || strings.Contains(output.String(), login) {
		t.Fatal("browser output contained a credential")
	}
	if err != nil || strings.TrimSpace(output.String()) != "PASS browser-slo-journey" {
		t.Fatalf("objective browser failed; inspect sanitized artifacts in %s", artifacts)
	}
	manifest, err := os.ReadFile(filepath.Join(artifacts, "slo-journey.json"))
	var evidence struct {
		SloID      string            `json:"slo_id"`
		ServiceID  string            `json:"service_id"`
		SliID      string            `json:"sli_id"`
		Evaluation api.SloEvaluation `json:"evaluation"`
	}
	if err != nil || json.Unmarshal(manifest, &evidence) != nil || !validCLIUUID(evidence.SloID) || !validCLIUUID(evidence.ServiceID) || evidence.SliID != sliID || evidence.Evaluation.Performance.Attainment == nil || !sameObjectiveDecimal(*evidence.Evaluation.Performance.Attainment, "0.99") {
		t.Fatalf("browser objective evidence invalid; inspect %s", artifacts)
	}
}

func verifyObjectiveHTTPBoundaries(t *testing.T, client *http.Client, baseURL, session, serviceID, sliID, databaseURL string) {
	t.Helper()
	call := func(method, path, body, csrf string, authenticated bool) (int, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal("create boundary request failed")
		}
		request.Header.Set("Origin", baseURL)
		request.Header.Set("Content-Type", "application/json")
		if authenticated {
			request.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: session})
		}
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		if method == http.MethodPost && path != "/api/v1/auth/context" {
			request.Header.Set("Idempotency-Key", "objective-boundary-test")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("send boundary request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal("read boundary response failed")
		}
		return response.StatusCode, data
	}
	for _, path := range []string{"/api/v1/services", "/api/v1/slos"} {
		status, _ := call(http.MethodGet, path, "", "", false)
		if status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s returned %d, want 401", path, status)
		}
	}
	status, body := call(http.MethodPost, "/api/v1/auth/context", "{}", "", true)
	var auth api.AuthResponse
	if status != http.StatusOK || json.Unmarshal(body, &auth) != nil || auth.Session == nil || auth.CsrfToken == "" {
		t.Fatal("authenticated context unavailable for boundary test")
	}
	serviceBody := `{"name":"invalid-service","display_name":"Invalid service"}`
	sloBody := `{"name":"invalid-slo","display_name":"Invalid SLO","service_id":"` + serviceID + `","sli_id":"` + sliID + `","target":"0.99","rolling_window_seconds":60}`
	for _, tc := range []struct{ path, body string }{{"/api/v1/services", serviceBody}, {"/api/v1/slos", sloBody}} {
		for _, csrf := range []string{"", "incorrect-csrf"} {
			status, _ = call(http.MethodPost, tc.path, tc.body, csrf, true)
			if status != http.StatusForbidden {
				t.Fatalf("%s mutation with missing or wrong CSRF returned %d", tc.path, status)
			}
		}
	}
	malformed := []struct{ path, body string }{
		{"/api/v1/services", `{"name":"invalid-service","display_name":"Invalid service","extra":true}`},
		{"/api/v1/services", `{"name":"invalid-service","display_name":null}`},
		{"/api/v1/services", `{"name":"invalid-service","name":"invalid-service","display_name":"Invalid service"}`},
		{"/api/v1/slos", strings.TrimSuffix(sloBody, "}") + `,"extra":true}`},
		{"/api/v1/slos", strings.Replace(sloBody, `"target":"0.99"`, `"target":null`, 1)},
		{"/api/v1/slos", strings.Replace(sloBody, `"target":"0.99"`, `"target":"0.99","target":"0.99"`, 1)},
	}
	for _, tc := range malformed {
		status, _ = call(http.MethodPost, tc.path, tc.body, auth.CsrfToken, true)
		if status != http.StatusBadRequest {
			t.Fatalf("strict body on %s returned %d, want 400", tc.path, status)
		}
	}
	db := testpostgres.Connect(t, databaseURL)
	var services, slos int
	if err := db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM public.almanac_services WHERE name='invalid-service'), (SELECT count(*) FROM public.almanac_slos WHERE name='invalid-slo')`).Scan(&services, &slos); err != nil || services != 0 || slos != 0 {
		t.Fatalf("denied mutations changed records: services=%d slos=%d", services, slos)
	}
}
