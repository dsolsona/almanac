//go:build integration

package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationAlertMaintenancePendingQueueWait(t *testing.T) {
	for _, mode := range []string{"expiry", "retry", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, claim, prepared := alertMaintenancePendingFixture(t)
			preparationEnqueue(t, alertMaintenanceFixture(f), prepared.Preparation, maintenancePreparationLive)
			gate, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(gate)
			var id string
			if err := gate.QueryRow(t.Context(), `SELECT id FROM almanac_maintenance_preparation FOR UPDATE`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			var instant atomic.Int64
			instant.Store(f.at.UnixMicro())
			clock := func() time.Time { return time.UnixMicro(instant.Load()).UTC() }
			done := make(chan error, 1)
			go func() {
				done <- f.runtime.deferAlertMaintenanceWork(t.Context(), claim, prepared, maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}, clock, 10*time.Millisecond)
			}()
			observer := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(observer)
			waitForAgentQueryWait(t, observer, "%INSERT INTO public.almanac_maintenance_preparation%")
			switch mode {
			case "expiry":
				instant.Store(claim.Deadline.UnixMicro())
			case "retry":
				instant.Store(f.at.Add(20 * time.Millisecond).UnixMicro())
			case "terminal":
				if _, err := gate.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='exhausted',attempts=3,reason='preparation_failed' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			if err := gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if mode == "retry" && err != nil || mode != "retry" && !errors.Is(err, ErrAlertStale) {
				t.Fatalf("queue wait %s: %v", mode, err)
			}
			cursor, err := dbgen.New(f.conn).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(claim.PolicyID), Revision: claim.Revision})
			if err != nil || !cursor.PendingCutoff.Valid || !cursor.PendingCutoff.Time.Equal(f.at) || !cursor.NextScheduledAt.Time.Equal(f.at) || cursor.FailureAttempts != 0 {
				t.Fatalf("queue wait changed original boundary: %+v %v", cursor, err)
			}
			if mode == "retry" {
				if cursor.ClaimToken.Valid || !cursor.AvailableAt.Time.Equal(f.at.Add(30*time.Millisecond)) {
					t.Fatalf("retry used pre-wait clock: %+v", cursor)
				}
			} else if cursor.ClaimToken != claim.Token {
				t.Fatal("rejected enqueue released claim")
			}
		})
	}
}

func TestIntegrationAlertMaintenancePendingRebound(t *testing.T) {
	t.Parallel()
	f, claim, prepared := alertMaintenancePendingFixture(t)
	row, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{Proof: alertMaintenanceFixture(f).proof, Name: "replacement-alert-target", DisplayName: "Replacement", ServiceID: row.ServiceID.String(), SliID: f.sliID, Target: "0.99", RollingWindowSeconds: 3600, IdempotencyKey: "replacement", RequestID: "replacement", Now: f.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	target := created.ID
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "rebind-pending"), ID: claim.PolicyID, ExpectedVersion: 1, SLOID: &target, Reason: "move", Limits: f.limits}); err != nil {
		t.Fatal(err)
	}
	gate, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(gate)
	if _, err := gate.Exec(t.Context(), `SELECT id FROM almanac_slos WHERE id=$1 FOR UPDATE`, target); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = f.runtime.deferAlertMaintenanceWork(ctx, claim, prepared, maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}, f.clock, time.Second)
	if !errors.Is(err, ErrAlertStale) {
		t.Fatalf("stale parent attempted to lock a different resource: %v", err)
	}
}

func TestIntegrationAlertMaintenancePendingOptionalComparison(t *testing.T) {
	t.Parallel()
	f, claim, _ := alertMaintenancePendingFixture(t)
	m := alertMaintenanceFixture(f)
	zero := int64(0)
	genesis := compilerCapture(t, m, &zero)
	compilerFinish(t, m, genesis.ID, 2)
	current := compilerCapture(t, m, nil)
	compilerFinish(t, m, current.ID, 2)
	prepared, err := alertMaintenancePrepare(t, f, claim)
	if !errors.Is(err, ErrDerivedPending) || len(prepared.Preparation) != 1 || prepared.Preparation[0].Kind != "scoped_hour" {
		t.Fatalf("expected urgent exact hour: %+v %v", prepared.Preparation, err)
	}
	prepared.Preparation = append(prepared.Preparation, maintenanceEvaluationPreparation{Kind: "comparison", RequestedRevision: 1, Comparison: &maintenanceEvaluationBuildPair{FirstBuild: genesis.ID, SecondBuild: current.ID}})
	preparationEnqueue(t, m, prepared.Preparation, maintenancePreparationLive)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='exhausted',attempts=3,reason='preparation_failed' WHERE kind='comparison'`); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.deferAlertMaintenanceWork(t.Context(), claim, prepared, maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}, f.clock, time.Second); err != nil {
		t.Fatalf("optional comparison blocked exact preparation: %v", err)
	}
	var pending, exhausted int
	err = f.conn.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE kind='scoped_hour' AND state='pending'),count(*) FILTER(WHERE kind='comparison' AND state='exhausted') FROM almanac_maintenance_preparation`).Scan(&pending, &exhausted)
	if err != nil || pending != 1 || exhausted != 1 {
		t.Fatalf("lost urgent work or reset optional failure: %d %d %v", pending, exhausted, err)
	}
}
