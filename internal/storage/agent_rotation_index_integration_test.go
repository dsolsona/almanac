//go:build integration

package storage

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type rotationIndexQueryCapture struct {
	pgx.Tx
	query string
	args  []any
}

func (tx *rotationIndexQueryCapture) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.query = query
	tx.args = append(tx.args[:0], args...)
	return tx.Tx.Query(ctx, query, args...)
}

type rotationIndexPlanNode struct {
	NodeType             string                  `json:"Node Type"`
	RelationName         string                  `json:"Relation Name"`
	IndexName            string                  `json:"Index Name"`
	IndexCond            string                  `json:"Index Cond"`
	ActualRows           float64                 `json:"Actual Rows"`
	ActualLoops          float64                 `json:"Actual Loops"`
	RowsRemovedByFilter  float64                 `json:"Rows Removed by Filter"`
	RowsRemovedByRecheck float64                 `json:"Rows Removed by Index Recheck"`
	Plans                []rotationIndexPlanNode `json:"Plans"`
}

func TestIntegrationAgentRotationIndexSkipsHistoricalCredentials(t *testing.T) {
	databaseURL := integrationDatabase(t)
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:7]); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	const targetAgent = "80000000-0000-4000-8000-000000000001"
	const otherAgent = "80000000-0000-4000-8000-000000000002"
	const actorID = "80000000-0000-4000-8000-000000000003"
	var epoch string
	if err := conn.QueryRow(t.Context(), "SELECT credential_epoch::text FROM public.almanac_installation WHERE singleton").Scan(&epoch); err != nil {
		t.Fatal("read fixture credential epoch failed")
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agents(id,name,display_name,created_at,updated_at) VALUES
($1,'rotation-index-target','Rotation index target',$3,$3),
($2,'rotation-index-other','Rotation index other',$3,$3)`, targetAgent, otherAgent, now.Add(-3*time.Hour)); err != nil {
		t.Fatal("seed agents before index migration failed")
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_users(id,login,password_verifier,role,enabled,must_change_password,created_at,updated_at)
VALUES($1,'rotation-index-admin','fixture-verifier','administrator',true,false,$2,$2)`, actorID, now); err != nil {
		t.Fatal("seed receipt actor failed")
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials
(id,agent_id,verifier,credential_epoch,issued_at,expires_at,revoked_at,retire_at)
SELECT (CASE WHEN n<=2000 THEN '00000000-0000-4000-8000-' ELSE 'ffffffff-ffff-4fff-8fff-' END || lpad((category*10000+n)::text,12,'0'))::uuid,
       $1, decode(repeat('01',32),'hex'),
       CASE WHEN category=4 THEN '77777777-7777-4777-8777-777777777774'::uuid ELSE $2::uuid END,
       $3::timestamptz-interval '2 hours',
       CASE WHEN category=1 THEN $3::timestamptz-interval '1 hour' ELSE $3::timestamptz+interval '1 hour' END,
       CASE WHEN category=3 THEN $3::timestamptz-interval '1 hour' END,
       CASE WHEN category=2 THEN $3::timestamptz-interval '1 hour' END
FROM generate_series(1,4000) AS n CROSS JOIN generate_series(1,4) AS category`, targetAgent, epoch, now); err != nil {
		t.Fatalf("seed v7 credential history failed: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at)
SELECT ('90000000-0000-4000-8000-' || lpad(n::text,12,'0'))::uuid, $1,
       decode(repeat('02',32),'hex'), $2::uuid,
       $3::timestamptz-interval '1 hour', $3::timestamptz+interval '1 hour'
FROM generate_series(1,4000) AS n`, otherAgent, epoch, now); err != nil {
		t.Fatal("seed other-agent credential history failed")
	}
	const receiptResult = `{"version":1,"preserved":true}`
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_management_receipts(actor_id,operation,idempotency_key,digest,result,created_at,expires_at)
VALUES($1,'agent.credential.rotate','preserved-index-upgrade',decode(repeat('ab',32),'hex'),$2,$3::timestamptz,$3::timestamptz+interval '24 hours')`, actorID, receiptResult, now); err != nil {
		t.Fatalf("seed v7 management receipt failed: %v", err)
	}
	var originalReceipt string
	if err := conn.QueryRow(t.Context(), `SELECT result::text FROM public.almanac_management_receipts WHERE actor_id=$1 AND idempotency_key='preserved-index-upgrade'`, actorID).Scan(&originalReceipt); err != nil {
		t.Fatal("read pre-upgrade receipt failed")
	}
	upgraded, err := Upgrade(t.Context(), databaseURL)
	if err != nil || upgraded.Applied != 11 || upgraded.DatabaseSchemaVersion != 18 {
		t.Fatalf("upgrade populated schema 7 to 13 = %+v, %v", upgraded, err)
	}
	var historyCount int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials WHERE agent_id=$1", targetAgent).Scan(&historyCount); err != nil || historyCount != 16000 {
		t.Fatalf("credential history after upgrade = %d, %v", historyCount, err)
	}
	var preservedReceipt string
	if err := conn.QueryRow(t.Context(), `SELECT result::text FROM public.almanac_management_receipts WHERE actor_id=$1 AND idempotency_key='preserved-index-upgrade'`, actorID).Scan(&preservedReceipt); err != nil || preservedReceipt != originalReceipt {
		t.Fatalf("management receipt after upgrade = %q, %v", preservedReceipt, err)
	}
	var invalidIndex bool
	if err := conn.QueryRow(t.Context(), `SELECT NOT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relname='almanac_agent_credentials_rotation_cutoff_idx'`).Scan(&invalidIndex); err != nil || invalidIndex {
		t.Fatalf("rotation cutoff index validity invalid=%t err=%v", invalidIndex, err)
	}
	if _, err := conn.Exec(t.Context(), "ANALYZE public.almanac_agent_credentials"); err != nil {
		t.Fatal("analyze credential history failed")
	}
	parseUUID := func(value string) pgtype.UUID {
		t.Helper()
		var parsed pgtype.UUID
		if err := parsed.Scan(value); err != nil {
			t.Fatal("parse test UUID failed")
		}
		return parsed
	}
	targetID, epochID := parseUUID(targetAgent), parseUUID(epoch)
	ids := []string{"80000000-0000-4000-8000-000000000011", "80000000-0000-4000-8000-000000000012"}
	for count := range 3 {
		t.Run(fmt.Sprintf("%d current credentials", count), func(t *testing.T) {
			if _, err := conn.Exec(t.Context(), "DELETE FROM public.almanac_agent_credentials WHERE id::text LIKE '80000000-0000-4000-8000-00000000001%'"); err != nil {
				t.Fatal("clear active-case credentials failed")
			}
			for i := range count {
				var retireAt any
				if i == 1 {
					retireAt = now.Add(time.Minute)
				}
				if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at,retire_at) VALUES($1,$2,decode(repeat('03',32),'hex'),$3,$4::timestamptz,$4::timestamptz+interval '2 hours',$5)`, ids[i], targetAgent, epoch, now.Add(-time.Hour), retireAt); err != nil {
					t.Fatalf("insert current credential failed: %v", err)
				}
			}
			if count == 0 {
				if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at)
VALUES('80000000-0000-4000-8000-000000000013',$1,decode(repeat('04',32),'hex'),$2,$3::timestamptz,$3::timestamptz+interval '2 hours')`, targetAgent, epoch, now.Add(time.Second)); err != nil {
					t.Fatalf("insert future-issued credential failed: %v", err)
				}
			}
			if count == 2 {
				if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at,retire_at)
VALUES('80000000-0000-4000-8000-000000000014',$1,decode(repeat('05',32),'hex'),$2,$3::timestamptz,$3::timestamptz+interval '2 hours',$3::timestamptz+interval '1 hour')`, targetAgent, epoch, now.Add(-time.Hour)); err != nil {
					t.Fatalf("insert exact cutoff credential failed: %v", err)
				}
				if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials(id,agent_id,verifier,credential_epoch,issued_at,expires_at)
VALUES('80000000-0000-4000-8000-000000000015',$1,decode(repeat('06',32),'hex'),$2,$3,$4)`, targetAgent, epoch, now.Add(-time.Hour), now); err != nil {
					t.Fatal("insert exact expiry credential failed")
				}
			}
			tx, err := conn.Begin(t.Context())
			if err != nil {
				t.Fatal("begin generated locking query transaction failed")
			}
			capturedTx := &rotationIndexQueryCapture{Tx: tx}
			rows, err := dbgen.New(capturedTx).LockActiveRotationCredentials(t.Context(), dbgen.LockActiveRotationCredentialsParams{
				AgentID: targetID, CredentialEpoch: epochID, Now: pgtype.Timestamptz{Time: now, Valid: true},
			})
			if err != nil {
				_ = tx.Rollback(t.Context())
				t.Fatal("generated active-credential query failed")
			}
			if len(rows) != count {
				_ = tx.Rollback(t.Context())
				t.Fatalf("generated query returned %d rows, want %d", len(rows), count)
			}
			for i, row := range rows {
				if row.ID.String() != ids[i] {
					_ = tx.Rollback(t.Context())
					t.Fatalf("generated query row %d ID=%s, want %s", i, row.ID.String(), ids[i])
				}
			}
			assertRotationIndexPlan(t, tx, capturedTx.query, capturedTx.args)
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal("release generated query locks failed")
			}
		})
	}
}

func assertRotationIndexPlan(t *testing.T, tx pgx.Tx, query string, args []any) {
	t.Helper()
	var rawPlan []byte
	if err := tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, args...).Scan(&rawPlan); err != nil {
		t.Fatal("explain actual locking query failed")
	}
	defer func() {
		if t.Failed() {
			t.Logf("fixture query plan: %s", rawPlan)
		}
	}()
	var explained []struct {
		Plan rotationIndexPlanNode `json:"Plan"`
	}
	if err := json.Unmarshal(rawPlan, &explained); err != nil || len(explained) != 1 {
		t.Fatalf("decode locking query plan failed: %v", err)
	}
	foundIndex := false
	var inspect func(rotationIndexPlanNode)
	inspect = func(node rotationIndexPlanNode) {
		if node.NodeType == "Seq Scan" && node.RelationName == "almanac_agent_credentials" && node.ActualLoops > 0 {
			t.Fatalf("locking query scanned credential history sequentially: %+v", node)
		}
		if node.NodeType == "Sort" && node.ActualRows > 20 {
			t.Fatalf("sort included historical rows: %+v", node)
		}
		if strings.Contains(node.NodeType, "Scan") {
			work := (node.ActualRows + node.RowsRemovedByFilter + node.RowsRemovedByRecheck) * node.ActualLoops
			if work > 20 {
				t.Fatalf("credential scan work includes history rows: %+v", node)
			}
		}
		if node.IndexName == "almanac_agent_credentials_rotation_cutoff_idx" {
			foundIndex = true
			condition := strings.ToLower(node.IndexCond)
			for _, required := range []string{"agent_id", "credential_epoch", "least(expires_at, retire_at)"} {
				if !strings.Contains(condition, required) {
					t.Fatalf("rotation index condition %q lacks %q", node.IndexCond, required)
				}
			}
		}
		for _, child := range node.Plans {
			inspect(child)
		}
	}
	inspect(explained[0].Plan)
	if !foundIndex {
		t.Fatalf("locking plan did not use cutoff index: %s", rawPlan)
	}
}
