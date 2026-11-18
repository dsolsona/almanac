//go:build integration

package storage

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationMaintenanceServiceProjectionPreservesMembership(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	q := dbgen.New(f.conn)
	subject, err := q.GetSlo(t.Context(), mustPGUUID(f.ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	input := f.input("project-service")
	input.ScopeKind, input.SloIDs, input.ServiceIDs = MaintenanceScopeServices, nil, []string{subject.ServiceID.String()}
	input.Start, input.End = f.at.Add(5*time.Minute), f.at.Add(20*time.Minute)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "project-destination", DisplayName: "Outside selection",
		IdempotencyKey: "project-destination", RequestID: "project-destination", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	overlayInput := ListSloMaintenanceInput{
		ListMaintenanceInput: ListMaintenanceInput{Scoped: true, Proof: f.proof, Now: f.clock},
		SloID:                f.ids[0], Start: window.Start, End: window.End,
	}
	pending, err := ListSloMaintenance(t.Context(), f.f.databaseURL, overlayInput)
	if err != nil || !pending.Pending || len(pending.Items) != 0 {
		t.Fatalf("unprojected overlay is advertised as ready: %+v: %v", pending, err)
	}
	if err := requireServiceMaintenanceReady(t.Context(), q, subject.ID, window.Start.Add(-time.Minute), window.Start); err != nil {
		t.Fatalf("past range was held up by future projection: %v", err)
	}
	if err := requireServiceMaintenanceReady(t.Context(), q, subject.ID, window.Start, window.End); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("fresh calculations admitted incomplete authority: %v", err)
	}

	step := func() {
		t.Helper()
		if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 1, time.Second, f.clock); err != nil {
			t.Fatal(err)
		}
	}
	step()
	var installed int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_exclusion_changes WHERE exclusion_id=$1`, window.ID).Scan(&installed); err != nil || installed != 1 {
		t.Fatalf("first page installed %d SLOs: %v", installed, err)
	}
	step()
	step()
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_slo_exclusion_changes WHERE exclusion_id=$1`, window.ID).Scan(&installed); err != nil || installed != 2 {
		t.Fatalf("resumed pagination installed %d SLOs: %v", installed, err)
	}

	departure := window.Start.Add(2*time.Minute + 30*time.Second)
	rejoin := window.Start.Add(7*time.Minute + 30*time.Second)
	moveMaintenanceSLO(t, f, other.ID, 2, departure)
	if witness := serviceSuppressionAt(t, f, f.ids[0], departure.Add(-time.Second)); witness == nil || witness.ID != window.ID {
		t.Fatalf("departed SLO lost historical suppression: %+v", witness)
	}
	if witness := serviceSuppressionAt(t, f, f.ids[1], departure); witness == nil || witness.ID != window.ID {
		t.Fatalf("remaining SLO lost direct suppression: %+v", witness)
	}
	if witness := serviceSuppressionAt(t, f, f.ids[0], departure); witness != nil {
		t.Fatalf("departed SLO kept direct suppression: %+v", witness)
	}
	moveMaintenanceSLO(t, f, subject.ServiceID.String(), 3, rejoin)
	if witness := serviceSuppressionAt(t, f, f.ids[0], rejoin); witness == nil || !witness.Start.Equal(rejoin) {
		t.Fatalf("unaligned rejoin has no direct suppression: %+v", witness)
	}
	f.now = rejoin
	if err := requireServiceMaintenanceReady(t.Context(), q, subject.ID, window.Start, window.End); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("prospective moves left stale coverage advertised as current: %v", err)
	}
	step()
	step()

	state, err := q.ReadMaintenanceState(t.Context(), subject.ID)
	if err != nil || state != 2 {
		t.Fatalf("membership recomputation revision=%d: %v", state, err)
	}
	snapshot, err := readSloExclusionSnapshotAt(t.Context(), q, subject.ID, state, window.Start, window.End)
	if err != nil || len(snapshot.Intervals) != 2 {
		t.Fatalf("current snapshot = %+v: %v", snapshot, err)
	}
	a, b := snapshot.Intervals[0], snapshot.Intervals[1]
	if !a.Start.Equal(window.Start) || !a.End.Equal(window.Start.Add(2*time.Minute)) ||
		!b.Start.Equal(window.Start.Add(8*time.Minute)) || !b.End.Equal(window.End) {
		t.Fatalf("unaligned assignment boundaries excluded partial observations: %+v", snapshot.Intervals)
	}
	old, err := readSloExclusionSnapshotAt(t.Context(), q, subject.ID, 1, window.Start, window.End)
	if err != nil || len(old.Intervals) != 1 || !old.Intervals[0].Start.Equal(window.Start) || !old.Intervals[0].End.Equal(window.End) {
		t.Fatalf("membership rewrite changed pinned history: %+v, %v", old, err)
	}

	overlay, err := ListSloMaintenance(t.Context(), f.f.databaseURL, overlayInput)
	if err != nil || overlay.Pending || len(overlay.Items) != 2 {
		t.Fatalf("overlay = %+v: %v", overlay, err)
	}
	if !overlay.Items[0].EffectiveStart.Equal(a.Start) || !overlay.Items[0].EffectiveEnd.Equal(a.End) ||
		!overlay.Items[1].EffectiveStart.Equal(b.Start) || !overlay.Items[1].EffectiveEnd.Equal(b.End) ||
		overlay.Items[1].ID != window.ID || !overlay.Items[1].Start.Equal(window.Start) {
		t.Fatalf("overlay lost declaration context or exact coverage: %+v", overlay.Items)
	}
	impact, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, MaintenanceImpactInput{
		Proof: f.proof, ID: window.ID, Revision: window.Revision, Now: f.clock, Limit: 1,
	})
	if err != nil || !impact.Ready || impact.Total != 2 || len(impact.Items) != 1 || impact.NextCursor == "" {
		t.Fatalf("first impact page = %+v: %v", impact, err)
	}

	build := compilerCapture(t, f, nil)
	compilerFinish(t, f, build.ID, 1)
	coverage, err := readMaintenanceCoverage(t.Context(), q, build.ID, window.Start, window.End)
	wantUS := (9 * time.Minute).Microseconds()
	if err != nil || coverage.ExcludedMicroseconds != wantUS {
		t.Fatalf("compiled service coverage = %+v, want %dµs: %v", coverage, wantUS, err)
	}

	shorter, err := EndMaintenance(t.Context(), f.f.databaseURL, EndMaintenanceInput{
		Scoped: true, Proof: f.proof, ID: window.ID, Revision: 1, End: window.Start.Add(10 * time.Minute),
		Reason: "Done early", IdempotencyKey: "project-service-end", RequestID: "project-service-end", Now: f.clock,
	})
	if err != nil || shorter.Revision != 2 {
		t.Fatalf("early end = %+v, %v", shorter, err)
	}
	compilerTx(t, f, func(tx pgx.Tx) error {
		stale, err := dbgen.New(tx).ApplyServiceMaintenanceProjectionMember(t.Context(), dbgen.ApplyServiceMaintenanceProjectionMemberParams{
			SloID: subject.ID, ExclusionID: mustPGUUID(window.ID), Revision: 1, NowAt: timestamp(f.now),
		})
		if err == nil && stale {
			t.Fatal("superseded projection wrote old coverage")
		}
		return err
	})
	historical, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, MaintenanceImpactInput{
		Proof: f.proof, ID: window.ID, Revision: 1, Now: f.clock,
	})
	endsAtOriginalBoundary := 0
	for _, member := range historical.Items {
		if member.End.Equal(window.End) {
			endsAtOriginalBoundary++
		}
	}
	if err != nil || !historical.Ready || historical.Total != 2 || len(historical.Items) != 3 || endsAtOriginalBoundary != 2 {
		t.Fatalf("historical impact substituted a newer revision: %+v, %v", historical, err)
	}
}

func moveMaintenanceSLO(t *testing.T, f maintenanceFixture, serviceID string, revision int64, at time.Time) {
	t.Helper()
	moveMaintenanceSLOAtCadence(t, f, serviceID, revision, at, 0)
}

func moveMaintenanceSLOAtCadence(t *testing.T, f maintenanceFixture, serviceID string, revision int64, at time.Time, cadence int) {
	t.Helper()
	tx, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from)
		SELECT slo_id,$3,$2,sli_id,measurement_revision,target,rolling_window_seconds,coalesce(nullif($5::integer,0),cadence_seconds),$4,$4
		FROM almanac_slo_definitions WHERE slo_id=$1 AND revision=$3::bigint-1
	`, f.ids[0], serviceID, revision, at, cadence); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE almanac_slos SET definition_revision=$2, updated_at=$3 WHERE id=$1`, f.ids[0], revision, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationMaintenanceAllServicesIncludesProspectiveJoins(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("project-all-services")
	input.ScopeKind, input.SloIDs = MaintenanceScopeAllServices, nil
	input.Start, input.End = f.at.Add(5*time.Minute), f.at.Add(10*time.Minute)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	f.now = window.Start.Add(30 * time.Second)
	service, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "during-window-service", DisplayName: "During-window service",
		IdempotencyKey: "during-window-service", RequestID: "during-window-service", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, objective, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "during-window-slo", DisplayName: "During-window SLO", ServiceID: service.ID,
		SliID: f.sliID, Target: "0.99", RollingWindowSeconds: 3600, IdempotencyKey: "during-window-slo", RequestID: "during-window-slo", Now: f.clock,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if witness := serviceSuppressionAt(t, f, objective.ID, window.Start); witness != nil {
		t.Fatalf("joined SLO was suppressed before its existence: %+v", witness)
	}
	if witness := serviceSuppressionAt(t, f, objective.ID, f.now); witness == nil || !witness.Start.Equal(f.now) {
		t.Fatalf("new service membership is absent before fanout: %+v", witness)
	}
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	snapshot, err := readSloExclusionSnapshot(t.Context(), dbgen.New(f.conn), mustPGUUID(objective.ID), window.Start, window.End)
	if err != nil || len(snapshot.Intervals) != 1 || !snapshot.Intervals[0].Start.Equal(window.Start.Add(time.Minute)) ||
		!snapshot.Intervals[0].End.Equal(window.End) {
		t.Fatalf("all-services membership lost prospective start: %+v, %v", snapshot, err)
	}
}

// The old cursor models a claim statement that took its snapshot before the
// move committed, then acquired the SLO root after the transaction was gone.
func TestIntegrationMaintenanceServiceProjectionRejectsStaleMembershipCursor(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	q := dbgen.New(f.conn)
	subject, err := q.GetSlo(t.Context(), mustPGUUID(f.ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	input := f.input("project-stale-cursor")
	input.ScopeKind, input.SloIDs, input.ServiceIDs = MaintenanceScopeServices, nil, []string{subject.ServiceID.String()}
	input.Start, input.End = f.at.Add(5*time.Minute), f.at.Add(10*time.Minute)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	claim := dbgen.AlmanacServiceMaintenanceMembershipWork{SloID: subject.ID, ThroughExclusion: mustPGUUID(window.ID), AffectedFrom: timestamp(window.Start)}
	if _, err := f.conn.Exec(t.Context(), `
		INSERT INTO almanac_service_maintenance_membership_work(slo_id, through_exclusion, affected_from) VALUES ($1,$2,$3)
	`, claim.SloID, claim.ThroughExclusion, claim.AffectedFrom); err != nil {
		t.Fatal(err)
	}
	destination, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "stale-cursor-destination", DisplayName: "Outside maintenance",
		IdempotencyKey: "stale-cursor-destination", RequestID: "stale-cursor-destination", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	f.now = window.Start.Add(time.Minute)
	moveMaintenanceSLO(t, f, destination.ID, 2, f.now)
	compilerTx(t, f, func(tx pgx.Tx) error {
		q := dbgen.New(tx)
		if _, err := q.LockMaintenanceSlo(t.Context(), subject.ID); err != nil {
			return err
		}
		return projectServiceMaintenanceMembershipClaim(t.Context(), q, claim, 64, f.clock)
	})
	if err := requireServiceMaintenanceReady(t.Context(), q, subject.ID, f.now, window.End); !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("stale work discarded an unprojected membership change: %v", err)
	}
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	snapshot, err := readSloExclusionSnapshot(t.Context(), q, subject.ID, window.Start, window.End)
	if err != nil || len(snapshot.Intervals) != 1 || !snapshot.Intervals[0].End.Equal(f.now) {
		t.Fatalf("fresh claim failed to preserve the departure: %+v, %v", snapshot, err)
	}
}

func TestIntegrationMaintenanceServiceImpactCursorFencesProjectionAndRevision(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	input := f.input("impact-paging")
	input.ScopeKind, input.SloIDs = MaintenanceScopeAllServices, nil
	input.Start, input.End = f.at.Add(5*time.Minute), f.at.Add(10*time.Minute)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	f.now = window.Start
	impact := MaintenanceImpactInput{Proof: f.proof, ID: window.ID, Now: f.clock, Limit: 1}
	first, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, impact)
	if err != nil || first.Ready || first.Total != 2 || first.NextCursor == "" {
		t.Fatalf("pending impact first page: %+v, %v", first, err)
	}
	impact.Cursor = first.NextCursor
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	if page, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, impact); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("continuation followed a completed projection: %+v, %v", page, err)
	}
	impact.Cursor = ""
	ready, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, impact)
	if err != nil || !ready.Ready || ready.Total != 2 || ready.NextCursor == "" {
		t.Fatalf("ready impact first page: %+v, %v", ready, err)
	}
	f.now = window.Start.Add(time.Minute)
	_, err = EndMaintenance(t.Context(), f.f.databaseURL, EndMaintenanceInput{
		Scoped: true, Proof: f.proof, ID: window.ID, Revision: window.Revision, End: window.Start.Add(3 * time.Minute),
		Reason: "Done", IdempotencyKey: "impact-paging-end", RequestID: "impact-paging-end", Now: f.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	impact.Cursor = ready.NextCursor
	if page, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, impact); !errors.Is(err, ErrMaintenanceSnapshotChanged) {
		t.Fatalf("continuation followed a different window revision: %+v, %v", page, err)
	}
	impact.Cursor, impact.Revision = "", window.Revision
	historical, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, impact)
	if err != nil || !historical.Ready || historical.NextCursor == "" || !historical.Items[0].End.Equal(window.End) {
		t.Fatalf("historical impact lost its original bounds: %+v, %v", historical, err)
	}
	impact.Cursor = historical.NextCursor
	next, err := ReadMaintenanceImpact(t.Context(), f.f.databaseURL, impact)
	if err != nil || !next.Ready || len(next.Items) != 1 || next.NextCursor != "" || !next.Items[0].End.Equal(window.End) {
		t.Fatalf("historical impact did not page its own revision: %+v, %v", next, err)
	}
}

func TestIntegrationMaintenanceServiceProjectionKeepsHistoricalGridsAndParts(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	q := dbgen.New(f.conn)
	subject, err := q.GetSlo(t.Context(), mustPGUUID(f.ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "grid-history-destination", DisplayName: "Outside selection",
		IdempotencyKey: "grid-history-destination", RequestID: "grid-history-destination", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	orphan := createProjectionFixtureSLO(t, f, other.ID)
	input := f.input("previous-grid-window")
	input.ScopeKind, input.SloIDs, input.ServiceIDs = MaintenanceScopeServices, nil, []string{subject.ServiceID.String()}
	input.Start, input.End = f.at.Add(time.Minute), f.at.Add(3*time.Minute)
	earlier, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	// The unrelated objective remains locked while projection pages the selected
	// service. Its lock must not delay publication.
	lock := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(lock)
	held, err := lock.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(held)
	if _, err := dbgen.New(held).LockMaintenanceSlo(t.Context(), mustPGUUID(orphan)); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}

	input.IdempotencyKey = "three-grid-parts"
	input.Start, input.End = f.at.Add(5*time.Minute), f.at.Add(30*time.Minute)
	current, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	base := f.at
	moveMaintenanceSLOAtCadence(t, f, other.ID, 2, base.Add(10*time.Minute), 120)
	moveMaintenanceSLOAtCadence(t, f, subject.ServiceID.String(), 3, base.Add(12*time.Minute), 60)
	moveMaintenanceSLOAtCadence(t, f, other.ID, 4, base.Add(15*time.Minute), 120)
	moveMaintenanceSLOAtCadence(t, f, subject.ServiceID.String(), 5, base.Add(20*time.Minute), 120)
	f.now = base.Add(20 * time.Minute)
	if err := requireServiceMaintenanceReady(t.Context(), q, subject.ID, earlier.Start, earlier.End); err != nil {
		t.Fatalf("earlier completed window was held by the later move: %v", err)
	}
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 64, time.Second, f.clock); err != nil {
		t.Fatal(err)
	}
	snapshot, err := readSloExclusionSnapshot(t.Context(), q, subject.ID, current.Start, current.End)
	want := [][2]time.Time{
		{current.Start, base.Add(10 * time.Minute)},
		{base.Add(12 * time.Minute), base.Add(15 * time.Minute)},
		{base.Add(20 * time.Minute), current.End},
	}
	if err != nil || len(snapshot.Intervals) != len(want) {
		t.Fatalf("projected coverage = %+v, err = %v", snapshot, err)
	}
	for i, bounds := range want {
		if !snapshot.Intervals[i].Start.Equal(bounds[0]) || !snapshot.Intervals[i].End.Equal(bounds[1]) {
			t.Fatalf("part %d = %+v, want %v", i, snapshot.Intervals[i], bounds)
		}
	}
	var count int
	var projected bool
	if err := f.conn.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM almanac_maintenance_changed_inputs WHERE slo_id=$1 AND revision=$2),
		       (SELECT complete FROM almanac_service_maintenance_projection WHERE exclusion_id=$3)
	`, f.ids[0], snapshot.Revision, current.ID).Scan(&count, &projected); err != nil || count != 3 || !projected {
		t.Fatalf("indexed %d parts, projection advanced=%t: %v", count, projected, err)
	}
}

func createProjectionFixtureSLO(t *testing.T, f maintenanceFixture, serviceID string) string {
	t.Helper()
	_, slo, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "unrelated-projection-slo", DisplayName: "Unrelated SLO",
		ServiceID: serviceID, SliID: f.sliID, Target: "0.99", RollingWindowSeconds: 3600,
		IdempotencyKey: "unrelated-projection-slo", RequestID: "unrelated-projection-slo", Now: f.clock,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return slo.ID
}

func TestIntegrationMaintenanceServiceProjectionAdvancesThroughSparseTail(t *testing.T) {
	t.Parallel()
	f := newMaintenanceFixture(t)
	subject, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.ids[1]))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{
		Proof: f.proof, Name: "sparse-projection-destination", DisplayName: "Outside selection",
		IdempotencyKey: "sparse-projection-destination", RequestID: "sparse-projection-destination", Now: f.clock,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	moveMaintenanceSLO(t, f, other.ID, 2, f.at.Add(time.Minute))
	const tailSize = 130
	tailID := func(i int) string { return fmt.Sprintf("ffffffff-ffff-ffff-ffff-%012d", i) }
	compilerTx(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `
INSERT INTO almanac_slos(id, name, display_name, definition_revision, created_at, updated_at)
SELECT ('ffffffff-ffff-ffff-ffff-' || lpad(i::text, 12, '0'))::uuid,
    'sparse-tail-' || i, 'Sparse tail ' || i, 1, $2, $2
FROM generate_series(1, $1::integer) i`, tailSize, f.at); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
INSERT INTO almanac_slo_definitions(slo_id, revision, service_id, sli_id, measurement_revision, target,
    rolling_window_seconds, cadence_seconds, activated_at, evaluation_valid_from)
SELECT s.id, 1, $2, d.sli_id, d.measurement_revision, d.target, d.rolling_window_seconds,
    d.cadence_seconds, d.activated_at, d.evaluation_valid_from
FROM almanac_slos s CROSS JOIN almanac_slo_definitions d
WHERE s.id BETWEEN $3 AND $4 AND d.slo_id=$1 AND d.revision=1`, subject.ID, other.ID, tailID(1), tailID(tailSize))
		return err
	})
	input := f.input("sparse-projection")
	input.ScopeKind, input.SloIDs, input.ServiceIDs = MaintenanceScopeServices, nil, []string{subject.ServiceID.String()}
	input.Start, input.End = f.at.Add(5*time.Minute), f.at.Add(10*time.Minute)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}

	// The first unmatched tail row is locked by an independent transaction.
	// Scanning its ID must not require a row lock or delay cursor advancement.
	conn := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(conn)
	held, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(held)
	if _, err := dbgen.New(held).LockMaintenanceSlo(t.Context(), mustPGUUID(tailID(1))); err != nil {
		t.Fatal(err)
	}

	for i, step := range []struct {
		limit    int
		through  string
		complete bool
	}{
		{2, max(f.ids[0], f.ids[1]), false},
		{64, tailID(64), false},
		{64, tailID(128), false},
		{64, tailID(tailSize), true},
	} {
		if err := f.runtime.runServiceMaintenanceProjection(t.Context(), step.limit, time.Second, f.clock); err != nil {
			t.Fatalf("projection turn %d: %v", i+1, err)
		}
		var through string
		var complete bool
		if err := f.conn.QueryRow(t.Context(), `SELECT through_slo, complete FROM almanac_service_maintenance_projection WHERE exclusion_id=$1`,
			window.ID).Scan(&through, &complete); err != nil || through != step.through || complete != step.complete {
			t.Fatalf("turn %d through=%s complete=%v, want %s/%v: %v", i+1, through, complete, step.through, step.complete, err)
		}
	}
	var installed []string
	if err := f.conn.QueryRow(t.Context(), `SELECT array_agg(slo_id::text ORDER BY slo_id)
FROM almanac_slo_exclusion_changes WHERE exclusion_id=$1`, window.ID).Scan(&installed); err != nil || len(installed) != 1 || installed[0] != subject.ID.String() {
		t.Fatalf("scope installed for %v, want only %s: %v", installed, subject.ID, err)
	}

	compilerTx(t, f, func(tx pgx.Tx) error {
		queries := &evaluationQueryTx{Tx: tx}
		if _, err := dbgen.New(queries).ListServiceMaintenanceProjectionSlos(t.Context(), dbgen.ListServiceMaintenanceProjectionSlosParams{
			ExclusionID: mustPGUUID(window.ID), Revision: 1, AfterSlo: uuidOrZero(""), PageLimit: 2,
		}); err != nil {
			return err
		}
		query := queries.calls[0]
		var plan string
		if err := tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, COSTS OFF, FORMAT JSON) "+query.sql, query.args...).Scan(&plan); err != nil {
			return err
		}
		if !strings.Contains(plan, "almanac_slos_pkey") {
			t.Fatalf("raw page did not use the SLO UUID primary key: %s", plan)
		}
		return nil
	})
}
