package slo

import (
	"reflect"
	"testing"
	"time"
)

func TestRequiredCoverageAnchors(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 10, 0, 0, time.UTC)
	input := WindowInput{At: at, Duration: 10 * time.Minute, Cadence: time.Minute, ValidFrom: at.Add(-9*time.Minute + time.Second), RetentionStart: at.Add(-7*time.Minute + time.Second)}
	totals := WindowTotals{Observed: 2, FirstObservation: new(at.Add(-5 * time.Minute)), LastObservation: new(at.Add(-2 * time.Minute))}
	got, err := RequiredCoverageAnchors(input, totals, at.Add(-time.Minute))
	want := []time.Time{at.Add(-8 * time.Minute), at.Add(-6 * time.Minute), at.Add(-5 * time.Minute), at.Add(-4 * time.Minute), at.Add(-3 * time.Minute), at.Add(-2 * time.Minute), at.Add(-time.Minute), at}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("anchors %v: %v", got, err)
	}
	totals.FirstObservation = nil
	if _, err := RequiredCoverageAnchors(input, totals, at); err == nil {
		t.Fatal("missing observed bounds accepted")
	}
	input.Cadence = 0
	if _, err := RequiredCoverageAnchors(input, WindowTotals{}, at); err == nil {
		t.Fatal("zero grid accepted")
	}
}

func TestRequiredCoverageAnchorsNineAndOwnership(t *testing.T) {
	at := time.Date(2026, 11, 15, 12, 10, 0, 0, time.UTC)
	input := WindowInput{At: at, Duration: 10 * time.Minute, Cadence: time.Minute, RetentionStart: at.Add(-9 * time.Minute)}
	totals := WindowTotals{Observed: 2, FirstObservation: new(at.Add(-8 * time.Minute)), LastObservation: new(at.Add(-4 * time.Minute))}
	want := []time.Time{}
	for _, minute := range []int{0, 1, 2, 3, 5, 6, 8, 9, 10} {
		want = append(want, at.Add(time.Duration(minute-10)*time.Minute))
	}
	got, err := RequiredCoverageAnchors(input, totals, at.Add(-time.Minute))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("nine anchors: %v %v", got, err)
	}
	got[2] = at
	if !totals.FirstObservation.Equal(at.Add(-8 * time.Minute)) {
		t.Fatal("output aliases observation pointer")
	}
}
