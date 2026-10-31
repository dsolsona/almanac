//go:build integration

package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
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
