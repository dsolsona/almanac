//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5"
)

func evaluationFixtureInput(f maintenanceFixture, in maintenanceHourInput) maintenanceEvaluationInput {
	w := slo.WindowInput{At: in.HourStart.Add(time.Hour), Now: f.now, Duration: time.Hour, Cadence: time.Minute, Target: "0.99", ValidFrom: f.historyStart, SliValidFrom: f.historyStart, RetentionStart: f.historyStart}
	return maintenanceEvaluationInput{SloID: in.SloID, SliID: in.SliID, DefinitionRevision: 1, MeasurementRevision: 1, Limits: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 10000, MaxFragments: 10000}, Requests: []maintenanceMaskWindows{{RequestedRevision: 0, Windows: []slo.WindowInput{w}}, {RequestedRevision: 1, Windows: []slo.WindowInput{w}}}}
}
func evaluateFixtureMaintenance(t *testing.T, f maintenanceFixture, in maintenanceEvaluationInput) (maintenanceEvaluationResult, error) {
	t.Helper()
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	return evaluateMaintenanceWindows(t.Context(), tx, in)
}
func evaluationRawWindow(t *testing.T, w slo.WindowInput, mask []slo.ExclusionInterval) slo.WindowResult {
	t.Helper()
	w.Exclusions = mask
	for at := w.At.Add(-w.Duration); at.Before(w.At); at = at.Add(w.Cadence) {
		w.Observations = append(w.Observations, slo.Observation{Start: at, End: at.Add(w.Cadence), Received: at.Add(w.Cadence), Outcome: "observed", Good: "990", Total: "1000"})
	}
	r, err := slo.EvaluateOperationalWindowContext(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestIntegrationMaintenanceEvaluationPendingAndOracle(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	in := evaluationFixtureInput(f, h)
	got, err := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) != 1 || got.Preparation[0].Kind != "scoped_hour" || got.Preparation[0].Hour != h.maintenanceHourKey {
		t.Fatalf("unprepared partial hour: %+v %v", got, err)
	}
	for _, m := range got.Masks {
		if len(m.Windows) != 0 {
			t.Fatal("pending batch exposed partial numeric success")
		}
	}
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Masks) != 2 || got.SourceGeneration <= 0 || len(got.Preparation) != 0 {
		t.Fatalf("batch identity %+v", got)
	}
	mask := []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}}
	for i, m := range got.Masks {
		var exclusions []slo.ExclusionInterval
		if i == 1 {
			exclusions = mask
		}
		want := evaluationRawWindow(t, in.Requests[i].Windows[0], exclusions)
		if len(m.Windows) != 1 || !reflect.DeepEqual(m.Windows[0], want) {
			t.Fatalf("mask %d: got %+v want %+v", i, m.Windows, want)
		}
		if m.RequestedRevision != int64(i) || m.Computed.Revision != int64(i) {
			t.Fatalf("mask identity %+v", m)
		}
	}
}
func TestIntegrationMaintenanceEvaluationAbsentAndBudget(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	clearMaintenanceHourFixture(t, f, h.HourStart)
	in := evaluationFixtureInput(f, h)
	// Fully eligible absent source is not a fabricated success and does not need
	// a partial-hour summary when there are no facts to partition.
	in.Requests = in.Requests[:1]
	got, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || len(got.Masks) != 1 || got.Masks[0].Windows[0].State != "no_data" || got.Masks[0].Windows[0].Good != nil {
		t.Fatalf("absent source %+v %v", got, err)
	}
	in.Limits.MaxSourceHours = 0
	if _, err = evaluateFixtureMaintenance(t, f, in); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("whole-hour budget ignored: %v", err)
	}
	in.Limits.MaxSourceHours = 100
	in.Requests[0].Windows[0].At = in.Requests[0].Windows[0].At.Add(-time.Minute)
	in.Limits.MaxRawRows = 1
	if _, err = evaluateFixtureMaintenance(t, f, in); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("canonical original edge budget ignored: %v", err)
	}
}
