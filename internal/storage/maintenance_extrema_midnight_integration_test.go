//go:build integration

package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func extremaMidnightFixture(t *testing.T) (maintenanceFixture, maintenanceHourInput, time.Time) {
	t.Helper()
	s := newSelfHealFixture(t, "day-worker", true)
	end := s.historyStart.Truncate(24 * time.Hour).Add(24 * time.Hour)
	shift := int64(end.Add(-2*time.Hour).Sub(s.historyStart) / time.Second)
	if _, err := execObservationFixture(t, s.conn, s.sliID, `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+make_interval(secs=>$2),sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at+make_interval(secs=>$2),recorded_at+make_interval(secs=>$2) FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start<$3`, s.sliID, shift, s.historyStart.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.now = end
	s.at = s.now
	id := s.createSLOWithHistory(t, "day-worker", time.Hour, end.Add(-2*time.Hour))
	var verifier [32]byte
	verifier[0] = 98
	f := maintenanceFixture{selfHealFixture: s, ids: []string{id}, proof: DatasourceProof{SessionVerifier: verifier}}
	m := f.input("day-mask")
	m.Start = end.Add(-50 * time.Minute)
	m.End = end.Add(-40 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	f.drain(t, "day source")
	var gen int64
	if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, end.Add(-time.Hour)).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	h := maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SloID: mustPGUUID(id), SliID: mustPGUUID(f.sliID), BuildID: b.ID, DefinitionRevision: 1, MeasurementRevision: 1, SourceGeneration: gen, HourStart: end.Add(-time.Hour)}, RetentionStart: f.historyStart, Deadline: f.now.Add(time.Minute)}
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	if _, err := dbgen.New(f.conn).EnsureSloExtremaCursors(t.Context(), dbgen.EnsureSloExtremaCursorsParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(f.now)}); err != nil {
		t.Fatal(err)
	}
	return f, h, end
}

func TestIntegrationMaintenanceExtremaMidnight(t *testing.T) {
	t.Parallel()
	f, h, end := extremaMidnightFixture(t)
	type snapshot struct {
		Hours, Days, Jobs int
		Built             time.Time
		Leased            bool
		Revision          int64
	}
	readState := func() snapshot {
		t.Helper()
		var s snapshot
		if err := f.conn.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600),
 (SELECT count(*) FROM almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=86400),
 (SELECT count(*) FROM almanac_maintenance_preparation WHERE slo_id=$1),
 built_through,lease_until IS NOT NULL,exclusion_revision
 FROM almanac_slo_extrema_cursor WHERE slo_id=$1`, h.SloID).Scan(&s.Hours, &s.Days, &s.Jobs, &s.Built, &s.Leased, &s.Revision); err != nil {
			t.Fatal(err)
		}
		return s
	}
	initial := readState()
	if initial.Hours != 0 || initial.Days != 0 || initial.Jobs != 0 || !initial.Built.Equal(end.Add(-2*time.Hour)) || initial.Leased {
		t.Fatalf("fixture did not begin at untouched source cursor: %+v", initial)
	}
	// The explicit clock boundary before the second hour's publication follows
	// the first hour INSERT and day enqueue inside the worker transaction.
	// A separate connection must still see neither artifact nor queued job.
	calls := 0
	observedUncommitted := false
	clock := func() time.Time {
		calls++
		if calls == 5 {
			s := readState()
			observedUncommitted = true
			if s.Hours != 0 || s.Days != 0 || s.Jobs != 0 || !s.Built.Equal(initial.Built) {
				t.Fatalf("partial source publication escaped before commit: %+v", s)
			}
		}
		return f.now
	}
	worked, err := f.runtime.buildSloExtrema(t.Context(), clock)
	if err != nil || !worked || !observedUncommitted {
		t.Fatalf("normal midnight turn: worked%v observed%v clockCalls%d err%v", worked, observedUncommitted, calls, err)
	}
	after := readState()
	if after.Hours != 2 || after.Days != 0 || after.Jobs != 1 || !after.Built.Equal(end) || after.Leased || after.Revision != 1 {
		t.Fatalf("hours/cursor/day target not committed together: %+v", after)
	}
	q := dbgen.New(f.conn)
	generation, err := q.GetDerivedSourceGeneration(t.Context(), dbgen.GetDerivedSourceGenerationParams{SliID: h.SliID, MeasurementRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	var job dbgen.AlmanacMaintenancePreparation
	compilerTx(t, f, func(tx pgx.Tx) error {
		jobs, err := claimMaintenancePreparation(t.Context(), tx, maintenancePreparationHistorical, f.now, time.Minute, 1, f.clock)
		if err != nil {
			return err
		}
		if len(jobs) != 1 {
			t.Fatalf("normal source turn queued %d runnable day targets", len(jobs))
		}
		job = jobs[0]
		return nil
	})
	if job.Kind != "extrema_day" || job.SloID != h.SloID || job.SliID != h.SliID || job.BuildID != h.BuildID || job.TargetRevision != 1 || job.SourceGeneration.Int64 != generation || !job.HourStart.Time.Equal(end.Add(-24*time.Hour)) {
		t.Fatalf("normal day target lost immutable identity: %+v", job)
	}
	state, err := f.runtime.processMaintenancePreparation(t.Context(), job, preparationOptions(f), f.clock)
	if err != nil || state != "complete" {
		t.Fatalf("outer day dispatch: %s %v", state, err)
	}
	final := readState()
	if final.Hours != 2 || final.Days != 1 || final.Jobs != 1 || !final.Built.Equal(end) || final.Leased {
		t.Fatalf("day dispatch changed source cursor or created child work: %+v", final)
	}
	var savedState string
	var leased bool
	var attempts int
	if err := f.conn.QueryRow(t.Context(), `SELECT state,lease_token IS NOT NULL,attempts FROM almanac_maintenance_preparation WHERE id=$1`, job.ID).Scan(&savedState, &leased, &attempts); err != nil || savedState != "complete" || leased || attempts != 0 {
		t.Fatalf("outer ACK: state%s leased%v attempts%d err%v", savedState, leased, attempts, err)
	}
	row, err := q.GetSlo(t.Context(), h.SloID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: h.SliID, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	base := extremaWindowInput(row, source, f.now, preparationOptions(f).RetentionStart)
	var hourFinal extremaMark
	for index := range 2 {
		var payload []byte
		var g, r int64
		if err := f.conn.QueryRow(t.Context(), `SELECT lanes,source_generation,exclusion_revision FROM almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, h.SloID, end.Add(time.Duration(index-2)*time.Hour)).Scan(&payload, &g, &r); err != nil {
			t.Fatal(err)
		}
		if g != generation || r != 1 {
			t.Fatalf("hour header G%d/R%d want%d/1", g, r, generation)
		}
		var lanes extremaLanes
		if err := unmarshalReceiptMetadata(payload, &lanes); err != nil {
			t.Fatal(err)
		}
		mark := lanes.Window.Marks[len(lanes.Window.Marks)-1]
		value, err := mark.result(t.Context(), base)
		good, total, excluded := int64(59400), int64(60000), int64(0)
		if index == 1 {
			good, total, excluded = 49500, 50000, 10
			hourFinal = mark
		}
		if err != nil || !terminalCount(value.Good, good) || !terminalCount(value.Total, total) || value.Excluded != excluded {
			t.Fatalf("hour%d independent counts: %+v %v", index, value, err)
		}
		if err := mark.validateMaintenanceRowAuthority(base, maintenanceRetainedRowAuthority{Scope: preparationScope(f), SourceGeneration: g, RequestedRevision: r}); err != nil {
			t.Fatal(err)
		}
	}
	var payload []byte
	var dayG, dayR int64
	if err := f.conn.QueryRow(t.Context(), `SELECT lanes,source_generation,exclusion_revision FROM almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=86400`, h.SloID).Scan(&payload, &dayG, &dayR); err != nil {
		t.Fatal(err)
	}
	var day extremaLanes
	if err := unmarshalReceiptMetadata(payload, &day); err != nil {
		t.Fatal(err)
	}
	if dayG != generation || dayR != 1 || day.Compliance == nil || !day.Compliance.DayEnd.Equal(end) || !day.Compliance.Mark.At.Equal(end) {
		t.Fatal("day header/compliance identity changed")
	}
	value, err := day.Compliance.Mark.result(t.Context(), base)
	if err != nil || !terminalCount(value.Good, 49500) || !terminalCount(value.Total, 50000) || value.Excluded != 10 || value.Observed != 50 || value.Performance != "met" {
		t.Fatalf("completed day compliance differs: %+v %v", value, err)
	}
	if !reflect.DeepEqual(day.Compliance.Mark.Maintenance, hourFinal.Maintenance) || !reflect.DeepEqual(day.Compliance.Mark.LatestReceipt, hourFinal.LatestReceipt) {
		t.Fatal("day recomputed/relabelled immutable hour evidence")
	}
}
