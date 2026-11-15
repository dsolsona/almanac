//go:build integration

package storage

import (
	"errors"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"reflect"
	"testing"
	"time"
)

func TestIntegrationMaintenanceHourSchema(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	var present bool
	if err := f.conn.QueryRow(t.Context(), `SELECT to_regclass('public.almanac_slo_maintenance_hours') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("exact scoped physical-hour population storage is absent")
	}
}

func hourFixture(t *testing.T) (maintenanceFixture, maintenanceHourInput) {
	t.Helper()
	f := newMaintenanceFixture(t)
	h := f.historyStart.Truncate(time.Hour)
	m := f.input("scoped-hour")
	m.Start = h.Add(10 * time.Minute)
	m.End = h.Add(20 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	f.drain(t, "hour source")
	var gen int64
	if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, h).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	return f, maintenanceHourInput{maintenanceHourKey: maintenanceHourKey{SliID: mustPGUUID(f.sliID), SloID: mustPGUUID(f.ids[0]), BuildID: b.ID, DefinitionRevision: 1, MeasurementRevision: 1, SourceGeneration: gen, HourStart: h}, RetentionStart: h.Add(-time.Hour), Deadline: f.now.Add(time.Minute)}
}
func hourPrepare(t *testing.T, f maintenanceFixture, in maintenanceHourInput) (maintenanceHourRecord, error) {
	t.Helper()
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	r, err := prepareMaintenanceHour(t.Context(), tx, in, f.clock)
	if err != nil {
		return r, err
	}
	return r, tx.Commit(t.Context())
}
func TestIntegrationMaintenanceHourExactOracle(t *testing.T) {
	t.Parallel()
	f, in := hourFixture(t)
	got, err := hourPrepare(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	h := in.HourStart
	facts := make([]slo.Observation, 60)
	for i := range facts {
		facts[i] = slo.Observation{Start: h.Add(time.Duration(i) * time.Minute), End: h.Add(time.Duration(i+1) * time.Minute), Received: h.Add(time.Duration(i+1) * time.Minute), Good: "990", Total: "1000", Outcome: "observed"}
	}
	input := slo.WindowInput{At: h.Add(time.Hour), Now: f.now, Duration: time.Hour, Cadence: time.Minute, Target: "0.99", ValidFrom: h, RetentionStart: h, Exclusions: []slo.ExclusionInterval{{Start: h.Add(10 * time.Minute), End: h.Add(20 * time.Minute)}}, Observations: facts}
	want, err := slo.EvaluateOperationalWindowContext(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Observations = nil
	totals := slo.WindowTotals{Good: got.Eligible.Good, Total: got.Eligible.Total, Observed: got.Eligible.Observed, KnownEmpty: got.Eligible.KnownEmpty, ZeroTraffic: got.Eligible.ZeroTraffic, FirstObservation: got.Eligible.FirstObservation, LastObservation: got.Eligible.LastObservation, LatestReceipt: got.Eligible.LatestReceipt, Excluded: slo.ExclusionTotals{Good: got.Excluded.Good, Total: got.Excluded.Total, Observed: got.Excluded.Observed, KnownEmpty: got.Excluded.KnownEmpty}}
	actual, err := slo.EvaluateOperationalTotals(t.Context(), input, totals)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, want) || got.Eligible.Expected != 50 || got.Excluded.Expected != 10 {
		t.Fatalf("scoped=%+v raw=%+v populations=%+v", actual, want, got)
	}
	again, err := readMaintenanceHour(t.Context(), dbgen.New(f.conn), in.maintenanceHourKey, f.now)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("stored read differs: %+v %v", again, err)
	}
}
func TestIntegrationMaintenanceHourAbsentAndRawLoss(t *testing.T) {
	t.Parallel()
	f, in := hourFixture(t)
	empty := in
	empty.HourStart = in.HourStart.Add(time.Hour)
	clearMaintenanceHourFixture(t, f, empty.HourStart)
	empty.RetentionStart = empty.HourStart
	empty.SourceGeneration = 0
	r, err := hourPrepare(t, f, empty)
	if err != nil || r.Eligible.Observed != 0 || r.Eligible.Expected != 60 {
		t.Fatalf("absent hour: %+v %v", r, err)
	}
	absent, err := slo.EvaluateOperationalTotals(t.Context(), slo.WindowInput{At: empty.HourStart.Add(time.Hour), Now: f.now, Duration: time.Hour, Cadence: time.Minute, Target: "0.99", ValidFrom: f.historyStart, RetentionStart: empty.HourStart}, hourTotals(r))
	if err != nil || absent.State != "no_data" || absent.Performance != "unknown" || absent.Good != nil {
		t.Fatalf("absent source invented success: %+v %v", absent, err)
	}

	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2`, f.sliID, in.HourStart); err != nil {
		t.Fatal(err)
	}
	if _, err := hourPrepare(t, f, in); !errors.Is(err, ErrMaintenanceHourUnavailable) {
		t.Fatalf("raw loss accepted: %v", err)
	}
	var n int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_maintenance_hours WHERE hour_start=$1`, in.HourStart).Scan(&n); err != nil || n != 0 {
		t.Fatalf("incomplete publication: %d %v", n, err)
	}
}

func clearMaintenanceHourFixture(t *testing.T, f maintenanceFixture, hour time.Time) {
	t.Helper()
	// Construct a genuinely never-observed retained hour inside definition validity
	// from the common populated fixture, before invoking the candidate.
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start>=$2 AND interval_start<$2::timestamptz+interval '1 hour';`, f.sliID, hour); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, hour); err != nil {
		t.Fatal(err)
	}
}
