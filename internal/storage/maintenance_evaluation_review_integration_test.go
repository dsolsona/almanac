//go:build integration

package storage

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationMaintenanceEvaluationPreferredGenesis(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	genesis := compilerCapture(t, f, nil)
	compilerFinish(t, f, genesis.ID, 10)
	f.drain(t, "preferred genesis source")
	m := f.input("future-genesis")
	m.Start = f.at.Add(time.Hour)
	m.End = m.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	w := slo.WindowInput{At: f.historyStart.Add(time.Hour), Now: f.now, Duration: time.Hour, Cadence: time.Minute, Target: "0.99", ValidFrom: f.historyStart, SliValidFrom: f.historyStart, RetentionStart: f.historyStart}
	in := maintenanceEvaluationInput{SloID: mustPGUUID(f.ids[0]), SliID: mustPGUUID(f.sliID), DefinitionRevision: 1, MeasurementRevision: 1, Limits: maintenanceWindowLimits{MaxSourceHours: 10, MaxRawRows: 1000, MaxFragments: 1000}, Requests: []maintenanceMaskWindows{{RequestedRevision: 1, Windows: []slo.WindowInput{w}}}}
	automatic, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Requests[0].Preferred = &maintenanceComputedMask{Revision: 0, BuildID: genesis.ID}
	preferred, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || !reflect.DeepEqual(preferred, automatic) {
		t.Fatalf("explicit completed genesis differs from automatic: %+v %v want %+v", preferred, err, automatic)
	}
	if preferred.Masks[0].Computed.BuildID != genesis.ID || preferred.Masks[0].Computed.Revision != 0 || preferred.Masks[0].RequestedRevision != 1 || preferred.Masks[0].Proofs[0].Kind != "indexed_absence" || !reflect.DeepEqual(preferred.Masks[0].Windows[0], evaluationRawWindow(t, w, nil)) {
		t.Fatalf("genesis provenance/math lost: %+v", preferred)
	}
	other := f
	other.ids = []string{f.ids[1]}
	foreign := compilerCapture(t, other, nil)
	compilerFinish(t, other, foreign.ID, 10)
	in.Requests[0].Preferred = &maintenanceComputedMask{Revision: 0, BuildID: foreign.ID}
	if got, err := evaluateFixtureMaintenance(t, f, in); err == nil || len(got.Masks[0].Windows) > 0 {
		t.Fatalf("foreign genesis accepted: %+v %v", got, err)
	}
	in.Requests[0].Preferred = &maintenanceComputedMask{Revision: 1, BuildID: genesis.ID}
	if got, err := evaluateFixtureMaintenance(t, f, in); err == nil || len(got.Masks[0].Windows) > 0 {
		t.Fatalf("mismatched genesis revision accepted: %+v %v", got, err)
	}
}

func TestIntegrationMaintenanceEvaluationSeventySecondInterior(t *testing.T) {
	t.Parallel()
	const cadence = 70 * time.Second
	s := newSelfHealFixtureCadence(t, "evaluation-interior", true, cadence)
	id := s.createSLO(t, "evaluation-interior")
	var verifier [32]byte
	verifier[0] = 98
	f := maintenanceFixture{selfHealFixture: s, ids: []string{id}, proof: DatasourceProof{SessionVerifier: verifier}}
	// The independent fixture explicitly chooses one full common-grid block,
	// not an hour rounded on only one of the two grids: 7h / 70s = 360 starts.
	start := ceilSliBoundary(f.at, 7*time.Hour)
	end := start.Add(7 * time.Hour)
	hour := start.Add(2 * time.Hour)
	m := f.input("interior-mask")
	m.Start = ceilSliBoundary(hour.Add(10*time.Minute), cadence)
	m.End = m.Start.Add(7 * cadence)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	build := compilerCapture(t, f, nil)
	compilerFinish(t, f, build.ID, 10)
	if _, err := execObservationFixture(t, f.conn, f.sliID, `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT point,base.sli_id,base.measurement_revision,'observed',990,1000,base.result_digest,'{}',gen_random_uuid(),gen_random_uuid(),base.agent_id,base.session_generation,base.configuration_revision,base.lease_id,point,point+interval '70 seconds'
 FROM (SELECT * FROM almanac_sli_observations WHERE sli_id=$1 ORDER BY interval_start LIMIT 1) base
 CROSS JOIN generate_series($2::timestamptz,$3::timestamptz-interval '70 seconds',interval '70 seconds') point`, f.sliID, start, end); err != nil {
		t.Fatal(err)
	}
	f.now = end.Add(time.Minute)
	f.drain(t, "seven-hour source")
	var generation int64
	if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, hour).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	h := maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SliID: mustPGUUID(f.sliID), SloID: mustPGUUID(id), DefinitionRevision: 1, MeasurementRevision: 1, BuildID: build.ID, SourceGeneration: generation, HourStart: hour}, RetentionStart: f.historyStart, Deadline: f.now.Add(time.Minute)}
	w := slo.WindowInput{At: end, Now: f.now, Duration: 7 * time.Hour, Cadence: cadence, Target: "0.99", ValidFrom: f.historyStart, SliValidFrom: f.historyStart, RetentionStart: f.historyStart}
	mask := []slo.ExclusionInterval{{Start: m.Start, End: m.End}}
	want := evaluationRawWindow(t, w, mask)
	if want.Observed != 353 || want.Excluded != 7 || want.Good == nil || *want.Good != "349470.000000000000" {
		t.Fatalf("independent fixture arithmetic %+v", want)
	}
	in := maintenanceEvaluationInput{SloID: h.SloID, SliID: h.SliID, DefinitionRevision: 1, MeasurementRevision: 1, Requests: []maintenanceMaskWindows{{RequestedRevision: 1, Windows: []slo.WindowInput{w}}}, Limits: maintenanceWindowLimits{MaxSourceHours: 20, MaxRawRows: 1, MaxFragments: 1}}
	pending, err := evaluateFixtureMaintenance(t, f, in)
	if err == nil || len(pending.Preparation) != 1 || pending.Preparation[0].Kind != "scoped_hour" || pending.Preparation[0].Hour != h.maintenanceHourKey {
		t.Fatalf("seven-hour interior did not require scoped population: %+v %v", pending, err)
	}
	prepared, err := hourPrepare(t, f, h)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Eligible.LastObservation == nil || !prepared.Eligible.LastObservation.After(hour.Add(time.Hour)) {
		t.Fatal("fixture lacks a 70-second interval crossing the physical hour end")
	}
	// Remove all interior raw only after independently computing the oracle and
	// preparing canonical/scoped populations. Success now cannot be a raw fallback.
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start>=$2 AND interval_start<$3`, f.sliID, start, end); err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	recorded := &evaluationQueryTx{Tx: tx}
	got, err := evaluateMaintenanceWindows(t.Context(), recorded, in)
	if err != nil || len(got.Preparation) != 0 || !reflect.DeepEqual(got.Masks[0].Windows[0], want) || got.Masks[0].Computed.BuildID != build.ID || got.Masks[0].Computed.Revision != 1 {
		t.Fatalf("canonical/scoped common-grid oracle mismatch: %+v %v want %+v", got, err, want)
	}
	canonical, scoped := 0, 0
	for _, call := range recorded.calls {
		switch {
		case strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationHours "):
			canonical++
			if len(call.args[3].([]pgtype.Timestamptz)) != 7 {
				t.Fatal("expected seven physical canonical hours")
			}
		case strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationScopedHours "):
			scoped++
			if len(call.args[0].([]pgtype.Timestamptz)) != 1 || !call.args[0].([]pgtype.Timestamptz)[0].Time.Equal(hour) {
				t.Fatal("wrong prepared partial hour consumed")
			}
		case strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationEdges "):
			t.Fatal("full common-grid interior performed a raw-edge read")
		}
	}
	if canonical != 1 || scoped != 1 {
		t.Fatalf("actual source paths: canonical=%d scoped=%d", canonical, scoped)
	}
}
