//go:build integration

package command

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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
		body, err := json.Marshal(report)
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
		body, err := json.Marshal(result)
		if err != nil {
			t.Fatal("encode controlled performance stage evidence failed")
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
	}{len(fleet.slis), len(fleet.objectives), len(fleet.agents), len(fleet.readers), fleet.setup}, nil)
	stage("environment")
	record("environment", performanceEnvironmentEvidence(t, fleet), nil)
	stage("seed-56-days")
	record("disk-admission-56-days", performanceFixtureDisk(t, ctx, performanceDiskReserve), nil)
	seeded := seedPerformanceHistory(t, fleet, 56)
	record("seed-56-days", seeded, nil)
	stage("automatic-maintenance-56-days")
	maintenance, err := awaitPerformanceHistory(t, ctx, fleet)
	if !record("automatic-maintenance-56-days", maintenance, err) {
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
	stage("blank-derived-rebuild-56-days")
	rebuild, err := restartPerformancePhase(t, fleet, true, time.Now().UTC().Truncate(time.Minute))
	if !record("blank-derived-rebuild-56-days", rebuild, err) {
		return
	}
	stage("ingestion-only-baseline")
	baseline, err := measurePerformanceLiveWindow(t, fleet, "ingestion-only-baseline", nil)
	if !record("ingestion-only-baseline", baseline, err) {
		return
	}
	stage("live-historical-saturation")
	saturation := newPerformanceSaturation(t, fleet)
	loaded, err := measurePerformanceLiveWindow(t, fleet, "live-historical-saturation", saturation.run)
	record("saturation-traffic-and-currentness", saturation.evidence(), nil)
	if !record("live-historical-saturation", loaded, err) {
		return
	}
	if err := checkPerformanceLiveIsolation(baseline, loaded); err != nil {
		record("live-isolation-gate", struct{}{}, err)
		return
	}
	record("live-isolation-gate", struct{ Result string }{"passed"}, nil)
	stage("production-browser")
	browserContext, stopBrowser := context.WithTimeout(ctx, 25*time.Minute)
	browserArtifacts, err := runPerformanceBrowser(t, browserContext, fleet)
	stopBrowser()
	if !record("production-browser", struct{ Artifacts string }{browserArtifacts}, err) {
		return
	}
	stage("seed-365-days")
	required, err := performanceDiskRequired(seeded.DatabaseBytes, seeded.CanonicalRows, 365*24*60*int64(size)-seeded.NominalSlots)
	if err != nil {
		t.Fatal(err)
	}
	record("disk-admission-365-days", performanceFixtureDisk(t, ctx, required), nil)
	record("seed-365-days", seedPerformanceHistory(t, fleet, 365), nil)
	stage("automatic-maintenance-365-days")
	maintenance, err = awaitPerformanceHistory(t, ctx, fleet)
	if !record("automatic-maintenance-365-days", maintenance, err) {
		return
	}
	stage("ready-365-days")
	retained, err := runPerformanceReadyPhase(ctx, fleet, "ready-365-days", time.Now().UTC().Truncate(time.Minute))
	if !record("ready-365-days", retained, err) {
		return
	}
	stage("query-plans-365-days")
	record("query-plans-365-days", performanceQueryPlans(t, fleet), nil)
	stage("persisted-restart-365-days")
	restart, err = restartPerformancePhase(t, fleet, false, time.Now().UTC().Truncate(time.Minute))
	if !record("persisted-restart-365-days", restart, err) {
		return
	}
	stage("retention-expiry")
	expiry, err := performanceRetentionExpiry(ctx, fleet)
	if !record("retention-expiry", expiry, err) {
		return
	}
	stage("completed")
	t.Logf("controlled performance evidence: %s", artifacts)
}
