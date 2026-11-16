//go:build integration

package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func waitProcessingResult(t *testing.T, ready func() bool) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-timer.C:
			t.Fatal("ordinary processor did not publish its result")
		case <-ticker.C:
		}
	}
}

func TestIntegrationOrdinaryProcessingLifecycle(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Ordinary runtime burn")
	f.drain(t, "ordinary runtime source")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stop, err := f.runtime.startProcessing(t.Context(), logger, true, processingControls{}, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if f.runtime.maintenanceConsumers.Load() == nil {
		t.Fatal("shared calculation profile was not installed")
	}
	if _, err := f.runtime.StartProcessing(t.Context(), logger, true); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("duplicate runtime replaced its owner: %v", err)
	}
	waitProcessingResult(t, func() bool {
		var state string
		return f.conn.QueryRow(t.Context(), `SELECT state FROM almanac_alert_occurrences WHERE policy_id=$1`, policy).Scan(&state) == nil && state == "firing"
	})
	stop()
	stop()
	if f.runtime.maintenanceConsumers.Load() != nil {
		t.Fatal("joined shutdown retained the calculation owner")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.runtime.StartProcessing(ctx, logger, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled runtime started: %v", err)
	}
}

func TestIntegrationOrdinaryProcessingWithoutAlerts(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Disabled evaluator")
	f.drain(t, "ordinary calculation source")
	m := alertMaintenanceFixture(f)
	declaration := m.input("ordinary-runtime-mask")
	declaration.Start, declaration.End = f.at.Add(-4*time.Minute), f.at.Add(-2*time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, declaration); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	clock := func() time.Time { return f.at.Add(time.Since(began)) }
	stop, err := f.runtime.startProcessing(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), false, processingControls{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	waitProcessingResult(t, func() bool {
		var count int
		return f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_builds WHERE phase='complete' AND target_revision=1`).Scan(&count) == nil && count > 0
	})
	var count int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1`, policy).Scan(&count); err != nil || count != 0 {
		t.Fatalf("backend without alerts evaluated a policy: %d %v", count, err)
	}
}
