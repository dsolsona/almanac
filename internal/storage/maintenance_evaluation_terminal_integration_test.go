//go:build integration

package storage

import (
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func terminalHourFixture(t *testing.T) (maintenanceFixture, maintenanceHourInput) {
	t.Helper()
	f, h := hourFixture(t)
	if _, err := execObservationFixture(t, f.conn, f.sliID, `INSERT INTO public.almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+interval '3 hours',sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at+interval '3 hours',recorded_at+interval '3 hours'
 FROM public.almanac_sli_observations WHERE sli_id=$1`, f.sliID); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(3 * time.Hour)
	f.at = f.at.Add(3 * time.Hour)
	h.Deadline = f.now.Add(time.Minute)
	f.drain(t, "six-hour terminal evidence fixture")
	return f, h
}

func terminalEvaluationInput(f maintenanceFixture, h maintenanceHourInput) maintenanceEvaluationInput {
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	w := in.Requests[0].Windows[0]
	w.At = h.HourStart.Add(6 * time.Hour)
	w.Now = w.At
	w.Duration = 5 * time.Minute
	long := w
	long.Duration = 6 * time.Hour
	in.Requests[0].Windows = []slo.WindowInput{w, long}
	return in
}
func terminalCount(value *string, want int64) bool {
	if value == nil {
		return false
	}
	got, ok := new(big.Rat).SetString(*value)
	return ok && got.Cmp(big.NewRat(want, 1)) == 0
}
func assertTerminalWindow(t *testing.T, result maintenanceEvaluationResult, err error, reason string) {
	t.Helper()
	if err != nil || len(result.Masks) != 1 {
		t.Fatalf("terminal metadata blocked usable windows: %+v %v", result, err)
	}
	m := result.Masks[0]
	if len(m.Windows) != 2 || len(m.Unavailable) != 2 || m.Unavailable[0] != "" || m.Unavailable[1] != reason || !terminalCount(m.Windows[0].Good, 4950) || !terminalCount(m.Windows[0].Total, 5000) || !reflect.DeepEqual(m.Windows[1], slo.WindowResult{}) || len(result.Preparation) != 0 {
		t.Fatalf("lost exact usable/terminal positions: %+v", result)
	}
}
func TestIntegrationMaintenanceEvaluationTerminalMask(t *testing.T) {
	t.Parallel()
	f, h := terminalHourFixture(t)
	m := f.input("missing-mask-history")
	m.Start = h.HourStart.Add(2 * time.Hour)
	m.End = m.Start.Add(10 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_builds SET phase='unavailable' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	in := terminalEvaluationInput(f, h)
	in.Requests[0].RequestedRevision = 2
	got, err := evaluateFixtureMaintenance(t, f, in)
	assertTerminalWindow(t, got, err, "maintenance_history_unavailable")
	if got.Masks[0].Computed.Revision != 1 || len(got.Masks[0].Proofs) != 2 || got.Masks[0].Proofs[0].Kind != "indexed_absence" {
		t.Fatal("usable older mask proof lost")
	}
}
func TestIntegrationMaintenanceEvaluationTerminalScopedHour(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"unavailable", "exhausted"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f, h := terminalHourFixture(t)
			preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "scoped_hour", RequestedRevision: 1, BuildID: h.BuildID, Hour: h.maintenanceHourKey}}, maintenancePreparationLive)
			if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state=$1,reason='history_unavailable'`, state); err != nil {
				t.Fatal(err)
			}
			in := terminalEvaluationInput(f, h)
			got, err := evaluateFixtureMaintenance(t, f, in)
			reason := "maintenance_history_unavailable"
			if state == "exhausted" {
				reason = "maintenance_preparation_failed"
			}
			assertTerminalWindow(t, got, err, reason)
			// A different hour remains recoverable. Its pending source must hold
			// the whole boundary even when the same long window is also terminal.
			if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, h.HourStart.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			got, err = evaluateFixtureMaintenance(t, f, in)
			if !errors.Is(err, ErrDerivedPending) || len(got.Masks[0].Windows) != 0 || len(got.Preparation) != 1 || got.Preparation[0].Kind != "source_hour" {
				t.Fatalf("terminal evidence swallowed pending source: %+v %v", got, err)
			}
			f.drain(t, "recover other pending hour")
			// A ready immutable artifact wins over an old terminal queue status.
			if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			got, err = evaluateFixtureMaintenance(t, f, in)
			if err != nil || got.Masks[0].Unavailable[1] != "" || got.Masks[0].Windows[1].Good == nil {
				t.Fatalf("ready artifact lost: %+v %v", got, err)
			}
			// Relevant new source facts change the exact key; the old terminal status is
			// not an authority for that new population.
			if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET good_count=989,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, h.HourStart); err != nil {
				t.Fatal(err)
			}
			f.drain(t, "new source after terminal job")
			got, err = evaluateFixtureMaintenance(t, f, in)
			if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) != 1 || got.Preparation[0].Hour.SourceGeneration <= h.SourceGeneration {
				t.Fatalf("old G poisoned new work: %+v %v", got, err)
			}
			for _, m := range got.Masks {
				if len(m.Windows) != 0 {
					t.Fatal("pending exposed partial numbers")
				}
			}
		})
	}
}

func TestIntegrationMaintenanceEvaluationTerminalMaskQueue(t *testing.T) {
	t.Parallel()
	f, h := terminalHourFixture(t)
	m := f.input("mask-queue-failure")
	m.Start = h.HourStart.Add(2 * time.Hour)
	m.End = m.Start.Add(10 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "mask", RequestedRevision: 2}}, maintenancePreparationLive)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='exhausted',reason='preparation_failed' WHERE kind='mask'`); err != nil {
		t.Fatal(err)
	}
	in := terminalEvaluationInput(f, h)
	in.Requests[0].RequestedRevision = 2
	got, err := evaluateFixtureMaintenance(t, f, in)
	assertTerminalWindow(t, got, err, "maintenance_preparation_failed")
	compilerFinish(t, f, b.ID, 10)
	in.Requests[0].Windows = in.Requests[0].Windows[:1]
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || got.Masks[0].Computed.BuildID != b.ID || got.Masks[0].Unavailable[0] != "" || !terminalCount(got.Masks[0].Windows[0].Good, 4950) {
		t.Fatalf("ready build lost to terminal queue: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceEvaluationTerminalHourPendingProof(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	m := f.input("nested-terminal-hour")
	m.Start = h.HourStart.Add(12 * time.Minute)
	m.End = m.Start.Add(3 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	old := h.BuildID
	h.BuildID = b.ID
	preparationEnqueue(t, f, []maintenanceEvaluationPreparation{{Kind: "scoped_hour", RequestedRevision: 2, BuildID: b.ID, Hour: h.maintenanceHourKey}}, maintenancePreparationLive)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='unavailable',reason='history_unavailable' WHERE kind='scoped_hour'`); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests[0].RequestedRevision = 2
	got, err := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || len(got.Masks[0].Windows) != 0 || len(got.Preparation) != 1 || got.Preparation[0].Kind != "comparison" {
		t.Fatalf("recoverable comparison lost behind terminal hour: %+v %v", got, err)
	}
	preparationEnqueue(t, f, got.Preparation, maintenancePreparationLive)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='exhausted',reason='preparation_failed' WHERE kind='comparison'`); err != nil {
		t.Fatal(err)
	}
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || len(got.Preparation) != 0 || got.Masks[0].Unavailable[0] != "maintenance_history_unavailable" || !reflect.DeepEqual(got.Masks[0].Windows[0], slo.WindowResult{}) {
		t.Fatalf("terminal alternatives remained pending: %+v %v", got, err)
	}
	var comparison dbgen.AlmanacMaintenanceComparison
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		comparison, err = prepareMaintenanceComparison(t.Context(), tx, old, b.ID, f.now)
		return err
	})
	compilerTx(t, f, func(tx pgx.Tx) error {
		var err error
		comparison, err = advanceMaintenanceComparison(t.Context(), tx, comparison.ID, 10, f.now)
		return err
	})
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || got.Masks[0].Unavailable[0] != "" || len(got.Masks[0].ReusedHours) != 1 || got.Masks[0].ReusedHours[0].Proof.ComparisonID != comparison.ID.String() {
		t.Fatalf("ready equal proof lost to terminal jobs: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceEvaluationTerminalReuseFallback(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"compatibility", "comparison"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, h := hourFixture(t)
			if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			if kind == "compatibility" {
				setLegacyMaintenanceAnalysis(t, f.conn, true)
			}
			m := f.input("reuse-failure")
			m.Start = h.HourStart.Add(12 * time.Minute)
			m.End = m.Start.Add(3 * time.Minute)
			if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
				t.Fatal(err)
			}
			if kind == "compatibility" {
				setLegacyMaintenanceAnalysis(t, f.conn, false)
			}
			b := compilerCapture(t, f, nil)
			compilerFinish(t, f, b.ID, 10)
			request := maintenanceEvaluationPreparation{Kind: kind, RequestedRevision: 2, BuildID: b.ID}
			if kind == "comparison" {
				request.Comparison = &maintenanceEvaluationBuildPair{FirstBuild: h.BuildID, SecondBuild: b.ID}
			}
			preparationEnqueue(t, f, []maintenanceEvaluationPreparation{request}, maintenancePreparationLive)
			if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='exhausted',reason='preparation_failed' WHERE kind=$1`, kind); err != nil {
				t.Fatal(err)
			}
			in := evaluationFixtureInput(f, h)
			in.Requests = in.Requests[1:]
			in.Requests[0].RequestedRevision = 2
			got, err := evaluateFixtureMaintenance(t, f, in)
			if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) != 1 || got.Preparation[0].Kind != "scoped_hour" {
				t.Fatalf("terminal reuse blocked exact preparation: %+v %v", got, err)
			}
			h.BuildID = b.ID
			if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			got, err = evaluateFixtureMaintenance(t, f, in)
			if err != nil || len(got.Preparation) != 0 || got.Masks[0].Unavailable[0] != "" || got.Masks[0].Windows[0].Good == nil {
				t.Fatalf("exact artifact waited on terminal reuse: %+v %v", got, err)
			}
		})
	}
}
