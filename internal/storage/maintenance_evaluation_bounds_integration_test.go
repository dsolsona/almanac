//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
)

func TestIntegrationMaintenanceEvaluationEdgeOverflow(t *testing.T) {
	t.Parallel()
	f, h, _ := hourGridFixture(t, 15*time.Second)
	at := ceilSliBoundary(h.HourStart.Add(30*time.Minute), 15*time.Second)
	// Corrupt fixture deliberately exceeds the admitted physical start count;
	// even failed rows count before any outcome/grid filtering.
	if _, err := execObservationFixture(t, f.conn, f.sliID, `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,error_code,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+n*interval '1 microsecond',sli_id,measurement_revision,'failed',NULL,NULL,result_digest,'{}','network',gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at FROM almanac_sli_observations CROSS JOIN generate_series(1,8) n WHERE sli_id=$1 AND interval_start=$2`, f.sliID, at); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests[0].Windows[0] = slo.WindowInput{At: at.Add(time.Minute), Now: f.now, Duration: time.Minute, Cadence: 15 * time.Second, Target: "0.99", ValidFrom: f.historyStart, RetentionStart: f.historyStart}
	if _, err := evaluateFixtureMaintenance(t, f, in); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("overflow was interpreted before unfiltered bound: %v", err)
	}
}
func TestIntegrationMaintenanceEvaluationMissingSourceAndRetainedSummary(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start>=$2 AND interval_start<$2::timestamptz+interval '1 hour'`, f.sliID, h.HourStart); err != nil {
		t.Fatal(err)
	}
	got, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || *got.Masks[0].Windows[0].Good != "49500.000000000000" {
		t.Fatalf("retained exact scoped population lost after raw removal: %+v %v", got, err)
	}
	clearMaintenanceHourFixture(t, f, h.HourStart)
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || got.Masks[0].Windows[0].State != "no_data" || got.Masks[0].Windows[0].Expected != 50 || got.Masks[0].Windows[0].Excluded != 10 {
		t.Fatalf("masked genuine absent hour: %+v %v", got, err)
	}
	// A different populated hour with no aggregate is pending, never absent.
	next := h.HourStart.Add(time.Hour)
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, next); err != nil {
		t.Fatal(err)
	}
	in.Requests[0].Windows[0].At = next.Add(time.Hour)
	got, err = evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) != 1 || got.Preparation[0].Kind != "source_hour" {
		t.Fatalf("unaggregated retained raw became no_data: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceEvaluationCanonicalPreparation(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2`, f.sliID, h.HourStart); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[:1]
	got, err := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) != 1 || got.Preparation[0].Kind != "source_window" || !got.Preparation[0].Start.Equal(h.HourStart) || !got.Preparation[0].End.Equal(h.HourStart.Add(time.Hour)) {
		t.Fatalf("canonical pending lost original work envelope: %+v %v", got, err)
	}
}
func TestIntegrationMaintenanceEvaluationUnavailableMask(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	m := f.input("unavailable-change")
	m.Start = h.HourStart.Add(25 * time.Minute)
	m.End = h.HourStart.Add(30 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_builds SET phase='unavailable' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests[0].RequestedRevision = 2
	got, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || len(got.Masks) != 1 || len(got.Masks[0].Unavailable) != 1 || got.Masks[0].Unavailable[0] != "maintenance_history_unavailable" || len(got.Masks[0].Windows) != 1 || !reflect.DeepEqual(got.Masks[0].Windows[0], slo.WindowResult{}) || len(got.Preparation) != 0 {
		t.Fatalf("terminal compiler gap lost its unavailable slot: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceEvaluationCombinedWindowCap(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	in := evaluationFixtureInput(f, h)
	window := in.Requests[0].Windows[0]
	in.Requests[0].Windows = make([]slo.WindowInput, derivedWindowBatchLimit)
	for i := range in.Requests[0].Windows {
		in.Requests[0].Windows[i] = window
	}
	if _, err := evaluateFixtureMaintenance(t, f, in); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("combined current/history work cap lost admission classification: %v", err)
	}
}
