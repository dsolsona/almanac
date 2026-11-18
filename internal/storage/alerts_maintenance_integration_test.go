//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func alertMaintenanceFixture(f alertTestFixture) maintenanceFixture {
	var verifier [32]byte
	verifier[0] = 98
	return maintenanceFixture{selfHealFixture: f.selfHealFixture, ids: []string{f.sloID}, proof: DatasourceProof{SessionVerifier: verifier}}
}

func alertMaintenancePrepare(t *testing.T, f alertTestFixture, claim alertClaim) (alertPreparedEvidence, error) {
	t.Helper()
	tx, err := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(tx)
	return prepareMaintenanceAlertEvidence(t.Context(), tx, claim, claim.ScheduledAt, f.limits, maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 2000, MaxFragments: 2000}, f.now)
}

func TestIntegrationAlertMaintenanceFuture(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Future maintenance")
	f.drain(t, "future alert maintenance")
	m := alertMaintenanceFixture(f)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m.input("future")); err != nil {
		t.Fatal(err)
	}
	prepared, err := alertMaintenancePrepare(t, f, f.claim(t, f.at))
	if err != nil || len(prepared.Input.Windows) != 2 || prepared.Input.Snapshot.ExclusionRevision != 1 {
		t.Fatalf("compatible future exclusion blocked evidence: %v", err)
	}
	for _, w := range prepared.Input.Windows {
		good, total := "4950", "5000"
		if w.End.Sub(w.Start) == time.Hour {
			good, total = "59400", "60000"
		}
		if w.Quality != alert.Complete || w.Good != good || w.Total != total {
			t.Fatalf("future exclusion changed counts: %+v", w)
		}
		if w.Maintenance == nil || w.Maintenance.ComputedRevision != 0 || w.Maintenance.RequestedRevision != 1 || w.Maintenance.Proof == nil || w.Maintenance.Proof.Kind != "indexed_absence" {
			t.Fatalf("actual computed identity/proof lost: %+v", w.Maintenance)
		}
	}
}

func TestIntegrationAlertMaintenancePreparation(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Partial maintenance")
	f.drain(t, "maintenance preparation")
	m := alertMaintenanceFixture(f)
	declaration := m.input("partial")
	declaration.Start, declaration.End = f.at.Add(-4*time.Minute), f.at.Add(-2*time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, declaration); err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.at)
	missing, err := alertMaintenancePrepare(t, f, claim)
	if !errors.Is(err, ErrDerivedPending) || len(missing.Input.Windows) != 0 || !missing.Cutoff.Equal(f.at) || len(missing.Preparation) != 1 || missing.Preparation[0].Kind != "mask" || missing.Preparation[0].RequestedRevision != 1 {
		t.Fatalf("missing mask lost original work: preparations=%+v err=%v", missing.Preparation, err)
	}
	build := compilerCapture(t, m, nil)
	compilerFinish(t, m, build.ID, 2)
	missing, err = alertMaintenancePrepare(t, f, claim)
	if !errors.Is(err, ErrDerivedPending) || len(missing.Input.Windows) != 0 || len(missing.Preparation) != 1 || missing.Preparation[0].Kind != "scoped_hour" {
		t.Fatalf("missing partial hour lost exact target: preparations=%+v err=%v", missing.Preparation, err)
	}
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		_, err := prepareMaintenanceHour(t.Context(), tx, maintenanceHourInput{maintenanceHourKey: missing.Preparation[0].Hour, RetentionStart: f.historyStart, Deadline: f.now.Add(time.Minute)}, f.clock)
		return err
	})
	ready, err := alertMaintenancePrepare(t, f, claim)
	if err != nil || len(ready.Input.Windows) != 2 || len(ready.Preparation) != 0 {
		t.Fatalf("prepared evidence remained unavailable: %v", err)
	}
	for _, w := range ready.Input.Windows {
		good, total := "2970", "3000"
		if w.End.Sub(w.Start) == time.Hour {
			good, total = "57420", "58000"
		}
		if w.Good != good || w.Total != total || w.Quality != alert.Complete {
			t.Fatalf("incorrect eligible population: %+v", w)
		}
	}
}

func TestIntegrationAlertMaintenanceRecovery(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "alert-maintenance-recovery", true)
	final := s.at
	s.at, s.now = final.Add(-time.Minute), final.Add(-time.Minute)
	f := alertTestFixture{s, s.createSLO(t, "recovery"), alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}}
	if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET good_count=CASE WHEN interval_start >= $2::timestamptz-interval '5 minutes' AND interval_start < $2::timestamptz-interval '1 minute' THEN 0 ELSE 1000 END,ingestion_generation=0 WHERE sli_id=$1`, f.sliID, final); err != nil {
		t.Fatal(err)
	}
	f.policy(t, "Maintenance recovery")
	f.drain(t, "recovery source")
	first := f.claim(t, f.at)
	before, err := alertMaintenancePrepare(t, f, first)
	if err != nil {
		t.Fatal(err)
	}
	fired, err := f.runtime.commitAlertEvaluation(t.Context(), first, before.Input, f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil || fired.Occurrence == nil {
		t.Fatalf("expected source breach: %+v %v", fired, err)
	}
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		_, err := acknowledgeAlertTx(t.Context(), tx, alertAcknowledgement{ID: fired.Occurrence.ID, ExpectedVersion: fired.Version, ActorID: f.f.userID, RequestID: "maintenance-ack", Now: func() time.Time { return f.at.Add(2 * time.Second) }})
		return err
	})
	f.at, f.now = final, final
	m := alertMaintenanceFixture(f)
	declaration := m.input("exclude-failures")
	declaration.Start, declaration.End = final.Add(-5*time.Minute), final.Add(-time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, declaration); err != nil {
		t.Fatal(err)
	}
	build := compilerCapture(t, m, nil)
	compilerFinish(t, m, build.ID, 2)
	claim := f.claim(t, final)
	prepared, err := alertMaintenancePrepare(t, f, claim)
	if !errors.Is(err, ErrDerivedPending) || len(prepared.Preparation) != 1 || prepared.Preparation[0].Kind != "scoped_hour" {
		t.Fatalf("expected exact source preparation: %+v %v", prepared.Preparation, err)
	}
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		_, err := prepareMaintenanceHour(t.Context(), tx, maintenanceHourInput{maintenanceHourKey: prepared.Preparation[0].Hour, RetentionStart: f.historyStart, Deadline: f.now.Add(time.Minute)}, f.clock)
		return err
	})
	prepared, err = alertMaintenancePrepare(t, f, claim)
	if err != nil || prepared.Input.RecoveryBaseline == nil || prepared.Input.RecoveryBaseline.ExclusionRevision != 0 || len(prepared.Calculations) != 2 {
		t.Fatalf("same-facts baseline unavailable: %v", err)
	}
	ev, err := alert.Evaluate(prepared.Input, f.limits)
	if err != nil || ev.Truth != alert.Clear || ev.RecoveryComparison == nil || ev.RecoveryComparison.Truth != alert.Breaching || ev.Snapshot.SourceGeneration != ev.RecoveryComparison.Snapshot.SourceGeneration {
		t.Fatalf("wrong recovery attribution: %+v %v", ev, err)
	}
	for _, w := range prepared.Input.Windows {
		want := "1000"
		if w.End.Sub(w.Start) == time.Hour {
			want = "56000"
		}
		if w.Good != want || w.Total != want {
			t.Fatalf("wrong independent eligible counts: %+v", w)
		}
	}
	resolved, err := f.runtime.commitAlertEvaluation(t.Context(), claim, prepared.Input, f.limits, func() time.Time { return final.Add(time.Second) })
	if err != nil || resolved.Occurrence == nil || resolved.Occurrence.ID != fired.Occurrence.ID || resolved.Occurrence.State != alert.Resolved || resolved.Occurrence.AcknowledgedBy != f.f.userID {
		t.Fatalf("lost shared lifecycle: %+v %v", resolved, err)
	}
	var cause string
	err = f.conn.QueryRow(t.Context(), `SELECT cause FROM almanac_alert_transitions WHERE occurrence_id=$1 ORDER BY sequence DESC LIMIT 1`, fired.Occurrence.ID).Scan(&cause)
	if err != nil || cause != string(alert.ResolvedAfterMaintenanceChange) {
		t.Fatalf("wrong persisted recovery cause: %s %v", cause, err)
	}
}

func TestIntegrationAlertMaintenanceFullyExcluded(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Fully excluded")
	f.drain(t, "excluded source")
	m := alertMaintenanceFixture(f)
	declaration := m.input("fully-excluded")
	declaration.Start, declaration.End = f.at.Add(-time.Hour), f.at
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, declaration); err != nil {
		t.Fatal(err)
	}
	build := compilerCapture(t, m, nil)
	compilerFinish(t, m, build.ID, 2)
	prepared, err := alertMaintenancePrepare(t, f, f.claim(t, f.at))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range prepared.Input.Windows {
		if w.Quality != alert.Excluded {
			t.Fatalf("excluded window became usable: %+v", w)
		}
	}
	ev, err := alert.Evaluate(prepared.Input, f.limits)
	if err != nil || ev.Truth != alert.Indeterminate {
		t.Fatalf("fully excluded population became truth: %+v %v", ev, err)
	}
}

func TestIntegrationAlertMaintenanceSuppressesOnInclusiveStart(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Covered firing")
	f.drain(t, "covered firing source")
	m := alertMaintenanceFixture(f)
	request := m.input("covered-firing")
	request.Start, request.End = f.at, f.at.Add(time.Minute)
	window, err := CreateMaintenance(t.Context(), f.f.databaseURL, request)
	if err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.at)
	prepared, err := alertMaintenancePrepare(t, f, claim)
	if err != nil {
		t.Fatal(err)
	}
	input := prepared.Input
	evaluation, err := alert.Evaluate(input, f.limits)
	if err != nil || evaluation.Truth != alert.Breaching {
		t.Fatalf("test requires eligible pre-window breach: %+v %v", evaluation, err)
	}
	clock := func() time.Time { return f.at.Add(time.Second) }
	saved, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, clock)
	if err != nil || saved.Occurrence != nil || saved.EvaluationID == "" {
		t.Fatalf("covered breach was not recorded without an occurrence: %+v %v", saved, err)
	}
	replayed, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, clock)
	if err != nil || replayed.EvaluationID != saved.EvaluationID || replayed.Occurrence != nil {
		t.Fatalf("covered breach replay changed its decision: %+v %v", replayed, err)
	}
	row, err := dbgen.New(f.conn).ReadAlertEvaluation(t.Context(), mustPGUUID(saved.EvaluationID))
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct{ Evaluation alert.Evaluation }
	if err := unmarshalAlertData(row.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	coverage := evidence.Evaluation.Maintenance
	if coverage == nil || !coverage.OpeningSuppressed || coverage.WindowID != window.ID ||
		coverage.Revision != window.Revision || !coverage.Start.Equal(request.Start) || !coverage.End.Equal(request.End) {
		t.Fatalf("recorded wrong suppression boundary or outcome: %+v", coverage)
	}
	if evidence.Evaluation.PolicyID != policy || evidence.Evaluation.Truth != alert.Breaching {
		t.Fatalf("suppression changed the numerical evaluation: %+v", evidence.Evaluation)
	}
	listed, err := ListAlertMaintenanceSuppressions(t.Context(), f.f.databaseURL, ListAlertMaintenanceSuppressionsInput{
		ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: m.proof, ID: policy, Now: clock},
		Revision:       1, Limit: 10,
	})
	if err != nil || len(listed.Items) != 1 || listed.NextCursor != "" {
		t.Fatalf("could not audit a suppressed opening without an occurrence: %+v %v", listed, err)
	}
	item := listed.Items[0]
	if item.EvaluationID != saved.EvaluationID || !item.Cutoff.Equal(f.at) || item.Suppression.WindowID != window.ID {
		t.Fatalf("audit did not preserve the evaluated decision: %+v", item)
	}
}

func TestIntegrationAlertMaintenanceRejectsUnprojectedPopulation(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Projected maintenance")
	initial := f.claim(t, f.at)
	firstClock := func() time.Time { return f.at.Add(time.Second) }
	if result, err := f.runtime.commitAlertEvaluation(t.Context(), initial, f.input(t, initial, alert.Complete, false), f.limits, firstClock); err != nil || result.Occurrence != nil {
		t.Fatalf("establish uncovered initial decision: %+v %v", result, err)
	}
	claim := f.claim(t, f.at.Add(time.Minute))
	stale := f.input(t, claim, alert.Complete, true)
	row, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	m := alertMaintenanceFixture(f)
	request := m.input("covered-unprojected")
	request.Scoped, request.ScopeKind, request.SloIDs = true, MaintenanceScopeServices, nil
	request.ServiceIDs = []string{row.ServiceID.String()}
	request.Start, request.End = f.at, f.at.Add(2*time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, request); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return claim.ScheduledAt.Add(time.Second) }
	if result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, stale, f.limits, clock); !errors.Is(err, ErrDerivedPending) || result.EvaluationID != "" {
		t.Fatalf("published stale evidence while membership was being projected: %+v %v", result, err)
	}
	if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 4, time.Second, clock); err != nil {
		t.Fatal(err)
	}
	if result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, stale, f.limits, clock); !errors.Is(err, ErrAlertStale) || result.EvaluationID != "" {
		t.Fatalf("reused pre-maintenance evidence after membership advanced: %+v %v", result, err)
	}
}

func TestIntegrationAlertMaintenanceOverlapResumesAtFirstUncoveredCutoff(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Overlapping maintenance")
	m := alertMaintenanceFixture(f)
	row, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	end := f.at.Add(3 * time.Minute)
	resumeAt := end.Add(time.Minute)
	if _, err := execObservationFixture(t, f.conn, f.sliID, `
WITH source AS (SELECT * FROM almanac_sli_observations WHERE sli_id=$1 LIMIT 1)
INSERT INTO almanac_sli_observations
    (interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
     execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point, s.sli_id,1,'observed',990,1000,s.result_digest,'{}',
       gen_random_uuid(),gen_random_uuid(),s.agent_id,s.session_generation,s.configuration_revision,s.lease_id,point,point+interval '1 minute'
FROM source s CROSS JOIN generate_series($2::timestamptz,$3::timestamptz-interval '1 minute',interval '1 minute') point`,
		f.sliID, f.at, resumeAt); err != nil {
		t.Fatal(err)
	}
	f.drain(t, "overlapping maintenance source")
	request := m.input("selected-service-suppression")
	request.Scoped, request.ScopeKind, request.SloIDs = true, MaintenanceScopeServices, nil
	request.ServiceIDs = []string{row.ServiceID.String()}
	request.Start, request.End = f.at, f.at.Add(2*time.Minute)
	selected, err := CreateMaintenance(t.Context(), f.f.databaseURL, request)
	if err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "all-service-suppression"
	request.ScopeKind, request.ServiceIDs = MaintenanceScopeAllServices, nil
	request.Start, request.End = f.at.Add(time.Minute), end
	all, err := CreateMaintenance(t.Context(), f.f.databaseURL, request)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.runtime.runServiceMaintenanceProjection(t.Context(), 4, time.Second, f.clock); err != nil {
			t.Fatal(err)
		}
	}
	build := compilerCapture(t, m, nil)
	compilerFinish(t, m, build.ID, 2)
	for offset := range 5 {
		f.now = f.at.Add(time.Duration(offset) * time.Minute)
		claim := f.claim(t, f.now)
		prepared, err := alertMaintenancePrepare(t, f, claim)
		if err != nil {
			t.Fatalf("prepare cutoff %s: %v", f.now, err)
		}
		ev, err := alert.Evaluate(prepared.Input, f.limits)
		want := alert.Breaching
		if f.now.Equal(end) {
			// The first cutoff outside the window still lacks recent eligible data.
			want = alert.Indeterminate
		}
		if err != nil || ev.Truth != want {
			t.Fatalf("evaluation at %s is %s, want %s: %+v %v", f.now, ev.Truth, want, ev, err)
		}
		result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, prepared.Input, f.limits, f.clock)
		if err != nil {
			t.Fatalf("publish cutoff %s: %v", f.now, err)
		}
		if f.now.Before(resumeAt) {
			if result.Occurrence != nil {
				t.Fatalf("maintenance opened an occurrence at %s: %+v", f.now, result)
			}
		} else if result.Occurrence == nil || !result.Occurrence.FiringSnapshot.Cutoff.Equal(resumeAt) {
			t.Fatalf("the first uncovered cutoff did not open its own occurrence: %+v", result)
		}
	}
	cursor := ""
	for i := 2; i >= 0; i-- {
		page, err := ListAlertMaintenanceSuppressions(t.Context(), f.f.databaseURL, ListAlertMaintenanceSuppressionsInput{
			ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: m.proof, ID: policy, Now: f.clock},
			Revision:       1, Limit: 1, Cursor: cursor,
		})
		if err != nil || len(page.Items) != 1 || !page.Items[0].Cutoff.Equal(f.at.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("audit cutoff %d: %+v %v", i, page, err)
		}
		if i == 2 && page.Items[0].Suppression.WindowID != all.ID || i == 0 && page.Items[0].Suppression.WindowID != selected.ID {
			t.Fatalf("audit cutoff %d recorded the wrong scope: %+v", i, page.Items[0])
		}
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatal("audit included a resumed evaluation")
	}
}
