//go:build integration

package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func preparationClaimOne(t *testing.T, f maintenanceFixture) dbgen.AlmanacMaintenancePreparation {
	t.Helper()
	var c dbgen.AlmanacMaintenancePreparation
	compilerTx(t, f, func(tx pgx.Tx) error {
		rows, err := claimMaintenancePreparation(t.Context(), tx, maintenancePreparationLive, f.now, time.Minute, 1, f.clock)
		if err == nil {
			if len(rows) != 1 {
				t.Fatalf("claims %d", len(rows))
			}
			c = rows[0]
		}
		return err
	})
	return c
}
func TestIntegrationMaintenancePreparationPostLockClock(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		name := "retry"
		if expiry {
			name = "expiry"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask"}}, maintenancePreparationLive)
			claim := preparationClaimOne(t, f)
			locker := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(locker)
			lock, err := locker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(lock)
			if _, err = dbgen.New(lock).LockMaintenancePreparation(t.Context(), claim.ID); err != nil {
				t.Fatal(err)
			}
			waiter := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(waiter)
			var nanos atomic.Int64
			nanos.Store(f.now.UnixNano())
			clock := func() time.Time { return time.Unix(0, nanos.Load()).UTC() }
			done := make(chan error, 1)
			go func() {
				tx, err := waiter.Begin(t.Context())
				if err != nil {
					done <- err
					return
				}
				err = finishMaintenancePreparation(t.Context(), tx, claim, "pending", "dependency_pending", time.Second, 3, clock)
				if err == nil {
					err = tx.Commit(t.Context())
				}
				rollbackTransaction(tx)
				done <- err
			}()
			waitForAgentQueryWait(t, f.conn, "%-- name: LockMaintenancePreparation %")
			elapsed := 30 * time.Second
			if expiry {
				elapsed = time.Minute
			}
			nanos.Store(f.now.Add(elapsed).UnixNano())
			if err = lock.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("ack did not finish")
			}
			if expiry {
				if !errors.Is(err, ErrMaintenancePreparationStale) {
					t.Fatalf("expired ack: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var available time.Time
			if err = f.conn.QueryRow(t.Context(), `SELECT available_at FROM almanac_maintenance_preparation WHERE id=$1`, claim.ID).Scan(&available); err != nil {
				t.Fatal(err)
			}
			if !available.Equal(f.now.Add(elapsed + time.Second)) {
				t.Fatalf("retry used pre-lock time: %v", available)
			}
		})
	}
}
func TestIntegrationMaintenancePreparationCompilerLeaseAndBackpressure(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask"}}, maintenancePreparationLive)
	var build dbgen.AlmanacMaintenanceBuild
	if err := f.conn.QueryRow(t.Context(), `SELECT build_id FROM almanac_maintenance_preparation`).Scan(&build.ID); err != nil {
		t.Fatal(err)
	}
	compilerClaim(t, f, build.ID)
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	got, err := runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock)
	if err != nil || got.Pending != 1 {
		t.Fatalf("resource lease conflict %+v %v", got, err)
	}
	// The next attempt actually waits on a PostgreSQL lock and hits the existing
	// 250ms ceiling; this is pressure, not a failed preparation attempt.
	locker := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(locker)
	tx, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err = dbgen.New(tx).LockMaintenanceBuild(t.Context(), build.ID); err != nil {
		t.Fatal(err)
	}
	now := f.now.Add(2 * time.Minute)
	got, err = runtime.runMaintenancePreparationTurn(t.Context(), now, preparationOptions(f), func() time.Time { return now })
	if err != nil || got.Pending != 1 {
		t.Fatalf("lock pressure %+v %v", got, err)
	}
	var attempts int
	var reason string
	if err = f.conn.QueryRow(t.Context(), `SELECT attempts,reason FROM almanac_maintenance_preparation`).Scan(&attempts, &reason); err != nil || attempts != 0 || reason != "database_backpressure" {
		t.Fatalf("pressure charged failure %d/%s %v", attempts, reason, err)
	}
}
func TestIntegrationMaintenancePreparationObsoleteSource(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "scoped_hour", RequestedRevision: 1, BuildID: h.BuildID, Hour: h.maintenanceHourKey}}, maintenancePreparationLive)
	if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET good_count=980,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, h.HourStart); err != nil {
		t.Fatal(err)
	}
	f.drain(t, "preparation source superseded")
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	got, err := runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock)
	if err != nil || got.Obsolete != 1 {
		t.Fatalf("stale exact G %+v %v", got, err)
	}
	var generation int64
	if err = f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_maintenance_preparation`).Scan(&generation); err != nil || generation != h.SourceGeneration {
		t.Fatalf("job retargeted %d %v", generation, err)
	}
}
func TestIntegrationMaintenancePreparationCancelledCompatibility(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "compatibility"}}, maintenancePreparationLive)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_builds SET phase='cancelled'`); err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	got, err := runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock)
	if err != nil || got.Unavailable != 1 || got.Pending != 0 {
		t.Fatalf("terminal cancelled input must not wait forever: %+v %v", got, err)
	}
}
func TestIntegrationMaintenancePreparationBoundsAndCancellation(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	_, err = prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), []maintenanceEvaluationPreparation{{Kind: "source_window", Start: f.historyStart.Truncate(time.Hour), End: f.historyStart.Truncate(time.Hour).Add(3 * time.Hour)}}, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 2}, f.now)
	if !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("source expansion bound %v", err)
	}
	_, err = prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), []maintenanceEvaluationPreparation{{Kind: "mask"}, {Kind: "invalid"}}, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
	if !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("invalid tail request was hidden by bounded admission: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = prepareMaintenanceEnqueue(ctx, tx, preparationScope(f), []maintenanceEvaluationPreparation{{Kind: "mask"}}, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled enqueue %v", err)
	}
	var n int
	if err = tx.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_builds`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("invalid/cancelled request leaked pins: %d %v", n, err)
	}
}

func TestIntegrationMaintenancePreparationMissingHistory(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	setLegacyMaintenanceAnalysis(t, f.conn, true)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("lost-history")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=1`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	setLegacyMaintenanceAnalysis(t, f.conn, false)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 1}, {Kind: "compatibility", RequestedRevision: 1}}, maintenancePreparationLive)
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	got, err := runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock)
	if err != nil || got.Unavailable != 2 {
		t.Fatalf("missing immutable history %+v %v", got, err)
	}
	var count int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation WHERE state='unavailable' AND attempts=0`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("history unavailable became retry/empty %d %v", count, err)
	}
}
func TestIntegrationMaintenancePreparationFailureBudget(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask"}}, maintenancePreparationLive)
	// Fail an actual compiler write, not a mock executor. Persist only a safe code.
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION preparation_failure_fixture() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'private fixture diagnostic'; END$$; CREATE TRIGGER preparation_failure_fixture BEFORE UPDATE ON almanac_maintenance_builds FOR EACH ROW EXECUTE FUNCTION preparation_failure_fixture()`); err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	options := preparationOptions(f)
	options.MaxAttempts = 2
	for i := range 2 {
		now := f.now.Add(time.Duration(i) * 3 * time.Second)
		got, err := runtime.runMaintenancePreparationTurn(t.Context(), now, options, func() time.Time { return now })
		if err == nil || got.Failed != 1 {
			t.Fatalf("actual error not recorded: %+v", got)
		}
	}
	var state, reason string
	var attempts int
	if err = f.conn.QueryRow(t.Context(), `SELECT state,reason,attempts FROM almanac_maintenance_preparation`).Scan(&state, &reason, &attempts); err != nil || state != "exhausted" || reason != "preparation_failed" || attempts != 2 {
		t.Fatalf("failure health %s/%s/%d %v", state, reason, attempts, err)
	}
	if _, err = f.conn.Exec(t.Context(), `DROP TRIGGER preparation_failure_fixture ON almanac_maintenance_builds`); err != nil {
		t.Fatal(err)
	}
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask"}}, maintenancePreparationLive)
	later := f.now.Add(time.Hour)
	got, err := runtime.runMaintenancePreparationTurn(t.Context(), later, options, func() time.Time { return later })
	if err != nil || got.Claimed != 0 {
		t.Fatalf("repeat reader silently reset exhausted work %+v %v", got, err)
	}
}

func TestIntegrationMaintenancePreparationResourceLocksBeforeQueue(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	_, err = prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), []maintenanceEvaluationPreparation{{Kind: "scoped_hour", RequestedRevision: 1, BuildID: h.BuildID, Hour: h.maintenanceHourKey}, {Kind: "source_window", Start: h.HourStart, End: h.HourStart.Add(time.Hour)}}, maintenancePreparationLimits{MaxRequests: 2, MaxSourceHours: 1}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	other := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(other)
	_, err = other.Exec(t.Context(), `SELECT id FROM almanac_maintenance_builds WHERE id=$1 FOR UPDATE NOWAIT`, h.BuildID)
	if compilerPGCode(err) != "55P03" {
		t.Fatalf("resource lock deferred until queue FK check: %v", err)
	}
	_, err = other.Exec(t.Context(), `SELECT generation FROM almanac_sli_source_generations WHERE sli_id=$1 AND measurement_revision=1 FOR UPDATE NOWAIT`, f.sliID)
	if compilerPGCode(err) != "55P03" {
		t.Fatalf("source authority lock deferred until dirty FK check: %v", err)
	}
}
