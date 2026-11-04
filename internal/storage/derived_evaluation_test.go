package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestRetainedRepairFillsBoundedCalculationBatch(t *testing.T) {
	for _, test := range []struct{ cadence, span time.Duration }{
		{15 * time.Second, time.Hour}, {30 * time.Second, 2 * time.Hour}, {time.Minute, 4 * time.Hour},
		{5 * time.Minute, 20 * time.Hour}, {17 * time.Minute, 23*time.Hour + 48*time.Minute},
		{90 * time.Minute, 24 * time.Hour}, {24 * time.Hour, 24 * time.Hour}, {36 * time.Hour, 36 * time.Hour},
	} {
		window := 28 * 24 * time.Hour
		span := retainedRepairSpan(window, test.cadence)
		inputs := int(span/test.cadence) * len(retainedRepairDurations(window, test.cadence))
		if span != test.span || span%test.cadence != 0 || inputs > derivedWindowBatchLimit || inputs < 1 {
			t.Fatalf("cadence %s: span=%s inputs=%d; want %s within %d inputs", test.cadence, span, inputs, test.span, derivedWindowBatchLimit)
		}
	}
}

func TestCurrentWindowBatchPreservesEachExactBoundary(t *testing.T) {
	end := time.Date(2026, 11, 1, 12, 34, 0, 0, time.UTC)
	now := end.Add(10 * time.Second)
	id := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	inputs := []slo.WindowInput{}
	for _, duration := range []time.Duration{28 * 24 * time.Hour, time.Hour, 6 * time.Hour} {
		inputs = append(inputs, slo.WindowInput{At: end, Now: now, Duration: duration, Cadence: time.Minute})
	}
	params, err := derivedWindowBatchParams(id, 3, inputs)
	if err != nil || params.SliID != id || params.MeasurementRevision != 3 || !params.Now.Time.Equal(now) || len(params.WindowEnds) != 3 {
		t.Fatalf("current window batch lost its source/clock scope: %v", err)
	}
	starts := []time.Time{time.Date(2026, 10, 4, 12, 34, 0, 0, time.UTC), end.Add(-time.Hour), end.Add(-6 * time.Hour)}
	for index, start := range starts {
		fullStart := start.Truncate(time.Hour).Add(time.Hour)
		fullEnd := end.Truncate(time.Hour)
		if !params.WindowEnds[index].Time.Equal(end) || !params.FullStarts[index].Time.Equal(fullStart) || !params.FullEnds[index].Time.Equal(fullEnd) ||
			!params.EdgeAStarts[index].Time.Equal(start) || !params.EdgeAEnds[index].Time.Equal(fullStart) || !params.EdgeBStarts[index].Time.Equal(fullEnd) || !params.EdgeBEnds[index].Time.Equal(end) {
			t.Fatalf("current/burn window %d changed exact source boundaries", index)
		}
	}
	for index := range inputs {
		inputs[index].At = end.Truncate(time.Hour)
	}
	params, err = derivedWindowBatchParams(id, 3, inputs)
	if err != nil {
		t.Fatal(err)
	}
	for index := range inputs {
		at := inputs[index].At
		if !params.EdgeAStarts[index].Time.Equal(at) || !params.EdgeAEnds[index].Time.Equal(at) || !params.EdgeBStarts[index].Time.Equal(at) || !params.EdgeBEnds[index].Time.Equal(at) {
			t.Fatal("aligned windows gained a spurious raw edge")
		}
	}
}

func TestCurrentWindowBatchRejectsMixedClockAndUnboundedInput(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	base := slo.WindowInput{At: at, Now: at, Duration: time.Hour, Cadence: time.Minute}
	for _, mutate := range []func(*slo.WindowInput){
		func(value *slo.WindowInput) { value.Now = at.Add(time.Second) },
		func(value *slo.WindowInput) { value.Cadence = 17 * time.Second },
		func(value *slo.WindowInput) { value.Cadence = 0 },
		func(value *slo.WindowInput) { value.Duration = time.Second },
	} {
		inputs := []slo.WindowInput{base, base}
		mutate(&inputs[1])
		if _, err := derivedWindowBatchParams(pgtype.UUID{}, 1, inputs); !errors.Is(err, slo.ErrInvalidCalculation) {
			t.Fatalf("incoherent window batch accepted: %v", err)
		}
	}
	if _, err := derivedWindowBatchParams(pgtype.UUID{}, 1, make([]slo.WindowInput, 961)); !errors.Is(err, ErrOperationalResource) {
		t.Fatalf("oversized window batch accepted: %v", err)
	}
}

func TestDerivedHourEdgesStayExactForIrregularAndLongCadences(t *testing.T) {
	for _, seconds := range []int{15, 17, 60, 90, 420, 3600, 3601, 86399, 86400} {
		cadence := time.Duration(seconds) * time.Second
		for _, instant := range []time.Time{time.Date(2026, 10, 30, 12, 37, 0, 0, time.UTC), time.Unix(-123456789, 0).UTC()} {
			end := floorSliBoundary(instant, cadence)
			for _, ticks := range []int{1, 17, 7201, 100000} {
				start := end.Add(-time.Duration(ticks) * cadence)
				from, to := derivedAggregateBounds(start, end, cadence)
				if !floorSliBoundary(from, cadence).Equal(from) || !floorSliBoundary(to, cadence).Equal(to) || !from.Truncate(time.Hour).Equal(from) || !to.Truncate(time.Hour).Equal(to) {
					t.Fatalf("cadence %ds produced an inexact full-hour boundary: %s %s", seconds, from, to)
				}
				edges := end.Sub(start)
				if !from.After(to) {
					edges = from.Sub(start) + end.Sub(to)
				}
				if edges/cadence > 7200 {
					t.Fatalf("cadence %ds left %d raw intervals", seconds, edges/cadence)
				}
			}
		}
	}
}
