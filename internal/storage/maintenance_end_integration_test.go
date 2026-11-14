//go:build integration

package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestIntegrationMaintenanceEndStrictShortening(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"extension", "equal end", "shortening and replay"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newMaintenanceFixture(t)
			setIntegrationUserRole(t, f.f, "editor")
			r, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("create"))
			if err != nil {
				t.Fatal(err)
			}
			f.now = r.Start.Add(time.Minute)
			var fresh [32]byte
			fresh[0] = 99
			if _, err := IssueSession(t.Context(), f.f.databaseURL, IssueSessionInput{UserID: f.f.userID, ExpectedCredentialEpoch: f.f.epoch, ExpectedCredentialRevision: f.f.revision, Verifier: fresh, Now: f.now, RequestID: "active-session"}); err != nil {
				t.Fatal(err)
			}
			f.proof.SessionVerifier = fresh
			// Capture exact durable maintenance effects, including work versions and
			// receipts, so refusal/replay cannot quietly enqueue or audit again.
			snapshot := func() string {
				t.Helper()
				var value string
				if err := f.conn.QueryRow(t.Context(), `SELECT jsonb_build_object(
'identity',(SELECT to_jsonb(x) FROM almanac_maintenance_exclusions x WHERE id=$1),
'revisions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY revision) FROM almanac_maintenance_exclusion_revisions x WHERE exclusion_id=$1),
'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM almanac_audit_events x WHERE subject_id=$1),
'state',(SELECT jsonb_agg(to_jsonb(x) ORDER BY slo_id) FROM almanac_slo_exclusion_state x),
'changes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY slo_id,revision) FROM almanac_slo_exclusion_changes x),
'work',(SELECT jsonb_agg(to_jsonb(x) ORDER BY slo_id) FROM almanac_slo_exclusion_work x),
'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation,idempotency_key) FROM almanac_management_receipts x WHERE operation LIKE 'maintenance.%'))::text`, r.ID).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			input := EndMaintenanceInput{Proof: f.proof, ID: r.ID, Revision: r.Revision, End: r.End, Reason: "Completed early", IdempotencyKey: "end", RequestID: "end", Now: f.clock}
			if mode != "shortening and replay" {
				if mode == "extension" {
					input.End = r.End.Add(5 * time.Hour)
				}
				before := snapshot()
				if _, err := EndMaintenance(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAgentForbidden) {
					t.Errorf("editor %s must be refused: %v", mode, err)
				}
				if after := snapshot(); after != before {
					t.Error("refused end changed durable maintenance state")
				}
				return
			}
			input.End = f.now.Add(time.Minute)
			ended, err := EndMaintenance(t.Context(), f.f.databaseURL, input)
			if err != nil || ended.Revision != 2 || !ended.Start.Equal(r.Start) || !ended.End.Equal(input.End) {
				t.Fatalf("prospective shortening: %+v %v", ended, err)
			}
			before := snapshot()
			f.now = input.End.Add(time.Minute)
			replayed, err := EndMaintenance(t.Context(), f.f.databaseURL, input)
			if err != nil || !reflect.DeepEqual(replayed, ended) || snapshot() != before {
				t.Fatalf("same-key replay after effective end changed evidence: %+v %v", replayed, err)
			}
			input.End = input.End.Add(time.Minute)
			if _, err := EndMaintenance(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceConflict) || snapshot() != before {
				t.Fatalf("changed replay did not conflict without writes: %v", err)
			}
			// A separate still-active declaration can be extended through the
			// Administrator edit operation, not through the prospective end path.
			setIntegrationUserRole(t, f.f, "administrator")
			activeInput := f.input("active")
			activeInput.Start = f.now.Add(-time.Minute)
			active, err := CreateMaintenance(t.Context(), f.f.databaseURL, activeInput)
			if err != nil {
				t.Fatal(err)
			}
			edit := EditMaintenanceInput{Proof: f.proof, ID: active.ID, Revision: active.Revision, Title: active.Title, Reason: "Work continues", Start: active.Start, End: active.End.Add(time.Hour), SloIDs: active.SloIDs, IdempotencyKey: "extend", RequestID: "extend", Now: f.clock}
			extended, err := EditMaintenance(t.Context(), f.f.databaseURL, edit)
			if err != nil || extended.Revision != 2 || !extended.End.Equal(edit.End) {
				t.Fatalf("administrator active edit extension: %+v %v", extended, err)
			}
		})
	}
}
