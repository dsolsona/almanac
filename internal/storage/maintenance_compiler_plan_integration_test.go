//go:build integration

package storage

import (
	"strings"
	"testing"
	"time"
)

func TestIntegrationMaintenanceCompilerIndexPaths(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("plan")); err != nil {
		t.Fatal(err)
	}
	b := compilerCapture(t, f, nil)
	compilerFinish(t, f, b.ID, 1)
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	// Small-fixture EXPLAIN is structural evidence only. Disabling seqscan here
	// demonstrates available bounded seek paths, not their production selection.
	if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, sql, index string
		args             []any
	}{
		{"raw events", `SELECT * FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision>0 AND revision<=1 ORDER BY revision LIMIT 1`, "almanac_slo_exclusion_changes_pkey", []any{f.ids[0]}},
		{"sealed members", `SELECT * FROM almanac_maintenance_members WHERE build_id=$1 AND (starts_at,exclusion_id)>($2::timestamptz,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY starts_at,exclusion_id LIMIT 1`, "almanac_maintenance_member_order_idx", []any{b.ID, f.at}},
		{"coverage predecessor", `SELECT * FROM almanac_maintenance_coverage WHERE build_id=$1 AND starts_at<$2 ORDER BY starts_at DESC LIMIT 1`, "almanac_maintenance_coverage_pkey", []any{b.ID, f.at.Add(3 * time.Hour)}},
	} {
		rows, err := tx.Query(t.Context(), "EXPLAIN (COSTS OFF) "+test.sql, test.args...)
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
		t.Logf("%s structural plan:\n%s", test.name, plan)
		if !strings.Contains(plan, test.index) || !strings.Contains(plan, "Limit") {
			t.Fatalf("missing seek path: %s", plan)
		}
	}
}
