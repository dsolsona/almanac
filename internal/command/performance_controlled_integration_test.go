//go:build integration

package command

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/testpostgres"
)

type performanceControlledReport struct {
	SchemaVersion int                       `json:"schema_version"`
	Started       time.Time                 `json:"started"`
	Ended         time.Time                 `json:"ended"`
	Result        string                    `json:"result"`
	ActiveStage   string                    `json:"active_stage"`
	Stages        map[string]jsontext.Value `json:"stages"`
	Failures      []string                  `json:"failures"`
}

// Called only by the performance-tagged entry. Ordinary integration discovery
// compiles these helpers but cannot accidentally launch this multi-hour fixture.
func runControlledBackendPerformance(t *testing.T) {
	t.Helper()
	if deadline, bounded := t.Deadline(); !bounded || time.Until(deadline) < 8*time.Hour+15*time.Minute {
		t.Fatal("controlled workload requires a bounded runner with at least eight hours fifteen minutes for execution and cleanup")
	}
	signals, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signals, 8*time.Hour)
	defer cancel()
	for _, size := range []int{10, 100, 1000} {
		if !t.Run(fmt.Sprintf("independent-%d", size), func(t *testing.T) {
			runControlledBackendPerformanceFleet(t, ctx, size)
		}) {
			return
		}
	}
}

func runControlledBackendPerformanceFleet(t *testing.T, ctx context.Context, size int) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("resolve controlled performance workspace failed")
	}
	artifacts, err := os.MkdirTemp(filepath.Join(root, ".local"), "backend-performance-controlled-")
	if err != nil {
		t.Fatal("create controlled performance evidence directory failed")
	}
	report := performanceControlledReport{SchemaVersion: 1, Started: time.Now().UTC(), Result: "running", Stages: make(map[string]jsontext.Value)}
	write := func() {
		t.Helper()
		body, err := marshalPerformanceEvidence(report)
		if err != nil || os.WriteFile(filepath.Join(artifacts, "performance.json"), append(body, '\n'), 0o600) != nil {
			t.Error("persist controlled performance evidence failed")
		}
	}
	t.Cleanup(func() {
		report.Ended = time.Now().UTC()
		if t.Failed() || len(report.Failures) != 0 {
			report.Result = "failed"
		} else {
			report.Result = "passed"
		}
		write()
	})
	stage := func(name string) {
		if ctx.Err() != nil {
			t.Fatal("controlled performance workload cancelled or exhausted its eight-hour budget")
		}
		report.ActiveStage = name
		write()
		t.Logf("controlled performance stage=%s", name)
	}
	record := func(name string, result any, resultErr error) bool {
		t.Helper()
		body, err := marshalPerformanceEvidence(result)
		if err != nil {
			t.Fatalf("encode controlled performance stage evidence %T failed: %v", result, err)
		}
		report.Stages[name] = jsontext.Value(body)
		if resultErr != nil {
			report.Failures = append(report.Failures, name+": "+resultErr.Error())
			t.Errorf("controlled performance stage %s failed: %v", name, resultErr)
		}
		write()
		return resultErr == nil
	}
	stage("fleet-setup")
	fleet := newPerformanceFleet(t, ctx, size)
	record("fleet-setup", struct {
		SLIs, SLOs, Agents, Users int
		Setup                     time.Duration
		ReservedDetail            string `json:"reserved_detail_slo_id"`
	}{len(fleet.slis), len(fleet.objectives), len(fleet.agents), len(fleet.readers), fleet.setup, fleet.objectives[len(fleet.objectives)-1]}, nil)
	stage("environment")
	record("environment", performanceEnvironmentEvidence(t, fleet), nil)
	stage("seed-56-days")
	record("disk-admission-56-days", performanceFixtureDisk(t, ctx, fleet, performanceDiskReserve), nil)
	seeded := seedPerformanceHistory(t, fleet, 56)
	record("seed-56-days", seeded, nil)
	stage("automatic-maintenance-56-days")
	maintenance, err := awaitPerformanceHistory(t, ctx, fleet)
	if !record("automatic-maintenance-56-days", maintenance, err) {
		return
	}
	storageObserver := testpostgres.Connect(t, fleet.fixture.databaseURL)
	var maintainedDatabaseBytes int64
	err = storageObserver.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&maintainedDatabaseBytes)
	closePerformanceObserver(storageObserver)
	if !record("maintained-storage-56-days", struct {
		DatabaseBytes int64 `json:"database_bytes"`
	}{maintainedDatabaseBytes}, err) {
		return
	}
	stage("ready-56-days")
	ready, err := runPerformanceReadyPhase(ctx, fleet, "ready-56-days", time.Now().UTC().Truncate(time.Minute))
	if !record("ready-56-days", ready, err) {
		return
	}
	stage("query-plans-56-days")
	record("query-plans-56-days", performanceQueryPlans(t, fleet), nil)
	stage("persisted-restart-56-days")
	restart, err := restartPerformancePhase(t, fleet, false, time.Now().UTC().Truncate(time.Minute))
	if !record("persisted-restart-56-days", restart, err) {
		return
	}
	stage("source-and-response-cache-rebuild-56-days")
	rebuild, err := restartPerformancePhase(t, fleet, true, time.Now().UTC().Truncate(time.Minute))
	if !record("source-and-response-cache-rebuild-56-days", rebuild, err) {
		return
	}
	stage("seed-365-days")
	required, err := performanceDiskRequired(maintainedDatabaseBytes, seeded.CanonicalRows, 365*24*60*int64(size)-seeded.NominalSlots)
	if err != nil {
		t.Fatal(err)
	}
	record("disk-admission-365-days", performanceFixtureDisk(t, ctx, fleet, required), nil)
	record("seed-365-days", seedPerformanceHistory(t, fleet, 365), nil)
	stage("automatic-maintenance-365-days")
	maintenance, err = awaitPerformanceHistory(t, ctx, fleet)
	if !record("automatic-maintenance-365-days", maintenance, err) {
		return
	}
	stage("imported-prefix-source-watermarks")
	observer := testpostgres.Connect(t, fleet.fixture.databaseURL)
	fleet.seedSources, err = performanceSourceVersions(ctx, observer, fleet.slis)
	closePerformanceObserver(observer)
	if !record("imported-prefix-source-watermarks", fleet.seedSources, err) {
		return
	}
	stage("full-acceptance-tracing")
	sampling := enablePerformanceFullTracing(t, fleet.fixture)
	record("full-acceptance-tracing", sampling, nil)
	stage("query-plans-365-days")
	record("query-plans-365-days", performanceQueryPlans(t, fleet), nil)
	stage("persisted-restart-365-days")
	restart, err = restartPerformancePhase(t, fleet, false, time.Now().UTC().Truncate(time.Minute))
	if !record("persisted-restart-365-days", restart, err) {
		return
	}
	stage("ingestion-only-baseline-365-days")
	baseline, err := measurePerformanceLiveWindow(t, fleet, "ingestion-only-baseline-365-days", nil)
	if !record("ingestion-only-baseline-365-days", baseline, err) {
		return
	}
	stage("live-historical-saturation-365-days")
	saturation := newPerformanceSaturation(t, fleet)
	loaded, err := measurePerformanceLiveWindow(t, fleet, "live-historical-saturation-365-days", saturation.run)
	record("saturation-traffic-and-currentness-365-days", saturation.evidence(), nil)
	if !record("live-historical-saturation-365-days", loaded, err) {
		return
	}
	if err := checkPerformanceLiveIsolation(baseline, loaded); err != nil {
		record("live-isolation-gate", struct{}{}, err)
		return
	}
	record("live-isolation-gate", struct{ Result string }{"passed"}, nil)
	stage("independent-concurrent-current-365-days")
	current, err := performanceConcurrentCurrent(t, ctx, fleet)
	if !record("independent-concurrent-current-365-days", current, err) {
		return
	}
	browserFailed := false
	for _, latency := range []int{0, 100} {
		label := fmt.Sprintf("production-browser-365-days-%dms", latency)
		stage(label + "-response-eviction")
		browserRestart := restartPerformanceForBrowser(t, fleet, latency == 0)
		record(label+"-response-eviction", browserRestart, nil)
		stage(label)
		browserContext, stopBrowser := context.WithTimeout(ctx, 25*time.Minute)
		browserArtifacts, browserErr := runPerformanceBrowser(t, browserContext, fleet, browserRestart, latency)
		stopBrowser()
		if !record(label, struct{ Artifacts string }{browserArtifacts}, browserErr) {
			browserFailed = true
		}
	}
	if browserFailed {
		return
	}
	fleet.reserveDetail = false
	stage("independent-accuracy-365-days")
	accuracy, err := runPerformanceAccuracy(ctx, fleet)
	if !record("independent-accuracy-365-days", accuracy, err) {
		return
	}
	stage("ready-365-days")
	retained, err := runPerformanceReadyPhase(ctx, fleet, "ready-365-days", time.Now().UTC().Truncate(time.Minute))
	if !record("ready-365-days", retained, err) {
		return
	}
	stage("late-correction-and-retained-restart")
	late, err := performanceLateCorrection(t, ctx, fleet)
	if !record("late-correction-and-retained-restart", late, err) {
		return
	}
	stage("retention-expiry")
	expiry, err := performanceRetentionExpiry(ctx, fleet)
	if !record("retention-expiry", expiry, err) {
		return
	}
	stage("populated-retention-cleanup")
	cleanup, err := performanceRetentionCleanup(t, ctx, fleet)
	if !record("populated-retention-cleanup", cleanup, err) {
		return
	}
	stage("supplemental-shared-source")
	shared, err := performanceSharedSource(t, ctx, fleet)
	if !record("supplemental-shared-source", shared, err) {
		return
	}
	stage("clock-only-current-and-global-urgency")
	clock, err := performanceClockOnly(t, ctx, fleet, shared, late)
	if !record("clock-only-current-and-global-urgency", clock, err) {
		return
	}
	stage("matched-background-work-traces")
	workTraces, err := capturePerformanceWork(t, ctx, fleet, clock, sampling)
	if !record("matched-background-work-traces", workTraces, err) {
		return
	}
	stage("completed")
	t.Logf("controlled performance evidence: %s", artifacts)
}
