//go:build integration

package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceEvaluationSnapshotAndReadiness(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	conn := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(conn)
	tx, err := conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	before, err := evaluateMaintenanceWindows(t.Context(), tx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET good_count=980,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, h.HourStart); err != nil {
		t.Fatal(err)
	}
	same, err := evaluateMaintenanceWindows(t.Context(), tx, in)
	if err != nil || !reflect.DeepEqual(same, before) {
		t.Fatalf("RR mixed sources after write: %+v %v", same, err)
	}
	pending, err := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || len(pending.Preparation) != 1 || pending.Preparation[0].Kind != "source_hour" || pending.SourceGeneration <= before.SourceGeneration {
		t.Fatalf("dirty source %+v %v", pending, err)
	}
	// The final active lease remains pending; only expired final failure is terminal.
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_derived_dirty_ranges SET attempts=5,lease_until=$3 WHERE sli_id=$1 AND range_start=$2`, f.sliID, h.HourStart, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluateFixtureMaintenance(t, f, in); !errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed) {
		t.Fatalf("active final lease became failed: %v", err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_derived_dirty_ranges SET lease_until=$3 WHERE sli_id=$1 AND range_start=$2`, f.sliID, h.HourStart, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluateFixtureMaintenance(t, f, in); !errors.Is(err, ErrDerivedSourceFailed) {
		t.Fatalf("exhausted source hidden: %v", err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_derived_dirty_ranges SET attempts=0,lease_until=NULL,next_attempt_at=$3 WHERE sli_id=$1 AND range_start=$2`, f.sliID, h.HourStart, f.now); err != nil {
		t.Fatal(err)
	}
	f.drain(t, "evaluation relevant source")
	pending, err = evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || len(pending.Preparation) != 1 || pending.Preparation[0].Kind != "scoped_hour" || pending.Preparation[0].Hour.SourceGeneration <= h.SourceGeneration {
		t.Fatalf("old scoped generation accepted %+v %v", pending, err)
	}
	h.maintenanceHourKey = pending.Preparation[0].Hour
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	after, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || after.Masks[0].Windows[0].Good == nil || *after.Masks[0].Windows[0].Good != "49490.000000000000" {
		t.Fatalf("new source %+v %v", after, err)
	}
	same, err = evaluateMaintenanceWindows(t.Context(), tx, in)
	if err != nil || !reflect.DeepEqual(same, before) {
		t.Fatalf("RR mixed rebuilt hour %+v %v", same, err)
	}
}
func TestIntegrationMaintenanceEvaluationScopeAndCancellation(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	in := evaluationFixtureInput(f, h)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluateMaintenanceWindows(t.Context(), tx, in); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("RC accepted: %v", err)
	}
	rollbackTransaction(tx)
	tx, err = f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	bad := in
	bad.DefinitionRevision++
	if _, err := evaluateMaintenanceWindows(t.Context(), tx, bad); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("definition mismatch: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := evaluateMaintenanceWindows(ctx, tx, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
	in.Requests[1].RequestedRevision = 2
	if _, err := evaluateMaintenanceWindows(t.Context(), tx, in); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("future authority accepted: %v", err)
	}
}
