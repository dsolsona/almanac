//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func verifyAuditedRetentionCleanup(t *testing.T, runtime *Runtime, databaseURL, id string, now time.Time) {
	t.Helper()
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	q := dbgen.New(conn)
	pgID, _ := canonicalUUID(id)
	row, err := q.GetSlo(t.Context(), pgID)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(now.Year()-2, now.Month(), 15, 12, 0, 0, 0, time.UTC)
	month := utcDayStart(old).AddDate(0, 0, -14)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if err := prepareSliPartitions(t.Context(), tx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_sli_observations
        (interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
        SELECT $3,sli_id,measurement_revision,outcome,90,100,result_digest,missing_components,
            gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,$3,$3
        FROM public.almanac_sli_observations WHERE sli_id=$1 AND measurement_revision=$2 AND outcome='observed' LIMIT 1`, row.SliID, row.MeasurementRevision, old); err != nil {
		t.Fatal(err)
	}
	if err := markObservationFixture(t.Context(), tx, row.SliID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO public.almanac_slo_extrema
(slo_id,definition_revision,measurement_revision,calculation_version,resolution_seconds,range_start,range_end,source_generation,retention_start,published_at,lanes)
VALUES ($1,$2,$3,$4,3600,$5,$5::timestamptz+interval '1 hour',0,$5,$6,'{}')`,
		pgID, row.DefinitionRevision, row.MeasurementRevision, operationalCalculationVersion, old.Truncate(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var canonicalBefore int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_observations WHERE sli_id=$1`, row.SliID).Scan(&canonicalBefore); err != nil {
		t.Fatal(err)
	}
	if err := runtime.discoverRetention(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	var plan pgtype.UUID
	var previewBytes []byte
	if err := conn.QueryRow(t.Context(), `SELECT id,preview FROM public.almanac_retention_work WHERE range_start=$1`, month).Scan(&plan, &previewBytes); err != nil {
		t.Fatal(err)
	}
	var preview retentionImpact
	if err := json.Unmarshal(previewBytes, &preview); err != nil || len(preview.Partitions) != 1 || preview.CalculatedRowsAtLeast["extrema"] != 1 || preview.PreviewRowLimit != 1000 {
		t.Fatalf("committed preview omitted canonical/calculated impact: %+v, %v", preview, err)
	}
	if worked, err := runtime.applyRetentionPreview(t.Context(), now); err != nil || worked {
		t.Fatalf("new preview was applied before its grace period: %t, %v", worked, err)
	}
	// Expired raw partitions can be removed while unrelated extrema work is leased.
	if _, err := conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor SET lease_until=$2 WHERE slo_id=$1`, pgID, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var canonicalAfter int
	for attempt := range 8 {
		if _, err := runtime.applyRetentionPreview(t.Context(), now.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := conn.QueryRow(t.Context(), `SELECT state FROM public.almanac_retention_work WHERE id=$1`, plan).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "applied" {
			break
		}
		if attempt == 7 {
			t.Fatal("small retention fixture did not finish bounded cleanup")
		}
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_observations WHERE sli_id=$1`, row.SliID).Scan(&canonicalAfter); err != nil || canonicalAfter != canonicalBefore-1 {
		t.Fatalf("retention removed retained rows or failed to remove the expired row: %d -> %d, %v", canonicalBefore, canonicalAfter, err)
	}
	missingAt := time.Date(now.Year(), now.Month(), 1, 0, 0, 1, 0, time.UTC)
	_, err = conn.Exec(t.Context(), `INSERT INTO public.almanac_sli_observations
(sli_id,measurement_revision,interval_start,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,
 agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT sli_id,measurement_revision+1000,$2,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),
 agent_id,session_generation,configuration_revision,lease_id,$2,$2
FROM public.almanac_sli_observations WHERE sli_id=$1 AND outcome='observed' LIMIT 1`, row.SliID, missingAt)
	if foreignKey, ok := errors.AsType[*pgconn.PgError](err); !ok || foreignKey.Code != "23503" {
		t.Fatalf("cleanup removed the retained observation measurement foreign key: %v", err)
	}
	var audit int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_audit_events WHERE subject_id=$1 AND action IN ('retention.preview','retention.apply')`, plan).Scan(&audit); err != nil || audit != 2 {
		t.Fatalf("retention preview/apply outcomes were not independently durable: %d, %v", audit, err)
	}
	var deleted int64
	if err := conn.QueryRow(t.Context(), `SELECT (cleanup_counts->>'extrema')::bigint FROM public.almanac_retention_work WHERE id=$1`, plan).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("retention lost its actual calculated cleanup count: %d, %v", deleted, err)
	}
	// A recreated relation with the same name/bounds is a different deletion scope.
	if err := runtime.discoverRetention(t.Context(), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var replacementPlan pgtype.UUID
	var payload []byte
	if err := conn.QueryRow(t.Context(), `SELECT id,preview FROM public.almanac_retention_work WHERE state='previewed' ORDER BY range_start LIMIT 1`).Scan(&replacementPlan, &payload); err != nil {
		t.Fatal(err)
	}
	var replacement retentionImpact
	if err := json.Unmarshal(payload, &replacement); err != nil {
		t.Fatal(err)
	}
	for _, part := range replacement.Partitions {
		if part.Parent == "almanac_sli_observations" {
			if _, err := conn.Exec(t.Context(), "ALTER TABLE public.almanac_sli_observations DETACH PARTITION "+pgx.Identifier{"public", part.Name}.Sanitize()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := conn.Exec(t.Context(), "DROP TABLE "+pgx.Identifier{"public", part.Name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	tx, err = conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if err := prepareSliPartitions(t.Context(), tx, old.AddDate(0, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.applyRetentionPreview(t.Context(), now.Add(4*time.Minute)); !errors.Is(err, errRetentionScope) {
		t.Fatalf("recreated relation reused a stale deletion preview: %v", err)
	}
}
