//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/testpostgres"
	"github.com/jackc/pgx/v5"
)

type performanceRetentionCleanupReport struct {
	Month                          time.Time     `json:"expired_month"`
	PlanID                         string        `json:"plan_id"`
	State                          string        `json:"state"`
	Elapsed                        time.Duration `json:"cleanup_elapsed_ns"`
	PreviewAudit, ApplyAudit       int64
	ExpiredCoverage, ExpiredValues int64
	PartitionsRemoved              bool                           `json:"partitions_removed"`
	Reads                          []performancePreparationSample `json:"retained_reads"`
}

// The normal maintenance loop must discover, preview and remove populated old
// partitions. Neither its clock nor its preview grace period is changed here.
func performanceRetentionCleanup(t *testing.T, parent context.Context, fleet performanceFleet) (report performanceRetentionCleanupReport, resultErr error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	month := time.Date(now.Year()-2, now.Month(), 1, 0, 0, 0, 0, time.UTC)
	report = performanceRetentionCleanupReport{Month: month}
	deployment := fleet.fixture.backend.deployment
	if deployment == nil || deployment.databaseURL != fleet.fixture.databaseURL {
		return report, errors.New("retention fixture requires its owned deployment database")
	}
	deployment.evidence(t)
	// Class0 remains unaffected by the earlier class1 late-import stage.
	id, sli := fleet.objectives[9], fleet.slis[9]
	end := time.Date(fleet.historyEnd.Year(), fleet.historyEnd.Month(), fleet.historyEnd.Day(), 0, 0, 0, 0, time.UTC).Add(-24 * time.Hour)
	start := end.Add(-7 * 24 * time.Hour)
	query := url.Values{"at": {end.Format(time.RFC3339)}, "start": {start.Format(time.RFC3339)}, "end": {end.Format(time.RFC3339)}, "burn_window_seconds": {"3600"}}
	operation := performanceOperation{Name: "retained-values-during-expiry", Paths: []string{"/api/v1/slos/" + id + "/operational?" + query.Encode()}, Budget: 500 * time.Millisecond}
	operation.Check = func(_ int, body []byte) error {
		var detail api.SloOperationalDetail
		if err := json.Unmarshal(body, &detail); err != nil {
			return err
		}
		return checkPerformanceAccuracyDetail(detail, id, 0, start, end, time.Hour)
	}
	read := func() error {
		samples, err := preparePerformanceOperation(ctx, fleet.readers, operation)
		report.Reads = append(report.Reads, samples...)
		return err
	}
	if err := read(); err != nil {
		return report, err
	}
	db := testpostgres.Connect(t, fleet.fixture.databaseURL)
	defer closePerformanceObserver(db)
	tx, err := db.Begin(ctx)
	if err != nil {
		return report, errors.New("begin expired partition fixture failed")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	names := make([]string, 0, 2)
	for _, table := range []string{"almanac_sli_coverage", "almanac_sli_observation_values"} {
		name := table + "_" + month.Format("200601")
		var absent bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NULL`, "public."+name).Scan(&absent); err != nil || !absent {
			return report, errors.New("expired fixture month already exists")
		}
		statement := fmt.Sprintf("CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')", pgx.Identifier{"public", name}.Sanitize(), pgx.Identifier{"public", table}.Sanitize(), month.Format(time.RFC3339), month.AddDate(0, 1, 0).Format(time.RFC3339))
		if _, err := tx.Exec(ctx, statement); err != nil {
			return report, errors.New("create expired fixture month failed")
		}
		names = append(names, "public."+name)
	}
	at := month.Add(14*24*time.Hour + 12*time.Hour)
	tag, err := tx.Exec(ctx, `INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,
 execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT $2,sli_id,measurement_revision,$2::timestamptz+interval '1 minute','observed',decode(repeat('ef',32),'hex'),'{}',
 gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,$2,CURRENT_TIMESTAMP
FROM public.almanac_sli_coverage WHERE sli_id=$1 AND measurement_revision=1 AND outcome='observed' ORDER BY interval_start DESC LIMIT 1`, sli, at)
	if err != nil || tag.RowsAffected() != 1 {
		return report, errors.New("populate expired coverage fixture failed")
	}
	report.ExpiredCoverage = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `INSERT INTO public.almanac_sli_observation_values(interval_start,sli_id,measurement_revision,good_count,total_count) VALUES($2,$1,1,90,100)`, sli, at)
	if err != nil || tag.RowsAffected() != 1 {
		return report, errors.New("populate expired values fixture failed")
	}
	report.ExpiredValues = tag.RowsAffected()
	if err := tx.Commit(ctx); err != nil {
		return report, errors.New("commit populated expiry fixture failed")
	}
	started := time.Now()
	defer func() { report.Elapsed = time.Since(started) }()
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	for {
		bounded, stop := context.WithTimeout(ctx, 5*time.Second)
		err := db.QueryRow(bounded, `SELECT COALESCE((SELECT id::text FROM public.almanac_retention_work WHERE range_start=$1 ORDER BY previewed_at DESC LIMIT 1),''),
COALESCE((SELECT state FROM public.almanac_retention_work WHERE range_start=$1 ORDER BY previewed_at DESC LIMIT 1),'')`, month).Scan(&report.PlanID, &report.State)
		stop()
		if err != nil {
			return report, errors.New("observe normal retention discovery/apply failed")
		}
		if report.State == "failed" {
			return report, errors.New("normal populated retention cleanup failed")
		}
		if report.State == "applied" {
			break
		}
		if err := read(); err != nil {
			return report, err
		}
		select {
		case <-ctx.Done():
			return report, errors.New("normal populated retention cleanup did not finish within observation budget")
		case <-ticks.C:
		}
	}
	var partitionsGone bool
	if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NULL AND to_regclass($2) IS NULL`, names[0], names[1]).Scan(&partitionsGone); err != nil || !partitionsGone {
		return report, errors.New("applied retention left expired canonical partitions")
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='retention.preview'),count(*) FILTER(WHERE action='retention.apply') FROM public.almanac_audit_events WHERE subject_id=$1`, report.PlanID).Scan(&report.PreviewAudit, &report.ApplyAudit); err != nil || report.PreviewAudit != 1 || report.ApplyAudit != 1 {
		return report, errors.New("populated retention lacks durable preview/apply audit")
	}
	var coverage, values int64
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.almanac_sli_coverage WHERE sli_id=$1 AND interval_start=$2),(SELECT count(*) FROM public.almanac_sli_observation_values WHERE sli_id=$1 AND interval_start=$2)`, sli, at).Scan(&coverage, &values); err != nil || coverage != 0 || values != 0 {
		return report, errors.New("populated retention left expired canonical observations")
	}
	if err := read(); err != nil {
		return report, err
	}
	report.PartitionsRemoved = true
	report.Elapsed = time.Since(started)
	return report, nil
}
