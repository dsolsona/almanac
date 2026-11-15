//go:build integration

package storage

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Compare two concrete built-in index strategies before adopting a persistent
// compatibility protocol. This is a query-shape experiment, not fleet acceptance.
func TestIntegrationMaintenanceCompatibilityIndexPrototype(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	if _, err := conn.Exec(t.Context(), `
 CREATE TEMP TABLE maintenance_compat_probe (
  slo_id uuid NOT NULL, scope_ordinal bigint NOT NULL, revision bigint NOT NULL,
  input_range tstzrange NOT NULL,
  scope_key int8range GENERATED ALWAYS AS (int8range(scope_ordinal,scope_ordinal,'[]')) STORED,
  revision_key int8range GENERATED ALWAYS AS (int8range(revision,revision,'[]')) STORED
 );
 INSERT INTO maintenance_compat_probe(slo_id,scope_ordinal,revision,input_range)
 SELECT md5(s::text)::uuid,s,r,tstzrange(
  '2026-01-01Z'::timestamptz+(r+CASE WHEN s=200 THEN 2000 ELSE 0 END)*interval '1 hour',
  '2026-01-01Z'::timestamptz+(r+CASE WHEN s=200 THEN 2000 ELSE 0 END+3)*interval '1 hour','[)')
 FROM generate_series(1,200) s CROSS JOIN generate_series(1,1000) r;
 CREATE INDEX maintenance_compat_scope_revision ON maintenance_compat_probe(slo_id,revision);
 CREATE INDEX maintenance_compat_window_revision ON maintenance_compat_probe USING gist(input_range,revision_key);
 ANALYZE maintenance_compat_probe;`); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	cases := []struct {
		name                  string
		scope, after, through int64
		start, end            time.Time
		want                  bool
	}{
		{"relevant", 199, 0, 1000, start, start.Add(time.Hour), true},
		{"only_foreign_scope", 200, 0, 1000, start, start.Add(time.Hour), false},
		{"later_revisions", 199, 900, 1000, start, start.Add(time.Hour), false},
		{"future_range", 199, 0, 1000, start.Add(4000 * time.Hour), start.Add(4001 * time.Hour), false},
		{"scope_local_range", 200, 0, 1000, start.Add(2000 * time.Hour), start.Add(2001 * time.Hour), true},
	}
	for _, strategy := range []string{"two_dimensions", "three_dimensions"} {
		if strategy == "three_dimensions" {
			if _, err := conn.Exec(t.Context(), `DROP INDEX maintenance_compat_window_revision;
 CREATE INDEX maintenance_compat_scope_window_revision ON maintenance_compat_probe USING gist(scope_key,input_range,revision_key);
 ANALYZE maintenance_compat_probe;`); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range cases {
			t.Run(strategy+"/"+c.name, func(t *testing.T) {
				statement := `SELECT EXISTS(SELECT 1 FROM maintenance_compat_probe
 WHERE slo_id=md5(($1::bigint)::text)::uuid AND scope_key @> $1::bigint
 AND revision>$2 AND revision<=$3
 AND revision_key && int8range($2::bigint+1,$3::bigint,'[]')
 AND input_range && tstzrange($4,$5,'[)') LIMIT 1)`
				args := []any{c.scope, c.after, c.through, c.start, c.end}
				var got bool
				if err := conn.QueryRow(t.Context(), statement, args...).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != c.want {
					t.Fatalf("changed=%v want=%v", got, c.want)
				}
				var raw []byte
				if err := conn.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+statement, args...).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var plan []struct {
					Plan      map[string]any `json:"Plan"`
					Execution float64        `json:"Execution Time"`
				}
				if err := json.Unmarshal(raw, &plan); err != nil || len(plan) != 1 {
					t.Fatalf("plan: %v", err)
				}
				var visits float64
				var indexes []string
				var inspect func(map[string]any)
				inspect = func(node map[string]any) {
					number := func(key string) float64 { n, _ := node[key].(float64); return n }
					if name, ok := node["Index Name"].(string); ok {
						indexes = append(indexes, name)
					}
					if node["Relation Name"] == "maintenance_compat_probe" || node["Index Name"] != nil {
						visits += (number("Actual Rows") + number("Rows Removed by Filter") + number("Rows Removed by Index Recheck")) * number("Actual Loops")
					}
					if node["Node Type"] == "Seq Scan" {
						t.Error("planner scanned the compatibility history")
					}
					if children, ok := node["Plans"].([]any); ok {
						for _, child := range children {
							if nested, ok := child.(map[string]any); ok {
								inspect(nested)
							}
						}
					}
				}
				inspect(plan[0].Plan)
				t.Logf("%s/%s: %.3f ms, candidate visits %.0f, indexes %v", strategy, c.name, plan[0].Execution, visits, indexes)
				path := filepath.Join("..", "..", ".local", fmt.Sprintf("maintenance-compat-%s-%s.json", strategy, c.name))
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
