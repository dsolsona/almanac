//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationMaintenanceEvaluationSummary(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	q := &evaluationQueryTx{Tx: tx}
	ordinary, err := evaluateMaintenanceWindows(t.Context(), q, in)
	if err != nil {
		t.Fatal(err)
	}
	calls := q.calls
	for _, option := range []struct{ summary, retain bool }{{true, false}, {true, true}, {false, true}} {
		retain := option.retain
		in.Summary = option.summary
		in.RetainFacts = retain
		q.calls = nil
		got, err := evaluateMaintenanceWindows(t.Context(), q, in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(q.calls, calls) {
			t.Fatal("summary/facts changed actual source query work")
		}
		for i, mask := range got.Masks {
			w := mask.Windows[0]
			if in.Summary && (w.Good != nil || w.Total != nil || w.Burn != nil || w.ExcludedGood != nil || w.Coverage != nil || w.ExactBurn == nil) {
				t.Fatalf("summary prematurely formatted or lost exact rank: %+v", w)
			}
			if !reflect.DeepEqual(w.WithValues(), ordinary.Masks[i].Windows[0]) {
				t.Fatal("selection-time formatting changed result")
			}
			if !retain {
				if mask.Facts != nil {
					t.Fatal("default retained facts allocated")
				}
				continue
			}
			if len(mask.Facts) != len(mask.Windows) {
				t.Fatal("facts not aligned")
			}
			if i == 0 {
				if !reflect.DeepEqual(mask.Facts[0], maintenanceWindowFacts{}) {
					t.Fatal("canonical zero redundantly extracted facts")
				}
				continue
			}
			facts := mask.Facts[0]
			if facts.Coverage == nil {
				t.Fatal("scoped facts missing certificate")
			}
			replay, err := slo.EvaluateOperationalCoveredTotals(t.Context(), in.Requests[i].Windows[0], facts.Totals, *facts.Coverage)
			if err != nil || !reflect.DeepEqual(replay, w.WithValues()) {
				t.Fatalf("facts replay differs: %v", err)
			}
		}
	}
}

func TestIntegrationMaintenanceEvaluationFactsReplay(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"partial", "fully_excluded", "unknown_receipt", "retained_raw_removed"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, h := hourFixture(t)
			mask := []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}}
			revision := int64(1)
			if kind == "fully_excluded" {
				m := f.input("facts-whole")
				m.Start = h.HourStart
				m.End = h.HourStart.Add(time.Hour)
				if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
					t.Fatal(err)
				}
				b := compilerCapture(t, f, nil)
				compilerFinish(t, f, b.ID, 10)
				h.BuildID = b.ID
				revision = 2
				mask = []slo.ExclusionInterval{{Start: m.Start, End: m.End}}
			} else if _, err := hourPrepare(t, f, h); err != nil {
				t.Fatal(err)
			}
			in := evaluationFixtureInput(f, h)
			in.Requests = in.Requests[1:]
			in.Requests[0].RequestedRevision = revision
			in.Summary = true
			in.RetainFacts = true
			if kind == "unknown_receipt" {
				in.Requests[0].Windows[0].At = in.Requests[0].Windows[0].At.Add(time.Hour)
				in.Requests[0].Windows[0].Duration = 2 * time.Hour
				if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sli_hourly_aggregates SET receipt_source_generation=NULL,certified_receipt_at=NULL WHERE sli_id=$1 AND hour_start=$2`, f.sliID, h.HourStart.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			want := evaluationRawWindow(t, in.Requests[0].Windows[0], mask)
			if kind == "unknown_receipt" {
				want.LatestReceipt = nil
			}
			if kind == "retained_raw_removed" {
				if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start>=$2 AND interval_start<$2::timestamptz+interval '1 hour'`, f.sliID, h.HourStart); err != nil {
					t.Fatal(err)
				}
			}
			got, err := evaluateFixtureMaintenance(t, f, in)
			if err != nil || len(got.Masks) != 1 || len(got.Masks[0].Facts) != 1 || got.Masks[0].Facts[0].Coverage == nil {
				t.Fatalf("facts unavailable: %+v %v", got, err)
			}
			fact := got.Masks[0].Facts[0]
			replay, err := slo.EvaluateOperationalCoveredTotals(t.Context(), in.Requests[0].Windows[0], fact.Totals, *fact.Coverage)
			if err != nil || !reflect.DeepEqual(replay, want) || !reflect.DeepEqual(got.Masks[0].Windows[0].WithValues(), want) {
				t.Fatalf("raw/retained/summary disagree: got %+v want %+v %v", replay, want, err)
			}
			if kind == "unknown_receipt" && fact.Totals.LatestReceipt != nil {
				t.Fatal("unknown receipt invented")
			}
		})
	}
}

func TestIntegrationMaintenanceEvaluationFactsOwnership(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Summary = true
	in.RetainFacts = true
	in.Requests[0].Windows = append(in.Requests[0].Windows, in.Requests[0].Windows[0])
	got, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	m := got.Masks[0]
	if len(m.Facts) != 2 || m.Facts[0].Coverage == nil {
		t.Fatal("missing duplicate window facts")
	}
	want := evaluationRawWindow(t, in.Requests[0].Windows[0], []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}})
	original := m.Windows[0].WithValues()
	first := m.Facts[0]
	first.Totals.Good.SetInt64(0)
	first.Totals.Total.SetInt64(0)
	first.Totals.Excluded.Good.SetInt64(0)
	first.Totals.Excluded.Total.SetInt64(0)
	*first.Totals.FirstObservation = time.Time{}
	*first.Totals.LastObservation = time.Time{}
	*first.Totals.LatestReceipt = time.Time{}
	first.Coverage.Points[0].ExcludedBefore = 999
	first.Coverage.LatestEligibleEnd = time.Time{}
	if !reflect.DeepEqual(m.Windows[0].WithValues(), original) || !reflect.DeepEqual(m.Windows[1].WithValues(), want) {
		t.Fatal("facts mutation leaked into lazy result/other result")
	}
	replay, err := slo.EvaluateOperationalCoveredTotals(t.Context(), in.Requests[0].Windows[1], m.Facts[1].Totals, *m.Facts[1].Coverage)
	if err != nil || !reflect.DeepEqual(replay, want) {
		t.Fatalf("facts alias another window: %v", err)
	}
	*m.Windows[1].FirstObservation = time.Time{}
	if m.Facts[1].Totals.FirstObservation.IsZero() {
		t.Fatal("result time aliases retained facts")
	}
}

func TestIntegrationMaintenanceEvaluationFactsPendingUnavailable(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	m := f.input("facts-second-mask")
	m.Start = h.HourStart.Add(25 * time.Minute)
	m.End = m.Start.Add(3 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests = append(in.Requests, maintenanceMaskWindows{RequestedRevision: 2, Windows: in.Requests[0].Windows})
	in.Summary = true
	in.RetainFacts = true
	got, err := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("expected unfinished new scoped hour: %v", err)
	}
	for _, mask := range got.Masks {
		if len(mask.Facts) != 0 || len(mask.Windows) != 0 {
			t.Fatal("pending leaked earlier mask numerical facts")
		}
	}
	preparationEnqueue(t, f, got.Preparation, maintenancePreparationLive)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='unavailable',reason='history_unavailable'`); err != nil {
		t.Fatal(err)
	}
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || len(got.Masks) != 2 || len(got.Masks[0].Facts) != 1 || got.Masks[0].Facts[0].Coverage == nil {
		t.Fatalf("usable historical facts lost: %+v %v", got, err)
	}
	if len(got.Masks[1].Facts) != 1 || !reflect.DeepEqual(got.Masks[1].Facts[0], maintenanceWindowFacts{}) || got.Masks[1].Unavailable[0] == "" || !reflect.DeepEqual(got.Masks[1].Windows[0], slo.WindowResult{}) {
		t.Fatal("unavailable slot invented facts")
	}
}

func TestIntegrationMaintenanceEvaluationFactsDatabaseError(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	// First mask has no common-grid interior; it finishes before the second
	// mask reaches the deliberately unavailable scoped-population relation.
	first := in.Requests[0].Windows[0]
	first.Duration = 5 * time.Minute
	in.Requests = append([]maintenanceMaskWindows{{RequestedRevision: 1, Windows: []slo.WindowInput{first}}}, in.Requests...)
	in.Summary = true
	in.RetainFacts = true
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `ALTER TABLE almanac_slo_maintenance_hours RENAME TO hidden_facts_fixture`); err != nil {
		t.Fatal(err)
	}
	got, err := evaluateMaintenanceWindows(t.Context(), tx, in)
	failure, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || failure.Code != "42P01" {
		t.Fatalf("database failure became truth/pending: %v", err)
	}
	for _, mask := range got.Masks {
		if len(mask.Facts) != 0 || len(mask.Windows) != 0 {
			t.Fatal("database failure leaked earlier mask facts")
		}
	}
}
