//go:build integration

package storage

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func compilerClaim(t *testing.T, f maintenanceFixture, id pgtype.UUID) maintenanceBuildClaim {
	t.Helper()
	var c maintenanceBuildClaim
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = claimMaintenanceBuild(t.Context(), tx, id, time.Minute, f.clock)
		return err
	})
	return c
}
func compilerPGCode(err error) string {
	if e, ok := errors.AsType[*pgconn.PgError](err); ok {
		return e.Code
	}
	return ""
}

func TestIntegrationMaintenanceCompilerPinsAndStaleCleanup(t *testing.T) {
	for _, scope := range []bool{false, true} {
		t.Run(fmt.Sprint(scope), func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			setLegacyMaintenanceAnalysis(t, f.conn, true)
			r, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("pinned"))
			if err != nil {
				t.Fatal(err)
			}
			setLegacyMaintenanceAnalysis(t, f.conn, false)
			old, err := pgx.Connect(t.Context(), f.f.databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close(t.Context())
			tx, err := old.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			var n int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_builds`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			b := compilerCapture(t, f, nil)
			query := `DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1`
			arg := any(f.ids[0])
			if scope {
				query = `DELETE FROM almanac_maintenance_exclusion_scopes WHERE exclusion_id=$1`
				arg = r.ID
			}
			if _, err := tx.Exec(t.Context(), query, arg); compilerPGCode(err) != "40001" {
				t.Fatalf("stale RR cleanup crossed capture: %v", err)
			}
			_ = tx.Rollback(t.Context())
			if _, err := f.conn.Exec(t.Context(), query, arg); compilerPGCode(err) != "23503" {
				t.Fatalf("current cleanup removed pinned input: %v", err)
			}
			// A lost lease has no effect on retention pins; the immutable historical
			// snapshot still finishes even though authority can advance independently.
			_ = compilerClaim(t, f, b.ID)
			f.now = f.now.Add(time.Minute)
			if _, err := f.conn.Exec(t.Context(), query, arg); compilerPGCode(err) != "23503" {
				t.Fatalf("lease loss released pin: %v", err)
			}
			compilerFinish(t, f, b.ID, 1)
			if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_coverage SET cumulative_us=cumulative_us+1 WHERE build_id=$1`, b.ID); compilerPGCode(err) != "23514" {
				t.Fatalf("completed coverage changed: %v", err)
			}
		})
	}
}

func TestIntegrationMaintenanceCompilerMissingHistory(t *testing.T) {
	for _, kind := range []string{"middle", "tail", "scope"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			setLegacyMaintenanceAnalysis(t, f.conn, true)
			for i := range 3 {
				if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input(fmt.Sprint(i))); err != nil {
					t.Fatal(err)
				}
			}
			setLegacyMaintenanceAnalysis(t, f.conn, false)
			query := `DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=2`
			if kind == "tail" {
				query = `DELETE FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=3`
			}
			if kind == "scope" {
				query = `DELETE FROM almanac_maintenance_exclusion_scopes WHERE slo_id=$1 AND exclusion_id=(SELECT exclusion_id FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=2)`
			}
			if _, err := f.conn.Exec(t.Context(), query, f.ids[0]); err != nil {
				t.Fatal(err)
			}
			b := compilerCapture(t, f, nil)
			var p maintenanceCompilerProgress
			for range 5 {
				p = compilerStep(t, f, b.ID, 1)
				if p.Phase == "unavailable" {
					break
				}
			}
			if p.Phase != "unavailable" {
				t.Fatal("missing history published")
			}
			if _, err := readMaintenanceCoverage(t.Context(), dbgen.New(f.conn), b.ID, f.at, f.at.Add(time.Hour)); !errors.Is(err, ErrMaintenanceCompileUnavailable) {
				t.Fatalf("missing history became empty coverage: %v", err)
			}
		})
	}
}

func TestIntegrationMaintenanceCompilerRollbackAndLease(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("one")); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	old := compilerClaim(t, f, b.ID)
	// Lost connection/rollback cannot commit page effects independently of cursor.
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanceMaintenanceBuild(t.Context(), tx, old, 1, f.clock); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_members WHERE build_id=$1`, b.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback left members %d: %v", n, err)
	}
	f.now = f.now.Add(time.Minute)
	fresh := compilerClaim(t, f, b.ID)
	tx, err = f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanceMaintenanceBuild(t.Context(), tx, old, 1, f.clock); !errors.Is(err, ErrMaintenanceCompileStale) {
		t.Fatalf("old owner wrote: %v", err)
	}
	_ = tx.Rollback(t.Context())
	// Advance may wait beyond its lease while the durable build row is locked.
	blocker, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(blocker)
	if _, err := dbgen.New(blocker).LockMaintenanceBuild(t.Context(), b.ID); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	var now atomic.Int64
	now.Store(f.now.UnixMicro())
	clock := func() time.Time { return time.UnixMicro(now.Load()).UTC() }
	done := make(chan error, 1)
	go func() {
		tx, e := conn.Begin(t.Context())
		if e != nil {
			done <- e
			return
		}
		_, e = advanceMaintenanceBuild(t.Context(), tx, fresh, 1, clock)
		if e == nil {
			e = tx.Commit(t.Context())
		}
		rollbackTransaction(tx)
		done <- e
	}()
	if err := waitForAgentAccessLockWait(t.Context(), t, f.f, "LockMaintenanceBuild"); err != nil {
		t.Fatal(err)
	}
	now.Store(fresh.Deadline.UnixMicro())
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrMaintenanceCompileStale) {
		t.Fatalf("expired waited owner committed: %v", err)
	}
	// A cancellation also leaves the original pin/cursor intact.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tx, err = f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanceMaintenanceBuild(ctx, tx, fresh, 1, clock); err == nil {
		t.Fatal("cancelled request succeeded")
	}
	_ = tx.Rollback(t.Context())
	f.now = fresh.Deadline
	compilerFinish(t, f, b.ID, 1)
}

func TestIntegrationMaintenanceCompilerCaptureSerializesMutation(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("initial")); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	b, err := captureMaintenanceBuild(t.Context(), tx, mustPGUUID(f.ids[0]), nil, f.now)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("concurrent")); done <- err }()
	if err := waitForAgentAccessLockWait(t.Context(), t, f.f, "LockMaintenanceSlo"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if b.TargetRevision != 1 {
		t.Fatalf("capture moved: %d", b.TargetRevision)
	}
	compilerFinish(t, f, b.ID, 1)
	var n int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_members WHERE build_id=$1`, b.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("postcapture mutation entered staged input: %d %v", n, err)
	}
}

func TestIntegrationMaintenanceCompilerFinalLeaseFence(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("initial")); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	c := compilerClaim(t, f, b.ID)
	calls := 0
	clock := func() time.Time {
		calls++
		if calls >= 2 {
			return c.Deadline
		}
		return f.now
	}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanceMaintenanceBuild(t.Context(), tx, c, 1, clock); !errors.Is(err, ErrMaintenanceCompileStale) {
		t.Fatalf("page write outlived lease: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := dbgen.New(f.conn).ReadMaintenanceBuild(t.Context(), b.ID)
	if err != nil || row.RawCursor != 0 || row.Phase != "replay" {
		t.Fatalf("expired page committed cursor: %+v %v", row, err)
	}
	var n int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_members WHERE build_id=$1`, b.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("expired page retained membership")
	}
	compilerTx(t, f, func(tx pgx.Tx) error { _, err := advanceMaintenanceBuild(t.Context(), tx, c, 1, f.clock); return err })
	compilerFinish(t, f, b.ID, 1)
}

func TestIntegrationMaintenanceCompilerInvalidClock(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	b := compilerCapture(t, f, nil)
	c := compilerClaim(t, f, b.ID)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := advanceMaintenanceBuild(t.Context(), tx, c, 1, func() time.Time { return time.Time{} }); !errors.Is(err, ErrMaintenanceCompileStale) {
		t.Fatalf("invalid clock satisfied lease fence: %v", err)
	}
}
