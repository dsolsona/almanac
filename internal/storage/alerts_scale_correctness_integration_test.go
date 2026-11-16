//go:build integration

package storage

import (
	"fmt"
	"testing"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/jackc/pgx/v5"
)

// These are correctness fixtures, not capacity or latency acceptance. Each size
// shares one SLI/SLO, six hours of minute observations and one scoped population.
// Runtime admission, concurrency and retry options are existing test-only values.
func TestIntegrationAlertPolicyScaleCorrectness(t *testing.T) {
	t.Parallel()
	for _, size := range []int{100, 200} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Parallel()
			m, hour := terminalHourFixture(t) // Six populated hours; no terminal failure injected.
			f := alertTestFixture{m.selfHealFixture, m.ids[0], alert.Limits{MaxConditions: 8, MaxDecimalBytes: 96}}
			expected := make(map[string][2]alert.ConditionOutcome, size)
			firing := 0
			alertTestTx(t, f.conn, func(tx pgx.Tx) error {
				for i := range size {
					id := uuid.New().String()
					thresholds := [2]string{"2", "2"}
					outcomes := [2]alert.ConditionOutcome{alert.NotMatching, alert.NotMatching}
					if i%3 != 2 {
						thresholds[i%3] = "1"
						outcomes[i%3] = alert.Matching
						firing++
					}
					expected[id] = outcomes
					if err := registerAlertPolicy(t.Context(), tx, alertRegistration{
						Policy: alert.PolicyRevision{ID: id, Revision: 1, Conditions: []alert.Condition{
							{ID: "fast", Short: 5 * time.Minute, Long: 10 * time.Minute, Threshold: thresholds[0]},
							{ID: "sustained", Short: 30 * time.Minute, Long: 6 * time.Hour, Threshold: thresholds[1]},
						}}, SLOID: f.sloID, Name: fmt.Sprintf("Scale %03d policy %03d", size, i), Severity: "high", FirstBoundary: f.at, Now: f.now, Limits: f.limits,
					}); err != nil {
						return err
					}
				}
				return nil
			})
			options := alertProcessorTestOptions(f)
			options.MaxPolicies = size
			options.Maintenance = &alertMaintenanceOptions{Windows: maintenanceWindowLimits{MaxSourceHours: 100, MaxRawRows: 2000, MaxFragments: 2000}, Preparation: maintenancePreparationLimits{MaxRequests: 16, MaxSourceHours: 100}}
			// Negative control: genuine missing shared preparation visits every policy,
			// preserves all original pins and commits neither partial truth nor alerts.
			pending, err := f.runtime.processAlertTick(t.Context(), f.at, options, f.clock)
			if err != nil || pending != (alertProcessorResult{Claimed: size, Pending: size}) {
				t.Fatalf("pending fleet: %+v %v", pending, err)
			}
			var heads, evals, occurrences, jobs int
			err = f.conn.QueryRow(t.Context(), `SELECT
   (SELECT count(*) FROM almanac_alert_cursors WHERE next_scheduled_at=$1 AND pending_cutoff=$1 AND failure_attempts=0 AND claim_token IS NULL),
   (SELECT count(*) FROM almanac_alert_evaluations),
   (SELECT count(*) FROM almanac_alert_occurrences),
   (SELECT count(*) FROM almanac_maintenance_preparation WHERE kind='scoped_hour')`, f.at).Scan(&heads, &evals, &occurrences, &jobs)
			if err != nil || heads != size || evals != 0 || occurrences != 0 || jobs != 1 {
				t.Fatalf("pending did not preserve/deduplicate fleet: heads=%d evals=%d occurrences=%d shared_jobs=%d %v", heads, evals, occurrences, jobs, err)
			}
			if _, err := hourPrepare(t, m, hour); err != nil {
				t.Fatal(err)
			}
			f.now = f.at.Add(options.PendingRetry)
			tick := f.now
			var abandoned []alertClaim
			if size == 200 {
				abandoned, err = f.runtime.claimAlertWork(t.Context(), tick, f.now, options.Lease, 7)
				if err != nil || len(abandoned) != 7 {
					t.Fatalf("reserve crashed claims: %d %v", len(abandoned), err)
				}
				for _, claim := range abandoned {
					pinned, err := f.runtime.pinAlertCutoff(t.Context(), claim, f.clock)
					if err != nil || pinned.SkippedBeforeActivation || !pinned.Cutoff.Equal(f.at) {
						t.Fatalf("crashed claim lost original pin: %+v %v", pinned, err)
					}
				}
			}
			ready, err := f.runtime.processAlertTick(t.Context(), tick, options, f.clock)
			if err != nil || ready != (alertProcessorResult{Claimed: size - len(abandoned), Evaluations: size - len(abandoned)}) {
				t.Fatalf("ready fleet: %+v %v", ready, err)
			}
			repeat, err := f.runtime.processAlertTick(t.Context(), tick, options, f.clock)
			if err != nil || repeat != (alertProcessorResult{}) {
				t.Fatalf("same tick duplicated work: %+v %v", repeat, err)
			}
			if len(abandoned) > 0 {
				// No fixture state writes: close the real pools, reopen normally and let the
				// persisted leases expire on the deterministic clock. Seven original heads
				// must precede already-processed policies' later scheduled boundaries.
				f.runtime.Close()
				restarted, err := OpenRuntime(t.Context(), f.f.databaseURL)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(restarted.Close)
				f.runtime = restarted
				f.now = abandoned[0].Deadline
				options.MaxPolicies = len(abandoned)
				recovered, err := f.runtime.processAlertTick(t.Context(), f.now, options, f.clock)
				if err != nil || recovered != (alertProcessorResult{Claimed: len(abandoned), Evaluations: len(abandoned)}) {
					t.Fatalf("restart did not prioritize original heads: %+v %v", recovered, err)
				}
			}
			assertAlertPolicyScaleEvidence(t, f, expected, firing, hour.BuildID.String())
			t.Logf("correctness only: %d policies / 1 SLO / 1 SLI; 360 observed minute intervals; one ten-minute exclusion; 4 windows; %d firing, %d clear; concurrency=2; no latency/capacity claim", size, firing, size-firing)
		})
	}
}

func assertAlertPolicyScaleEvidence(t *testing.T, f alertTestFixture, expected map[string][2]alert.ConditionOutcome, firing int, build string) {
	t.Helper()
	var generation int64
	if err := f.conn.QueryRow(t.Context(), `SELECT generation FROM almanac_sli_source_generations WHERE sli_id=$1 AND measurement_revision=1`, f.sliID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	rows, err := f.conn.Query(t.Context(), `SELECT policy_id::text,scheduled_at,effective_cutoff,truth,evidence FROM almanac_alert_evaluations ORDER BY policy_id`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	// The fixture contributes 990/1000 per minute. Only the six-hour window
	// intersects its ten excluded minutes: (360-10)*990 / (360-10)*1000.
	counts := map[time.Duration][2]string{5 * time.Minute: {"4950", "5000"}, 10 * time.Minute: {"9900", "10000"}, 30 * time.Minute: {"29700", "30000"}, 6 * time.Hour: {"346500", "350000"}}
	for rows.Next() {
		var id, truth string
		var scheduled, cutoff time.Time
		var raw []byte
		if err := rows.Scan(&id, &scheduled, &cutoff, &truth, &raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		outcomes, ok := expected[id]
		if !ok || seen[id] || !scheduled.Equal(f.at) || !cutoff.Equal(f.at) {
			rows.Close()
			t.Fatalf("duplicate/unexpected policy or moved original boundary: %s %s %s", id, scheduled, cutoff)
		}
		seen[id] = true
		var saved struct {
			FormatVersion int
			Input         alert.Input
			Evaluation    alert.Evaluation
		}
		if err := unmarshalAlertData(raw, &saved); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		want := alert.Clear
		if outcomes[0] == alert.Matching || outcomes[1] == alert.Matching {
			want = alert.Breaching
		}
		if truth != string(want) || saved.Evaluation.Truth != want || saved.Input.Policy.ID != id || saved.Input.Snapshot.SLOID != f.sloID || saved.Input.Snapshot.SourceGeneration != generation || saved.Input.Snapshot.ExclusionRevision != 1 || !saved.Input.Snapshot.Cutoff.Equal(f.at) || len(saved.Input.Windows) != 4 || len(saved.Evaluation.Conditions) != 2 {
			rows.Close()
			t.Fatalf("policy identity/OR outcome changed: %+v", saved)
		}
		for i, condition := range saved.Evaluation.Conditions {
			if condition.ID != []string{"fast", "sustained"}[i] || condition.Outcome != outcomes[i] {
				rows.Close()
				t.Fatalf("wrong independent condition result: %+v", condition)
			}
		}
		durations := map[time.Duration]bool{}
		for _, w := range saved.Input.Windows {
			duration := w.End.Sub(w.Start)
			pair, ok := counts[duration]
			if !ok || durations[duration] || w.Good != pair[0] || w.Total != pair[1] || w.Quality != alert.Complete || w.Snapshot != saved.Input.Snapshot || w.Maintenance == nil || w.Maintenance.ComputedRevision != 1 || w.Maintenance.RequestedRevision != 1 || w.Maintenance.BuildID != build {
				rows.Close()
				t.Fatalf("wrong shared count/provenance for %s: %+v", duration, w)
			}
			durations[duration] = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(seen) != len(expected) {
		t.Fatalf("starved policies: %d of %d evaluated", len(seen), len(expected))
	}
	var open, unique, advanced, prepared int
	err = f.conn.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM almanac_alert_occurrences WHERE state='firing'),
 (SELECT count(DISTINCT policy_id) FROM almanac_alert_occurrences),
 (SELECT count(*) FROM almanac_alert_cursors WHERE next_scheduled_at=$1 AND pending_cutoff IS NULL AND claim_token IS NULL AND NOT exhausted AND failure_attempts=0),
 (SELECT count(*) FROM almanac_slo_maintenance_hours)`, f.at.Add(time.Minute)).Scan(&open, &unique, &advanced, &prepared)
	if err != nil || open != firing || unique != firing || advanced != len(expected) || prepared != 1 {
		t.Fatalf("duplicate/lost occurrences or work: firing=%d unique=%d advanced=%d shared_population=%d %v", open, unique, advanced, prepared, err)
	}
}
