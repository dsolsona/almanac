//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationMaintenancePreparationCompatibilityLockOrder(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("first")); err != nil {
		t.Fatal(err)
	}
	first := int64(1)
	compilerCapture(t, f, &first)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("second")); err != nil {
		t.Fatal(err)
	}
	second := compilerCapture(t, f, nil)
	workerConn := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(workerConn)
	enqueueConn := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(enqueueConn)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	worker, err := workerConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(worker)
	if _, err = dbgen.New(worker).LockMaintenanceBuild(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		tx, err := enqueueConn.Begin(ctx)
		if err == nil {
			p, e := prepareMaintenanceEnqueue(ctx, tx, preparationScope(f), []maintenanceEvaluationPreparation{{Kind: "compatibility", RequestedRevision: 1}, {Kind: "mask", RequestedRevision: 2}}, maintenancePreparationLimits{MaxRequests: 2, MaxSourceHours: 1}, f.now)
			err = e
			if err == nil {
				err = enqueueMaintenancePreparation(ctx, tx, p, maintenancePreparationLive, f.now)
			}
			if err == nil {
				err = tx.Commit(ctx)
			}
			rollbackTransaction(tx)
		}
		done <- err
	}()
	// Enqueue is waiting on worker-owned build R2. It must not own the shared
	// compatibility authority yet: this real worker takes build -> authority.
	waitForAgentQueryWait(t, f.conn, "%-- name: CaptureMaintenanceBuild %")
	progress, workerErr := advanceMaintenanceCompatibility(ctx, worker, second.ID, 1)
	if workerErr == nil {
		workerErr = worker.Commit(ctx)
	}
	rollbackTransaction(worker)
	enqueueErr := <-done
	if workerErr != nil || enqueueErr != nil {
		t.Fatalf("mixed-revision enqueue/worker lock cycle: worker=%s enqueue=%s", compilerPGCode(workerErr), compilerPGCode(enqueueErr))
	}
	if progress.State != "complete" {
		t.Fatalf("worker outcome %+v", progress)
	}
	var jobs int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_preparation`).Scan(&jobs); err != nil || jobs != 2 {
		t.Fatalf("atomic enqueued jobs %d %v", jobs, err)
	}
}

func TestIntegrationMaintenancePreparationExecutionCancellation(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask"}}, maintenancePreparationLive)
	claim := preparationClaimOne(t, f)
	locker := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(locker)
	tx, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err = dbgen.New(tx).LockMaintenanceBuild(t.Context(), claim.BuildID); err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runtime.processMaintenancePreparation(ctx, claim, preparationOptions(f), f.clock)
		done <- err
	}()
	waitForAgentQueryWait(t, f.conn, "%-- name: LockMaintenanceBuild %")
	cancel()
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("resource execution ignored cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resource error %v", err)
	}
	var attempts int
	var tokenMatches bool
	if err = f.conn.QueryRow(t.Context(), `SELECT attempts,lease_token=$2 FROM almanac_maintenance_preparation WHERE id=$1`, claim.ID, claim.LeaseToken).Scan(&attempts, &tokenMatches); err != nil || attempts != 0 || !tokenMatches {
		t.Fatalf("cancellation spent attempts or lost recoverable claim: %d/%v %v", attempts, tokenMatches, err)
	}
	rollbackTransaction(tx)
	later := f.now.Add(2 * time.Minute)
	got, err := runtime.runMaintenancePreparationTurn(t.Context(), later, preparationOptions(f), func() time.Time { return later })
	if err != nil || got.Claimed != 1 || got.Pending != 1 {
		t.Fatalf("cancelled claim did not resume bounded compiler progress %+v %v", got, err)
	}
}
