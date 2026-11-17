//go:build integration

package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func maintenanceWindowBoundsError(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, ErrDerivedPending):
		return "derived_pending"
	case errors.Is(err, slo.ErrOperationalFacts):
		return "operational_facts"
	case errors.Is(err, slo.ErrInvalidCalculation):
		return "invalid_calculation"
	case errors.Is(err, ErrMaintenanceInvalid):
		return "maintenance_invalid"
	case errors.Is(err, ErrAlertInvalid):
		return "alert_invalid"
	case errors.Is(err, ErrOperationalResource):
		return "operational_resource"
	default:
		if p, ok := errors.AsType[*pgconn.PgError](err); ok {
			return "postgres_" + p.Code
		}
		return fmt.Sprintf("type_%T", err)
	}
}

func TestIntegrationMaintenanceWindowObservationBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		offset   time.Duration
		trailing bool
	}{{"hour_aligned", 0, true}, {"leading_full_trailing", 40 * time.Minute, true}, {"missing_trailing", 40 * time.Minute, false}} {
		t.Run(tc.name, func(t *testing.T) { maintenanceWindowObservationBounds(t, tc.offset, tc.trailing) })
	}
}
func maintenanceWindowObservationBounds(t *testing.T, offset time.Duration, trailing bool) {
	s := newSelfHealFixture(t, "window-bounds", true)
	// Six complete hours; retain the real fixture collector provenance and mark
	// only the bounded newly inserted/changed rows through the usual fixture hook.
	if _, err := execObservationFixture(t, s.conn, s.sliID, `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+interval '3 hours',sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at+interval '3 hours',recorded_at+interval '3 hours' FROM almanac_sli_observations WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatalf("extend fixture: %s", maintenanceWindowBoundsError(err))
	}
	if trailing && offset > 0 {
		if _, err := execObservationFixture(t, s.conn, s.sliID, `INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+interval '6 hours',sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at+interval '6 hours',recorded_at+interval '6 hours' FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start<$2::timestamptz+interval '40 minutes'`, s.sliID, s.historyStart); err != nil {
			t.Fatal(maintenanceWindowBoundsError(err))
		}
	}
	s.now = s.now.Add(3*time.Hour + offset)
	s.at = s.at.Add(3*time.Hour + offset)
	if _, err := execObservationFixture(t, s.conn, s.sliID, `UPDATE almanac_sli_observations SET good_count=9000,total_count=10000,ingestion_generation=0 WHERE sli_id=$1`, s.sliID); err != nil {
		t.Fatalf("population fixture: %s", maintenanceWindowBoundsError(err))
	}
	anchor := s.createSLO(t, "fragment-anchor")
	original, err := dbgen.New(s.conn).GetSlo(t.Context(), mustPGUUID(anchor))
	if err != nil {
		t.Fatal(maintenanceWindowBoundsError(err))
	}
	var verifier [32]byte
	verifier[0] = 98
	_, objective, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{Proof: DatasourceProof{SessionVerifier: verifier}, Name: "fragment-30day", DisplayName: "Fragment 30-day", ServiceID: original.ServiceID.String(), SliID: s.sliID, Target: "0.999", RollingWindowSeconds: 2592000, EvaluationValidFrom: new(s.historyStart), IdempotencyKey: "fragment-30day", RequestID: "fragment-30day", Now: s.clock}, true)
	if err != nil {
		t.Fatalf("create30day: %s", maintenanceWindowBoundsError(err))
	}
	f := alertTestFixture{s, objective.ID, alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}}
	m := alertMaintenanceFixture(f)
	for part := range 12 {
		in := m.input(fmt.Sprintf("fragment-%d", part))
		in.Start = f.at.Add(-25*time.Minute + time.Duration(part*2)*time.Minute)
		in.End = in.Start.Add(time.Minute)
		if _, err = CreateMaintenance(t.Context(), f.f.databaseURL, in); err != nil {
			t.Fatalf("authority part%d: %s", part, maintenanceWindowBoundsError(err))
		}
	}
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, CreateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "fragment-policy"), Name: "Fragment default policy", SLOID: f.sloID, Severity: "high", Limits: f.limits, Conditions: []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "14.4"}, {ID: "sustained", Short: 30 * time.Minute, Long: 6 * time.Hour, Threshold: "6"}}})
	if err != nil {
		t.Fatalf("create default policy: %s", maintenanceWindowBoundsError(err))
	}
	if created.Policy == nil {
		t.Fatal("missing created policy")
	}
	f.drain(t, "fragment canonical")
	claim := f.claim(t, f.at)
	prepare := func() (alertPreparedEvidence, error) {
		tx, e := f.conn.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if e != nil {
			return alertPreparedEvidence{}, e
		}
		defer rollbackTransaction(tx)
		return prepareMaintenanceAlertEvidence(t.Context(), tx, claim, f.at, f.limits, maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, f.now)
	}
	pending, err := prepare()
	if !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("expected initial compiler work, class=%s type=%T", maintenanceWindowBoundsError(err), err)
	}
	build := compilerCapture(t, m, nil)
	compilerFinish(t, m, build.ID, 64)
	pending, err = prepare()
	var coverage, changes, indexed int
	if e := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_coverage WHERE build_id=$1),(SELECT count(*) FROM almanac_slo_exclusion_changes WHERE slo_id=$2),(SELECT indexed_through FROM almanac_maintenance_compatibility WHERE slo_id=$2)`, build.ID, mustPGUUID(f.sloID)).Scan(&coverage, &changes, &indexed); e != nil {
		t.Fatal(maintenanceWindowBoundsError(e))
	}
	if err != nil && !errors.Is(err, ErrDerivedPending) {
		t.Fatalf("compiled mask rejected before any scoped-hour enqueue: class=%s type=%T", maintenanceWindowBoundsError(err), err)
	}
	if build.TargetRevision != 12 || coverage != 12 || changes != 12 || indexed != 12 {
		t.Fatalf("invalid fixture authority: target=%d coverage=%d changes=%d indexed=%d", build.TargetRevision, coverage, changes, indexed)
	}
	for _, request := range pending.Preparation {
		if request.Kind == "scoped_hour" {
			alertTestTx(t, f.conn, func(tx pgx.Tx) error {
				_, e := prepareMaintenanceHour(t.Context(), tx, maintenanceHourInput{maintenanceHourKey: request.Hour, RetentionStart: f.historyStart, Deadline: f.now.Add(time.Minute)}, f.clock)
				return e
			})
		}
	}
	ready, err := prepare()
	if err != nil {
		t.Fatalf("prepared fragment evidence: class=%s type=%T", maintenanceWindowBoundsError(err), err)
	}
	if trailing {
		want := map[time.Duration][2]string{5 * time.Minute: {"27000", "30000"}, 30 * time.Minute: {"162000", "180000"}, time.Hour: {"432000", "480000"}, 6 * time.Hour: {"3132000", "3480000"}}
		for _, w := range ready.Input.Windows {
			v := want[w.End.Sub(w.Start)]
			if w.Quality != alert.Complete || w.Good != v[0] || w.Total != v[1] {
				t.Fatalf("complete fragmented population %v got%s/%s quality%s want%s/%s", w.End.Sub(w.Start), w.Good, w.Total, w.Quality, v[0], v[1])
			}
		}
	} else {
		for _, w := range ready.Input.Windows {
			duration := w.End.Sub(w.Start)
			if duration == time.Hour && (w.Good != "180000" || w.Total != "200000" || w.Quality != alert.Partial) {
				t.Fatalf("missing trailing1h got%s/%s quality%s", w.Good, w.Total, w.Quality)
			}
			if duration == 6*time.Hour && (w.Good != "2880000" || w.Total != "3200000" || w.Quality != alert.Partial) {
				t.Fatalf("missing trailing6h got%s/%s quality%s", w.Good, w.Total, w.Quality)
			}
		}
	}
	f.runtime.maintenanceConsumers.Store(&maintenanceConsumerOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 1000, MaxRawRows: 20000, MaxFragments: 20000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 1000}, MaxRetainedProofs: 960})
	if e := f.runtime.refreshCurrentProjection(t.Context(), mustPGUUID(f.sloID), f.clock); e != nil {
		t.Fatalf("shared current rejected fragment bounds: %s", maintenanceWindowBoundsError(e))
	}
	projection, e := dbgen.New(f.conn).GetPublishedCurrentProjection(t.Context(), dbgen.GetPublishedCurrentProjectionParams{SloID: mustPGUUID(f.sloID), Now: timestamp(f.now), CalculationVersion: operationalCalculationVersion})
	if e != nil || !projection.Current {
		t.Fatalf("current projection did not publish: %s", maintenanceWindowBoundsError(e))
	}
	current, e := publishedCurrentStatus(projection)
	if e != nil {
		t.Fatal(maintenanceWindowBoundsError(e))
	}
	if offset > 0 && trailing {
		//30day current is shortened only by validity:400 observed minutes minus12.
		if current.Current.Performance.Good == nil || *current.Current.Performance.Good != "3492000.000000000000" || current.Current.Performance.Total == nil || *current.Current.Performance.Total != "3880000.000000000000" {
			t.Fatalf("current exact fragment counts disagree: %+v", current.Current.Performance)
		}
	}

}
