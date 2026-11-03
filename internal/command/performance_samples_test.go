package command

// Shared by the opt-in controlled workload and small, ordinary unit regressions.
// Keeping sampling outside the long integration entry makes its accounting
// testable without starting a database, executable, browser or agent.

import (
	"bytes"
	"context"
	"crypto/rand"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"sync"
	"time"
)

const performanceUsers = 10
const performanceReadsPerOperation = 100

// Capacity evidence records durations as nanoseconds, including nested reports.
func marshalPerformanceEvidence(value any) ([]byte, error) {
	return json.Marshal(value, jsonv1.FormatDurationAsNano(true))
}

// Retain fixed error categories, never PostgreSQL statement text or parameters.
func performancePostgresFailureCounts(logs []byte) map[string]int {
	counts := make(map[string]int)
	for _, message := range []string{"could not serialize access", "deadlock detected", "canceling statement due to statement timeout", "canceling statement due to lock timeout", "numeric field overflow", "violates check constraint", "violates foreign key constraint"} {
		counts[message] = bytes.Count(logs, []byte(message))
	}
	return counts
}

type performanceReader struct {
	// Identity is a fixture ID, never a login, cookie or credential.
	Identity        string
	Client          *http.Client
	Request         func(context.Context, string) (*http.Request, error)
	AuthenticatedAt time.Time
}

type performanceOperation struct {
	Name   string
	Paths  []string
	Budget time.Duration
	// Check must validate the actual result, including generation provenance
	// where applicable. A valid HTTP status alone is insufficient.
	Check func(int, []byte) error
}

type performancePreparationSample struct {
	Target      int           `json:"target"`
	Started     time.Time     `json:"started"`
	Elapsed     time.Duration `json:"elapsed_ns"`
	Pending     int           `json:"pending_responses"`
	Status      int           `json:"status"`
	ProblemCode string        `json:"problem_code,omitempty"`
	TraceID     string        `json:"trace_id"`
	Failure     string        `json:"failure,omitempty"`
	Body        []byte        `json:"-"`
}

func preparePerformanceTarget(ctx context.Context, reader performanceReader, operation performanceOperation, target int) performancePreparationSample {
	started := time.Now()
	sample := performancePreparationSample{Target: target, Started: started.UTC()}
	requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := reader.Request(requestContext, operation.Paths[target])
	if err != nil || request == nil {
		sample.Failure = "request_construction"
		sample.Elapsed = time.Since(started)
		return sample
	}
	var parent [24]byte
	if _, err := rand.Read(parent[:]); err != nil {
		sample.Failure = "trace_identity"
		sample.Elapsed = time.Since(started)
		return sample
	}
	sample.TraceID = fmt.Sprintf("%x", parent[:16])
	request.Header.Set("traceparent", fmt.Sprintf("00-%s-%x-01", sample.TraceID, parent[16:]))
	response, err := reader.Client.Do(request)
	if err != nil {
		sample.Failure = "transport"
		sample.Elapsed = time.Since(started)
		return sample
	}
	sample.Status = response.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (20<<20)+1))
	closeErr := response.Body.Close()
	sample.Elapsed = time.Since(started)
	switch {
	case readErr != nil || closeErr != nil || len(body) > 20<<20:
		sample.Failure = "response_read_or_size"
	case response.StatusCode != http.StatusOK:
		sample.Failure = "normal_navigation_unavailable"
		var problem struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(body, &problem) == nil && len(problem.Code) <= 80 {
			sample.ProblemCode = problem.Code
		}
		if response.StatusCode == http.StatusServiceUnavailable {
			sample.Pending = 1
		}
	case operation.Check(target, body) != nil:
		sample.Failure = "response_contract"
	case sample.Elapsed >= time.Second:
		sample.Failure = "first_request_navigation_budget"
	default:
		sample.Body = body
	}
	return sample
}

type performanceSample struct {
	First   bool          `json:"first_visit"`
	Actor   int           `json:"actor"`
	Target  int           `json:"target"`
	Started time.Time     `json:"started"`
	Elapsed time.Duration `json:"elapsed_ns"`
	Status  int           `json:"status"`
	Bytes   int           `json:"decoded_bytes"`
	Failure string        `json:"failure,omitempty"`
}

type performanceDistribution struct {
	Count int           `json:"count"`
	P50   time.Duration `json:"p50_ns"`
	P95   time.Duration `json:"p95_ns"`
	P99   time.Duration `json:"p99_ns"`
	Max   time.Duration `json:"max_ns"`
}

type performanceReadReport struct {
	Operation  string                  `json:"operation"`
	Started    time.Time               `json:"started"`
	Ended      time.Time               `json:"ended"`
	Wall       time.Duration           `json:"wall_ns"`
	Invocation time.Duration           `json:"invocation_ns"`
	Latency    performanceDistribution `json:"latency"`
	Failures   int                     `json:"failures"`
	Samples    []performanceSample     `json:"samples"`
}

// Nearest-rank percentiles include failures. The acceptance gate separately
// rejects any failed request; excluding them would hide overload/cancellation.
func performancePercentiles(values []time.Duration) performanceDistribution {
	ordered := slices.Sorted(slices.Values(values))
	if len(ordered) == 0 {
		return performanceDistribution{}
	}
	percentile := func(p float64) time.Duration {
		return ordered[int(math.Ceil(p*float64(len(ordered))))-1]
	}
	return performanceDistribution{Count: len(ordered), P50: percentile(.50), P95: percentile(.95), P99: percentile(.99), Max: ordered[len(ordered)-1]}
}

func samplePerformanceReads(ctx context.Context, readers []performanceReader, operation performanceOperation, first []performancePreparationSample) (performanceReadReport, error) {
	report := performanceReadReport{Operation: operation.Name}
	if len(readers) != performanceUsers || operation.Name == "" || len(operation.Paths) == 0 || operation.Budget <= 0 || operation.Check == nil {
		return report, errors.New("controlled reads require ten users, named targets, a budget and result validation")
	}
	identities := make(map[string]bool, performanceUsers)
	for _, reader := range readers {
		if reader.Identity == "" || identities[reader.Identity] || reader.Client == nil || reader.Request == nil {
			return report, errors.New("controlled readers must have distinct fixture identities and authenticated clients")
		}
		identities[reader.Identity] = true
	}
	report.Samples = make([]performanceSample, len(first)+performanceReadsPerOperation)
	for index, sample := range first {
		if sample.Failure != "" || sample.Status != http.StatusOK || sample.Pending != 0 || sample.Elapsed <= 0 || sample.Elapsed >= time.Second {
			return report, errors.New("first visit was unsuccessful or exceeded the navigation limit")
		}
		report.Samples[index] = performanceSample{First: true, Actor: sample.Target % performanceUsers, Target: sample.Target,
			Started: sample.Started, Elapsed: sample.Elapsed, Status: sample.Status, Bytes: len(sample.Body)}
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for actor, reader := range readers {
		workers.Go(func() {
			<-start
			for turn := range performanceReadsPerOperation / performanceUsers {
				turnIndex := turn*performanceUsers + actor
				index := len(first) + turnIndex
				target := turnIndex % len(operation.Paths)
				started := time.Now()
				sample := performanceSample{Actor: actor, Target: target, Started: started.UTC()}
				// Bound the whole exchange, including validation and response reads;
				// pending preparation is measured by a separate phase, never retried
				// inside this ready-read sample.
				requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
				request, err := reader.Request(requestContext, operation.Paths[target])
				if err != nil || request == nil {
					sample.Failure = "request_construction"
				} else {
					response, err := reader.Client.Do(request)
					if err != nil {
						sample.Failure = "transport"
					} else {
						sample.Status = response.StatusCode
						body, readErr := io.ReadAll(io.LimitReader(response.Body, (20<<20)+1))
						closeErr := response.Body.Close()
						sample.Bytes = len(body)
						switch {
						case readErr != nil || closeErr != nil:
							sample.Failure = "response_read"
						case len(body) > 20<<20:
							sample.Failure = "response_size"
						case response.StatusCode != http.StatusOK:
							sample.Failure = "http_status"
						case operation.Check(target, body) != nil:
							sample.Failure = "response_contract"
						}
					}
				}
				cancel()
				sample.Elapsed = time.Since(started)
				report.Samples[index] = sample
			}
		})
	}
	started := time.Now()
	report.Started = started.UTC()
	for _, sample := range first {
		if !sample.Started.IsZero() && sample.Started.Before(report.Started) {
			report.Started = sample.Started
		}
	}
	close(start)
	workers.Wait()
	report.Ended = time.Now().UTC()
	report.Wall = report.Ended.Sub(report.Started)
	durations := make([]time.Duration, len(report.Samples))
	for index, sample := range report.Samples {
		durations[index] = sample.Elapsed
		report.Invocation += sample.Elapsed
		if sample.Failure != "" {
			report.Failures++
		}
	}
	report.Latency = performancePercentiles(durations)
	if report.Failures != 0 {
		return report, fmt.Errorf("%s: %d/%d requests failed", operation.Name, report.Failures, len(report.Samples))
	}
	if report.Latency.P95 > operation.Budget {
		return report, fmt.Errorf("%s: p95 %s exceeds %s", operation.Name, report.Latency.P95, operation.Budget)
	}
	return report, nil
}
