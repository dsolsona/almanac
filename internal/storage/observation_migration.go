package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	errObservationMigrationCounts   = errors.New("storage: legacy observation counts do not match their outcome; old tables retained")
	errObservationMigrationInterval = errors.New("storage: legacy observation interval does not match its cadence; old tables retained")
	errObservationMigrationMismatch = errors.New("storage: copied observations differ from legacy facts; old tables retained")
)

const observationMigrationColumns = "sli_id,measurement_revision,interval_start,outcome,good_count,total_count,result_digest,error_code,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at,ingestion_generation"

// This source is used only while the legacy parents are locked against writes.
const observationMigrationSource = `SELECT c.sli_id,c.measurement_revision,c.interval_start,c.outcome,
v.good_count,v.total_count,c.result_digest,c.error_code,c.missing_components,c.execution_id,c.delivery_id,
c.agent_id,c.session_generation,c.configuration_revision,c.lease_id,c.agent_created_at,c.recorded_at,c.ingestion_generation,
c.observation_id,c.interval_end,
c.interval_end<>c.interval_start+datasource.polling_interval_seconds*interval '1 second' AS invalid_interval,
(c.outcome='observed')<>(v.good_count IS NOT NULL AND v.total_count IS NOT NULL)
 OR (c.outcome<>'observed' AND (v.good_count IS NOT NULL OR v.total_count IS NOT NULL)) AS invalid_counts
FROM %s c
JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
JOIN public.almanac_datasources datasource ON datasource.id=m.datasource_id
LEFT JOIN LATERAL (
 SELECT good_count,total_count FROM public.almanac_sli_observation_values v
 WHERE (v.interval_start,v.sli_id,v.measurement_revision)=(c.interval_start,c.sli_id,c.measurement_revision)
   AND v.interval_start>=$4::timestamptz AND v.interval_start<$5::timestamptz
 LIMIT 1
) v ON true`

type legacyObservationPartition struct {
	name       string
	start, end time.Time
}

// Copy commits independently of the migration transaction. Its source locks stay
// held through final table removal and recording migration 0048, including retries.
func copyLegacySliObservations(ctx context.Context, guard pgx.Tx, config *pgx.ConnConfig) error {
	if _, err := guard.Exec(ctx, "SET LOCAL statement_timeout='5s'; LOCK TABLE public.almanac_sli_coverage, public.almanac_sli_observation_values IN SHARE MODE"); err != nil {
		return err
	}
	rows, err := guard.Query(ctx, `SELECT child.relname,
(regexp_match(pg_get_expr(child.relpartbound,child.oid),'^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$'))[1]::timestamptz,
(regexp_match(pg_get_expr(child.relpartbound,child.oid),'^FOR VALUES FROM \(''([^'']+)''\) TO \(''([^'']+)''\)$'))[2]::timestamptz
FROM pg_inherits i JOIN pg_class child ON child.oid=i.inhrelid
WHERE i.inhparent='public.almanac_sli_coverage'::regclass ORDER BY child.relname LIMIT 1000`)
	if err != nil {
		return err
	}
	var parts []legacyObservationPartition
	for rows.Next() {
		var name string
		var start, end pgtype.Timestamptz
		if err := rows.Scan(&name, &start, &end); err != nil {
			rows.Close()
			return err
		}
		if !start.Valid || !end.Valid || name != "almanac_sli_coverage_"+start.Time.UTC().Format("200601") ||
			!start.Time.Equal(utcMonthStart(start.Time)) || !end.Time.Equal(start.Time.UTC().AddDate(0, 1, 0)) {
			rows.Close()
			return errors.New("storage: unexpected legacy observation partition")
		}
		parts = append(parts, legacyObservationPartition{name: name, start: start.Time.UTC(), end: end.Time.UTC()})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(parts) >= 1000 {
		return ErrOperationalResource
	}
	if len(parts) == 0 {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	copyConn, err := pgx.ConnectConfig(bounded, config.Copy())
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = copyConn.Close(cleanup)
	}()
	if _, err := copyConn.Exec(ctx, "SET statement_timeout='5s'; SET lock_timeout='250ms'"); err != nil {
		return err
	}
	for _, part := range parts {
		if err := copyLegacyObservationPartition(ctx, copyConn, part); err != nil {
			return err
		}
	}
	return nil
}

func utcMonthStart(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func copyLegacyObservationPartition(ctx context.Context, conn *pgx.Conn, part legacyObservationPartition) error {
	partition := pgx.Identifier{"public", part.name}.Sanitize()
	// Limit canonical keys before joining values and checking foreign keys.
	batchSource := fmt.Sprintf(observationMigrationSource, `(SELECT * FROM `+partition+`
WHERE ($1::uuid IS NULL OR (interval_start,sli_id,measurement_revision)>($3::timestamptz,$1::uuid,$2::bigint))
ORDER BY interval_start,sli_id,measurement_revision LIMIT 1000)`)
	batchSQL := `WITH batch AS MATERIALIZED (` + batchSource + `
), inserted AS (
INSERT INTO public.almanac_sli_observations (` + observationMigrationColumns + `)
SELECT ` + observationMigrationColumns + ` FROM batch
WHERE NOT EXISTS(SELECT 1 FROM batch WHERE invalid_interval OR invalid_counts)
ON CONFLICT(sli_id,measurement_revision,interval_start) DO NOTHING RETURNING 1
), last AS (SELECT sli_id,measurement_revision,interval_start FROM batch ORDER BY interval_start DESC,sli_id DESC,measurement_revision DESC LIMIT 1)
SELECT (SELECT count(*) FROM batch),(SELECT count(*) FROM inserted),
coalesce((SELECT bool_or(invalid_counts) FROM batch),false),coalesce((SELECT bool_or(invalid_interval) FROM batch),false),
last.sli_id,coalesce(last.measurement_revision,0),last.interval_start FROM (SELECT 1) singleton LEFT JOIN last ON true`
	// Compare every retained field in the same bounded transaction, including
	// rows skipped by ON CONFLICT when a previously interrupted copy resumes.
	columns := observationMigrationColumns
	verifyBatchSQL := `WITH legacy AS MATERIALIZED (` + batchSource + `), copied AS MATERIALIZED (
SELECT o.` + strings.ReplaceAll(columns, ",", ",o.") + ` FROM public.almanac_sli_observations o
JOIN legacy l USING(sli_id,measurement_revision,interval_start)
WHERE o.interval_start>=$4::timestamptz AND o.interval_start<$5::timestamptz
), difference AS (
(SELECT ` + columns + ` FROM legacy EXCEPT SELECT ` + columns + ` FROM copied)
UNION ALL (SELECT ` + columns + ` FROM copied EXCEPT SELECT ` + columns + ` FROM legacy)
)
SELECT (SELECT count(*) FROM legacy)=(SELECT count(*) FROM copied)
 AND NOT EXISTS(SELECT 1 FROM difference)
 AND NOT EXISTS(SELECT 1 FROM legacy l LEFT JOIN public.almanac_sli_delivery_receipts r
 ON r.agent_id=l.agent_id AND r.delivery_id=l.delivery_id WHERE r.observation_id IS DISTINCT FROM l.observation_id)`
	var lastSli pgtype.UUID
	var lastRevision int64
	var lastStart pgtype.Timestamptz
	for {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		tx, err := conn.Begin(bounded)
		if err != nil {
			cancel()
			return err
		}
		if _, err = tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'"); err != nil {
			rollbackTransaction(tx)
			cancel()
			return err
		}
		var count, inserted int64
		var invalidCounts, invalidInterval bool
		args := []any{lastSli, lastRevision, lastStart, timestamp(part.start), timestamp(part.end)}
		err = tx.QueryRow(bounded, batchSQL, args...).Scan(
			&count, &inserted, &invalidCounts, &invalidInterval, &lastSli, &lastRevision, &lastStart)
		if err == nil && invalidCounts {
			err = errObservationMigrationCounts
		}
		if err == nil && invalidInterval {
			err = errObservationMigrationInterval
		}
		if err == nil && count > 0 && (!lastSli.Valid || !lastStart.Valid) {
			err = errObservationMigrationMismatch
		}
		if err == nil && count > 0 {
			err = copyLegacyObservationReceipts(bounded, tx, batchSource, args)
		}
		if err == nil && count > 0 {
			var matches bool
			err = tx.QueryRow(bounded, verifyBatchSQL, args...).Scan(&matches)
			if err == nil && !matches {
				err = errObservationMigrationMismatch
			}
		}
		if err == nil {
			err = tx.Commit(bounded)
		}
		if err != nil {
			rollbackTransaction(tx)
			cancel()
			return fmt.Errorf("storage: observation copy batch in %s: %w", part.name, err)
		}
		cancel()
		if count == 0 {
			break
		}
	}
	// Every source key/value was checked above; the partition count also rejects
	// unexpected destination keys that were not part of any source batch.
	verification := `SELECT (SELECT count(*) FROM ` + partition + `)=
(SELECT count(*) FROM public.almanac_sli_observations WHERE interval_start>=$1::timestamptz AND interval_start<$2::timestamptz)`
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var matches bool
	if err := conn.QueryRow(bounded, verification, timestamp(part.start), timestamp(part.end)).Scan(&matches); err != nil {
		return fmt.Errorf("storage: observation copy count in %s: %w", part.name, err)
	}
	if !matches {
		return errObservationMigrationMismatch
	}
	return nil
}

func copyLegacyObservationReceipts(ctx context.Context, tx pgx.Tx, source string, args []any) error {
	rows, err := tx.Query(ctx, `WITH legacy AS (`+source+`)
SELECT l.observation_id,l.agent_id,l.delivery_id,l.sli_id,l.measurement_revision,l.interval_start,
l.session_generation,l.lease_id,l.configuration_revision,l.recorded_at,l.execution_id::text,l.agent_created_at,
l.interval_end,l.outcome,coalesce(l.good_count::text,''),coalesce(l.total_count::text,''),
coalesce(l.error_code,''),l.missing_components
FROM legacy l LEFT JOIN public.almanac_sli_delivery_receipts r ON r.agent_id=l.agent_id AND r.delivery_id=l.delivery_id
WHERE r.delivery_id IS NULL`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var batch pgx.Batch
	for rows.Next() {
		var receipt dbgen.InsertSliDeliveryReceiptParams
		var item sli.ObservationItem
		var outcome string
		if err := rows.Scan(&receipt.ObservationID, &receipt.AgentID, &receipt.DeliveryID, &receipt.SliID, &receipt.MeasurementRevision, &receipt.IntervalStart,
			&receipt.SessionGeneration, &receipt.LeaseID, &receipt.ConfigurationRevision, &receipt.CreatedAt, &item.ExecutionID, &item.CreatedAt,
			&item.IntervalEnd, &outcome, &item.Outcome.GoodCount, &item.Outcome.TotalCount, &item.Outcome.ErrorCode, &item.Outcome.MissingComponents); err != nil {
			return err
		}
		item.DeliveryID, item.SLIID, item.LeaseID = receipt.DeliveryID.String(), receipt.SliID.String(), receipt.LeaseID
		item.MeasurementRevision = strconv.FormatInt(receipt.MeasurementRevision, 10)
		item.SessionGeneration = strconv.FormatInt(receipt.SessionGeneration, 10)
		item.ConfigurationRevision = strconv.FormatInt(receipt.ConfigurationRevision, 10)
		item.IntervalStart, item.Outcome.State = receipt.IntervalStart.Time, sli.OutcomeState(outcome)
		digest, err := sliObservationPayloadDigest(item)
		if err != nil {
			return err
		}
		batch.Queue(`INSERT INTO public.almanac_sli_delivery_receipts
(delivery_id,payload_digest,sli_id,measurement_revision,interval_start,delivery_status,observation_id,replay_until,
agent_id,session_generation,lease_id,configuration_revision,created_at)
VALUES($1,$2,$3,$4,$5,'accepted',$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(agent_id,delivery_id) DO NOTHING`,
			receipt.DeliveryID, digest[:], receipt.SliID, receipt.MeasurementRevision, receipt.IntervalStart, receipt.ObservationID,
			timestamp(receipt.CreatedAt.Time.Add(sli.ObservationReplayWindow)), receipt.AgentID, receipt.SessionGeneration, receipt.LeaseID, receipt.ConfigurationRevision, receipt.CreatedAt)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if batch.Len() == 0 {
		return nil
	}
	results := tx.SendBatch(ctx, &batch)
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			return errors.Join(err, results.Close())
		}
	}
	return results.Close()
}
