//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func alertMaintenancePendingFixture(t *testing.T) (alertTestFixture, alertClaim, alertPreparedEvidence) {
	t.Helper()
	f := newAlertTestFixture(t)
	f.policy(t, "Durable preparation")
	f.drain(t, "durable preparation source")
	m := alertMaintenanceFixture(f)
	declaration := m.input("durable-preparation")
	declaration.Start, declaration.End = f.at.Add(-4*time.Minute), f.at.Add(-2*time.Minute)
	if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, declaration); err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.at)
	pinned, err := f.runtime.pinAlertCutoff(t.Context(), claim, f.clock)
	if err != nil || !pinned.Cutoff.Equal(f.at) {
		t.Fatalf("pin: %+v %v", pinned, err)
	}
	prepared, err := alertMaintenancePrepare(t, f, claim)
	if !errors.Is(err, ErrDerivedPending) || len(prepared.Preparation) != 1 {
		t.Fatalf("expected work: %+v %v", prepared.Preparation, err)
	}
	return f, claim, prepared
}

func TestIntegrationAlertMaintenancePendingDurable(t *testing.T) {
	t.Parallel()
	f, claim, prepared := alertMaintenancePendingFixture(t)
	err := f.runtime.deferAlertMaintenanceWork(t.Context(), claim, prepared, maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}, f.clock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := dbgen.New(f.conn).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(claim.PolicyID), Revision: claim.Revision})
	if err != nil || cursor.ClaimToken.Valid || !cursor.PendingCutoff.Valid || !cursor.PendingCutoff.Time.Equal(f.at) || !cursor.NextScheduledAt.Time.Equal(f.at) || cursor.FailureAttempts != 0 {
		t.Fatalf("pending publication lost ordered head: %+v %v", cursor, err)
	}
	var jobs, pinned int
	err = f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_preparation WHERE kind='mask' AND target_revision=1),(SELECT count(*) FROM almanac_maintenance_builds WHERE target_revision=1)`).Scan(&jobs, &pinned)
	if err != nil || jobs != 1 || pinned != 1 {
		t.Fatalf("cursor released without durable shared work: jobs=%d builds=%d %v", jobs, pinned, err)
	}
}

func TestIntegrationAlertMaintenancePendingFences(t *testing.T) {
	for _, kind := range []string{"disabled", "source_changed", "exclusion_changed", "expired"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, claim, prepared := alertMaintenancePendingFixture(t)
			switch kind {
			case "disabled":
				if _, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "disable-pending"), ID: claim.PolicyID, ExpectedVersion: 1, Reason: "stop"}); err != nil {
					t.Fatal(err)
				}
			case "source_changed":
				if _, err := execObservationFixture(t, f.conn, f.sliID, `UPDATE almanac_sli_observations SET good_count=998,ingestion_generation=0 WHERE sli_id=$1 AND interval_start=$2::timestamptz-interval '1 minute'`, f.sliID, f.at); err != nil {
					t.Fatal(err)
				}
			case "exclusion_changed":
				m := alertMaintenanceFixture(f)
				if _, err := CreateMaintenance(t.Context(), f.f.databaseURL, m.input("new-authority")); err != nil {
					t.Fatal(err)
				}
			case "expired":
				f.now = claim.Deadline
			}
			err := f.runtime.deferAlertMaintenanceWork(t.Context(), claim, prepared, maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}, f.clock, time.Second)
			if !errors.Is(err, ErrAlertStale) {
				t.Fatalf("changed authority/claim published pending work: %v", err)
			}
			var jobs, builds int
			err = f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_preparation),(SELECT count(*) FROM almanac_maintenance_builds)`).Scan(&jobs, &builds)
			if err != nil || jobs != 0 || builds != 0 {
				t.Fatalf("rejected parent leaked captured work: jobs=%d builds=%d %v", jobs, builds, err)
			}
		})
	}
}

func TestIntegrationAlertMaintenancePendingRestart(t *testing.T) {
	t.Parallel()
	f, claim, prepared := alertMaintenancePendingFixture(t)
	limits := maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}
	if err := f.runtime.deferAlertMaintenanceWork(t.Context(), claim, prepared, limits, f.clock, time.Second); err != nil {
		t.Fatal(err)
	}
	// Close the original processor's pools. Only persisted authority/jobs survive.
	f.runtime.Close()
	restarted, err := OpenRuntime(t.Context(), f.f.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	f.runtime = restarted
	options := preparationOptions(alertMaintenanceFixture(f))
	for range 12 {
		f.now = f.now.Add(time.Second)
		turn, err := restarted.runMaintenancePreparationTurn(t.Context(), f.now, options, f.clock)
		if err != nil || turn.Failed != 0 || turn.Unavailable != 0 {
			t.Fatalf("restart preparation: %+v %v", turn, err)
		}
		claim = f.claim(t, f.now)
		prepared, err = alertMaintenancePrepare(t, f, claim)
		if errors.Is(err, ErrDerivedPending) {
			if err := restarted.deferAlertMaintenanceWork(t.Context(), claim, prepared, limits, f.clock, time.Second); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err != nil || !prepared.Cutoff.Equal(f.at) {
			t.Fatalf("catch-up moved original cutoff: %v", err)
		}
		committed, err := restarted.commitAlertEvaluation(t.Context(), claim, prepared.Input, f.limits, f.clock)
		if err != nil || committed.Occurrence == nil || committed.Occurrence.State != alert.Firing {
			t.Fatalf("prepared original breach lost: %+v %v", committed, err)
		}
		retained, err := ReadAlertEvidence(t.Context(), f.f.databaseURL, ReadAlertEvidenceInput{ReadAlertInput: ReadAlertInput{Runtime: restarted, Proof: alertMaintenanceFixture(f).proof, ID: committed.Occurrence.ID, Now: f.clock}, Selection: "firing"})
		if err != nil || retained.Input.Windows[0].Maintenance == nil || retained.Input.Windows[0].Maintenance.RequestedRevision != 1 || retained.Input.Windows[0].Maintenance.ComputedRevision != 1 {
			t.Fatalf("immutable provenance not readable after restart: %+v %v", retained.Input.Windows, err)
		}
		return
	}
	t.Fatal("finite resource preparation did not finish")
}

func TestIntegrationAlertMaintenancePendingQueueFailure(t *testing.T) {
	t.Parallel()
	f, claim, _ := alertMaintenancePendingFixture(t)
	if _, err := f.conn.Exec(t.Context(), `ALTER TABLE almanac_maintenance_preparation ADD CONSTRAINT fixture_reject_mask CHECK(kind<>'mask')`); err != nil {
		t.Fatal(err)
	}
	options := alertProcessorTestOptions(f)
	options.Maintenance = &alertMaintenanceOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 2000, MaxFragments: 2000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}}
	kind, err := f.runtime.processAlertClaim(t.Context(), claim, options, f.clock)
	pgerr, ok := errors.AsType[*pgconn.PgError](err)
	if kind != "pending" || !ok || pgerr.Code != "23514" {
		t.Fatalf("expected actual queue publication failure: %s %v", kind, err)
	}
	cursor, err := dbgen.New(f.conn).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(claim.PolicyID), Revision: claim.Revision})
	if err != nil || cursor.ClaimToken != claim.Token || cursor.FailureAttempts != 0 || !cursor.PendingCutoff.Valid || !cursor.PendingCutoff.Time.Equal(f.at) || !cursor.NextScheduledAt.Time.Equal(f.at) {
		t.Fatalf("failed enqueue released or advanced parent: %+v %v", cursor, err)
	}
	var jobs, builds int
	err = f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_maintenance_preparation),(SELECT count(*) FROM almanac_maintenance_builds)`).Scan(&jobs, &builds)
	if err != nil || jobs != 0 || builds != 0 {
		t.Fatalf("failed enqueue leaked prefix: jobs=%d builds=%d %v", jobs, builds, err)
	}
}

func TestIntegrationAlertMaintenanceProcessor(t *testing.T) {
	t.Parallel()
	f, claim, _ := alertMaintenancePendingFixture(t)
	options := alertProcessorTestOptions(f)
	options.Maintenance = &alertMaintenanceOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 2000, MaxFragments: 2000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}}
	kind, err := f.runtime.processAlertClaim(t.Context(), claim, options, f.clock)
	if err != nil || kind != "pending" {
		t.Fatalf("processor did not durably defer: %s %v", kind, err)
	}
	preparation := preparationOptions(alertMaintenanceFixture(f))
	for range 12 {
		f.now = f.now.Add(time.Second)
		if _, err := f.runtime.runMaintenancePreparationTurn(t.Context(), f.now, preparation, f.clock); err != nil {
			t.Fatal(err)
		}
		turn, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
		if err != nil || turn.Failed != 0 || turn.Stale != 0 {
			t.Fatalf("processor turn: %+v %v", turn, err)
		}
		if turn.Evaluations == 1 {
			var count int
			var cutoff time.Time
			err := f.conn.QueryRow(t.Context(), `SELECT count(*),min(effective_cutoff) FROM almanac_alert_evaluations`).Scan(&count, &cutoff)
			if err != nil || count != 1 || !cutoff.Equal(f.at) {
				t.Fatalf("processor skipped/duplicated/moved boundary: count=%d cutoff=%s %v", count, cutoff, err)
			}
			return
		}
		if turn.Pending != 1 {
			t.Fatalf("ordered pending work disappeared: %+v", turn)
		}
	}
	t.Fatal("processor did not finish finite preparation")
}

func TestIntegrationAlertMaintenanceTerminalEvidence(t *testing.T) {
	for _, kind := range []string{"mask", "scoped_hour"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, claim, prepared := alertMaintenancePendingFixture(t)
			m := alertMaintenanceFixture(f)
			if kind == "scoped_hour" {
				build := compilerCapture(t, m, nil)
				compilerFinish(t, m, build.ID, 2)
				var err error
				prepared, err = alertMaintenancePrepare(t, f, claim)
				if !errors.Is(err, ErrDerivedPending) || len(prepared.Preparation) != 1 || prepared.Preparation[0].Kind != kind {
					t.Fatalf("expected exact hour request: %+v %v", prepared.Preparation, err)
				}
			}
			preparationEnqueue(t, m, prepared.Preparation, maintenancePreparationLive)
			if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='exhausted',attempts=3,reason='preparation_failed' WHERE kind=$1`, kind); err != nil {
				t.Fatal(err)
			}
			options := alertProcessorTestOptions(f)
			options.Maintenance = &alertMaintenanceOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 2000, MaxFragments: 2000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}}
			outcome, err := f.runtime.processAlertClaim(t.Context(), claim, options, f.clock)
			if err != nil || outcome != "evaluation" {
				t.Fatalf("terminal resource became failure/pending: %s %v", outcome, err)
			}
			cursor, err := dbgen.New(f.conn).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(claim.PolicyID), Revision: claim.Revision})
			if err != nil || cursor.Exhausted || cursor.FailureAttempts != 0 || cursor.PendingCutoff.Valid || !cursor.NextScheduledAt.Time.Equal(f.at.Add(time.Minute)) || !cursor.LastEvaluationID.Valid {
				t.Fatalf("terminal input exhausted/stuck cursor: %+v %v", cursor, err)
			}
			row, err := dbgen.New(f.conn).ReadAlertEvaluation(t.Context(), cursor.LastEvaluationID)
			if err != nil {
				t.Fatal(err)
			}
			var saved struct{ Input alert.Input }
			if err := unmarshalAlertData(row.Evidence, &saved); err != nil || row.Truth != string(alert.Indeterminate) {
				t.Fatalf("terminal input invented truth: %s %v", row.Truth, err)
			}
			for _, window := range saved.Input.Windows {
				if kind == "scoped_hour" && window.End.Sub(window.Start) == 5*time.Minute {
					if window.Quality != alert.Complete || window.Good != "2970" || window.Total != "3000" {
						t.Fatalf("terminal hour suppressed usable edge window: %+v", window)
					}
					continue
				}
				if window.Quality != alert.CalculationFailed || window.Reason != "maintenance_preparation_failed" || window.Good != "" || window.Total != "" {
					t.Fatalf("terminal input invented counts: %+v", window)
				}
			}
		})
	}
}

func TestIntegrationAlertMaintenanceTerminalIndependentCondition(t *testing.T) {
	t.Parallel()
	m, hour := terminalHourFixture(t)
	f := alertTestFixture{m.selfHealFixture, m.ids[0], alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}}
	policy := newAlertID().String()
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		return registerAlertPolicy(t.Context(), tx, alertRegistration{Policy: alert.PolicyRevision{ID: policy, Revision: 1, Conditions: []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: 10 * time.Minute, Threshold: "1"}, {ID: "sustained", Short: 30 * time.Minute, Long: 6 * time.Hour, Threshold: "1"}}}, SLOID: f.sloID, Name: "Independent ready fast condition", Severity: "high", FirstBoundary: f.at, Now: f.now, Limits: f.limits})
	})
	preparationEnqueue(t, m, []maintenanceEvaluationPreparation{{Kind: "scoped_hour", RequestedRevision: 1, BuildID: hour.BuildID, Hour: hour.maintenanceHourKey}}, maintenancePreparationLive)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_maintenance_preparation SET state='unavailable',reason='history_unavailable' WHERE kind='scoped_hour'`); err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.at)
	options := alertProcessorTestOptions(f)
	options.Maintenance = &alertMaintenanceOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 2000, MaxFragments: 2000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}}
	outcome, err := f.runtime.processAlertClaim(t.Context(), claim, options, f.clock)
	if err != nil || outcome != "evaluation" {
		t.Fatalf("independent condition blocked: %s %v", outcome, err)
	}
	cursor, err := dbgen.New(f.conn).ReadAlertCursor(t.Context(), dbgen.ReadAlertCursorParams{PolicyID: mustPGUUID(policy), Revision: 1})
	if err != nil || !cursor.OpenOccurrenceID.Valid || cursor.FailureAttempts != 0 {
		t.Fatalf("ready fast condition did not fire: %+v %v", cursor, err)
	}
	retained, err := ReadAlertEvidence(t.Context(), f.f.databaseURL, ReadAlertEvidenceInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: m.proof, ID: cursor.OpenOccurrenceID.String(), Now: f.clock}, Selection: "firing"})
	if err != nil || retained.Evaluation.Truth != alert.Breaching || len(retained.Evaluation.Conditions) != 2 || retained.Evaluation.Conditions[0].Outcome != alert.Matching || retained.Evaluation.Conditions[1].Outcome != alert.Unavailable || retained.Evaluation.Conditions[1].Long.Total != "" {
		t.Fatalf("mixed readiness lost exact outcome: %+v %v", retained.Evaluation, err)
	}
}
