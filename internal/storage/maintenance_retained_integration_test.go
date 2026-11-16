//go:build integration

package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
)

func TestIntegrationMaintenanceRetainedStoredInputs(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Summary, in.RetainFacts = true, true
	result, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	window := in.Requests[0].Windows[0]
	want := evaluationRawWindow(t, window, []slo.ExclusionInterval{{Start: h.HourStart.Add(10 * time.Minute), End: h.HourStart.Add(20 * time.Minute)}})
	lane, err := makeExtremaSummaryLane(result.Masks[0].Windows, h.HourStart, h.HourStart.Add(time.Hour), window.Target, "budget", "1")
	if err != nil || len(lane.Marks) != 1 {
		t.Fatalf("selected mark: %+v %v", lane, err)
	}
	mark, err := lane.Marks[0].withMaintenanceInputs(in, result, 0, 0)
	if err != nil || mark.Maintenance == nil || mark.Maintenance.BuildID != h.BuildID.String() {
		t.Fatalf("selected replay inputs: %+v %v", mark.Maintenance, err)
	}
	encoded, err := marshalReceiptMetadata(mark)
	if err != nil {
		t.Fatal(err)
	}
	// Keep only the retained mark. Neither its certificate nor its eligible/
	// excluded population can depend on caller-owned batch state or raw rows.
	result.Masks[0].Facts[0].Totals.Good.SetInt64(0)
	result.Masks[0].Facts[0].Coverage.Points[0].ExcludedBefore = 999
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start>=$2 AND interval_start<$2::timestamptz+interval '1 hour'`, f.sliID, h.HourStart); err != nil {
		t.Fatal(err)
	}
	var saved extremaMark
	if err := unmarshalReceiptMetadata(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	got, err := saved.result(t.Context(), window)
	if err != nil || !reflect.DeepEqual(got, want) || saved.Maintenance.SourceGeneration != result.SourceGeneration || saved.Maintenance.Scope.SloID != in.SloID || saved.Maintenance.ComputedRevision != 1 || saved.Maintenance.RequestedRevision != 1 {
		t.Fatalf("persisted replay diverged after raw expiry: %+v %v want %+v", got, err, want)
	}
	if _, err := mark.result(t.Context(), window); err != nil {
		t.Fatalf("retained inputs alias source batch: %v", err)
	}
}

func TestIntegrationMaintenanceRetainedSelectedLane(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	if _, err := hourPrepare(t, f, h); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	base := in.Requests[0].Windows[0]
	in.Requests[0].Windows = nil
	var indices []int
	for i := range 60 {
		window := base
		window.At = h.HourStart.Add(time.Duration(i+1) * time.Minute)
		in.Requests[0].Windows = append(in.Requests[0].Windows, window)
		indices = append(indices, i)
	}
	in.Summary, in.RetainFacts = true, true
	result, err := evaluateFixtureMaintenance(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	result.Masks[0].Unavailable[30] = "maintenance_history_unavailable"
	result.Masks[0].Windows[30] = slo.WindowResult{}
	result.Masks[0].Facts[30] = maintenanceWindowFacts{}
	lane, err := makeMaintenanceExtremaLane(in, result, 0, indices, h.HourStart, h.HourStart.Add(time.Hour), "budget", "1")
	if err != nil || len(lane.Marks) < 3 || len(lane.Marks) >= len(indices) {
		t.Fatalf("selected covered lane: %+v %v", lane, err)
	}
	encoded, err := marshalReceiptMetadata(lane)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start>=$2 AND interval_start<$2::timestamptz+interval '1 hour'`, f.sliID, h.HourStart); err != nil {
		t.Fatal(err)
	}
	var saved extremaLane
	if err := unmarshalReceiptMetadata(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	replay := base
	samples, err := saved.samples(t.Context(), replay, h.HourStart, h.HourStart.Add(time.Hour), "1", "budget")
	if err != nil || len(samples) != len(saved.Marks) {
		t.Fatalf("selected replay after raw expiry: %d %v", len(samples), err)
	}
	gap := false
	for i, mark := range saved.Marks {
		if mark.Maintenance == nil {
			t.Fatal("selected point lost scoped inputs")
		}
		if mark.At.Equal(h.HourStart.Add(31 * time.Minute)) {
			gap = true
			if mark.Maintenance.UnavailableReason != "maintenance_history_unavailable" || samples[i].Quality.State != "unavailable" || samples[i].Good != nil || samples[i].Burn != nil {
				t.Fatalf("gap invented a plotted value: %+v", samples[i])
			}
			continue
		}
		var original slo.WindowInput
		for _, window := range in.Requests[0].Windows {
			if window.At.Equal(mark.At) {
				original = window
				break
			}
		}
		want := result.Masks[0].Windows[int(mark.At.Sub(h.HourStart)/time.Minute)-1].WithValues()
		got, err := mark.result(t.Context(), original)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("selected point changed: %+v %v want %+v", got, err, want)
		}
	}
	if !gap {
		t.Fatal("reduction hid terminal unavailable interval")
	}
}
