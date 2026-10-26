//go:build integration

package storage

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"
)

type agentPagePlan struct {
	NodeType   string          `json:"Node Type"`
	IndexName  string          `json:"Index Name"`
	ActualRows float64         `json:"Actual Rows"`
	Plans      []agentPagePlan `json:"Plans"`
}

func TestIntegrationAgentPaginationUpgradeUsesOrderedIndexes(t *testing.T) {
	databaseURL := integrationDatabase(t)
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:5]); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	// Seed the pre-index schema, including retained credential history.
	if _, err := conn.Exec(t.Context(), `
INSERT INTO public.almanac_agents(id,name,display_name,created_at,updated_at)
SELECT md5('page-agent-' || n)::uuid, 'page-agent-' || n, 'Page agent ' || n,
       '2026-01-01T00:00:00Z'::timestamptz + n * interval '1 second',
       '2026-01-01T00:00:00Z'::timestamptz + n * interval '1 second'
FROM generate_series(1,20000) AS n;
INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at,revoked_at)
SELECT md5('page-credential-' || n)::uuid, md5('page-agent-1')::uuid,
       decode(repeat('01',32),'hex'), i.credential_epoch,
       '2026-01-01T00:00:00Z'::timestamptz + n * interval '1 second',
       '2026-01-01T01:00:00Z'::timestamptz + n * interval '1 second',
       '2026-01-01T00:10:00Z'::timestamptz + n * interval '1 second'
FROM generate_series(1,20000) AS n CROSS JOIN public.almanac_installation i
WHERE i.singleton;`); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Upgrade(t.Context(), databaseURL)
	if err != nil || upgraded.Applied != len(migrations)-5 {
		t.Fatalf("upgrade existing pagination data: applied=%d err=%v", upgraded.Applied, err)
	}
	if _, err := conn.Exec(t.Context(), "ANALYZE public.almanac_agents; ANALYZE public.almanac_agent_credentials"); err != nil {
		t.Fatal(err)
	}
	var agents, credentials int
	if err := conn.QueryRow(t.Context(), `SELECT
(SELECT count(*) FROM public.almanac_agents),
(SELECT count(*) FROM public.almanac_agent_credentials)`).Scan(&agents, &credentials); err != nil {
		t.Fatal(err)
	}
	if agents != 20000 || credentials != 20000 {
		t.Fatal("index upgrade changed existing pagination data")
	}
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(15000 * time.Second)
	// These are the production keyset query shapes; the ordinary lifecycle suite
	// separately executes the actual guarded storage list operations.
	for _, test := range []struct {
		name, query, index string
	}{
		{"agents", `SELECT id,name,display_name,enabled,version,created_at,updated_at
FROM public.almanac_agents
WHERE (created_at,id)>($1::timestamptz,'00000000-0000-0000-0000-000000000000'::uuid)
ORDER BY created_at,id LIMIT 51`, "almanac_agents_created_id_idx"},
		{"credential history", `SELECT id,agent_id,issued_at,expires_at,revoked_at
FROM public.almanac_agent_credentials
WHERE agent_id=md5('page-agent-1')::uuid
AND (issued_at,id)>($1::timestamptz,'00000000-0000-0000-0000-000000000000'::uuid)
ORDER BY issued_at,id LIMIT 51`, "almanac_agent_credentials_agent_issued_id_idx"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var raw []byte
			if err := conn.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+test.query, after).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var plans []struct {
				Plan agentPagePlan `json:"Plan"`
			}
			if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
				t.Fatal("invalid PostgreSQL query plan")
			}
			found := false
			var inspect func(agentPagePlan)
			inspect = func(plan agentPagePlan) {
				if strings.Contains(plan.NodeType, "Sort") || plan.NodeType == "Seq Scan" {
					t.Fatalf("page requires unbounded scan or sort: %s", plan.NodeType)
				}
				if plan.IndexName == test.index {
					found = true
					if plan.ActualRows > 51 {
						t.Fatalf("ordered page scanned %.0f rows for limit 51", plan.ActualRows)
					}
				}
				for _, child := range plan.Plans {
					inspect(child)
				}
			}
			inspect(plans[0].Plan)
			if !found || plans[0].Plan.ActualRows != 51 {
				t.Fatal("deep page did not use its ordered index and return the bounded page")
			}
		})
	}
}
