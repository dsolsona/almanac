//go:build integration

package storage

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationMaintenanceConsumerUnadmitted(t *testing.T) {
	t.Parallel()
	f, _ := hourFixture(t)
	got, err := ReadCurrentSloStatus(t.Context(), f.f.databaseURL, CurrentSloInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], Now: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentState == nil || *got.CurrentState != "unavailable" || got.Current.Performance.Total != nil || got.Generation.ExclusionRevision != "1" {
		t.Fatalf("unadmitted nonzero authority exposed canonical success: %+v", got)
	}
}

func TestIntegrationMaintenanceConsumerAdmittedCurrent(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 10000, MaxFragments: 10000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}, MaxRetainedProofs: 960})
	got, err := ReadCurrentSloStatus(t.Context(), f.f.databaseURL, CurrentSloInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], Now: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentState == nil || *got.CurrentState != "ready" || got.Generation.ExclusionRevision != "1" || got.Burn6h.Performance.Total == nil || *got.Burn6h.Performance.Total != "170000.000000000000" || got.Burn6h.Quality.ExcludedIntervals == nil || *got.Burn6h.Quality.ExcludedIntervals != 10 || got.Burn6h.CalculationProvenance == nil || got.Burn6h.CalculationProvenance.ComputedRevision != "1" || got.Current.CalculationProvenance == nil || got.Current.Quality.ExcludedIntervals == nil || *got.Current.Quality.ExcludedIntervals != 0 {
		t.Fatalf("authenticated current failed shared adoption: %+v", got)
	}
}

func TestIntegrationMaintenanceConsumerReadQueuesPreparation(t *testing.T) {
	t.Parallel()
	f, _ := hourFixture(t)
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 10000, MaxFragments: 10000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}, MaxRetainedProofs: 960})
	got, err := ReadCurrentSloStatus(t.Context(), f.f.databaseURL, CurrentSloInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], Now: f.clock})
	if err != nil || got.CurrentState == nil || *got.CurrentState != "unavailable" {
		t.Fatalf("unprepared read: %+v %v", got, err)
	}
	var jobs int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_maintenance_preparation WHERE kind='scoped_hour' AND state='pending'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("authenticated read lost durable prerequisite: jobs=%d err=%v", jobs, err)
	}
	turn, err := f.runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparationOptions(f), f.clock)
	if err != nil || turn.Complete != 1 {
		t.Fatalf("queued consumer preparation did not complete: %+v %v", turn, err)
	}
	ready, err := ReadCurrentSloStatus(t.Context(), f.f.databaseURL, CurrentSloInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], Now: f.clock})
	if err != nil || ready.CurrentState == nil || *ready.CurrentState != "ready" || ready.Generation.ExclusionRevision != "1" || ready.Burn6h.Performance.Total == nil || *ready.Burn6h.Performance.Total != "170000.000000000000" {
		t.Fatalf("prepared consumer read did not recover exact values: %+v %v", ready, err)
	}
}

func TestIntegrationMaintenanceConsumerPreparationExpiry(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 10000, MaxFragments: 10000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}, MaxRetainedProofs: 960})
	locker := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(locker)
	lock, err := locker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(lock)
	if _, err := dbgen.New(lock).LockMaintenanceSlo(t.Context(), h.SloID); err != nil {
		t.Fatal(err)
	}
	var now atomic.Int64
	now.Store(f.now.UnixMicro())
	clock := func() time.Time { return time.UnixMicro(now.Load()).UTC() }
	done := make(chan error, 1)
	go func() {
		_, err := ReadCurrentSloStatus(t.Context(), f.f.databaseURL, CurrentSloInput{Runtime: f.runtime, Proof: f.proof, ID: f.ids[0], Now: clock})
		done <- err
	}()
	waitForAgentQueryWait(t, f.conn, "%-- name: LockMaintenanceSlo %")
	now.Store(f.now.Add(24 * time.Hour).UnixMicro())
	if err := lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("expired viewer preparation committed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expired viewer did not complete")
	}
	var jobs int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_maintenance_preparation`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("expired viewer leaked queued work: %d %v", jobs, err)
	}
}
