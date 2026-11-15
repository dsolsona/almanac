//go:build integration

package storage

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationAlertRetentionUpgrade(t *testing.T) {
	t.Parallel()
	if latest := latestTestSchemaVersion(t); latest != 59 {
		t.Fatalf("update the exact 0058 reconstruction for migration suffix after 0059: latest=%d", latest)
	}
	f := newAlertTestFixture(t)
	active := f.policy(t, "Upgrade active")
	terminal := f.policy(t, "Upgrade terminal")
	type retained struct {
		occurrence  string
		evaluations []string
	}
	want := map[string]*retained{active: {}, terminal: {}}
	for index, quality := range []alert.Quality{alert.Complete, alert.Complete, alert.Missing} {
		f.now = f.at.Add(time.Duration(index) * time.Minute)
		claims, err := f.runtime.claimAlertWork(t.Context(), f.now, f.now, time.Minute, 2)
		if err != nil || len(claims) != 2 {
			t.Fatalf("claim both populated policies: %+v %v", claims, err)
		}
		for _, claim := range claims {
			result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, quality, true), f.limits, f.clock)
			if err != nil || result.Occurrence == nil {
				t.Fatalf("seed retained occurrence: %+v %v", result, err)
			}
			entry := want[claim.PolicyID]
			entry.occurrence = result.Occurrence.ID
			entry.evaluations = append(entry.evaluations, result.EvaluationID)
		}
	}
	f.now = f.at.Add(3 * time.Minute)
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		_, err := disableAlertPolicyTx(t.Context(), tx, alertDisableInput{PolicyID: terminal, ActorID: f.f.userID, RequestID: "upgrade-disable", Reason: "Preserve terminal history", ExpectedVersion: 1, EffectiveAt: f.now, Now: f.clock})
		return err
	})
	// No application worker or pooled connection remains during this disposable
	// database's exact suffix reconstruction; immutable history triggers stay on.
	f.runtime.Close()
	snapshot := func() string {
		t.Helper()
		var value string
		if err := f.conn.QueryRow(t.Context(), `SELECT jsonb_build_object(
'occurrences',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM almanac_alert_occurrences o),
'evaluations',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM almanac_alert_evaluations e),
'transitions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM almanac_alert_transitions t),
'ranges',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM almanac_alert_ranges r),
'ledger',(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM almanac_schema_migrations m WHERE version<59)
)::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	var checksum []byte
	var filename string
	if err := f.conn.QueryRow(t.Context(), `SELECT filename,checksum FROM almanac_schema_migrations WHERE version=59`).Scan(&filename, &checksum); err != nil {
		t.Fatal(err)
	}
	tag, err := f.conn.Exec(t.Context(), `
DROP TABLE public.almanac_alert_breach_pins,public.almanac_alert_retention_work;
DROP INDEX public.almanac_alert_firing_evaluation_idx,public.almanac_alert_latest_evaluation_idx,
public.almanac_alert_transition_evaluation_idx,public.almanac_alert_cursor_evaluation_idx,
public.almanac_alert_range_evaluation_idx,public.almanac_alert_terminal_retention_idx,
public.almanac_alert_range_retention_idx;
ALTER TABLE public.almanac_alert_ranges DROP CONSTRAINT almanac_alert_range_summary;
ALTER TABLE public.almanac_alert_ranges DROP COLUMN summary;
ALTER TABLE public.almanac_alert_ranges DROP CONSTRAINT almanac_alert_ranges_kind_check;
ALTER TABLE public.almanac_alert_ranges ADD CONSTRAINT almanac_alert_ranges_kind_check CHECK(kind IN ('repeated_cutoff','unreconstructed_gap','administrative_cancellation'));
DELETE FROM public.almanac_schema_migrations WHERE version=59;`)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("reconstruct populated 0058: removed=%d %v", tag.RowsAffected(), err)
	}
	if state, err := CheckCompatibility(t.Context(), f.f.databaseURL); err != nil || state != CompatibilityPending {
		t.Fatalf("0058 compatibility: %v %v", state, err)
	}
	result, err := Upgrade(t.Context(), f.f.databaseURL)
	if err != nil || result.Applied != 1 || result.DatabaseSchemaVersion != 59 {
		t.Fatalf("populated 0058 to 0059: %+v %v", result, err)
	}
	if after := snapshot(); after != before {
		t.Fatal("forward upgrade changed occurrence/evaluation/transition/range history or prior migration ledger")
	}
	var newChecksum []byte
	var newFilename string
	if err := f.conn.QueryRow(t.Context(), `SELECT filename,checksum FROM almanac_schema_migrations WHERE version=59`).Scan(&newFilename, &newChecksum); err != nil || newFilename != filename || !bytes.Equal(checksum, newChecksum) {
		t.Fatalf("0059 source identity changed: %v", err)
	}
	for policy, entry := range want {
		var pin, first, latest, state, truth string
		if err := f.conn.QueryRow(t.Context(), `SELECT p.evaluation_id::text,o.firing_evaluation_id::text,o.latest_evaluation_id::text,o.state,e.truth
FROM almanac_alert_occurrences o JOIN almanac_alert_breach_pins p ON p.occurrence_id=o.id
JOIN almanac_alert_evaluations e ON e.id=o.latest_evaluation_id
WHERE o.id=$1 AND p.policy_id=$2 AND p.revision=o.revision`, entry.occurrence, policy).Scan(&pin, &first, &latest, &state, &truth); err != nil {
			t.Fatal(err)
		}
		wantState := string(alert.Firing)
		if policy == terminal {
			wantState = string(alert.Disabled)
		}
		if pin != entry.evaluations[1] || first != entry.evaluations[0] || latest != entry.evaluations[2] || truth != string(alert.Indeterminate) || state != wantState {
			t.Fatalf("wrong reconstructed breach provenance: pin=%s first=%s latest=%s truth=%s state=%s", pin, first, latest, truth, state)
		}
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_alert_occurrences SET updated_at=updated_at WHERE id=$1`, want[terminal].occurrence); err == nil {
		t.Fatal("forward upgrade lost terminal immutability")
	} else if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23514" {
		t.Fatalf("unexpected terminal immutability failure: %v", err)
	}
	if state, err := CheckCompatibility(t.Context(), f.f.databaseURL); err != nil || state != CompatibilityCurrent {
		t.Fatalf("upgraded compatibility: %v %v", state, err)
	}
}
