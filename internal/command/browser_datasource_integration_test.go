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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	spki := sha256.Sum256(parsedCertificate.RawSubjectPublicKeyInfo)
	fixture := struct {
		URL          string `json:"url"`
		SPKI         string `json:"spki"`
		Certificate  string `json:"certificate"`
		Login        string `json:"login"`
		Password     string `json:"password"`
		AgentID      string `json:"agent_id"`
		DatasourceID string `json:"datasource_id"`
		Query        string `json:"query"`
		CAPEM        string `json:"ca_pem"`
		Artifacts    string `json:"artifacts"`
	}{
		URL: baseURL, SPKI: base64.StdEncoding.EncodeToString(spki[:]), Certificate: string(certificatePEM),
		Login: login, Password: password, AgentID: agentID, DatasourceID: datasourceID, Query: query, CAPEM: caPEM, Artifacts: artifacts,
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
		if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) {
			t.Fatal("datasource browser fixture leaked a credential in output")
		}
		t.Fatalf("datasource browser acceptance failed: %s; inspect sanitized artifacts in %s", safeBrowserFailureMarker(output.String()), artifacts)
	}
	if strings.Contains(output.String(), login) || strings.Contains(output.String(), password) {
		t.Fatal("datasource browser fixture leaked a credential in output")
	}
	if strings.TrimSpace(output.String()) != "PASS browser-datasources" {
		t.Fatalf("datasource browser acceptance did not produce its exact completion marker; inspect sanitized artifacts in %s", artifacts)
	}
}

func safeBrowserFailureMarker(output string) string {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) != 11 || fields[0] != "FAIL" || fields[1] != "browser-datasources" {
		return "stage=none network=none api_status=none request_id=none api_code=none"
	}
	values := make(map[string]string, 6)
	for _, field := range fields[2:] {
		key, value, ok := strings.Cut(field, "=")
		if !ok || value == "" {
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
	if class != "timeout" && class != "other" {
		class = "other"
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
		"capability_unavailable": true, "conflict": true, "csrf_invalid": true, "datasource_unavailable": true,
		"diagnostic_unavailable": true, "forbidden": true, "invalid_agent": true, "invalid_request": true,
		"method_not_allowed": true, "not_found": true, "origin_rejected": true, "rate_limited": true,
		"secure_transport_required": true, "session_present": true, "source_secret_unavailable": true,
		"unauthenticated": true, "unauthorized": true, "unsupported": true, "unsupported_media_type": true}
	if code != "none" && !safeCodes[code] {
		code = "none"
	}
	diagnosticState := values["diagnostic_state"]
	validDiagnosticStates := map[string]bool{"pending": true, "running": true, "succeeded": true, "failed": true, "canceled": true, "timed_out": true, "unknown": true}
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
	return "stage=" + stage + " network=" + network + " class=" + class + " api_status=" + status + " request_id=" + requestID + " api_code=" + code + " diagnostic_state=" + diagnosticState + " diagnostic_error_code=" + diagnosticErrorCode + " diagnostic_result_class=" + diagnosticResultClass
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
