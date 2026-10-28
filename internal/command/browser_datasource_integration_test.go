//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json/v2"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// checkDatasourceBrowser exercises the real authenticated datasource UI against
// the live backend and assigned agent, keeping browser output and screenshots
// free of credential values.
func checkDatasourceBrowser(t *testing.T, baseURL string, certificatePEM []byte, login, password, agentID, datasourceID, query, caPEM string) {
	t.Helper()
	parsedURL, err := url.Parse(baseURL)
	caPool := x509.NewCertPool()
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Hostname() != "localhost" || parsedURL.Path != "" && parsedURL.Path != "/" || login == "" || password == "" || !validCLIUUID(agentID) || !validCLIUUID(datasourceID) || query != "vector(1)" || len(certificatePEM) > 16*1024 || len(caPEM) == 0 || len(caPEM) > 16*1024 || !caPool.AppendCertsFromPEM([]byte(caPEM)) {
		t.Fatal("datasource browser fixture is invalid")
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil {
		t.Fatal("decode datasource browser certificate failed")
	}
	parsedCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(parsedCertificate.RawSubjectPublicKeyInfo) == 0 {
		t.Fatal("parse datasource browser certificate failed")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("resolve repository failed")
	}
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "browser-datasources-")
	if err != nil {
		t.Fatal("create datasource browser artifact directory failed")
	}
	authSource := newBrowserAuthSource(t, filepath.Join(artifacts, "provider-started"))
	defer authSource.server.Close()
	spki := sha256.Sum256(parsedCertificate.RawSubjectPublicKeyInfo)
	fixture := struct {
		URL           string `json:"url"`
		SPKI          string `json:"spki"`
		Certificate   string `json:"certificate"`
		Login         string `json:"login"`
		Password      string `json:"password"`
		AgentID       string `json:"agent_id"`
		DatasourceID  string `json:"datasource_id"`
		Query         string `json:"query"`
		CAPEM         string `json:"ca_pem"`
		AuthEndpoint  string `json:"auth_endpoint"`
		AuthCAPEM     string `json:"auth_ca_pem"`
		BasicPassword string `json:"basic_password"`
		BearerToken   string `json:"bearer_token"`
		Artifacts     string `json:"artifacts"`
	}{
		URL: baseURL, SPKI: base64.StdEncoding.EncodeToString(spki[:]), Certificate: string(certificatePEM),
		Login: login, Password: password, AgentID: agentID, DatasourceID: datasourceID, Query: query, CAPEM: caPEM,
		AuthEndpoint: authSource.server.URL, AuthCAPEM: string(authSource.certificate), BasicPassword: authSource.basicPassword, BearerToken: authSource.bearerToken, Artifacts: artifacts,
	}
	input, err := json.Marshal(fixture)
	if err != nil || len(input) > 48*1024 {
		t.Fatal("encode datasource browser fixture failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "scripts/check-datasource-browser.mjs")
	browser.Dir = filepath.Join(root, "web")
	browser.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	browser.Stdout, browser.Stderr = &output, &output
	if err := browser.Run(); err != nil {
		if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) || strings.Contains(output.String(), authSource.basicPassword) || strings.Contains(output.String(), authSource.bearerToken) {
			t.Fatal("datasource browser fixture leaked a credential in output")
		}
		t.Fatalf("datasource browser acceptance failed: %s; inspect sanitized artifacts in %s", safeBrowserFailureMarker(output.String()), artifacts)
	}
	if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) || strings.Contains(output.String(), authSource.basicPassword) || strings.Contains(output.String(), authSource.bearerToken) {
		t.Fatal("datasource browser fixture leaked a credential in output")
	}
	if strings.TrimSpace(output.String()) != "PASS browser-datasources" {
		t.Fatalf("datasource browser acceptance did not produce its exact completion marker; inspect sanitized artifacts in %s", artifacts)
	}
	basicSeen, bearerSeen, canceled := authSource.evidence()
	if !basicSeen || !bearerSeen || !canceled {
		t.Fatalf("saved authentication/cancellation journey incomplete: basic=%t bearer=%t canceled=%t", basicSeen, bearerSeen, canceled)
	}
}

type browserAuthSource struct {
	server        *httptest.Server
	certificate   []byte
	basicPassword string
	bearerToken   string
	startMarker   string
	mu            sync.Mutex
	basicSeen     bool
	bearerSeen    bool
	canceled      bool
}

func (source *browserAuthSource) evidence() (basic, bearer, canceled bool) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.basicSeen, source.bearerSeen, source.canceled
}

func newBrowserAuthSource(t *testing.T, startMarker string) *browserAuthSource {
	t.Helper()
	source := &browserAuthSource{basicPassword: "browser-basic-secret", bearerToken: "browser-bearer-secret", startMarker: startMarker}
	source.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mode := strings.TrimPrefix(request.URL.Path, "/")
		mode = strings.SplitN(mode, "/", 2)[0]
		var valid bool
		switch mode {
		case "basic":
			username, password, ok := request.BasicAuth()
			valid = ok && username == "browser-user" && password == source.basicPassword
			if valid {
				source.mu.Lock()
				source.basicSeen = true
				source.mu.Unlock()
			}
		case "bearer":
			valid = request.Header.Get("Authorization") == "Bearer "+source.bearerToken
			if valid {
				source.mu.Lock()
				source.bearerSeen = true
				source.mu.Unlock()
			}
		}
		if !valid {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.URL.Query().Get("query") == "block_until_cancel" {
			if err := os.WriteFile(source.startMarker, []byte("started\n"), 0600); err != nil {
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			<-request.Context().Done()
			source.mu.Lock()
			source.canceled = true
			source.mu.Unlock()
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"1"]}]}}`))
	}))
	encodedCertificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: source.server.Certificate().Raw})
	if len(encodedCertificate) == 0 {
		source.server.Close()
		t.Fatal("encode saved-credential source certificate failed")
	}
	source.certificate = encodedCertificate
	return source
}

func safeBrowserFailureMarker(output string) string {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) != 15 || fields[0] != "FAIL" || fields[1] != "browser-datasources" {
		return "stage=none network=none api_status=none request_id=none api_code=none"
	}
	values := make(map[string]string, 13)
	for _, field := range fields[2:] {
		key, value, ok := strings.Cut(field, "=")
		if !ok || value == "" {
			return "stage=none network=none api_status=none request_id=none api_code=none"
		}
		if _, exists := values[key]; exists {
			return "stage=none network=none api_status=none request_id=none api_code=none"
		}
		values[key] = value
	}
	stage := values["stage"]
	if len(stage) == 0 || len(stage) > 96 || strings.Trim(stage, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
		stage = "none"
	}
	network := values["network"]
	if network != "none" && (len(network) > 40 || !strings.HasPrefix(network, "ERR_") || strings.Trim(network, "ABCDEFGHIJKLMNOPQRSTUVWXYZ_") != "") {
		network = "none"
	}
	class := values["class"]
	if class != "timeout" && class != "assertion" && class != "body_unavailable" && class != "element_detached" && class != "other" {
		class = "other"
	}
	line := values["source_line"]
	if line != "none" && (len(line) == 0 || len(line) > 4 || strings.Trim(line, "0123456789") != "") {
		line = "none"
	}
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
	safeCodes := map[string]bool{"agent_unavailable": true, "authentication_unavailable": true, "backend_unavailable": true,
		"capability_unavailable": true, "conflict": true, "configuration_failed": true, "configuration_pending": true, "csrf_invalid": true, "datasource_unavailable": true,
		"diagnostic_unavailable": true, "forbidden": true, "invalid_agent": true, "invalid_request": true,
		"method_not_allowed": true, "not_found": true, "origin_rejected": true, "rate_limited": true,
		"secure_transport_required": true, "session_present": true, "source_secret_unavailable": true,
		"unauthenticated": true, "unauthorized": true, "unsupported": true, "unsupported_media_type": true}
	if code != "none" && !safeCodes[code] {
		code = "none"
	}
	diagnosticState := values["diagnostic_state"]
	validDiagnosticStates := map[string]bool{"pending": true, "running": true, "succeeded": true, "failed": true, "canceled": true, "timed_out": true, "unknown": true}
	safeAssertionValue := func(value string) string {
		if value == "none" || validDiagnosticStates[value] {
			return value
		}
		if len(value) == 3 && value[0] >= '1' && value[0] <= '5' && strings.Trim(value[1:], "0123456789") == "" {
			return value
		}
		return "none"
	}
	actual, expected := safeAssertionValue(values["assert_actual"]), safeAssertionValue(values["assert_expected"])
	cancelState := values["cancel_response_state"]
	if cancelState != "none" && !validDiagnosticStates[cancelState] {
		cancelState = "none"
	}
	if diagnosticState != "none" && !validDiagnosticStates[diagnosticState] {
		diagnosticState = "none"
	}
	diagnosticErrorCode := values["diagnostic_error_code"]
	validDiagnosticErrorCodes := map[string]bool{"authentication": true, "authentication_failed": true, "deadline": true,
		"diagnostic_failed": true, "incomplete": true, "network": true, "network_failed": true, "query": true,
		"query_failed": true, "rate_limited": true, "tls": true, "tls_failed": true, "timeout": true,
		"unsupported": true, "assignment_revoked": true}
	if diagnosticErrorCode != "none" && !validDiagnosticErrorCodes[diagnosticErrorCode] {
		diagnosticErrorCode = "none"
	}
	diagnosticResultClass := values["diagnostic_result_class"]
	validDiagnosticResultClasses := map[string]bool{"complete": true, "empty": true, "incomplete": true}
	if diagnosticResultClass != "none" && !validDiagnosticResultClasses[diagnosticResultClass] {
		diagnosticResultClass = "none"
	}
	return "stage=" + stage + " network=" + network + " class=" + class + " source_line=" + line + " assert_actual=" + actual + " assert_expected=" + expected + " cancel_response_state=" + cancelState + " api_status=" + status + " request_id=" + requestID + " api_code=" + code + " diagnostic_state=" + diagnosticState + " diagnostic_error_code=" + diagnosticErrorCode + " diagnostic_result_class=" + diagnosticResultClass
}

func TestIntegrationSafeBrowserFailureMarkerPreservesBoundedCancellationEvidence(t *testing.T) {
	marker := "FAIL browser-datasources stage=cancel_response_state network=ERR_CONNECTION_RESET class=assertion source_line=401 assert_actual=succeeded assert_expected=canceled cancel_response_state=succeeded api_status=200 request_id=00000000-0000-4000-8000-000000000001 api_code=none diagnostic_state=running diagnostic_error_code=none diagnostic_result_class=none"
	got := safeBrowserFailureMarker(marker)
	for _, want := range []string{"stage=cancel_response_state", "class=assertion", "source_line=401", "assert_actual=succeeded", "assert_expected=canceled", "cancel_response_state=succeeded", "diagnostic_state=running"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	hostile := strings.Replace(marker, "source_line=401", "source_line=secret-token", 1)
	hostile = strings.Replace(hostile, "assert_actual=succeeded", "assert_actual=secret-token", 1)
	if result := safeBrowserFailureMarker(hostile); strings.Contains(result, "secret-token") || !strings.Contains(result, "source_line=none") || !strings.Contains(result, "assert_actual=none") {
		t.Fatalf("unsafe evidence escaped: %s", result)
	}
	worst := "FAIL browser-datasources stage=" + strings.Repeat("a", 48) + " network=ERR_" + strings.Repeat("A", 36) + " class=body_unavailable source_line=9999 assert_actual=succeeded assert_expected=succeeded cancel_response_state=succeeded api_status=599 request_id=00000000-0000-4000-8000-000000000001 api_code=authentication_unavailable diagnostic_state=succeeded diagnostic_error_code=authentication_failed diagnostic_result_class=incomplete\n"
	if len(worst) > 512 || !strings.Contains(safeBrowserFailureMarker(worst), "source_line=9999") {
		t.Fatalf("bounded marker length=%d", len(worst))
	}
}

func safeBrowserRequestID(value string) bool {
	if len(value) == 32 {
		return strings.Trim(value, "0123456789abcdef") == ""
	}
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
