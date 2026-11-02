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
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func runPerformanceBrowser(t *testing.T, ctx context.Context, fleet performanceFleet, restart performanceBrowserRestartReport, networkLatency int) (string, error) {
	t.Helper()
	f := fleet.fixture
	if len(fleet.objectives) != 10 && len(fleet.objectives) != 100 && len(fleet.objectives) != 1000 {
		return "", errors.New("browser performance requires the complete controlled fleet")
	}
	if networkLatency != 0 && networkLatency != 100 || restart.NeverVisited != (networkLatency == 0) || restart.Target != fleet.objectives[len(fleet.objectives)-1] {
		return "", errors.New("browser acceptance requires its reserved never-visited detail target")
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
	if f.backend.deployment == nil {
		return artifacts, errors.New("browser capacity acceptance requires normal container deployment")
	}
	deployment := f.backend.deployment.evidence(t)
	if deployment.BackendSHA != hex.EncodeToString(digest.Sum(nil)) {
		return artifacts, errors.New("browser candidate differs from normal deployed executable")
	}
	spki := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	input, err := marshalPerformanceEvidence(struct {
		URL         string                          `json:"url"`
		SPKI        string                          `json:"spki"`
		Login       string                          `json:"login"`
		Password    string                          `json:"password"`
		SloID       string                          `json:"slo_id"`
		Artifacts   string                          `json:"artifacts"`
		Candidate   string                          `json:"candidate_binary_sha256"`
		FleetSize   int                             `json:"fleet_size"`
		Datasources []string                        `json:"datasource_ids"`
		ServiceID   string                          `json:"service_id"`
		CacheSetup  performanceBrowserRestartReport `json:"cache_setup"`
		TraceURL    string                          `json:"trace_url"`
		Deployment  performanceDeploymentEvidence   `json:"deployment"`
		Latency     int                             `json:"network_latency_ms"`
	}{f.baseURL, base64.StdEncoding.EncodeToString(spki[:]), "sli-owner", f.password, restart.Target, artifacts, hex.EncodeToString(digest.Sum(nil)), len(fleet.objectives), fleet.datasources, fleet.serviceID, restart, fleet.traceURL, deployment, networkLatency})
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
	type measuredLoad struct {
		Index   int       `json:"index"`
		View    string    `json:"view"`
		Elapsed float64   `json:"primary_ms"`
		Failure string    `json:"failure"`
		Before  time.Time `json:"before_evaluated_through"`
		TraceID string    `json:"trace_id"`
		TraceMS float64   `json:"trace_duration_ms"`
		Detail  struct {
			Through    time.Time `json:"history_evaluated_through"`
			History    string    `json:"history_generation"`
			Rendered   string    `json:"rendered_generation"`
			Status     string    `json:"status_generation"`
			RangeStart time.Time `json:"range_start"`
			RangeEnd   time.Time `json:"range_end"`
			Burn       int       `json:"burn_window_seconds"`
			Zone       string    `json:"time_zone"`
		} `json:"detail"`
	}
	type traceEvidence struct {
		Sample     string  `json:"sample"`
		TraceID    string  `json:"trace_id"`
		Duration   float64 `json:"duration_ms"`
		Connected  bool    `json:"connected"`
		Generation string  `json:"history_generation"`
		Origin     string  `json:"cache_source_trace_id"`
		Artifact   string  `json:"artifact"`
	}
	var report struct {
		Environment struct {
			CPU      int    `json:"cpu_slowdown"`
			Latency  int    `json:"latency_ms"`
			Download *int64 `json:"download_bits_per_second"`
		} `json:"environment"`
		Result         string                          `json:"result"`
		Candidate      string                          `json:"candidate_binary_sha256"`
		ColdLoads      []measuredLoad                  `json:"cold_loads"`
		WarmLoads      []measuredLoad                  `json:"warm_loads"`
		LifecycleLoads []measuredLoad                  `json:"lifecycle_loads"`
		CacheSetup     performanceBrowserRestartReport `json:"cache_setup"`
		Interactions   []jsontext.Value                `json:"interactions"`
		ListCoverage   struct {
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
		Failures   []string                      `json:"failures"`
		Traces     []traceEvidence               `json:"trace_evidence"`
		Deployment performanceDeploymentEvidence `json:"deployment"`
	}
	if readErr != nil || reportCloseErr != nil || len(reportBody) > 8<<20 || json.Unmarshal(reportBody, &report) != nil ||
		report.Result != "passed" || report.Candidate != hex.EncodeToString(digest.Sum(nil)) || report.Deployment != deployment ||
		report.Environment.CPU != 1 || report.Environment.Latency != networkLatency || report.Environment.Download != nil ||
		len(report.ColdLoads) != 60 || len(report.WarmLoads) != 60 || len(report.LifecycleLoads) != 1 || len(report.Interactions) != 50 || len(report.Failures) != 0 || len(report.Traces) != 171 {
		return artifacts, errors.New("browser performance report is incomplete, failed or from another executable")
	}
	traceByID, traceBySample := make(map[string]traceEvidence), make(map[string]traceEvidence)
	for _, evidence := range report.Traces {
		if !evidence.Connected || len(evidence.TraceID) != 32 || strings.Trim(evidence.TraceID, "0123456789abcdef") != "" || evidence.Sample == "" ||
			traceByID[evidence.TraceID].TraceID != "" || traceBySample[evidence.Sample].TraceID != "" || !filepath.IsLocal(evidence.Artifact) {
			return artifacts, errors.New("browser trace evidence is missing, duplicated or disconnected")
		}
		info, err := os.Lstat(filepath.Join(artifacts, evidence.Artifact))
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 5<<20 {
			return artifacts, errors.New("browser trace omitted its bounded captured document")
		}
		traceByID[evidence.TraceID], traceBySample[evidence.Sample] = evidence, evidence
	}
	for _, evidence := range report.Traces {
		if evidence.Origin != "" {
			producer, found := traceByID[evidence.Origin]
			if !found || producer.Origin != "" || producer.TraceID == evidence.TraceID || !validCLIUUID(evidence.Generation) || producer.Generation != evidence.Generation {
				return artifacts, errors.New("cached browser graph is not joined to its captured network-backed generation")
			}
		}
	}
	usedTraces := make(map[string]bool)
	validTrace := func(label, id string, elapsed, duration, budget float64) bool {
		evidence, found := traceBySample[label]
		if !found || evidence.TraceID != id || usedTraces[id] || duration <= 0 || duration >= budget || math.Abs(evidence.Duration-duration) > 0.002 || math.Abs(elapsed-duration) > 100 {
			return false
		}
		usedTraces[id] = true
		return true
	}
	if report.CacheSetup.NeverVisited != restart.NeverVisited || report.CacheSetup.Result != restart.Result || report.CacheSetup.Target != restart.Target || report.CacheSetup.PreservedPoints != restart.PreservedPoints || report.CacheSetup.PreservedTiles != restart.PreservedTiles || report.CacheSetup.PreservedCurrent != int64(len(fleet.objectives)) {
		return artifacts, errors.New("browser report omitted retained-restart/cache-eviction provenance")
	}
	validDetail := func(sample measuredLoad) bool {
		value := sample.Detail
		return validCLIUUID(value.History) && value.Rendered == value.History && validCLIUUID(value.Status) &&
			!value.Through.IsZero() && value.RangeEnd.Equal(value.Through) && value.RangeEnd.Sub(value.RangeStart) == 28*24*time.Hour && value.Burn == 3600 && value.Zone == "UTC"
	}
	for cohortIndex, cohort := range [][]measuredLoad{report.ColdLoads, report.WarmLoads} {
		counts := make(map[string]int)
		for _, sample := range cohort {
			budget := 1000.0
			if sample.View == "slo-detail" {
				if !validDetail(sample) {
					return artifacts, errors.New("browser omitted the rendered generation, current status or requested graph bounds")
				}
			} else if sample.View != "sli-list" && sample.View != "slo-list" {
				return artifacts, errors.New("browser load has unknown journey identity")
			}
			label := fmt.Sprintf("%s-%s-%d", []string{"cold", "warm"}[cohortIndex], sample.View, sample.Index)
			if sample.Failure != "" || sample.Elapsed <= 0 || sample.Elapsed >= budget || !validTrace(label, sample.TraceID, sample.Elapsed, sample.TraceMS, budget) {
				return artifacts, errors.New("cold/warm browser load exceeded usable-content budget")
			}
			counts[sample.View]++
		}
		for _, view := range []string{"sli-list", "slo-list", "slo-detail"} {
			if counts[view] != 20 {
				return artifacts, errors.New("browser omitted a cold/warm journey cohort")
			}
		}
	}
	minute := report.LifecycleLoads[0]
	if minute.View != "slo-detail" || minute.Failure != "" || minute.Elapsed <= 0 || minute.Elapsed >= 1000 || minute.Before.IsZero() || !minute.Detail.Through.After(minute.Before) || !validDetail(minute) || !validTrace("lifecycle-slo-detail-0", minute.TraceID, minute.Elapsed, minute.TraceMS, 1000) {
		return artifacts, errors.New("browser minute-boundary visit omitted newer rendered evaluation or violated latency")
	}
	for index, body := range report.Interactions {
		var sample struct {
			Index   int     `json:"index"`
			Elapsed float64 `json:"total_ms"`
			TraceID string  `json:"trace_id"`
			TraceMS float64 `json:"trace_duration_ms"`
			Failure string  `json:"failure"`
		}
		if json.Unmarshal(body, &sample) != nil || sample.Index != index || sample.Failure != "" || sample.Elapsed <= 0 || sample.Elapsed >= 1000 || !validTrace(fmt.Sprintf("interaction-%d", index), sample.TraceID, sample.Elapsed, sample.TraceMS, 1000) {
			return artifacts, errors.New("browser graph interaction lacks matching rendered trace evidence")
		}
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
