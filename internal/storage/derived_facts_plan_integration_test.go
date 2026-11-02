//go:build integration

package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

var errFactsQueryCaptured = errors.New("test: operational facts query captured")

// Capture SQLC's actual statement without executing a second raw read or keeping
// a separately optimized SQL copy in the regression fixture.
type factsQueryCapture struct {
	dbgen.DBTX
	statement string
	arguments []any
}

func logRetainedRepairPlan(t *testing.T, conn *pgx.Conn, id string, start, end, now time.Time) {
	t.Helper()
	q := dbgen.New(conn)
	row, err := q.GetSlo(t.Context(), mustPGUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	source, err := q.GetSloOperationalContext(t.Context(), dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		t.Fatal(err)
	}
	capture := &factsQueryCapture{}
	_, err = buildRetainedRepairPoints(t.Context(), dbgen.New(capture), row, source, start, end, now)
	// Production error sanitization strips the capture marker; verify the actual SQL.
	if err == nil || !strings.HasPrefix(capture.statement, "-- name: ReadRetainedWindowBatch :many\n") {
		t.Fatal("capture retained repair query failed", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE public.almanac_sli_coverage; ANALYZE public.almanac_sli_observation_values; ANALYZE public.almanac_sli_hourly_aggregates`); err != nil {
		t.Fatal("analyze retained fixture before query-plan acceptance", err)
	}
	var plan []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+capture.statement, capture.arguments...).Scan(&plan); err != nil {
		t.Fatal("retained repair plan failed", err)
	}
	path := filepath.Join("..", "..", ".local", "retained-repair-plan-"+id+".json")
	if err := os.WriteFile(path, plan, 0o600); err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		Plan      map[string]any `json:"Plan"`
		Planning  float64        `json:"Planning Time"`
		Execution float64        `json:"Execution Time"`
	}
	if err := json.Unmarshal(plan, &decoded); err != nil || len(decoded) != 1 {
		t.Fatal("invalid retained repair plan", err)
	}
	type work struct {
		Node, Relation               string
		Rows, Loops, Removed, Visits float64
	}
	var expensive []work
	var coverageBlocks float64
	var inspect func(map[string]any)
	inspect = func(node map[string]any) {
		name, _ := node["Node Type"].(string)
		relation, _ := node["Relation Name"].(string)
		if strings.HasPrefix(relation, "almanac_sli_coverage") {
			hits, _ := node["Shared Hit Blocks"].(float64)
			reads, _ := node["Shared Read Blocks"].(float64)
			coverageBlocks += hits + reads
		}
		rows, _ := node["Actual Rows"].(float64)
		loops, _ := node["Actual Loops"].(float64)
		removed, _ := node["Rows Removed by Join Filter"].(float64)
		expensive = append(expensive, work{name, relation, rows, loops, removed, (rows + removed) * loops})
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				if next, ok := child.(map[string]any); ok {
					inspect(next)
				}
			}
		}
	}
	inspect(decoded[0].Plan)
	slices.SortFunc(expensive, func(a, b work) int {
		if a.Visits > b.Visits {
			return -1
		}
		if a.Visits < b.Visits {
			return 1
		}
		return 0
	})
	t.Logf("retained repair plan planning=%.3fms execution=%.3fms coverage_blocks=%.0f expensive=%+v artifact=%s", decoded[0].Planning, decoded[0].Execution, coverageBlocks, expensive[:min(8, len(expensive))], path)
	// Four windows share bounded raw edges across the batch's evaluation points.
	// Allow index/heap overhead without allowing a retained-table scan per edge.
	points := int64(end.Sub(start) / (time.Duration(row.CadenceSeconds) * time.Second))
	if bound := float64(32 * (4*points + 1)); coverageBlocks > bound {
		t.Fatalf("retained repair coverage access grows beyond bounded edges: %.0f blocks > %.0f", coverageBlocks, bound)
	}
}

func (c *factsQueryCapture) Query(_ context.Context, statement string, arguments ...any) (pgx.Rows, error) {
	c.statement, c.arguments = statement, append([]any(nil), arguments...)
	return nil, errFactsQueryCaptured
}

func logRetainedFactsPlan(t *testing.T, conn *pgx.Conn, id string, start, end time.Time, cadence int) {
	t.Helper()
	capture := &factsQueryCapture{}
	_, err := dbgen.New(capture).ListOperationalSloFacts(t.Context(), dbgen.ListOperationalSloFactsParams{
		SliID: mustPGUUID(id), MeasurementRevision: 1, IntervalStart: timestamp(start), IntervalEnd: timestamp(end), Limit: sloOperationalFactsLimit + 1,
	})
	if !errors.Is(err, errFactsQueryCaptured) || capture.statement == "" {
		t.Fatal("capture retained operational fact statement failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal("begin retained operational fact plan failed")
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off`); err != nil {
		t.Fatal("configure retained operational fact plan failed")
	}
	var plan []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON,SETTINGS) "+capture.statement, capture.arguments...).Scan(&plan); err != nil {
		t.Fatal("explain retained operational fact statement failed")
	}
	t.Logf("retained facts plan cadence=%d: %s", cadence, plan)
	var decoded []struct {
		Plan map[string]any `json:"Plan"`
	}
	if json.Unmarshal(plan, &decoded) != nil || len(decoded) != 1 {
		t.Fatal("retained operational fact plan was invalid")
	}
	valueLookups := 0
	var inspect func(map[string]any)
	inspect = func(node map[string]any) {
		if relation, _ := node["Relation Name"].(string); strings.HasPrefix(relation, "almanac_sli_observation_values") {
			condition, _ := node["Index Cond"].(string)
			if !strings.Contains(condition, "c.interval_start") {
				t.Fatal("retained fact join rescans a value range instead of looking up the outer interval")
			}
			valueLookups++
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				if next, ok := child.(map[string]any); ok {
					inspect(next)
				}
			}
		}
	}
	inspect(decoded[0].Plan)
	if valueLookups == 0 {
		t.Fatal("retained operational fact plan omitted value lookup evidence")
	}
}
