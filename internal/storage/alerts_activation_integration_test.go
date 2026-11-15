//go:build integration

package storage

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func createActivationPolicy(t *testing.T, f alertTestFixture) string {
	t.Helper()
	input := alertManagementCreate(f, "activation-create")
	input.FirstBoundary = time.Time{}
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	return created.Policy.ID
}

func TestIntegrationAlertActivationSkipsCanonicalHead(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.drain(t, "activation history")
	f.now = f.at.Add(10 * time.Second)
	policy := createActivationPolicy(t, f)
	f.now = f.at.Add(time.Minute)
	turn, err := f.runtime.processAlertTick(t.Context(), f.now, alertProcessorTestOptions(f), f.clock)
	if err != nil {
		t.Fatal(err)
	}
	var evaluations, occurrences, ranges, attempts int
	var next time.Time
	var pinned, leased bool
	err = f.conn.QueryRow(t.Context(), `SELECT next_scheduled_at,pending_cutoff IS NOT NULL,claim_token IS NOT NULL,failure_attempts,
 (SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1),
 (SELECT count(*) FROM almanac_alert_occurrences WHERE policy_id=$1),
 (SELECT count(*) FROM almanac_alert_ranges WHERE policy_id=$1 AND kind='before_policy_activation' AND starts_at=$2 AND ends_at=$3)
 FROM almanac_alert_cursors WHERE policy_id=$1`, policy, f.now, f.now.Add(time.Minute)).Scan(&next, &pinned, &leased, &attempts, &evaluations, &occurrences, &ranges)
	if err != nil || turn.BeforeActivation != 1 || turn.Evaluations != 0 || turn.Pending != 0 || evaluations != 0 || occurrences != 0 || ranges != 1 || attempts != 0 || pinned || leased || !next.Equal(f.now.Add(time.Minute)) {
		t.Fatalf("ineligible boundary was evaluated or pinned: turn=%+v next=%s pin=%t lease=%t attempts=%d evals=%d occurrences=%d ranges=%d error=%v", turn, next, pinned, leased, attempts, evaluations, occurrences, ranges, err)
	}
	// A newly received complete interval makes the following cutoff eligible.
	// Its one-hour population includes history from before policy activation.
	if _, err := execObservationFixture(t, f.conn, f.sliID, `INSERT INTO almanac_sli_observations
 (interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+interval '1 minute',sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at+interval '1 minute',recorded_at+interval '1 minute'
 FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2::timestamptz-interval '1 minute'`, f.sliID, f.at); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(2 * time.Minute)
	f.drain(t, "activation eligible history")
	turn, err = f.runtime.processAlertTick(t.Context(), f.now, alertProcessorTestOptions(f), f.clock)
	if err != nil || turn.Evaluations != 1 {
		t.Fatalf("eligible follow-up did not evaluate: %+v %v", turn, err)
	}
	var state string
	if err := f.conn.QueryRow(t.Context(), `SELECT state FROM almanac_alert_occurrences WHERE policy_id=$1`, policy).Scan(&state); err != nil || state != "firing" {
		t.Fatalf("historical full window could not fire: %s %v", state, err)
	}
	rangePage, err := ListAlertHistoryRanges(t.Context(), f.f.databaseURL, ListAlertRangesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "activation-ranges").Proof, ID: policy, Now: f.clock}, Revision: 1, Limit: 20})
	if err != nil || len(rangePage.Items) != 1 || rangePage.Items[0].Kind != "before_policy_activation" {
		t.Fatalf("protected range history: %+v %v", rangePage, err)
	}
	var occurrence string
	if err := f.conn.QueryRow(t.Context(), `SELECT id::text FROM almanac_alert_occurrences WHERE policy_id=$1`, policy).Scan(&occurrence); err != nil {
		t.Fatal(err)
	}
	series, err := ReadAlertSeries(t.Context(), f.f.databaseURL, ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "activation-series").Proof, ID: occurrence, Now: f.clock}, ConditionID: "fast", Start: f.at, End: f.now, Limit: 120})
	if err != nil || !slices.ContainsFunc(series.Gaps, func(g AlertSeriesGap) bool {
		return g.Kind == "before_policy_activation" && g.Basis == "scheduled_boundary"
	}) {
		t.Fatalf("activation gap lost its scheduled basis: %+v %v", series.Gaps, err)
	}
}

func TestIntegrationAlertActivationSubMinuteAndEquality(t *testing.T) {
	t.Parallel()
	for _, seconds := range []int{10, 15} {
		t.Run((time.Duration(seconds) * time.Second).String(), func(t *testing.T) {
			t.Parallel()
			s := newSelfHealFixtureCadence(t, "activation-subminute", true, 15*time.Second)
			f := alertTestFixture{s, s.createSLO(t, "activation-subminute"), alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}}
			f.now = f.at.Add(time.Duration(seconds) * time.Second)
			createActivationPolicy(t, f)
			if _, err := execObservationFixture(t, f.conn, f.sliID, `INSERT INTO almanac_sli_observations
 (interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
 SELECT interval_start+interval '15 seconds',sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,agent_created_at+interval '15 seconds',recorded_at+interval '15 seconds'
 FROM almanac_sli_observations WHERE sli_id=$1 AND interval_start=$2::timestamptz-interval '15 seconds'`, f.sliID, f.at); err != nil {
				t.Fatal(err)
			}
			f.now = f.at.Add(time.Minute)
			f.drain(t, "subminute activation")
			claim := f.claim(t, f.now)
			prepared, err := f.runtime.readAlertEvidence(t.Context(), claim, time.Time{}, f.limits, f.clock)
			if err != nil || !prepared.Cutoff.Equal(f.at.Add(15*time.Second)) {
				t.Fatalf("eligible cutoff before rounded origin rejected: %+v %v", prepared, err)
			}
			got, err := f.runtime.commitAlertEvaluation(t.Context(), claim, prepared.Input, f.limits, f.clock)
			if err != nil || got.Occurrence == nil || got.Occurrence.State != alert.Firing {
				t.Fatalf("equality/subminute publication: %+v %v", got, err)
			}
		})
	}
}

func TestIntegrationAlertActivationFencesSkip(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"expiry after write", "disable", "semantic replacement"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			f.now = f.at.Add(10 * time.Second)
			policy := createActivationPolicy(t, f)
			f.now = f.at.Add(time.Minute)
			claim := f.claim(t, f.now)
			clock := f.clock
			switch mode {
			case "expiry after write":
				var calls atomic.Int32
				clock = func() time.Time {
					if calls.Add(1) >= 3 {
						return f.now.Add(time.Minute)
					}
					return f.now
				}
			case "disable":
				if _, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "activation-disable"), ID: policy, ExpectedVersion: 1, Reason: "Paused"}); err != nil {
					t.Fatal(err)
				}
			case "semantic replacement":
				conditions := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "2"}}
				if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "activation-update"), ID: policy, ExpectedVersion: 1, Conditions: &conditions, Reason: "Changed", Limits: f.limits}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.runtime.pinAlertCutoff(t.Context(), claim, clock); !errors.Is(err, ErrAlertStale) {
				t.Fatalf("stale skip accepted: %v", err)
			}
			var count int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_ranges WHERE policy_id=$1 AND kind='before_policy_activation'`, policy).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale range committed: %d %v", count, err)
			}
			if mode == "expiry after write" {
				var next time.Time
				if err := f.conn.QueryRow(t.Context(), `SELECT next_scheduled_at FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&next); err != nil || !next.Equal(claim.ScheduledAt) {
					t.Fatalf("expired write advanced head: %s %v", next, err)
				}
			}
		})
	}
}

func TestIntegrationAlertActivationDirectGuards(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"preparation", "journal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			f.drain(t, "direct activation history")
			f.now = f.at.Add(10 * time.Second)
			policy := createActivationPolicy(t, f)
			f.now = f.at.Add(time.Minute)
			claim := f.claim(t, f.now)
			var err error
			if mode == "preparation" {
				_, err = prepareAlertEvidence(t.Context(), dbgen.New(f.conn), claim, f.at, f.limits, f.now)
			} else {
				input := f.input(t, claim, alert.Complete, true)
				input.Snapshot.Cutoff = f.at
				for i := range input.Windows {
					d := input.Windows[i].End.Sub(input.Windows[i].Start)
					input.Windows[i].Snapshot = input.Snapshot
					input.Windows[i].End = f.at
					input.Windows[i].Start = f.at.Add(-d)
				}
				_, err = f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, f.clock)
			}
			if !errors.Is(err, ErrAlertInvalid) {
				t.Fatalf("pre-activation %s accepted: %v", mode, err)
			}
			var count int
			if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=$1`, policy).Scan(&count); err != nil || count != 0 {
				t.Fatalf("guard published evidence: %d %v", count, err)
			}
		})
	}
}

func TestIntegrationAlertActivationExistingPinPreservesFailures(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.now = f.at.Add(10 * time.Second)
	policy := createActivationPolicy(t, f)
	f.now = f.at.Add(time.Minute)
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_alert_cursors SET pending_cutoff=$2,failure_attempts=1,failure_since=$3,last_failure_at=$3 WHERE policy_id=$1`, policy, f.at, f.now); err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.now)
	if _, err := f.runtime.pinAlertCutoff(t.Context(), claim, f.clock); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var next time.Time
	var pinned bool
	if err := f.conn.QueryRow(t.Context(), `SELECT next_scheduled_at,pending_cutoff IS NOT NULL,failure_attempts FROM almanac_alert_cursors WHERE policy_id=$1`, policy).Scan(&next, &pinned, &attempts); err != nil || pinned || attempts != 1 || !next.Equal(f.now.Add(time.Minute)) {
		t.Fatalf("existing pin not skipped preserving failure budget: next=%s pin=%t attempts=%d error=%v", next, pinned, attempts, err)
	}
}

func TestIntegrationAlertActivationRevisionClocks(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.now = f.at.Add(10 * time.Second)
	policy := createActivationPolicy(t, f)
	assertActivation := func(revision int64, seconds int) {
		t.Helper()
		var at time.Time
		if err := f.conn.QueryRow(t.Context(), `SELECT created_at FROM almanac_alert_policy_revisions WHERE policy_id=$1 AND revision=$2`, policy, revision).Scan(&at); err != nil || !at.Equal(f.at.Add(time.Duration(seconds)*time.Second)) {
			t.Fatalf("revision activation: %d %s %v", revision, at, err)
		}
	}
	f.now = f.at.Add(20 * time.Second)
	name := "Metadata only"
	metadata, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "activation-name"), ID: policy, ExpectedVersion: 1, Name: &name, Limits: f.limits})
	if err != nil || metadata.Policy.EvaluationRevision != 1 {
		t.Fatalf("metadata changed revision: %+v %v", metadata, err)
	}
	assertActivation(1, 10)
	f.now = f.at.Add(30 * time.Second)
	conditions := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "2"}}
	changed, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "activation-meaning"), ID: policy, ExpectedVersion: 2, Conditions: &conditions, Limits: f.limits, Reason: "New meaning"})
	if err != nil || changed.Policy.EvaluationRevision != 2 {
		t.Fatalf("semantic revision: %+v %v", changed, err)
	}
	assertActivation(2, 30)
	f.now = f.at.Add(40 * time.Second)
	if _, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "activation-pause"), ID: policy, ExpectedVersion: 3, Reason: "Pause"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(110 * time.Second)
	enabled, err := EnableAlertPolicy(t.Context(), f.f.databaseURL, EnableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "activation-resume"), ID: policy, ExpectedVersion: 4, Limits: f.limits, Reason: "Resume"})
	if err != nil || enabled.Policy.EvaluationRevision != 3 {
		t.Fatalf("enable revision: %+v %v", enabled, err)
	}
	assertActivation(3, 110)
	// A persisted cutoff eligible for the old revision is still before the
	// new enable activation; the immutable current revision must win.
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_alert_cursors SET pending_cutoff=$2 WHERE policy_id=$1 AND revision=3`, policy, f.at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(2 * time.Minute)
	turn, err := f.runtime.processAlertTick(t.Context(), f.now, alertProcessorTestOptions(f), f.clock)
	if err != nil || turn.BeforeActivation != 1 || turn.Evaluations != 0 {
		t.Fatalf("enabled revision reused old activation: %+v %v", turn, err)
	}
}
