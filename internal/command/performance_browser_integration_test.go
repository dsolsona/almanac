//go:build integration

package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func runPerformanceBrowser(t *testing.T, ctx context.Context, fleet performanceFleet) (string, error) {
	t.Helper()
	f := fleet.fixture
	if len(fleet.objectives) != 10 && len(fleet.objectives) != 100 && len(fleet.objectives) != 1000 {
		return "", errors.New("browser performance requires the complete controlled fleet")
	}
	block, rest := pem.Decode(f.certificate)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return "", errors.New("invalid browser performance certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", errors.New("invalid browser performance certificate")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return "", errors.New("resolve browser performance workspace failed")
	}
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "backend-performance-browser-")
	if err != nil {
		return "", errors.New("create browser performance evidence directory failed")
	}
	file, err := os.Open(f.binary)
	if err != nil {
		return artifacts, errors.New("open candidate executable for browser evidence failed")
	}
	digest := sha256.New()
	_, hashErr := io.Copy(digest, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		return artifacts, errors.New("identify candidate executable for browser evidence failed")
	}
	spki := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	input, err := json.Marshal(struct {
		URL         string   `json:"url"`
		SPKI        string   `json:"spki"`
		Login       string   `json:"login"`
		Password    string   `json:"password"`
		SloID       string   `json:"slo_id"`
		Artifacts   string   `json:"artifacts"`
		Candidate   string   `json:"candidate_binary_sha256"`
		FleetSize   int      `json:"fleet_size"`
		Datasources []string `json:"datasource_ids"`
		ServiceID   string   `json:"service_id"`
	}{f.baseURL, base64.StdEncoding.EncodeToString(spki[:]), "sli-owner", f.password, fleet.objectives[0], artifacts, hex.EncodeToString(digest.Sum(nil)), len(fleet.objectives), fleet.datasources, fleet.serviceID})
	if err != nil || len(input) > 16*1024 {
		return artifacts, errors.New("encode protected browser performance input failed")
	}
	defer clear(input)
	command := exec.CommandContext(ctx, "node", "scripts/measure-backend-browser.mjs")
	command.Dir = filepath.Join(root, "web")
	command.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	runErr := command.Run()
	if strings.Contains(output.String(), f.password) {
		return artifacts, errors.New("browser performance output contained a credential")
	}
	// Do not print raw Playwright or process errors. The protected report retains
	// static failure stages and all completed samples, including the first failure.
	if runErr != nil {
		return artifacts, errors.New("browser performance failed; inspect the retained browser-performance.json report")
	}
	reportFile, err := os.Open(filepath.Join(artifacts, "browser-performance.json"))
	if err != nil {
		return artifacts, errors.New("browser performance omitted its evidence report")
	}
	reportBody, readErr := io.ReadAll(io.LimitReader(reportFile, (8<<20)+1))
	reportCloseErr := reportFile.Close()
	var report struct {
		Result       string           `json:"result"`
		Candidate    string           `json:"candidate_binary_sha256"`
		ColdLoads    []jsontext.Value `json:"cold_loads"`
		Interactions []jsontext.Value `json:"interactions"`
		ListCoverage struct {
			Slis []string `json:"slis"`
			Slos []string `json:"slos"`
		} `json:"list_coverage"`
		ListActions []struct {
			View     string   `json:"view"`
			Action   string   `json:"action"`
			PageSize int      `json:"page_size"`
			Elapsed  float64  `json:"elapsed_ms"`
			IDs      []string `json:"ids"`
		} `json:"list_actions"`
		Failures []string `json:"failures"`
	}
	if readErr != nil || reportCloseErr != nil || len(reportBody) > 8<<20 || json.Unmarshal(reportBody, &report) != nil ||
		report.Result != "passed" || report.Candidate != hex.EncodeToString(digest.Sum(nil)) ||
		len(report.ColdLoads) != 60 || len(report.Interactions) != 50 || len(report.Failures) != 0 {
		return artifacts, errors.New("browser performance report is incomplete, failed or from another executable")
	}
	for _, coverage := range []struct{ actual, expected []string }{{report.ListCoverage.Slis, fleet.slis}, {report.ListCoverage.Slos, fleet.objectives}} {
		actual, expected := slices.Clone(coverage.actual), slices.Clone(coverage.expected)
		slices.Sort(actual)
		slices.Sort(expected)
		if !slices.Equal(actual, expected) {
			return artifacts, errors.New("browser pagination did not traverse the complete fixture identities")
		}
	}
	for _, view := range []string{"slis", "slos"} {
		actions := make(map[string]bool)
		for _, action := range report.ListActions {
			if action.View != view {
				continue
			}
			if action.Elapsed <= 0 || action.Elapsed >= 1000 || action.PageSize != 10 && action.PageSize != 20 || len(action.IDs) > action.PageSize {
				return artifacts, errors.New("browser list action violated latency or bounded page acceptance")
			}
			actions[action.Action] = true
		}
		required := []string{"size-10", "search-match", "search-empty", "search-clear"}
		if view == "slis" {
			required = append(required, "datasource-filter", "datasource-clear")
		} else {
			required = append(required, "service-filter", "service-clear", "urgency-filter", "urgency-clear")
		}
		if len(fleet.objectives) > 20 {
			required = append(required, "back-20", "back-10", "next-again-20", "next-again-10")
		}
		for _, action := range required {
			if !actions[action] {
				return artifacts, errors.New("browser list report omitted required pagination/filter interactions")
			}
		}
	}
	return artifacts, nil
}
