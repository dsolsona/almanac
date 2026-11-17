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

func TestIntegrationMaintenancePreparationBoundedProgress(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	second := f.input("second-partial-hour")
	second.Start = h.HourStart.Add(70 * time.Minute)
	second.End = h.HourStart.Add(80 * time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, second); err != nil {
		t.Fatal(err)
	}
	build := compilerCapture(t, f, nil)
	compilerFinish(t, f, build.ID, 10)
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests[0].RequestedRevision = 2
	in.Requests[0].Windows[0].Duration = 2 * time.Hour
	in.Requests[0].Windows[0].At = h.HourStart.Add(2 * time.Hour)
	runtime, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	for step := range 2 {
		got, err := evaluateFixtureMaintenance(t, f, in)
		if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) != 2-step {
			t.Fatalf("step %d prerequisites: %+v %v", step, got, err)
		}
		for _, mask := range got.Masks {
			if len(mask.Windows) != 0 {
				t.Fatal("pending exposed numeric decisions")
			}
		}
		before := append([]maintenanceEvaluationPreparation(nil), got.Preparation...)
		compilerTx(t, f, func(tx pgx.Tx) error {
			p, err := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), got.Preparation, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
			if err != nil {
				return err
			}
			if len(p.jobs) != 1 || len(p.hours) != 0 {
				t.Fatalf("enqueue exceeded one prerequisite: jobs=%d hours=%d", len(p.jobs), len(p.hours))
			}
			return enqueueMaintenancePreparation(t.Context(), tx, p, maintenancePreparationLive, f.now)
		})
		if !reflect.DeepEqual(before, got.Preparation) {
			t.Fatal("enqueue mutated caller's remaining prerequisites")
		}
		opts := preparationOptions(f)
		opts.MaxLive = 1
		opts.Concurrency = 1
		turn, err := runtime.runMaintenancePreparationTurn(t.Context(), f.now, opts, f.clock)
		if err != nil || turn.Complete != 1 {
			t.Fatalf("bounded worker step %d: %+v %v", step, turn, err)
		}
	}
	got, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil || len(got.Preparation) != 0 || len(got.Masks) != 1 || len(got.Masks[0].Windows) != 1 {
		t.Fatalf("finite prerequisites failed to complete: %+v %v", got, err)
	}
	want := evaluationRawWindow(t, in.Requests[0].Windows[0], []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}, {Start: second.Start, End: second.End}})
	if !reflect.DeepEqual(got.Masks[0].Windows[0], want) {
		t.Fatalf("bounded progress changed math: %+v want %+v", got.Masks[0].Windows[0], want)
	}
	var jobs, attempts int
	if err = f.conn.QueryRow(t.Context(), `SELECT count(*),COALESCE(sum(attempts),0) FROM almanac_maintenance_preparation WHERE state='complete'`).Scan(&jobs, &attempts); err != nil || jobs != 2 || attempts != 0 {
		t.Fatalf("durable completion %d attempts %d: %v", jobs, attempts, err)
	}
}

func TestIntegrationMaintenancePreparationCanonicalPendingOnly(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	missing := h.HourStart.Add(time.Hour)
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_derived_dirty_ranges(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
SELECT sli_id,measurement_revision,source_generation,hour_start,hour_start+interval '1 hour',$3,$3 FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, h.SliID, missing, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, h.SliID, missing); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[:1]
	ready := in.Requests[0].Windows[0]
	pending := ready
	pending.At = missing.Add(time.Hour)
	in.Requests[0].Windows = []slo.WindowInput{ready, ready, pending}
	got, err := evaluateFixtureMaintenance(t, f, in)
	if !errors.Is(err, ErrDerivedPending) || len(got.Preparation) != 1 || got.Preparation[0].Kind != "source_window" || !got.Preparation[0].Start.Equal(missing) || !got.Preparation[0].End.Equal(missing.Add(time.Hour)) {
		t.Fatalf("ready spans consumed pending admission: %+v %v", got.Preparation, err)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		p, err := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), got.Preparation, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
		if err != nil {
			return err
		}
		if len(p.hours) != 1 || !p.hours[0].Time.Equal(missing) {
			t.Fatalf("one-request admission omitted the missing hour: %+v", p.hours)
		}
		return enqueueMaintenancePreparation(t.Context(), tx, p, maintenancePreparationLive, f.now)
	})
	var attempts int
	if err := f.conn.QueryRow(t.Context(), `SELECT attempts FROM almanac_derived_dirty_ranges WHERE sli_id=$1 AND range_start=$2`, h.SliID, missing).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("enqueue changed canonical failure budget: %d %v", attempts, err)
	}
	f.drain(t, "canonical missing source")
	got, err = evaluateFixtureMaintenance(t, f, in)
	if err != nil || len(got.Preparation) != 0 || len(got.Masks[0].Windows) != 3 {
		t.Fatalf("canonical repair did not recover: %+v %v", got, err)
	}
	for _, w := range got.Masks[0].Windows {
		if w.Good == nil || *w.Good != "59400.000000000000" || w.Total == nil || *w.Total != "60000.000000000000" {
			t.Fatalf("canonical repair changed exact counts: %+v", w)
		}
	}
}

func TestIntegrationMaintenancePreparationBoundedSourcePrefix(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	first := h.maintenanceHourKey
	second := first
	second.HourStart = first.HourStart.Add(time.Hour)
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start >= $2 AND hour_start < $3`, f.sliID, first.HourStart, second.HourStart.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	requests := []maintenanceEvaluationPreparation{{Kind: "source_hour", RequestedRevision: 1, Hour: first}, {Kind: "source_hour", RequestedRevision: 1, Hour: second}}
	compilerTx(t, f, func(tx pgx.Tx) error {
		p, err := prepareMaintenanceEnqueue(t.Context(), tx, preparationScope(f), requests, maintenancePreparationLimits{MaxRequests: 1, MaxSourceHours: 1}, f.now)
		if err != nil {
			return err
		}
		if len(p.hours) != 1 || !p.hours[0].Time.Equal(first.HourStart) || len(p.jobs) != 0 {
			t.Fatalf("source tail expanded outside admission: %+v", p)
		}
		return enqueueMaintenancePreparation(t.Context(), tx, p, maintenancePreparationLive, f.now)
	})
	var count int
	var at time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*),min(range_start) FROM almanac_derived_dirty_ranges WHERE sli_id=$1`, f.sliID).Scan(&count, &at); err != nil || count != 1 || !at.Equal(first.HourStart) {
		t.Fatalf("source prefix publication count=%d at=%v: %v", count, at, err)
	}
}
