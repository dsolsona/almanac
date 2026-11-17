//go:build integration && alertacceptance

package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func acceptanceLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestIntegrationAlertAcceptanceSourcePause(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	dir := t.TempDir()
	pause := filepath.Join(dir, "pause-source")
	if err := os.WriteFile(pause, nil, 0600); err != nil {
		t.Fatal(err)
	}
	stop, err := f.runtime.startAlertAcceptance(t.Context(), acceptanceLogger(), AlertAcceptanceConfig{
		RetentionStart: f.historyStart, SourcePauseFile: pause,
	}, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	count := func() int {
		var n int
		if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_derived_dirty_ranges WHERE sli_id=$1`, f.sliID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	if before == 0 {
		t.Fatal("fixture has no pending source work")
	}
	_, next := f.runtime.runDerivedTurn(t.Context(), acceptanceLogger(), f.clock, f.now.Add(time.Hour))
	if got := count(); got != before || !next.Equal(f.now.Add(time.Second)) {
		t.Fatalf("paused source advanced: before=%d after=%d next=%v", before, got, next)
	}
	if err := os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	f.runtime.runDerivedTurn(t.Context(), acceptanceLogger(), f.clock, f.now.Add(time.Hour))
	if got := count(); got != 0 {
		t.Fatalf("resumed source did not publish original work: %d dirty ranges", got)
	}
}

func acceptanceWait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-deadline.C:
			t.Fatal("acceptance runner did not publish expected persisted outcome")
		case <-poll.C:
		}
	}
}
func TestIntegrationAlertAcceptanceLifecycle(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	cfg := AlertAcceptanceConfig{RetentionStart: f.historyStart, PauseFile: filepath.Join(t.TempDir(), "pause")}
	stop, err := f.runtime.StartAlertAcceptance(t.Context(), acceptanceLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	profile := f.runtime.maintenanceConsumers.Load()
	if profile == nil {
		t.Fatal("adoption profile not installed before return")
	}
	if _, err = f.runtime.StartAlertAcceptance(t.Context(), acceptanceLogger(), cfg); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("duplicate adapter replaced original owner: %v", err)
	}
	stop()
	if f.runtime.maintenanceConsumers.Load() != nil {
		t.Fatal("stopped adapter retained adoption profile")
	}
	stop()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = f.runtime.StartAlertAcceptance(cancelled, acceptanceLogger(), cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled adapter started: %v", err)
	}
	if _, err = f.runtime.StartAlertAcceptance(t.Context(), acceptanceLogger(), AlertAcceptanceConfig{}); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("missing retention authority accepted: %v", err)
	}
}

func TestIntegrationAlertAcceptancePausedPreparationAndOccurrence(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Acceptance live burn")
	f.drain(t, "acceptance canonical warmup")
	m := alertMaintenanceFixture(f)
	declaration := m.input("acceptance-mask")
	declaration.Start = f.at.Add(-4 * time.Minute)
	declaration.End = f.at.Add(-2 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, declaration); err != nil {
		t.Fatal(err)
	}
	pause := filepath.Join(t.TempDir(), "pause")
	if err := os.WriteFile(pause, []byte("pause evaluator only"), 0600); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	clock := func() time.Time { return f.at.Add(time.Since(began)) }
	stop, err := f.runtime.startAlertAcceptance(t.Context(), acceptanceLogger(), AlertAcceptanceConfig{RetentionStart: f.historyStart, PauseFile: pause}, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// No direct compiler/preparation calls: the deployed runner must make its
	// shared mask and scoped population ready while alert evaluation is paused.
	acceptanceWait(t, func() bool {
		var builds, hours int
		if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_builds WHERE phase='complete' AND target_revision=1),(SELECT count(*) FROM almanac_maintenance_preparation WHERE kind='scoped_hour' AND state='complete')`).Scan(&builds, &hours); err != nil {
			t.Fatal(err)
		}
		return builds > 0 && hours > 0
	})
	var evaluations, occurrences int
	if err = f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_evaluations),(SELECT count(*) FROM almanac_alert_occurrences)`).Scan(&evaluations, &occurrences); err != nil || evaluations != 0 || occurrences != 0 {
		t.Fatalf("paused evaluator committed: evaluations=%d occurrences=%d %v", evaluations, occurrences, err)
	}
	if err = os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	acceptanceWait(t, func() bool {
		var state string
		err := f.conn.QueryRow(t.Context(), `SELECT state FROM almanac_alert_occurrences WHERE policy_id=$1`, policy).Scan(&state)
		return err == nil && state == "firing"
	})
	stop()
	var raw []byte
	if err = f.conn.QueryRow(t.Context(), `SELECT evidence FROM almanac_alert_evaluations WHERE policy_id=$1 ORDER BY scheduled_at LIMIT 1`, policy).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Input      alert.Input
		Evaluation alert.Evaluation
	}
	if err = unmarshalAlertData(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Input.Snapshot.ExclusionRevision != 1 || saved.Evaluation.Truth != alert.Breaching || len(saved.Input.Windows) != 2 {
		t.Fatalf("runner did not retain scoped evidence: %+v", saved)
	}
	// Independent literal populations: 990/1000 per minute, with two excluded
	// minutes in each configured window. Threshold1 equals observed burn1.
	expected := map[time.Duration][2]string{5 * time.Minute: {"2970", "3000"}, time.Hour: {"57420", "58000"}}
	for _, w := range saved.Input.Windows {
		want, ok := expected[w.End.Sub(w.Start)]
		if !ok || w.Good != want[0] || w.Total != want[1] || w.Maintenance == nil || w.Maintenance.RequestedRevision != 1 {
			t.Fatalf("unexpected exact scoped population: %+v", w)
		}
	}
	if f.runtime.maintenanceConsumers.Load() != nil {
		t.Fatal("joined runner leaked adoption profile")
	}
}

func TestIntegrationAlertAcceptancePauseReadError(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Acceptance unreadable pause")
	f.drain(t, "acceptance pause error source")
	pause := filepath.Join(t.TempDir(), "pause")
	if err := os.Symlink(pause, pause); err != nil {
		t.Fatal(err)
	}
	if paused, err := alertAcceptancePaused(pause); !paused || err == nil {
		t.Fatalf("unreadable control did not fail closed: paused=%v err=%v", paused, err)
	}
	began := time.Now()
	clock := func() time.Time { return f.at.Add(time.Since(began)) }
	stop, err := f.runtime.startAlertAcceptance(t.Context(), acceptanceLogger(), AlertAcceptanceConfig{RetentionStart: f.historyStart, PauseFile: pause}, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// Let more than one real scheduler tick pass; filesystem failure must never
	// become an instruction to evaluate.
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	var n int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unreadable pause allowed evaluation: %d %v", n, err)
	}
	if err = os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	acceptanceWait(t, func() bool {
		var n int
		return f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations`).Scan(&n) == nil && n > 0
	})
}

func TestIntegrationAlertAcceptanceCancellationJoinsWorkers(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Acceptance cancellation")
	f.drain(t, "acceptance cancellation source")
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION hold_acceptance_eval() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(7304); RETURN NEW; END$$; CREATE TRIGGER hold_acceptance_eval BEFORE INSERT ON almanac_alert_evaluations FOR EACH ROW EXECUTE FUNCTION hold_acceptance_eval()`); err != nil {
		t.Fatal(err)
	}
	gate := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(gate)
	lock, err := gate.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(lock)
	if _, err = lock.Exec(t.Context(), `SELECT pg_advisory_xact_lock(7304)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	began := time.Now()
	clock := func() time.Time { return f.at.Add(time.Since(began)) }
	stop, err := f.runtime.startAlertAcceptance(ctx, acceptanceLogger(), AlertAcceptanceConfig{RetentionStart: f.historyStart}, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	waitForAgentQueryWait(t, f.conn, "%INSERT INTO public.almanac_alert_evaluations%")
	cancel()
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not join in-flight processors")
	}
	if f.runtime.maintenanceConsumers.Load() != nil {
		t.Fatal("parent cancellation retained profile after workers joined")
	}
	cursor, err := dbgen.New(f.conn).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(policy), Revision: 1})
	var count int
	if err != nil {
		t.Fatal(err)
	}
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations`).Scan(&count); err != nil || count != 0 || cursor.FailureAttempts != 0 {
		t.Fatalf("cancelled work committed or spent attempts: count=%d attempts=%d err=%v", count, cursor.FailureAttempts, err)
	}
}
