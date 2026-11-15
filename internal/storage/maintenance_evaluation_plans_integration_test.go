//go:build integration

package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type evaluationRecordedQuery struct {
	sql  string
	args []any
}
type evaluationQueryTx struct {
	pgx.Tx
	calls []evaluationRecordedQuery
}

func (q *evaluationQueryTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.calls = append(q.calls, evaluationRecordedQuery{sql, args})
	return q.Tx.Query(ctx, sql, args...)
}
func (q *evaluationQueryTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.calls = append(q.calls, evaluationRecordedQuery{sql, args})
	return q.Tx.QueryRow(ctx, sql, args...)
}
func TestIntegrationMaintenanceEvaluationBoundedPlans(t *testing.T) {
	t.Parallel()
	f, h := hourFixture(t)
	for i := 1; i < 3; i++ {
		m := f.input("hour-" + time.Duration(i).String())
		m.Start = h.HourStart.Add(time.Duration(i)*time.Hour + 10*time.Minute)
		m.End = m.Start.Add(10 * time.Minute)
		if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m); err != nil {
			t.Fatal(err)
		}
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 10)
	for i := range 3 {
		p := h
		p.HourStart = h.HourStart.Add(time.Duration(i) * time.Hour)
		p.BuildID = b.ID
		if err := f.conn.QueryRow(t.Context(), `SELECT source_generation FROM almanac_sli_hourly_aggregates WHERE sli_id=$1 AND hour_start=$2`, f.sliID, p.HourStart).Scan(&p.SourceGeneration); err != nil {
			t.Fatal(err)
		}
		if _, err := hourPrepare(t, f, p); err != nil {
			t.Fatal(err)
		}
	}
	future := f.input("future-mask")
	future.Start = f.at.Add(time.Hour)
	future.End = future.Start.Add(time.Hour)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, future); err != nil {
		t.Fatal(err)
	}
	in := evaluationFixtureInput(f, h)
	in.Requests = in.Requests[1:]
	in.Requests[0].RequestedRevision = 4
	in.Requests[0].Windows[0].At = h.HourStart.Add(3 * time.Hour)
	in.Requests[0].Windows[0].Duration = 3 * time.Hour
	var candidate evaluationRecordedQuery
	run := func() *evaluationQueryTx {
		tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		q := &evaluationQueryTx{Tx: tx}
		got, err := evaluateMaintenanceWindows(t.Context(), q, in)
		if err != nil {
			rollbackTransaction(tx)
			t.Fatal(err)
		}
		if len(got.Masks[0].Windows) != 1 {
			t.Fatal("no result")
		}
		count := 0
		for _, call := range q.calls {
			if strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationCandidate ") {
				candidate = call
			}
			if strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationScopedHours ") {
				count++
				if len(call.args) == 0 || len(call.args[0].([]pgtype.Timestamptz)) != 3 {
					t.Fatal("partial hours not batched")
				}
			}
		}
		if count != 1 {
			t.Fatalf("three partial hours performed %d queries", count)
		}
		return q
	}
	first := run()
	rollbackTransaction(first.Tx)
	current := compilerCapture(t, f, nil)
	compilerFinish(t, f, current.ID, 10)
	second := run()
	defer rollbackTransaction(second.Tx)
	if candidate.sql == "" {
		t.Fatal("candidate branch was not exercised")
	}
	if _, err := second.Tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	check := func(call evaluationRecordedQuery, index string) {
		var plan string
		if err := second.Tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON, COSTS OFF) "+call.sql, call.args...).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, index) {
			t.Fatalf("indexed bound missing %s: %s", index, plan)
		}
		t.Logf("available indexed path %s (small fixture, sequential scans disabled; no capacity claim)", index)
	}
	check(candidate, "almanac_maintenance_complete_candidate_idx")
	for _, call := range second.calls {
		if strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationScopedHours ") {
			check(call, "almanac_maintenance_hour_candidate_idx")
		}
	}
	// Same canonical SQL path, no raw preflight query for revision zero.
	in.Requests[0].RequestedRevision = 0
	zero := &evaluationQueryTx{Tx: second.Tx}
	if _, err := evaluateMaintenanceWindows(t.Context(), zero, in); err != nil {
		t.Fatal(err)
	}
	canonical := 0
	for _, call := range zero.calls {
		if strings.Contains(call.sql, "-- name: ReadMaintenanceEvaluationEdges ") {
			t.Fatal("canonical path gained raw preflight")
		}
		if strings.Contains(call.sql, "-- name: ReadRetainedWindowBatch ") {
			canonical++
		}
	}
	if canonical != 1 {
		t.Fatalf("canonical batch calls %d", canonical)
	}
}
