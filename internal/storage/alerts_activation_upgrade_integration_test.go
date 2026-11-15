//go:build integration

package storage

import (
	"testing"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
)

func TestIntegrationAlertActivationPopulatedUpgrade(t *testing.T) {
	t.Parallel()
	if latest := latestTestSchemaVersion(t); latest != 60 {
		t.Fatalf("update exact 0059 reconstruction for migration suffix: %d", latest)
	}
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Activation upgrade")
	claim := f.claim(t, f.at)
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Minute)
	claim = f.claim(t, f.now)
	if err := f.runtime.recordAlertRepeatedCutoff(t.Context(), claim, f.at, f.clock); err != nil {
		t.Fatal(err)
	}
	f.runtime.Close()
	snapshot := func() string {
		t.Helper()
		var value string
		if err := f.conn.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY policy_id,revision) FROM almanac_alert_policy_revisions r),
 'cursors',(SELECT jsonb_agg(to_jsonb(c) ORDER BY policy_id,revision) FROM almanac_alert_cursors c),
 'evaluations',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM almanac_alert_evaluations e),
 'occurrences',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM almanac_alert_occurrences o),
 'transitions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM almanac_alert_transitions t),
 'ranges',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM almanac_alert_ranges r),
 'pins',(SELECT jsonb_agg(to_jsonb(p) ORDER BY occurrence_id) FROM almanac_alert_breach_pins p),
 'ledger',(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM almanac_schema_migrations m WHERE version<60))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_alert_ranges DROP CONSTRAINT almanac_alert_ranges_kind_check;
 ALTER TABLE almanac_alert_ranges ADD CONSTRAINT almanac_alert_ranges_kind_check CHECK(kind IN ('repeated_cutoff','unreconstructed_gap','administrative_cancellation','compacted_evaluations'));
 DELETE FROM almanac_schema_migrations WHERE version=60;`); err != nil {
		t.Fatal(err)
	}
	result, err := Upgrade(t.Context(), f.f.databaseURL)
	if err != nil || result.Applied != 1 || result.DatabaseSchemaVersion != 60 {
		t.Fatalf("populated 0059 upgrade: %+v %v", result, err)
	}
	if snapshot() != before {
		t.Fatal("constraint upgrade rewrote retained evidence, work, pins or earlier migration receipts")
	}
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_alert_ranges(id,policy_id,revision,starts_at,ends_at,cadence_seconds,kind,reason,recorded_at)
 VALUES($1,$2,1,$3,$4,60,'before_policy_activation','before_policy_activation',$3)`, uuid.New().String(), policy, f.at.Add(2*time.Minute), f.at.Add(3*time.Minute)); err != nil {
		t.Fatalf("new range kind rejected after upgrade: %v", err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_alert_ranges SET reason='changed' WHERE policy_id=$1`, policy); err == nil {
		t.Fatal("upgrade weakened immutable range guard")
	}
}
