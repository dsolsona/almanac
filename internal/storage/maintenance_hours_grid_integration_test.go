//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func hourGridFixture(t *testing.T, cadence time.Duration) (maintenanceFixture, maintenanceHourInput, []slo.ExclusionInterval) {
	t.Helper()
	s := newSelfHealFixtureCadence(t, "hour-grid", true, cadence)
	id := s.createSLO(t, "hour-grid-objective")
	var verifier [32]byte
	verifier[0] = 98
	f := maintenanceFixture{selfHealFixture: s, ids: []string{id}, proof: DatasourceProof{SessionVerifier: verifier}}
	h := s.historyStart.Add(time.Hour).Truncate(time.Hour)
	m := f.input("grid-mask")
	m.Start = ceilSliBoundary(h.Add(10*time.Minute), cadence)
	m.End = ceilSliBoundary(h.Add(20*time.Minute), cadence)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	f.drain(t, "grid source")
	var generation int64
	if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, h).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	in := maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SliID: mustPGUUID(f.sliID), SloID: mustPGUUID(id), BuildID: b.ID, DefinitionRevision: 1, MeasurementRevision: 1, SourceGeneration: generation, HourStart: h}, RetentionStart: h.Add(-time.Hour), Deadline: f.now.Add(time.Minute)}
	return f, in, []slo.ExclusionInterval{{Start: m.Start, End: m.End}}
}
func TestIntegrationMaintenanceHourGridAndBound(t *testing.T) {
	t.Parallel()
	for _, cadence := range []time.Duration{70 * time.Second, 15 * time.Second} {
		t.Run(cadence.String(), func(t *testing.T) {
			t.Parallel()
			f, in, mask := hourGridFixture(t, cadence)
			got, err := hourPrepare(t, f, in)
			if err != nil {
				t.Fatal(err)
			}
			start, end := ceilSliBoundary(in.HourStart, cadence), ceilSliBoundary(in.HourStart.Add(time.Hour), cadence)
			var facts []slo.Observation
			for at := start; at.Before(in.HourStart.Add(time.Hour)); at = at.Add(cadence) {
				facts = append(facts, slo.Observation{Start: at, End: at.Add(cadence), Received: at.Add(cadence), Outcome: "observed", Good: "990", Total: "1000"})
			}
			input := slo.WindowInput{At: end, Now: f.now, Duration: end.Sub(start), Cadence: cadence, Target: "0.99", ValidFrom: start, RetentionStart: start, Observations: facts, Exclusions: mask}
			want, err := slo.EvaluateOperationalWindowContext(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			input.Observations = nil
			actual, err := slo.EvaluateOperationalTotals(t.Context(), input, hourTotals(got))
			if err != nil || !reflect.DeepEqual(actual, want) {
				t.Fatalf("%s start-hour oracle differs: %+v want %+v error %v", cadence, actual, want, err)
			}
			if cadence == 70*time.Second && !got.Eligible.LastObservation.After(in.HourStart.Add(time.Hour)) {
				t.Fatal("fixture did not exercise crossing end")
			}
			if cadence == 15*time.Second && got.Eligible.Observed+got.Excluded.Observed != 240 {
				t.Fatal("maximum currently configurable physical-hour population missing")
			}
			if cadence != 15*time.Second {
				return
			}
			// Deliberately corrupt the physical grid while keeping source summary and
			// dirty publication coherent: bounded read must stop at its overflow sentinel.
			if _, err := execObservationFixture(t, f.conn, f.sliID, `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+extra*interval '1 microsecond',sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at FROM almanac_sli_observations CROSS JOIN generate_series(1,3600) extra WHERE sli_id=$1 AND interval_start=$2`, f.sliID, in.HourStart); err != nil {
				t.Fatal(err)
			}
			f.drain(t, "overflow source")
			if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, in.HourStart).Scan(&in.SourceGeneration); err != nil {
				t.Fatal(err)
			}
			if _, err := hourPrepare(t, f, in); !errors.Is(err, ErrOperationalResource) {
				t.Fatalf("overflow population accepted: %v", err)
			}
			rows, err := dbgen.New(f.conn).ReadMaintenanceHourRaw(t.Context(), dbgen.ReadMaintenanceHourRawParams{SliID: in.SliID, MeasurementRevision: in.MeasurementRevision, HourStart: timestamp(in.HourStart)})
			if err != nil || len(rows) != 3601 {
				t.Fatalf("bounded sentinel read: %d %v", len(rows), err)
			}
		})
	}
}
