//go:build integration

package storage

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type maintenanceFixture struct {
	*selfHealFixture
	ids   []string
	proof DatasourceProof
}

func newMaintenanceFixture(t *testing.T) maintenanceFixture {
	t.Helper()
	s := newSelfHealFixture(t, "maintenance", true)
	a := s.createSLO(t, "maintenance-a")
	var verifier [32]byte
	verifier[0] = 98
	proof := DatasourceProof{SessionVerifier: verifier}
	id, _ := canonicalUUID(a)
	row, err := dbgen.New(s.conn).GetSlo(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "maintenance-b", DisplayName: "Independent SLO", ServiceID: row.ServiceID.String(), SliID: s.sliID, Target: "0.99", RollingWindowSeconds: 3600, IdempotencyKey: "maintenance-b", RequestID: "maintenance-b", Now: s.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	return maintenanceFixture{s, []string{a, b.ID}, proof}
}

func (f maintenanceFixture) input(key string) CreateMaintenanceInput {
	return CreateMaintenanceInput{Proof: f.proof, Title: "Database work", Reason: "Replace storage", Start: f.at.Add(time.Hour), End: f.at.Add(2 * time.Hour), SloIDs: []string{f.ids[0]}, IdempotencyKey: key, RequestID: key, Now: f.clock}
}

func TestIntegrationMaintenanceAuthorityRevisionAndReplay(t *testing.T) {
	f := newMaintenanceFixture(t)
	input := f.input("create")
	r, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != 1 || r.CreatorID != f.f.userID || !slices.Equal(r.SloIDs, input.SloIDs) {
		t.Fatalf("lost persisted identity: %+v", r)
	}
	replay, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil || replay.ID != r.ID || replay.Revision != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	input.Reason = "different body"
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceConflict) {
		t.Fatalf("digest conflict: %v", err)
	}
	edit := EditMaintenanceInput{Proof: f.proof, ID: r.ID, Revision: 1, Title: r.Title, Reason: "Move scope", Start: r.Start, End: r.End, SloIDs: []string{f.ids[1]}, IdempotencyKey: "edit", RequestID: "edit", Now: f.clock}
	changed, err := EditMaintenance(t.Context(), f.f.databaseURL, edit)
	if err != nil || changed.Revision != 2 {
		t.Fatalf("edit: %+v %v", changed, err)
	}
	edit.IdempotencyKey = "stale"
	if _, err := EditMaintenance(t.Context(), f.f.databaseURL, edit); !errors.Is(err, ErrMaintenanceConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	old, err := ReadMaintenance(t.Context(), f.f.databaseURL, ReadMaintenanceInput{Proof: f.proof, ID: r.ID, Revision: 1, Now: f.clock})
	if err != nil || old.Reason != "Replace storage" || !slices.Equal(old.SloIDs, []string{f.ids[0]}) {
		t.Fatalf("old evidence changed: %+v %v", old, err)
	}
	var audits, changes, work int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_audit_events WHERE subject_id=$1),(SELECT count(*) FROM almanac_slo_exclusion_changes),(SELECT count(*) FROM almanac_slo_exclusion_work)`, r.ID).Scan(&audits, &changes, &work); err != nil {
		t.Fatal(err)
	}
	if audits != 2 || changes != 3 || work != 2 {
		t.Fatalf("non-atomic/repeated effects: audit=%d changes=%d work=%d", audits, changes, work)
	}
	for i, want := range []int64{2, 1} {
		var revision int64
		if err := f.conn.QueryRow(t.Context(), `SELECT revision FROM almanac_slo_exclusion_state WHERE slo_id=$1`, f.ids[i]).Scan(&revision); err != nil || revision != want {
			t.Fatalf("scope %d revision=%d want %d: %v", i, revision, want, err)
		}
	}
}

func TestIntegrationMaintenanceAuthorityPermissions(t *testing.T) {
	f := newMaintenanceFixture(t)
	for _, role := range []string{"viewer", "editor", "administrator"} {
		setIntegrationUserRole(t, f.f, role)
		input := f.input(role)
		input.Start, input.End = f.at.Add(-time.Hour), f.at.Add(time.Hour)
		_, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
		if role == "administrator" {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrAgentForbidden) {
			t.Fatalf("%s retrospective authority: %v", role, err)
		}
	}
	setIntegrationUserRole(t, f.f, "editor")
	input := f.input("future")
	r, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
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
	end := EndMaintenanceInput{Proof: f.proof, ID: r.ID, Revision: 1, End: r.End.Add(-time.Minute), Reason: "Completed early", IdempotencyKey: "end", RequestID: "end", Now: f.clock}
	ended, err := EndMaintenance(t.Context(), f.f.databaseURL, end)
	if err != nil || !ended.Start.Equal(r.Start) || !ended.End.Equal(end.End) {
		t.Fatalf("prospective end: %+v %v", ended, err)
	}
	end.Revision, end.End, end.IdempotencyKey = 2, f.now.Add(-time.Minute), "remove-elapsed"
	if _, err := EndMaintenance(t.Context(), f.f.databaseURL, end); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("removed elapsed coverage: %v", err)
	}
	command := MaintenanceCommandInput{Proof: f.proof, ID: r.ID, Revision: 2, Reason: "Correct declaration", IdempotencyKey: "withdraw", RequestID: "withdraw", Now: f.clock}
	if _, err := WithdrawMaintenance(t.Context(), f.f.databaseURL, command); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("editor withdrew elapsed coverage: %v", err)
	}
	setIntegrationUserRole(t, f.f, "administrator")
	withdrawn, err := WithdrawMaintenance(t.Context(), f.f.databaseURL, command)
	if err != nil || !withdrawn.Tombstone || withdrawn.Revision != 3 {
		t.Fatalf("withdraw: %+v %v", withdrawn, err)
	}
	setIntegrationUserRole(t, f.f, "viewer")
	page, err := ListMaintenance(t.Context(), f.f.databaseURL, ListMaintenanceInput{Proof: f.proof, Limit: 1, Now: f.clock})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("viewer page: %+v %v", page, err)
	}
	page2, err := ListMaintenance(t.Context(), f.f.databaseURL, ListMaintenanceInput{Proof: f.proof, Limit: 1, Cursor: page.NextCursor, Now: f.clock})
	if err != nil || len(page2.Items) != 1 || page2.Items[0].ID == page.Items[0].ID {
		t.Fatalf("next page: %+v %v", page2, err)
	}
}

func TestIntegrationMaintenanceAuthorityAlignmentAndRollback(t *testing.T) {
	f := newMaintenanceFixture(t)
	input := f.input("unaligned")
	input.Start = input.Start.Add(time.Second)
	_, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	alignment, ok := errors.AsType[*MaintenanceAlignmentError](err)
	if !ok || !alignment.SuggestedStart.Equal(f.at.Add(time.Hour)) || !alignment.SuggestedEnd.Equal(input.End) {
		t.Fatalf("alignment preview: %#v %v", alignment, err)
	}
	input = f.input("missing")
	input.SloIDs = append(input.SloIDs, "00000000-0000-0000-0000-000000000001")
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceNotFound) {
		t.Fatalf("missing scope: %v", err)
	}
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION reject_maintenance_work() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'work failure'; END $$; CREATE TRIGGER reject_work BEFORE INSERT ON almanac_slo_exclusion_work FOR EACH ROW EXECUTE FUNCTION reject_maintenance_work()`); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("rollback")); err == nil {
		t.Fatal("work failure accepted")
	}
	var roots, revisions, audits, receipts, state int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_exclusions),(SELECT count(*) FROM almanac_maintenance_exclusion_revisions),(SELECT count(*) FROM almanac_audit_events WHERE action LIKE 'maintenance.%'),(SELECT count(*) FROM almanac_management_receipts WHERE operation LIKE 'maintenance.%'),(SELECT sum(revision) FROM almanac_slo_exclusion_state)`).Scan(&roots, &revisions, &audits, &receipts, &state); err != nil {
		t.Fatal(err)
	}
	if roots+revisions+audits+receipts+state != 0 {
		t.Fatalf("partial mutation after failure: %d %d %d %d %d", roots, revisions, audits, receipts, state)
	}
}

func TestIntegrationMaintenanceAuthorityHistoricalLockWait(t *testing.T) {
	f := newMaintenanceFixture(t)
	setIntegrationUserRole(t, f.f, "editor")
	input := f.input("wait")
	input.Start, input.End = f.at.Add(time.Minute), f.at.Add(2*time.Minute)
	var clock atomic.Int64
	clock.Store(f.at.UnixMicro())
	input.Now = func() time.Time { return time.UnixMicro(clock.Load()).UTC() }
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `SELECT id FROM almanac_slos WHERE id=$1 FOR UPDATE`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); result <- err }()
	if err := waitForAgentAccessLockWait(t.Context(), t, f.f, "LockMaintenanceSlo"); err != nil {
		t.Fatal(err)
	}
	clock.Store(input.Start.UnixMicro())
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("request-start authority escaped lock wait: %v", err)
	}
}

func TestIntegrationMaintenanceAuthoritySnapshotFence(t *testing.T) {
	f := newMaintenanceFixture(t)
	id, _ := canonicalUUID(f.ids[0])
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	snapshot, err := readSloExclusionSnapshot(t.Context(), q, id, f.at, f.at.Add(4*time.Hour))
	if err != nil || snapshot.Revision != 0 || len(snapshot.Intervals) != 0 {
		t.Fatalf("zero snapshot: %+v %v", snapshot, err)
	}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("fence")); err != nil {
		t.Fatal(err)
	}
	if err := fenceSloExclusionSnapshot(t.Context(), q, id, 0); err == nil {
		t.Fatal("old snapshot permitted publication after revision advanced")
	}
	rollbackTransaction(tx)
	current, err := readSloExclusionSnapshot(t.Context(), dbgen.New(f.conn), id, f.at, f.at.Add(4*time.Hour))
	if err != nil || current.Revision != 1 || len(current.Intervals) != 1 {
		t.Fatalf("current snapshot: %+v %v", current, err)
	}
	other, _ := canonicalUUID(f.ids[1])
	independent, err := readSloExclusionSnapshot(t.Context(), dbgen.New(f.conn), other, f.at, f.at.Add(4*time.Hour))
	if err != nil || independent.Revision != 0 || len(independent.Intervals) != 0 {
		t.Fatalf("SLI-wide exclusion: %+v %v", independent, err)
	}
}

func TestIntegrationMaintenanceAuthorityAffectedScopeBound(t *testing.T) {
	f := newMaintenanceFixture(t)
	var clones []string
	if err := f.conn.QueryRow(t.Context(), `
 WITH roots AS (
  INSERT INTO almanac_slos(id,name,display_name,created_at,updated_at)
  SELECT gen_random_uuid(),'maintenance-scope-'||n,'Scope '||n,$2,$2 FROM generate_series(1,99) n RETURNING id
 ), definitions AS (
  INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
  SELECT roots.id,1,d.service_id,d.sli_id,d.measurement_revision,d.target,d.rolling_window_seconds,d.cadence_seconds,$2,$2
  FROM roots CROSS JOIN almanac_slo_definitions d WHERE d.slo_id=$1 AND d.revision=1 RETURNING slo_id
 ) SELECT array_agg(slo_id::text ORDER BY slo_id) FROM definitions`, f.ids[0], f.at).Scan(&clones); err != nil {
		t.Fatal(err)
	}
	input := f.input("hundred")
	input.SloIDs = append([]string{f.ids[0]}, clones...)
	r, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil || len(r.SloIDs) != 100 {
		t.Fatalf("100 scopes rejected: %+v %v", r, err)
	}
	edit := EditMaintenanceInput{Proof: f.proof, ID: r.ID, Revision: 1, Title: r.Title, Reason: "Replacement affects 101", Start: r.Start, End: r.End, SloIDs: append([]string{f.ids[1]}, clones...), IdempotencyKey: "union-overflow", RequestID: "union-overflow", Now: f.clock}
	if _, err := EditMaintenance(t.Context(), f.f.databaseURL, edit); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("old/new scope union escaped bound: %v", err)
	}
	input.IdempotencyKey = "over-limit"
	input.SloIDs = append(input.SloIDs, f.ids[1])
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceInvalid) {
		t.Fatalf("101 scopes accepted: %v", err)
	}
	var revision int64
	if err := f.conn.QueryRow(t.Context(), `SELECT revision FROM almanac_slo_exclusion_state WHERE slo_id=$1`, f.ids[1]).Scan(&revision); err != nil || revision != 0 {
		t.Fatalf("failed union mutation touched added scope: %d %v", revision, err)
	}
}

func TestIntegrationMaintenanceAuthorityCompletionTime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance time.Duration
		want    error
	}{
		{"elapsed", time.Hour, ErrAgentForbidden},
		{"expired", 31 * time.Minute, ErrInvalidSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaintenanceFixture(t)
			setIntegrationUserRole(t, f.f, "editor")
			if tc.name == "expired" {
				// The installed policy defaults to eight hours. Give this session
				// an explicit shorter lifetime to cross while waiting on the write.
				if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sessions SET idle_minutes=30 WHERE verifier=$1`, f.proof.SessionVerifier[:]); err != nil {
					t.Fatal(err)
				}
			}
			r, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("initial"))
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(blocker)
			if _, err := blocker.Exec(t.Context(), `SELECT slo_id FROM almanac_slo_exclusion_work WHERE slo_id=$1 FOR UPDATE`, f.ids[0]); err != nil {
				t.Fatal(err)
			}
			var at atomic.Int64
			at.Store(f.at.UnixMicro())
			clock := func() time.Time { return time.UnixMicro(at.Load()).UTC() }
			input := EditMaintenanceInput{Proof: f.proof, ID: r.ID, Revision: 1, Title: r.Title, Reason: "Completion race", Start: r.Start, End: r.End.Add(time.Minute), SloIDs: r.SloIDs, IdempotencyKey: "waiting", RequestID: "waiting", Now: clock}
			result := make(chan error, 1)
			go func() { _, err := EditMaintenance(t.Context(), f.f.databaseURL, input); result <- err }()
			if err := waitForAgentAccessLockWait(t.Context(), t, f.f, "EnqueueMaintenanceWork"); err != nil {
				t.Fatal(err)
			}
			at.Store(f.at.Add(tc.advance).UnixMicro())
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, tc.want) {
				t.Fatalf("post-write authority: %v want %v", err, tc.want)
			}
			var revision, changes, audits, receipts int64
			if err := f.conn.QueryRow(t.Context(), `SELECT current_revision,(SELECT count(*) FROM almanac_slo_exclusion_changes),(SELECT count(*) FROM almanac_audit_events WHERE subject_id=$1),(SELECT count(*) FROM almanac_management_receipts WHERE operation LIKE 'maintenance.%') FROM almanac_maintenance_exclusions WHERE id=$1`, r.ID).Scan(&revision, &changes, &audits, &receipts); err != nil {
				t.Fatal(err)
			}
			if revision != 1 || changes != 1 || audits != 1 || receipts != 1 {
				t.Fatalf("completion failure did not roll back: %d %d %d %d", revision, changes, audits, receipts)
			}
		})
	}
}

func TestIntegrationMaintenanceAuthoritySnapshotsAndDurableWork(t *testing.T) {
	f := newMaintenanceFixture(t)
	a, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("first"))
	if err != nil {
		t.Fatal(err)
	}
	second := f.input("second")
	second.Start, second.End = a.Start.Add(30*time.Minute), a.End.Add(time.Hour)
	b, err := CreateMaintenance(t.Context(), f.f.databaseURL, second)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := canonicalUUID(f.ids[0])
	q := dbgen.New(f.conn)
	union, err := readSloExclusionSnapshot(t.Context(), q, id, f.at, f.at.Add(4*time.Hour))
	if err != nil || union.Revision != 2 || len(union.Intervals) != 1 || !union.Intervals[0].Start.Equal(a.Start) || !union.Intervals[0].End.Equal(b.End) || len(union.Evidence) != 2 {
		t.Fatalf("overlap snapshot: %+v %v", union, err)
	}
	old, err := readSloExclusionSnapshotAt(t.Context(), q, id, 1, f.at, f.at.Add(4*time.Hour))
	if err != nil || len(old.Intervals) != 1 || !old.Intervals[0].End.Equal(a.End) || len(old.Evidence) != 1 {
		t.Fatalf("old snapshot changed: %+v %v", old, err)
	}
	edited, err := EditMaintenance(t.Context(), f.f.databaseURL, EditMaintenanceInput{Proof: f.proof, ID: a.ID, Revision: 1, Title: a.Title, Reason: "Correct reason only", Start: a.Start, End: a.End, SloIDs: a.SloIDs, IdempotencyKey: "reason", RequestID: "reason", Now: f.clock})
	if err != nil || edited.Revision != 2 {
		t.Fatalf("reason revision: %+v %v", edited, err)
	}
	var changed bool
	if err := f.conn.QueryRow(t.Context(), `SELECT coverage_changed FROM almanac_slo_exclusion_changes WHERE slo_id=$1 AND revision=3`, id).Scan(&changed); err != nil || changed {
		t.Fatalf("reason-only change attributed to math: %v %v", changed, err)
	}
	if rows, err := q.AcknowledgeMaintenanceWork(t.Context(), dbgen.AcknowledgeMaintenanceWorkParams{SloID: id, DesiredRevision: 1, WorkVersion: 1}); err != nil || rows != 0 {
		t.Fatalf("stale ack removed newer work: %d %v", rows, err)
	}
	var desired, version int64
	var start, end time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT desired_revision,work_version,affected_start,affected_end FROM almanac_slo_exclusion_work WHERE slo_id=$1`, id).Scan(&desired, &version, &start, &end); err != nil {
		t.Fatal(err)
	}
	if desired != 3 || version != 3 || !start.Equal(a.Start) || !end.Equal(b.End) {
		t.Fatalf("lost coalesced work: %d %d %v %v", desired, version, start, end)
	}
	for _, record := range []MaintenanceRecord{edited, b} {
		cancelled, err := CancelMaintenance(t.Context(), f.f.databaseURL, MaintenanceCommandInput{Proof: f.proof, ID: record.ID, Revision: record.Revision, Reason: "Cancelled", IdempotencyKey: record.ID, RequestID: "cancel", Now: f.clock})
		if err != nil || !cancelled.Tombstone {
			t.Fatalf("cancel: %+v %v", cancelled, err)
		}
	}
	empty, err := readSloExclusionSnapshot(t.Context(), q, id, f.at, f.at.Add(4*time.Hour))
	if err != nil || empty.Revision != 5 || len(empty.Intervals) != 0 {
		t.Fatalf("cancelled masks remain: %+v %v", empty, err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_exclusion_revisions SET reason='rewritten' WHERE exclusion_id=$1 AND revision=1`, a.ID); err == nil {
		t.Fatal("immutable provenance updated")
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_exclusion_scopes SET slo_id=$2 WHERE exclusion_id=$1 AND revision=1`, a.ID, f.ids[1]); err == nil {
		t.Fatal("immutable scope updated")
	}
}

func TestIntegrationMaintenanceAuthorityAuditFailureAndRevokedReplay(t *testing.T) {
	f := newMaintenanceFixture(t)
	input := f.input("audit")
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION reject_maintenance_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'maintenance.%' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_maintenance_audit BEFORE INSERT ON almanac_audit_events FOR EACH ROW EXECUTE FUNCTION reject_maintenance_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err == nil {
		t.Fatal("unaudited write accepted")
	}
	var roots int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_maintenance_exclusions`).Scan(&roots); err != nil || roots != 0 {
		t.Fatalf("audit rollback: %d %v", roots, err)
	}
	if _, err := f.conn.Exec(t.Context(), `DROP TRIGGER reject_maintenance_audit ON almanac_audit_events`); err != nil {
		t.Fatal(err)
	}
	r, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `DELETE FROM almanac_sessions WHERE verifier=$1`, f.proof.SessionVerifier[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked replay disclosed result: %v", err)
	}
	if _, err := ReadMaintenance(t.Context(), f.f.databaseURL, ReadMaintenanceInput{Proof: f.proof, ID: r.ID, Now: f.clock}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked read disclosed result: %v", err)
	}
}

func TestIntegrationMaintenanceAuthorityPublicationLockSerializes(t *testing.T) {
	f := newMaintenanceFixture(t)
	r, err := CreateMaintenance(t.Context(), f.f.databaseURL, f.input("initial"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := canonicalUUID(f.ids[0])
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if err := fenceSloExclusionSnapshot(t.Context(), dbgen.New(tx), id, 1); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := EditMaintenance(t.Context(), f.f.databaseURL, EditMaintenanceInput{Proof: f.proof, ID: r.ID, Revision: 1, Title: r.Title, Reason: "Concurrent change", Start: r.Start, End: r.End.Add(time.Minute), SloIDs: r.SloIDs, IdempotencyKey: "change", RequestID: "change", Now: f.clock})
		result <- err
	}()
	if err := waitForAgentAccessLockWait(t.Context(), t, f.f, "LockMaintenanceState"); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := tx.QueryRow(t.Context(), `SELECT revision FROM almanac_slo_exclusion_state WHERE slo_id=$1`, id).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("mutation crossed held publication fence: %d %v", revision, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := fenceSloExclusionSnapshot(t.Context(), dbgen.New(f.conn), id, 1); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("old publisher passed after new change: %v", err)
	}
}

func TestIntegrationMaintenanceAuthorityEffectiveGrids(t *testing.T) {
	f := newMaintenanceFixture(t)
	// 1,800,000,300 is divisible by both 60 and 70. This models a scheduled
	// definition backed by its own 70-second datasource/measurement revision.
	activation := time.Unix(1_800_000_300, 0).UTC()
	if _, err := f.conn.Exec(t.Context(), `
 WITH source AS (
  INSERT INTO almanac_datasources(id,name,display_name,provider,configuration_format,endpoint,tls,authentication_mode,polling_interval_seconds,agent_id,created_at,updated_at)
  SELECT gen_random_uuid(),'maintenance-next-grid','Next grid',d.provider,d.configuration_format,d.endpoint,d.tls,'none',70,d.agent_id,$2,$2
  FROM almanac_datasources d JOIN almanac_sli_measurements m ON m.datasource_id=d.id WHERE m.sli_id=$1 AND m.revision=1 RETURNING id
 ), measurement AS (
  INSERT INTO almanac_sli_measurements(sli_id,revision,datasource_id,agent_id,measurement_kind,component_mode,good_query,total_query,effective_from,created_at)
  SELECT m.sli_id,2,source.id,m.agent_id,m.measurement_kind,m.component_mode,m.good_query,m.total_query,$2,$2
  FROM almanac_sli_measurements m CROSS JOIN source WHERE m.sli_id=$1 AND m.revision=1 RETURNING sli_id,revision
 ) INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
 SELECT d.slo_id,2,d.service_id,m.sli_id,m.revision,d.target,4200,70,$2,$2
 FROM almanac_slo_definitions d CROSS JOIN measurement m WHERE d.slo_id=$3 AND d.revision=1`, f.sliID, activation, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	input := f.input("before-grid")
	input.Start, input.End = activation.Add(-120*time.Second), activation.Add(-60*time.Second)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatalf("unrelated future grid constrained earlier interval: %v", err)
	}
	input.IdempotencyKey = "cross-grid"
	input.Start, input.End = activation.Add(-60*time.Second), activation.Add(60*time.Second)
	_, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	alignment, ok := errors.AsType[*MaintenanceAlignmentError](err)
	if !ok || !alignment.SuggestedStart.Equal(activation.Add(-420*time.Second)) || !alignment.SuggestedEnd.Equal(activation.Add(420*time.Second)) {
		t.Fatalf("effective common grid: %+v %v", alignment, err)
	}
	input.IdempotencyKey = "after-grid"
	input.Start, input.End = activation, activation.Add(70*time.Second)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatalf("effective 70-second interval rejected: %v", err)
	}
	input.IdempotencyKey = "both-grids"
	input.SloIDs = f.ids
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrMaintenanceAlignment) {
		t.Fatalf("independent 60-second scope ignored: %v", err)
	}
}

func TestIntegrationMaintenanceAuthorityUpgradeBackfillsFence(t *testing.T) {
	databaseURL := integrationDatabase(t)
	migrations, err := LoadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeWithMigrations(t.Context(), databaseURL, migrations[:53]); err != nil {
		t.Fatal(err)
	}
	conn := integrationConnect(t, databaseURL)
	defer closeIntegrationConn(conn)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `
 INSERT INTO almanac_agents(id,name,display_name,created_at,updated_at) VALUES(md5('maintenance-agent')::uuid,'upgrade-agent','Upgrade agent',now(),now());
 INSERT INTO almanac_datasources(id,name,display_name,provider,configuration_format,endpoint,tls,authentication_mode,polling_interval_seconds,agent_id,created_at,updated_at)
 VALUES(md5('maintenance-source')::uuid,'upgrade-source','Upgrade source','prometheus','prometheus-http-v1','https://example.test','{}','none',60,md5('maintenance-agent')::uuid,now(),now());
 INSERT INTO almanac_slis(id,name,display_name,created_at,updated_at) VALUES(md5('maintenance-sli')::uuid,'upgrade-sli','Upgrade SLI',now(),now());
 INSERT INTO almanac_sli_measurements(sli_id,revision,datasource_id,agent_id,measurement_kind,component_mode,good_query,total_query,effective_from,created_at)
 VALUES(md5('maintenance-sli')::uuid,1,md5('maintenance-source')::uuid,md5('maintenance-agent')::uuid,'event_counts','interval_count','good','total',now(),now());
 INSERT INTO almanac_services(id,name,display_name,created_at,updated_at) VALUES(md5('maintenance-service')::uuid,'upgrade-service','Upgrade service',now(),now());
 INSERT INTO almanac_slos(id,name,display_name,created_at,updated_at) VALUES(md5('maintenance-slo')::uuid,'upgrade-slo','Upgrade SLO',now(),now());
 INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
 VALUES(md5('maintenance-slo')::uuid,1,md5('maintenance-service')::uuid,md5('maintenance-sli')::uuid,1,0.99,3600,60,now(),now());`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	var count, revision int
	if err := conn.QueryRow(t.Context(), `SELECT count(*),sum(revision) FROM almanac_slo_exclusion_state`).Scan(&count, &revision); err != nil || count != 1 || revision != 0 {
		t.Fatalf("existing SLO has no zero fence: %d %d %v", count, revision, err)
	}
}
