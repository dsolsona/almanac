//go:build integration

package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type compatibilityQueryTx struct {
	pgx.Tx
	sql  string
	args []any
}

func (c *compatibilityQueryTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.sql = sql
	c.args = args
	return c.Tx.QueryRow(ctx, sql, args...)
}

func TestIntegrationMaintenanceCompatibilityGeneratedSeek(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("plan")
	input.SloIDs = f.ids
	r, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	// Physical index fixture, not an analyzed-history/capacity acceptance claim.
	// The logical event/old-new arithmetic is covered independently in Events.
	if _, err := tx.Exec(t.Context(), `ALTER TABLE almanac_slo_exclusion_changes DISABLE TRIGGER almanac_maintenance_change_analyze`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
 INSERT INTO almanac_slo_exclusion_changes(slo_id,revision,exclusion_id,exclusion_revision,included,coverage_changed,recorded_at)
 SELECT a.slo_id,n,$1,1,true,true,$2 FROM almanac_maintenance_compatibility a CROSS JOIN generate_series(2,2001) n;
 `, r.ID, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
 INSERT INTO almanac_maintenance_change_sets(slo_id,revision,scope_ordinal,range_count)
 SELECT a.slo_id,n,a.scope_ordinal,1 FROM almanac_maintenance_compatibility a CROSS JOIN generate_series(2,2001) n;
 INSERT INTO almanac_maintenance_changed_inputs(slo_id,revision,part,scope_ordinal,input_range)
 SELECT a.slo_id,n,1,a.scope_ordinal,tstzrange('2026-01-01Z'::timestamptz+(n+a.scope_ordinal*10000)*interval '1 hour','2026-01-01Z'::timestamptz+(n+a.scope_ordinal*10000+1)*interval '1 hour','[)')
 FROM almanac_maintenance_compatibility a CROSS JOIN generate_series(2,2001) n;
 ANALYZE almanac_maintenance_changed_inputs;`); err != nil {
		t.Fatal(err)
	}
	var ordinal int64
	if err := tx.QueryRow(t.Context(), `SELECT scope_ordinal FROM almanac_maintenance_compatibility WHERE slo_id=$1`, f.ids[0]).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	recorder := &compatibilityQueryTx{Tx: tx}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(ordinal*10000+1500) * time.Hour)
	changed, err := dbgen.New(recorder).HasMaintenanceChangedInput(t.Context(), dbgen.HasMaintenanceChangedInputParams{SloID: mustPGUUID(f.ids[0]), ScopeOrdinal: ordinal, AfterRevision: 1499, ThroughRevision: 1500, WindowStart: timestamp(at), WindowEnd: timestamp(at.Add(time.Hour))})
	if err != nil || !changed {
		t.Fatalf("indexed hit:%v %v", changed, err)
	}
	rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF) "+recorder.sql, recorder.args...)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	t.Log(plan)
	if !strings.Contains(plan, "almanac_maintenance_compatibility_gist") || strings.Contains(plan, "Seq Scan") {
		t.Fatalf("planner did not choose scoped GiST:\n%s", plan)
	}
}
