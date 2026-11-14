//go:build integration

package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

// Counts real canonical reads: pending work must not execute sums/raw edges.
// Every statement that does execute still goes to the fixture PostgreSQL.
type preflightQueryCounter struct {
	dbgen.DBTX
	canonical      int
	afterReadiness func()
}

func (c *preflightQueryCounter) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	row := c.DBTX.QueryRow(ctx, sql, args...)
	if strings.HasPrefix(sql, "-- name: ReadDerivedWindowReadiness :one\n") && c.afterReadiness != nil {
		return preflightSnapshotRow{Row: row, after: c.afterReadiness}
	}
	return row
}

type preflightSnapshotRow struct {
	pgx.Row
	after func()
}

func (r preflightSnapshotRow) Scan(dest ...any) error {
	if err := r.Row.Scan(dest...); err != nil {
		return err
	}
	r.after()
	return nil
}

func (c *preflightQueryCounter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.HasPrefix(sql, "-- name: ReadRetainedWindowBatch :many\n") {
		c.canonical++
	}
	return c.DBTX.Query(ctx, sql, args...)
}

func TestIntegrationFirstWindowReadiness(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "first-window", true)
	s.drain(t, "ready first-window source")
	end := s.historyStart.Truncate(time.Hour).Add(2*time.Hour + 30*time.Minute)
	base := slo.WindowInput{At: end, Now: end.Add(time.Minute), Duration: 2 * time.Hour, Cadence: time.Minute,
		ValidFrom: s.historyStart, SliValidFrom: s.historyStart, RetentionStart: s.historyStart, Target: "0.99"}
	for _, test := range []struct {
		name     string
		dirty    time.Time
		attempts int
		lease    bool
		clip     string
		want     error
	}{
		{name: "clean"},
		{name: "dirty interior", dirty: end.Truncate(time.Hour).Add(-time.Hour), want: ErrDerivedPending},
		{name: "exhausted interior", dirty: end.Truncate(time.Hour).Add(-time.Hour), attempts: 5, want: ErrDerivedSourceFailed},
		{name: "leased final attempt", dirty: end.Truncate(time.Hour).Add(-time.Hour), attempts: 5, lease: true, want: ErrDerivedPending},
		{name: "raw edge", dirty: end.Truncate(time.Hour)},
		{name: "eligibility clips dirty hour", dirty: end.Truncate(time.Hour).Add(-time.Hour), clip: "valid"},
		{name: "retention clips dirty hour", dirty: end.Truncate(time.Hour).Add(-time.Hour), clip: "retention"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := s.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			input := base
			if test.clip == "valid" {
				input.ValidFrom = end.Truncate(time.Hour)
			}
			if test.clip == "retention" {
				input.RetentionStart = end.Truncate(time.Hour)
			}
			if !test.dirty.IsZero() {
				var lease *time.Time
				if test.lease {
					value := input.Now.Add(time.Minute)
					lease = &value
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,attempts,lease_until,next_attempt_at,updated_at)
VALUES ($1,1,1,$2,$2::timestamptz+interval '1 hour',$3,$4,$5,$5)`, s.sliID, test.dirty, test.attempts, lease, input.Now); err != nil {
					t.Fatal(err)
				}
			}
			counter := &preflightQueryCounter{DBTX: tx}
			got, err := evaluateDerivedWindows(t.Context(), dbgen.New(counter), mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
			if test.want != nil {
				if !errors.Is(err, test.want) || (test.want == ErrDerivedPending && errors.Is(err, ErrDerivedSourceFailed)) {
					t.Fatalf("wrong pending/failure semantics: %v", err)
				}
				if counter.canonical != 0 {
					t.Fatalf("known-pending first window performed %d canonical sums/edge reads", counter.canonical)
				}
				return
			}
			if err != nil || counter.canonical != 1 {
				t.Fatalf("ready first window: reads=%d err=%v", counter.canonical, err)
			}
			params, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := dbgen.New(tx).ReadRetainedWindowBatch(t.Context(), params)
			if err != nil || len(rows) != 1 {
				t.Fatalf("canonical reference: %v", err)
			}
			r := rows[0]
			want, err := evaluateDerivedCounts(t.Context(), input, dbgen.ReadDerivedWindowTotalsRow{Good: r.Good, Total: r.Total, Observed: r.Observed, KnownEmpty: r.KnownEmpty,
				ZeroTraffic: r.ZeroTraffic, FirstObservation: r.FirstObservation, LastObservation: r.LastObservation, LatestReceipt: r.LatestReceipt, Prepared: r.Prepared, Exhausted: r.Exhausted})
			if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Fatalf("fast path changed canonical results: %v", err)
			}
		})
	}
}

func TestIntegrationFirstWindowReadinessPreservesEarlierFactsFailure(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "first-facts", true)
	s.drain(t, "ready facts source")
	end := s.at.Truncate(time.Hour)
	tx, err := s.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `UPDATE public.almanac_sli_hourly_aggregates SET good=0,total=0,zero_traffic_intervals=0 WHERE sli_id=$1 AND hour_start=$2`, s.sliID, end.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
VALUES ($1,1,1,$2,$2::timestamptz+interval '1 hour',$3,$3)`, s.sliID, end.Add(-2*time.Hour), end); err != nil {
		t.Fatal(err)
	}
	first := slo.WindowInput{At: end, Now: end, Duration: time.Hour, Cadence: time.Minute, ValidFrom: s.historyStart, SliValidFrom: s.historyStart, Target: "0.99"}
	second := first
	second.Duration = 2 * time.Hour
	inputs := []slo.WindowInput{first, second}
	params, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, inputs)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := dbgen.New(tx).ReadRetainedWindowBatch(t.Context(), params)
	if err != nil || len(rows) != 2 || !rows[0].Prepared || rows[1].Prepared {
		t.Fatalf("expected first ready and later pending: %+v %v", rows, err)
	}
	counter := &preflightQueryCounter{DBTX: tx}
	_, err = evaluateDerivedWindows(t.Context(), dbgen.New(counter), mustPGUUID(s.sliID), 1, inputs)
	if !errors.Is(err, slo.ErrOperationalFacts) || counter.canonical != 1 {
		t.Fatalf("later pending hid earlier inconsistent facts: %v reads=%d", err, counter.canonical)
	}
}

func TestIntegrationFirstWindowReadinessCrossHourCadence(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixtureCadence(t, "first-70s", true, 70*time.Second)
	s.drain(t, "70-second source")
	end := ceilSliBoundary(s.historyStart, 7*time.Hour).Add(7 * time.Hour)
	input := slo.WindowInput{At: end, Now: end, Duration: 14 * time.Hour, Cadence: s.cadence, ValidFrom: s.historyStart, SliValidFrom: s.historyStart, Target: "0.99"}
	params, err := derivedWindowBatchParams(mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
	if err != nil || !params.FullStarts[0].Time.Equal(end.Add(-7*time.Hour)) || !params.FullEnds[0].Time.Equal(end) {
		t.Fatalf("expected 7-hour aligned interior: %+v %v", params, err)
	}
	tx, err := s.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
VALUES ($1,1,1,$2,$2::timestamptz+interval '1 hour',$3,$3)`, s.sliID, end.Add(-time.Hour), end); err != nil {
		t.Fatal(err)
	}
	counter := &preflightQueryCounter{DBTX: tx}
	_, err = evaluateDerivedWindows(t.Context(), dbgen.New(counter), mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
	if !errors.Is(err, ErrDerivedPending) || counter.canonical != 0 {
		t.Fatalf("70-second pending interior performed canonical work: %v reads=%d", err, counter.canonical)
	}
	// The same dirty hour is a raw edge in a sub-grid window, so it cannot gate it.
	input.Duration = (6 * time.Hour / s.cadence) * s.cadence
	_, err = evaluateDerivedWindows(t.Context(), dbgen.New(counter), mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
	if err != nil || counter.canonical != 1 {
		t.Fatalf("70-second raw-only window was blocked: %v reads=%d", err, counter.canonical)
	}
}

func TestIntegrationFirstWindowReadinessSnapshotAndCapture(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "first-snapshot", true)
	s.drain(t, "snapshot source")
	id := s.createSLOWithHistory(t, "first-snapshot", 2*time.Hour, s.historyStart)
	end := s.at.Truncate(time.Hour)
	// The existing plan helper must get past readiness to capture the canonical query.
	logRetainedRepairPlan(t, s.conn, id, end.Add(-30*time.Minute), end, s.now)
	input := slo.WindowInput{At: end, Now: s.now, Duration: 2 * time.Hour, Cadence: time.Minute, ValidFrom: s.historyStart, SliValidFrom: s.historyStart, Target: "0.99"}
	tx, err := s.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	writer := integrationConnect(t, s.f.databaseURL)
	defer closeIntegrationConn(writer)
	changed := false
	counter := &preflightQueryCounter{DBTX: tx, afterReadiness: func() {
		if _, err := writer.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
VALUES ($1,1,1,$2,$2::timestamptz+interval '1 hour',$3,$3)`, s.sliID, end.Add(-time.Hour), s.now); err != nil {
			t.Fatal(err)
		}
		changed = true
	}}
	_, err = evaluateDerivedWindows(t.Context(), dbgen.New(counter), mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
	if err != nil || !changed || counter.canonical != 1 {
		t.Fatalf("calculation mixed preflight and concurrent source snapshots: %v changed=%v reads=%d", err, changed, counter.canonical)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := s.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(next)
	counter = &preflightQueryCounter{DBTX: next}
	_, err = evaluateDerivedWindows(t.Context(), dbgen.New(counter), mustPGUUID(s.sliID), 1, []slo.WindowInput{input})
	if !errors.Is(err, ErrDerivedPending) || counter.canonical != 0 {
		t.Fatalf("fresh snapshot did not see new dirty work: %v reads=%d", err, counter.canonical)
	}
}
