//go:build integration

package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationObservationCopyCommitsBoundedBatchesAndResumes(t *testing.T) {
	s := legacyObservationCopyFixture(t, "copy-resume", 200_000)
	location, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	s.conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: location}})
	threshold := s.historyStart.Add(20_000 * 15 * time.Second)
	if _, err := s.conn.Exec(t.Context(), `CREATE FUNCTION public.almanac_test_interrupt_observation_copy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.interval_start>=TG_ARGV[0]::timestamptz THEN RAISE EXCEPTION 'copy interruption'; END IF; RETURN NEW; END; $$;
CREATE TRIGGER almanac_test_interrupt_observation_copy BEFORE INSERT ON public.almanac_sli_observations
FOR EACH ROW EXECUTE FUNCTION public.almanac_test_interrupt_observation_copy('`+threshold.Format(time.RFC3339)+`')`); err != nil {
		t.Fatal(err)
	}
	copyFacts := func() error {
		t.Helper()
		guard, err := s.conn.Begin(t.Context())
		if err != nil {
			return err
		}
		defer rollbackTransaction(guard)
		if err := copyLegacySliObservations(t.Context(), guard, s.conn.Config()); err != nil {
			return err
		}
		return guard.Commit(t.Context())
	}
	interrupted := copyFacts()
	if failure, ok := errors.AsType[*pgconn.PgError](interrupted); !ok || failure.Code != "P0001" || failure.Message != "copy interruption" {
		t.Fatalf("copy failed before its injected interruption: %v", interrupted)
	}
	var partial, source int64
	if err := s.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM public.almanac_sli_observations),(SELECT count(*) FROM public.almanac_sli_coverage)`).Scan(&partial, &source); err != nil {
		t.Fatal(err)
	}
	if partial <= 0 || partial >= 200_000 || source != 200_000 {
		t.Fatalf("copy lost its committed prefix or source: partial=%d source=%d", partial, source)
	}
	if _, err := s.conn.Exec(t.Context(), `DROP TRIGGER almanac_test_interrupt_observation_copy ON public.almanac_sli_observations; DROP FUNCTION public.almanac_test_interrupt_observation_copy()`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := copyFacts(); err != nil {
		t.Fatal(err)
	}
	if err := copyFacts(); err != nil {
		t.Fatalf("completed copy is not idempotent: %v", err)
	}
	var count int64
	var good, total string
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*),sum(good_count)::text,sum(total_count)::text FROM public.almanac_sli_observations`).Scan(&count, &good, &total); err != nil {
		t.Fatal(err)
	}
	if count != 200_000 || good != "19824691.357802400000" || total != "20000000.000000200000" {
		t.Fatalf("copy changed exact rows/values: %d %s/%s", count, good, total)
	}
	verifySliProjectionPlanBounds(t, s, s.historyStart.Add(200_000*15*time.Second))
	t.Logf("200000 legacy observations copied with five-second batch bounds and interrupted-prefix recovery in %s", time.Since(started))
}

func verifySliProjectionPlanBounds(t *testing.T, s *selfHealFixture, at time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='5s'; SET LOCAL jit=off"); err != nil {
		t.Fatal(err)
	}
	captured := &rotationIndexQueryCapture{Tx: tx}
	rows, err := dbgen.New(captured).ListSlis(ctx, dbgen.ListSlisParams{
		CreatedAt: timestamp(time.Time{}), CursorID: uuidOrZero(""), LimitCount: 1,
		RetentionCutoff: timestamp(at.Add(-365 * 24 * time.Hour)), ListNow: timestamp(at),
	})
	if err != nil || len(rows) != 1 || !rows[0].LatestIntervalStart.Time.Equal(at.Add(-15*time.Second)) {
		t.Fatalf("large-history SLI projection: rows=%d err=%v", len(rows), err)
	}
	var body []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+captured.query, captured.args...).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan rotationIndexPlanNode }
	if err := json.Unmarshal(body, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("SLI projection plan: %v", err)
	}
	var visits float64
	var inspect func(rotationIndexPlanNode)
	inspect = func(node rotationIndexPlanNode) {
		if strings.HasPrefix(node.RelationName, "almanac_sli_observations_") {
			visits += (node.ActualRows + node.RowsRemovedByFilter + node.RowsRemovedByRecheck) * node.ActualLoops
		}
		for _, child := range node.Plans {
			inspect(child)
		}
	}
	inspect(plans[0].Plan)
	if visits > 256 {
		t.Fatalf("one-row SLI projection visited %.0f retained observations; want at most 256", visits)
	}
}

func TestIntegrationObservationCopyRejectsInvalidLegacyFacts(t *testing.T) {
	for _, test := range []struct {
		name, sql string
		want      error
	}{
		{"missing values", `DELETE FROM public.almanac_sli_observation_values`, errObservationMigrationCounts},
		{"changed cadence", `UPDATE public.almanac_sli_coverage SET interval_end=interval_end+interval '1 second'`, errObservationMigrationInterval},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := legacyObservationCopyFixture(t, "copy-invalid", 2)
			if _, err := s.conn.Exec(t.Context(), test.sql); err != nil {
				t.Fatal(err)
			}
			guard, err := s.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(guard)
			if err := copyLegacySliObservations(t.Context(), guard, s.conn.Config()); !errors.Is(err, test.want) {
				t.Fatalf("invalid source was not rejected: %v", err)
			}
			var count int
			if err := guard.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_observations`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("invalid source fabricated canonical facts")
			}
		})
	}
}

// Reconstruct actual legacy raw schemas under a valid current control plane;
// the copy is tested directly without bypassing the application's schema gate.
func legacyObservationCopyFixture(t *testing.T, name string, count int) *selfHealFixture {
	t.Helper()
	s := newSelfHealFixtureCadence(t, name, true, 15*time.Second)
	end := s.historyStart.Add(time.Duration(count) * 15 * time.Second)
	for month := utcMonthStart(s.historyStart); month.Before(end); month = month.AddDate(0, 1, 0) {
		tx, err := s.conn.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := prepareSliPartitions(t.Context(), tx, month); err != nil {
			rollbackTransaction(tx)
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	recreateLegacyObservationTables(t, s.conn)
	if _, err := s.conn.Exec(t.Context(), "TRUNCATE public.almanac_sli_observation_values, public.almanac_sli_coverage"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at,ingestion_generation)
SELECT point,c.sli_id,c.measurement_revision,point+interval '15 seconds','observed',c.result_digest,'{}',
gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),c.agent_id,c.session_generation,c.configuration_revision,c.lease_id,point,point+interval '15 seconds',row_number() OVER(ORDER BY point)
FROM (SELECT * FROM public.almanac_sli_observations WHERE sli_id=$1 LIMIT 1)c
CROSS JOIN generate_series($2::timestamptz,$3::timestamptz-interval '15 seconds',interval '15 seconds')point`, s.sliID, s.historyStart, end); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_observation_values
SELECT interval_start,sli_id,measurement_revision,99.123456789012,100.000000000001 FROM public.almanac_sli_coverage;
TRUNCATE public.almanac_sli_observations`); err != nil {
		t.Fatal(err)
	}
	return s
}

func recreateLegacyObservationTables(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	data, err := fs.ReadFile(embeddedMigrations, "migrations/0013_sli_observations.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"almanac_sli_coverage", "almanac_sli_observation_values"} {
		marker := "CREATE TABLE public." + name + " ("
		_, tail, found := strings.Cut(string(data), marker)
		if !found {
			t.Fatal("missing legacy table definition")
		}
		statement, _, found := strings.Cut(tail, ";")
		if !found {
			t.Fatal("missing legacy statement end")
		}
		if _, err := conn.Exec(t.Context(), marker+statement+";"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(t.Context(), `ALTER TABLE public.almanac_sli_coverage ADD COLUMN ingestion_generation bigint NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `DO $$
DECLARE part record; parent text; suffix text;
BEGIN
 FOR part IN SELECT child.relname,pg_get_expr(child.relpartbound,child.oid) AS bound
 FROM pg_inherits i JOIN pg_class child ON child.oid=i.inhrelid
 WHERE i.inhparent='public.almanac_sli_observations'::regclass
 LOOP
  suffix:=substring(part.relname FROM length('almanac_sli_observations_')+1);
  FOREACH parent IN ARRAY ARRAY['almanac_sli_coverage','almanac_sli_observation_values'] LOOP
   EXECUTE format('CREATE TABLE public.%I PARTITION OF public.%I %s',parent||'_'||suffix,parent,part.bound);
  END LOOP;
 END LOOP;
END; $$;
INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,error_code,missing_components,
execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at,ingestion_generation)
SELECT o.interval_start,o.sli_id,o.measurement_revision,o.interval_start+datasource.polling_interval_seconds*interval '1 second',
o.outcome,o.result_digest,o.error_code,o.missing_components,o.execution_id,o.delivery_id,coalesce(receipt.observation_id,gen_random_uuid()),
o.agent_id,o.session_generation,o.configuration_revision,o.lease_id,o.agent_created_at,o.recorded_at,o.ingestion_generation
FROM public.almanac_sli_observations o
JOIN public.almanac_sli_measurements m ON m.sli_id=o.sli_id AND m.revision=o.measurement_revision
JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
LEFT JOIN public.almanac_sli_delivery_receipts receipt ON receipt.agent_id=o.agent_id AND receipt.delivery_id=o.delivery_id;
INSERT INTO public.almanac_sli_observation_values
SELECT interval_start,sli_id,measurement_revision,good_count,total_count FROM public.almanac_sli_observations WHERE outcome='observed'`); err != nil {
		t.Fatal(err)
	}
	var same bool
	if err := conn.QueryRow(t.Context(), `SELECT
(SELECT count(*) FROM public.almanac_sli_coverage)=(SELECT count(*) FROM public.almanac_sli_observations)
AND NOT EXISTS(
 SELECT interval_start,sli_id,measurement_revision,good_count,total_count FROM public.almanac_sli_observations WHERE outcome='observed'
 EXCEPT SELECT interval_start,sli_id,measurement_revision,good_count,total_count FROM public.almanac_sli_observation_values)`).Scan(&same); err != nil || !same {
		t.Fatalf("legacy fixture changed canonical rows or exact values: %v", err)
	}
}
