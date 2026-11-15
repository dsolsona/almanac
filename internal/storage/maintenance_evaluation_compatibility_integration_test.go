//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceEvaluationCompatibleMasks(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	before, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	m := f.input("future")
	m.Start = f.at.Add(time.Hour)
	m.End = m.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	in.Requests[1].RequestedRevision = 2
	got, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || got.Masks[1].Computed.Revision != 1 || len(got.Masks[1].Proofs) != 1 || got.Masks[1].Proofs[0].Kind != "indexed_absence" || !reflect.DeepEqual(got.Masks[1].Windows, before.Masks[1].Windows) {
		t.Fatalf("future edit blocked unchanged window: %+v %v", got, err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || got.Masks[1].Computed.Revision != 2 || len(got.Masks[1].ReusedHours) != 1 || got.Masks[1].ReusedHours[0].Key.BuildID != h.BuildID || got.Masks[1].ReusedHours[0].ComputedRevision != 1 || !reflect.DeepEqual(got.Masks[1].Windows, before.Masks[1].Windows) {
		t.Fatalf("exact current build lost old scoped provenance: %+v %v", got, err)
	}
	// Historical and current windows share present source facts and one budget.
	in.Requests = append(in.Requests, in.Requests[1])
	in.Requests[2].RequestedRevision = 1
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || !reflect.DeepEqual(got.Masks[1].Windows, got.Masks[2].Windows) {
		t.Fatalf("same-facts historical mask: %+v %v", got, err)
	}
	in.Limits.MaxSourceHours = 3
	if _, err = evaluateFixtureMaintenance(t, f, in); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("separate masks escaped shared budget: %v", err)
	}
}
func TestIntegrationMaintenanceEvaluationCompatibilityPending(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes DISABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	m := f.input("legacy-future")
	m.Start = f.at.Add(time.Hour)
	m.End = m.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes ENABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests[0].RequestedRevision = 2
	got, err := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || !errors.Is(err, ErrMaintenanceCompilePending) || len(got.Preparation) == 0 || got.Preparation[0].Kind != "compatibility" {
		t.Fatalf("compatibility wait lost its durable preparation key: %+v %v", got, err)
	}
	for _, mask := range got.Masks {
		if len(mask.Windows) > 0 {
			t.Fatal("partial numeric batch")
		}
	}
	// Exact row+build readiness is independent of historical proof catch-up.
	h.BuildID = b.ID
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || got.Masks[0].Computed.Revision != 2 {
		t.Fatalf("exact ready snapshot waited for compatibility: %+v %v", got, err)
	}
}
func TestIntegrationMaintenanceEvaluationEqualUnion(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	m := f.input("nested")
	m.Start = h.HourStart.Add(12 * time.Minute)
	m.End = h.HourStart.Add(15 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests[0].RequestedRevision = 2
	pending, pendingErr := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(pendingErr, ErrDerivedPending) {
		t.Fatalf("changed provenance reused without proof: %v", pendingErr)
	}
	comparisons := 0
	for _, request := range pending.Preparation {
		if request.Kind == "comparison" {
			comparisons++
		}
	}
	if comparisons != 1 {
		t.Fatalf("unchanged union lacks one executable comparison request: %+v", pending.Preparation)
	}
	for _, request := range pending.Preparation {
		if request.Kind == "comparison" && (request.Comparison == nil || request.Comparison.FirstBuild != h.BuildID || request.Comparison.SecondBuild != b.ID) {
			t.Fatalf("wrong immutable comparison pair %+v", request)
		}
	}
	var c dbgen.AlmanacMaintenanceComparison
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = prepareMaintenanceComparison(t.Context(), tx, h.BuildID, b.ID, f.now)
		return err
	})
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = advanceMaintenanceComparison(t.Context(), tx, c.ID, 10, f.now)
		return err
	})
	if c.State != "equal" {
		t.Fatalf("fixture comparison %+v", c)
	}
	got, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || len(got.Masks[0].ReusedHours) != 1 || got.Masks[0].ReusedHours[0].Proof.Kind != "equal_union" || got.Masks[0].ReusedHours[0].Proof.ComparisonID != c.ID.String() {
		t.Fatalf("equal union unavailable: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceEvaluationDifferentComparison(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	m := f.input("different")
	m.Start = h.HourStart.Add(25 * time.Minute)
	m.End = h.HourStart.Add(26 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	var c dbgen.AlmanacMaintenanceComparison
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = prepareMaintenanceComparison(t.Context(), tx, h.BuildID, b.ID, f.now)
		return err
	})
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		c, err = advanceMaintenanceComparison(t.Context(), tx, c.ID, 10, f.now)
		return err
	})
	if c.State != "different" {
		t.Fatal(c.State)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests[0].RequestedRevision = 2
	got, err := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) != 1 || got.Preparation[0].Kind != "scoped_hour" {
		t.Fatalf("terminal comparison requeued or hour lost: %+v %v", got, err)
	}
}
