//go:build integration

package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type performanceWorkTraceEvidence struct {
	Kind          string        `json:"kind"`
	GenerationKey string        `json:"generation_key,omitempty"`
	Source        string        `json:"source_generation,omitempty"`
	Through       string        `json:"evaluated_through,omitempty"`
	TraceID       string        `json:"trace_id"`
	Duration      int64         `json:"duration_us"`
	Artifact      string        `json:"artifact"`
	Connected     bool          `json:"connected"`
	Failure       string        `json:"failure,omitempty"`
	Lookups       []traceLookup `json:"lookups"`
}

type performanceWorkTraceReport struct {
	Artifacts            string `json:"artifacts"`
	BackendSHA, AgentSHA string
	Deployment           performanceDeploymentEvidence  `json:"deployment"`
	Traces               []performanceWorkTraceEvidence `json:"traces"`
}

func capturePerformanceWork(t *testing.T, parent context.Context, fleet performanceFleet, clock performanceClockReport, sampling performanceSamplingTransition) (performanceWorkTraceReport, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	report := performanceWorkTraceReport{BackendSHA: fleet.fixture.backend.deployment.digest, Deployment: fleet.fixture.backend.deployment.evidence(t)}
	if sampling.RootSampling != 1 || sampling.ReadyAt.IsZero() || len(clock.Phases) != 2 {
		return report, errors.New("work traces require fully sampled measured stages and both clock snapshots")
	}
	file, err := os.Open(filepath.Join(filepath.Dir(fleet.fixture.binary), "almanac-agent"))
	if err != nil {
		return report, errors.New("identify actual capacity agent executable failed")
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		return report, errors.New("hash actual capacity agent executable failed")
	}
	report.AgentSHA = hex.EncodeToString(hash.Sum(nil))
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return report, errors.New("resolve private work-trace artifacts failed")
	}
	report.Artifacts, err = os.MkdirTemp(filepath.Join(root, ".local"), "backend-performance-work-traces-")
	if err != nil {
		return report, errors.New("create private work-trace artifacts failed")
	}
	secrets := []string{fleet.fixture.password}
	for index := range fleet.agents {
		token, err := os.ReadFile(filepath.Join(fleet.fixture.dir, fmt.Sprintf("agent-%02d", index), "sli-agent-token"))
		if err != nil {
			return report, errors.New("read protected fixture material for trace sanitization failed")
		}
		secret := strings.TrimSpace(string(token))
		clear(token)
		if len(secret) < 16 {
			return report, errors.New("invalid fixture trace sanitization material")
		}
		secrets = append(secrets, secret)
	}
	var wants []performanceWorkTraceWant
	for _, phase := range clock.Phases {
		if len(phase.Cases) != 12 || !phase.GenerationsStable {
			return report, errors.New("publication traces lack representative independently verified generations")
		}
		for _, sample := range phase.Cases {
			key := sha256.Sum256([]byte(sample.Generation.GenerationId.String()))
			wants = append(wants, performanceWorkTraceWant{Kind: "publication", Key: hex.EncodeToString(key[:]), Source: sample.Generation.SourceGeneration, Through: sample.Generation.EvaluatedThrough.UTC().Format(time.RFC3339Nano), BackendSHA: report.BackendSHA, AgentSHA: report.AgentSHA, NotBefore: sampling.ReadyAt})
		}
	}
	for _, kind := range []string{"source", "ingestion"} {
		wants = append(wants, performanceWorkTraceWant{Kind: kind, BackendSHA: report.BackendSHA, AgentSHA: report.AgentSHA, NotBefore: sampling.ReadyAt})
	}
	report.Traces = make([]performanceWorkTraceEvidence, len(wants))
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	var workers sync.WaitGroup
	for actor := range 4 {
		workers.Go(func() {
			for index := actor; index < len(wants); index += 4 {
				report.Traces[index] = capturePerformanceWorkTrace(ctx, client, fleet.traceURL, report.Artifacts, index, wants[index], secrets)
			}
		})
	}
	workers.Wait()
	body, err := json.Marshal(report)
	if err != nil || os.WriteFile(filepath.Join(report.Artifacts, "work-traces.json"), append(body, '\n'), 0o600) != nil {
		return report, errors.New("persist work-trace capture report failed")
	}
	for _, evidence := range report.Traces {
		if !evidence.Connected || evidence.Failure != "" {
			return report, errors.New("VictoriaTraces lacks matched publication/source/agent-ingestion work; inspect retained work-traces.json")
		}
	}
	return report, nil
}

func capturePerformanceWorkTrace(parent context.Context, client *http.Client, origin, artifacts string, index int, want performanceWorkTraceWant, secrets []string) performanceWorkTraceEvidence {
	result := performanceWorkTraceEvidence{Kind: want.Kind, GenerationKey: want.Key, Source: want.Source, Through: want.Through, Artifact: fmt.Sprintf("trace-%02d-%s.json", index, want.Kind)}
	ctx, cancel := context.WithTimeout(parent, 40*time.Second)
	defer cancel()
	endpoint, err := url.Parse(origin)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.User != nil {
		return resultWithTraceFailure(result, "invalid_owned_trace_endpoint")
	}
	query := url.Values{"service": {"almanac-backend"}, "lookback": {"1h"}, "limit": {"4"}}
	tags := map[string]string{}
	switch want.Kind {
	case "publication":
		query.Set("operation", "derived.current.evaluate")
		tags["almanac.publication.key"], tags["almanac.result"] = want.Key, "published"
	case "source":
		query.Set("operation", "derived.source.batch")
		tags["almanac.result"] = "published"
	case "ingestion":
		query.Set("operation", "POST /api/agent/v1/observations")
		tags["http.response.status_code"] = "200"
	default:
		return resultWithTraceFailure(result, "invalid_work_kind")
	}
	tagBody, err := json.Marshal(tags)
	if err != nil {
		return resultWithTraceFailure(result, "invalid_work_tags")
	}
	query.Set("tags", string(tagBody))
	endpoint.Path, endpoint.RawQuery = "/select/jaeger/api/traces", query.Encode()
	started := time.Now()
	var lastSafe []byte
	for ctx.Err() == nil {
		lookup := traceLookup{ElapsedMS: time.Since(started).Milliseconds()}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return resultWithTraceFailure(result, "trace_request")
		}
		response, err := client.Do(request)
		if err == nil {
			lookup.Status = response.StatusCode
			body, readErr := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
			closeErr := response.Body.Close()
			if readErr == nil && closeErr == nil && len(body) <= 8<<20 && response.StatusCode == http.StatusOK {
				for _, secret := range secrets {
					if strings.Contains(string(body), secret) {
						return resultWithTraceFailure(result, "unsafe_trace_material")
					}
				}
				var document journeyTraceDocument
				if json.Unmarshal(body, &document) == nil {
					lookup.Decoded = true
					for _, data := range document.Data {
						lookup.Spans += len(data.Spans)
						for _, span := range data.Spans {
							if unsafeWorkTraceTags(span.Tags) {
								return resultWithTraceFailure(result, "unsafe_trace_metadata")
							}
						}
						for _, process := range data.Processes {
							if unsafeWorkTraceTags(process.Tags) {
								return resultWithTraceFailure(result, "unsafe_trace_metadata")
							}
						}
					}
					lastSafe = body
					result.TraceID, result.Duration, result.Connected = connectedPerformanceWork(document, want)
					lookup.Connected = result.Connected
				}
			}
		}
		result.Lookups = append(result.Lookups, lookup)
		if result.Connected {
			break
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if len(lastSafe) > 0 {
		if err := os.WriteFile(filepath.Join(artifacts, result.Artifact), lastSafe, 0o600); err != nil {
			return resultWithTraceFailure(result, "trace_artifact")
		}
	}
	if !result.Connected {
		return resultWithTraceFailure(result, "trace_incomplete")
	}
	return result
}

func resultWithTraceFailure(result performanceWorkTraceEvidence, failure string) performanceWorkTraceEvidence {
	result.Failure = failure
	result.Connected = false
	return result
}

func unsafeWorkTraceTags(tags []journeyTraceTag) bool {
	for _, tag := range tags {
		key := strings.ToLower(tag.Key)
		if strings.Contains(key, "password") || strings.Contains(key, "cookie") || strings.Contains(key, "authorization") || strings.Contains(key, "token") || key == "db.statement" || key == "db.query.text" || key == "db.connection_string" || key == "url.full" || key == "url.query" {
			return true
		}
	}
	return false
}
