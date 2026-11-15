//go:build integration

package storage

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

func TestIntegrationAlertPolicyRevisionAutomaticCreate(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.now = f.at.Add(17 * time.Second)
	input := alertManagementCreate(f, "automatic-create")
	input.FirstBoundary = time.Time{}
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatalf("automatic authoritative activation: %v", err)
	}
	var origin time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT origin_at FROM almanac_alert_cursors WHERE policy_id=$1`, created.Policy.ID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if !origin.Equal(f.at.Add(time.Minute)) || !created.Policy.CreatedAt.Equal(f.now) {
		t.Fatalf("wrong activation: origin=%s created=%s", origin, created.Policy.CreatedAt)
	}
}

func TestIntegrationAlertPolicyRevisionPresetConfirmation(t *testing.T) {
	t.Parallel()
	for _, preset := range []alert.Condition{{ID: "fast-renamed", Short: 5 * time.Minute, Long: time.Hour, Threshold: "014.400"}, {ID: "slow-renamed", Short: 30 * time.Minute, Long: 6 * time.Hour, Threshold: "06.000"}} {
		for _, operation := range []string{"create", "update"} {
			t.Run(preset.ID+"-"+operation, func(t *testing.T) {
				t.Parallel()
				f := newAlertTestFixture(t)
				conditions := []alert.Condition{preset}
				input := alertManagementCreate(f, "preset-create")
				var accepted AlertCommandResult
				var err error
				if operation == "create" {
					input.Conditions = conditions
					if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertPresetConfirmation) {
						t.Fatalf("unconfirmed preset: %v", err)
					}
					input.ConfirmNon30DayPresets = true
					accepted, err = CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
					if err != nil {
						t.Fatal(err)
					}
					input.ConfirmNon30DayPresets = false
					if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertKeyConflict) {
						t.Fatalf("confirmation missing from receipt digest: %v", err)
					}
				} else {
					created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
					if err != nil {
						t.Fatal(err)
					}
					change := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "preset-update"), ID: created.Policy.ID, ExpectedVersion: 1, Conditions: &conditions, Reason: "Use preset", Limits: f.limits}
					if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, change); !errors.Is(err, ErrAlertPresetConfirmation) {
						t.Fatalf("unconfirmed update: %v", err)
					}
					change.ConfirmNon30DayPresets = true
					accepted, err = UpdateAlertPolicy(t.Context(), f.f.databaseURL, change)
					if err != nil {
						t.Fatal(err)
					}
					change.ConfirmNon30DayPresets = false
					if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, change); !errors.Is(err, ErrAlertKeyConflict) {
						t.Fatalf("update confirmation missing from receipt digest: %v", err)
					}
				}
				name := "Metadata without reconfirmation"
				metadata, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "preset-metadata"), ID: accepted.Policy.ID, ExpectedVersion: accepted.Policy.Version, Name: &name, Limits: f.limits})
				if err != nil {
					t.Fatalf("metadata demanded reconfirmation: %v", err)
				}
				stopped, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "preset-disable"), ID: accepted.Policy.ID, ExpectedVersion: metadata.Policy.Version, Reason: "Pause"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := EnableAlertPolicy(t.Context(), f.f.databaseURL, EnableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "preset-enable"), ID: accepted.Policy.ID, ExpectedVersion: stopped.Policy.Version, Reason: "Resume", Limits: f.limits}); err != nil {
					t.Fatalf("unchanged enable demanded reconfirmation: %v", err)
				}
			})
		}
	}
}

func alertRevisionTarget(t *testing.T, f alertTestFixture, name, sliID string, period int64) string {
	t.Helper()
	definition, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	_, target, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{Proof: alertManagementCommand(f, name).Proof, Name: name, DisplayName: name, ServiceID: definition.ServiceID.String(), SliID: sliID, Target: "0.99", RollingWindowSeconds: period, EvaluationValidFrom: &f.historyStart, IdempotencyKey: name, RequestID: name, Now: f.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	return target.ID
}

func TestIntegrationAlertPolicyRevisionPresetPeriodAndRebind(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	thirtyDay := alertRevisionTarget(t, f, "thirty-day", f.sliID, 30*86400)
	input := alertManagementCreate(f, "thirty-create")
	input.SLOID = thirtyDay
	input.Conditions = []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "14.4"}}
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatalf("30-day preset needs no confirmation: %v", err)
	}
	rebind := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "period-rebind"), ID: created.Policy.ID, ExpectedVersion: 1, SLOID: &f.sloID, Reason: "Rebind", Limits: f.limits}
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, rebind); !errors.Is(err, ErrAlertPresetConfirmation) {
		t.Fatalf("rebind skipped confirmation: %v", err)
	}
	rebind.ConfirmNon30DayPresets = true
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, rebind); err != nil {
		t.Fatal(err)
	}
	input = alertManagementCreate(f, "custom-create")
	input.Name = "Custom burn"
	input.Conditions = []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "14.4001"}}
	if _, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input); err != nil {
		t.Fatalf("custom value incorrectly classified as preset: %v", err)
	}
}

func TestIntegrationAlertPolicyRevisionMetadataAndReplacement(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "revision-create"))
	if err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(2 * time.Second)
	ack, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, AcknowledgeAlertInput{AlertCommandInput: alertManagementCommand(f, "revision-ack"), ID: opened.Occurrence.ID, ExpectedVersion: opened.Version})
	if err != nil {
		t.Fatal(err)
	}
	name, severity := "Edited name", "low"
	metadata := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "revision-metadata"), ID: created.Policy.ID, ExpectedVersion: 1, Name: &name, Severity: &severity, Limits: f.limits}
	edited, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, metadata)
	if err != nil || edited.Policy.Version != 2 || edited.Policy.EvaluationRevision != 1 {
		t.Fatalf("metadata: %+v %v", edited, err)
	}
	current, err := ReadAlert(t.Context(), f.f.databaseURL, ReadAlertInput{Runtime: f.runtime, Proof: metadata.Proof, Now: f.clock, ID: opened.Occurrence.ID})
	if err != nil || current.Version != ack.Alert.Version || current.Occurrence.State != alert.Acknowledged || current.OriginalPolicy.Name != "Managed burn" || current.OriginalPolicy.Severity != "high" {
		t.Fatalf("metadata changed original or global state: %+v %v", current, err)
	}
	// Same meaning must not manufacture an evaluation revision or an audit change.
	conditions := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "001.000"}}
	noop := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "revision-noop"), ID: created.Policy.ID, ExpectedVersion: 2, Conditions: &conditions, Limits: f.limits}
	unchanged, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, noop)
	if err != nil || unchanged.Policy.Version != 2 {
		t.Fatalf("equivalent update: %+v %v", unchanged, err)
	}
	var audits int
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*) FROM almanac_audit_events WHERE action='alert-policy.updated'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("no-op created audit change: %d %v", audits, err)
	}
	f.now = f.at.Add(3*time.Minute + 17*time.Second)
	oldClaim := f.claim(t, f.now)
	oldInput := f.input(t, oldClaim, alert.Complete, false)
	if _, err := f.runtime.pinAlertCutoff(t.Context(), oldClaim, f.clock); err != nil {
		t.Fatal(err)
	}
	conditions = append(conditions, alert.Condition{ID: "medium", Short: 10 * time.Minute, Long: 2 * time.Hour, Threshold: "2"}, alert.Condition{ID: "slow", Short: 30 * time.Minute, Long: 6 * time.Hour, Threshold: "3"})
	semantic := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "revision-semantic"), ID: created.Policy.ID, ExpectedVersion: 2, Conditions: &conditions, Reason: "Changed burn windows", Limits: f.limits}
	replaced, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, semantic)
	if err != nil || replaced.Policy.Version != 3 || replaced.Policy.EvaluationRevision != 2 || len(replaced.Policy.Conditions) != 3 || replaced.Alert == nil || replaced.Alert.Occurrence.State != alert.Superseded || !replaced.Alert.Occurrence.ClosedAt.Equal(f.now) {
		t.Fatalf("semantic edit: %+v %v", replaced, err)
	}
	var start, end, origin time.Time
	var schedulable, pinned, claimed bool
	if err := f.conn.QueryRow(t.Context(), `SELECT starts_at,ends_at FROM almanac_alert_ranges WHERE policy_id=$1 AND revision=1`, created.Policy.ID).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	if !start.Equal(f.at.Add(time.Minute)) || !end.Equal(f.at.Add(4*time.Minute)) {
		t.Fatalf("incorrect cancellation: %s..%s", start, end)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT schedulable,pending_cutoff IS NOT NULL,claim_token IS NOT NULL FROM almanac_alert_cursors WHERE policy_id=$1 AND revision=1`, created.Policy.ID).Scan(&schedulable, &pinned, &claimed); err != nil || schedulable || pinned || claimed {
		t.Fatalf("old work not fenced: %v %v %v %v", schedulable, pinned, claimed, err)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT origin_at FROM almanac_alert_cursors WHERE policy_id=$1 AND revision=2`, created.Policy.ID).Scan(&origin); err != nil || !origin.Equal(end) {
		t.Fatalf("new activation: %s %v", origin, err)
	}
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), oldClaim, oldInput, f.limits, f.clock); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("old worker committed: %v", err)
	}
	replay, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, semantic)
	if err != nil || !reflect.DeepEqual(replay, replaced) {
		t.Fatalf("replacement replay: %+v %v", replay, err)
	}
	semantic.IdempotencyKey, semantic.RequestID = "revision-stale", "revision-stale"
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, semantic); !errors.Is(err, ErrAlertStale) {
		t.Fatalf("stale edit: %v", err)
	}
}

func TestIntegrationAlertPolicyRevisionClockAfterWait(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"create", "metadata", "replace", "disable", "enable"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			var policy *AlertPolicyRecord
			var occurrence *AlertRecord
			if operation != "create" {
				input := alertManagementCreate(f, "wait-create")
				if operation == "enable" {
					enabled := false
					input.Enabled = &enabled
				}
				created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
				if err != nil {
					t.Fatal(err)
				}
				policy = created.Policy
				if operation == "replace" || operation == "disable" {
					claim := f.claim(t, f.at)
					opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
					if err != nil {
						t.Fatal(err)
					}
					occurrence = &AlertRecord{Occurrence: *opened.Occurrence, Version: opened.Version}
				}
			}
			holder, err := f.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(holder)
			pattern := "%FROM public.almanac_alert_policies%FOR UPDATE%"
			switch operation {
			case "create":
				_, err = holder.Exec(t.Context(), `SELECT id FROM almanac_slis WHERE id=$1 FOR UPDATE`, f.sliID)
				pattern = "%FROM public.almanac_slis WHERE id=%FOR SHARE%"
			case "replace", "disable":
				_, err = dbgen.New(holder).LockAlertOccurrence(t.Context(), mustPGUUID(occurrence.Occurrence.ID))
				pattern = "%FROM public.almanac_alert_occurrences%FOR UPDATE%"
			default:
				_, err = dbgen.New(holder).LockAlertPolicy(t.Context(), mustPGUUID(policy.ID))
			}
			if err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64
			clock.Store(f.at.Add(2 * time.Second).UnixNano())
			command := alertManagementCommand(f, "wait-"+operation)
			command.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			type outcome struct {
				result AlertCommandResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				var result AlertCommandResult
				var err error
				switch operation {
				case "create":
					input := alertManagementCreate(f, "wait-new")
					input.AlertCommandInput = command
					input.FirstBoundary = time.Time{}
					result, err = CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
				case "metadata":
					name := "Waited metadata"
					result, err = UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: command, ID: policy.ID, ExpectedVersion: 1, Name: &name, Limits: f.limits})
				case "replace":
					conditions := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "2"}}
					result, err = UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: command, ID: policy.ID, ExpectedVersion: 1, Conditions: &conditions, Reason: "Replace after wait", Limits: f.limits})
				case "disable":
					result, err = DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: command, ID: policy.ID, ExpectedVersion: 1, Reason: "Disable after wait"})
				case "enable":
					result, err = EnableAlertPolicy(t.Context(), f.f.databaseURL, EnableAlertPolicyInput{AlertCommandInput: command, ID: policy.ID, ExpectedVersion: 1, Reason: "Enable after wait", Limits: f.limits})
				}
				done <- outcome{result, err}
			}()
			observer := integrationConnect(t, f.f.databaseURL)
			defer closeIntegrationConn(observer)
			waitForAgentQueryWait(t, observer, pattern)
			after := f.at.Add(time.Minute + 17*time.Second)
			clock.Store(after.UnixNano())
			if err := holder.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err != nil || !got.result.Policy.UpdatedAt.Equal(after) {
				t.Fatalf("pre-wait management time: %+v %v", got.result, got.err)
			}
			if occurrence != nil && (got.result.Alert == nil || !got.result.Alert.Occurrence.ClosedAt.Equal(after) || !got.result.Alert.Occurrence.UpdatedAt.Equal(after)) {
				t.Fatalf("pre-wait terminal time: %+v", got.result.Alert)
			}
			if operation == "create" || operation == "replace" || operation == "enable" {
				var origin time.Time
				if err := f.conn.QueryRow(t.Context(), `SELECT origin_at FROM almanac_alert_cursors WHERE policy_id=$1 AND revision=$2`, got.result.Policy.ID, got.result.Policy.EvaluationRevision).Scan(&origin); err != nil || !origin.Equal(f.at.Add(2*time.Minute)) {
					t.Fatalf("activation precedes completion: %s %v", origin, err)
				}
			}
		})
	}
}

func TestIntegrationAlertPolicyRevisionAuditRollback(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "rollback-create"))
	if err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `CREATE FUNCTION fail_alert_revision_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='alert-policy.updated' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_alert_revision_audit BEFORE INSERT ON almanac_audit_events FOR EACH ROW EXECUTE FUNCTION fail_alert_revision_audit()`); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(3 * time.Minute)
	conditions := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "2"}}
	input := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "rollback-edit"), ID: created.Policy.ID, ExpectedVersion: 1, Conditions: &conditions, Reason: "Replacement", Limits: f.limits}
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, input); err == nil {
		t.Fatal("injected audit failure committed")
	}
	var revision, ranges, receipts int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT evaluation_revision FROM almanac_alert_policies),(SELECT count(*) FROM almanac_alert_ranges),(SELECT count(*) FROM almanac_management_receipts WHERE operation='alert-policy.update')`).Scan(&revision, &ranges, &receipts); err != nil || revision != 1 || ranges != 0 || receipts != 0 {
		t.Fatalf("partial replacement committed: rev=%d ranges=%d receipts=%d %v", revision, ranges, receipts, err)
	}
	row, err := dbgen.New(f.conn).LockAlertOccurrence(t.Context(), mustPGUUID(opened.Occurrence.ID))
	if err != nil || row.State != "firing" || row.Version != opened.Version {
		t.Fatalf("rollback closed occurrence: %+v %v", row, err)
	}
}

func TestIntegrationAlertPolicyRevisionSourceFence(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "source-create"))
	if err != nil {
		t.Fatal(err)
	}
	conditions := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "2"}}
	input := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "source-edit"), ID: created.Policy.ID, ExpectedVersion: 1, Conditions: &conditions, Reason: "Replace", Limits: f.limits}
	holder, err := f.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackTransaction(holder)
	if _, err := holder.Exec(t.Context(), `SELECT id FROM almanac_slis WHERE id=$1 FOR UPDATE`, f.sliID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, input); done <- err }()
	observer := integrationConnect(t, f.f.databaseURL)
	defer closeIntegrationConn(observer)
	waitForAgentQueryWait(t, observer, "%FROM public.almanac_slis WHERE id=%FOR SHARE%")
	// A committed measurement-revision change wins while management waits.
	if _, err := holder.Exec(t.Context(), `INSERT INTO almanac_sli_measurements(sli_id,revision,datasource_id,agent_id,measurement_kind,component_mode,good_query,total_query,effective_from,activated_at,created_at)
SELECT sli_id,2,datasource_id,agent_id,measurement_kind,component_mode,good_query,total_query,effective_from,activated_at,created_at FROM almanac_sli_measurements WHERE sli_id=$1 AND revision=1`, f.sliID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(t.Context(), `UPDATE almanac_slis SET measurement_revision=measurement_revision+1 WHERE id=$1`, f.sliID); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrAlertStale) {
		t.Fatalf("source changed beneath validation: %v", err)
	}
	var version int64
	if err := f.conn.QueryRow(t.Context(), `SELECT version FROM almanac_alert_policies WHERE id=$1`, created.Policy.ID).Scan(&version); err != nil || version != 1 {
		t.Fatalf("fenced change mutated policy: %d %v", version, err)
	}
}

func TestIntegrationAlertPolicyRevisionDisabledAndEnable(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	input := alertManagementCreate(f, "disabled-create")
	disabled := false
	input.Enabled = &disabled
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil || created.Policy.Enabled || created.Policy.Version != 1 {
		t.Fatalf("disabled create: %+v %v", created, err)
	}
	var schedulable bool
	if err := f.conn.QueryRow(t.Context(), `SELECT schedulable FROM almanac_alert_cursors WHERE policy_id=$1`, created.Policy.ID).Scan(&schedulable); err != nil || schedulable {
		t.Fatalf("disabled cursor active: %v %v", schedulable, err)
	}
	claims, err := f.runtime.claimAlertWork(t.Context(), f.at, f.at, time.Minute, 10)
	if err != nil || len(claims) != 0 {
		t.Fatalf("disabled claimed: %+v %v", claims, err)
	}
	f.now = f.at.Add(17*time.Minute + time.Second)
	conditions := []alert.Condition{{ID: "replacement", Short: 5 * time.Minute, Long: time.Hour, Threshold: "2"}}
	edited, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "disabled-edit"), ID: created.Policy.ID, ExpectedVersion: 1, Conditions: &conditions, Reason: "Prepare new definition", Limits: f.limits})
	if err != nil || edited.Policy.Enabled || edited.Policy.EvaluationRevision != 2 {
		t.Fatalf("disabled edit: %+v %v", edited, err)
	}
	enable := EnableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "enable"), ID: created.Policy.ID, ExpectedVersion: 2, Reason: "Resume", Limits: f.limits}
	enabled, err := EnableAlertPolicy(t.Context(), f.f.databaseURL, enable)
	if err != nil || !enabled.Policy.Enabled || enabled.Policy.EvaluationRevision != 3 || enabled.Policy.Version != 3 {
		t.Fatalf("enable: %+v %v", enabled, err)
	}
	var origin time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT origin_at FROM almanac_alert_cursors WHERE policy_id=$1 AND revision=3`, created.Policy.ID).Scan(&origin); err != nil || !origin.Equal(f.at.Add(18*time.Minute)) {
		t.Fatalf("disabled time replayed: %s %v", origin, err)
	}
	f.now = f.at.Add(19 * time.Minute)
	stopped, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "disable-again"), ID: created.Policy.ID, ExpectedVersion: 3, Reason: "Pause"})
	if err != nil || stopped.Policy.Enabled {
		t.Fatalf("automatic disable: %+v %v", stopped, err)
	}
	replay, err := EnableAlertPolicy(t.Context(), f.f.databaseURL, enable)
	if err != nil || !reflect.DeepEqual(replay, enabled) {
		t.Fatalf("enable lost-response receipt changed: %+v %v", replay, err)
	}
	var revisions, ranges int
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM almanac_alert_policy_revisions),(SELECT count(*) FROM almanac_alert_ranges)`).Scan(&revisions, &ranges); err != nil || revisions != 3 || ranges != 1 {
		t.Fatalf("replay/disabled edit invented work: revisions=%d ranges=%d %v", revisions, ranges, err)
	}
}

func TestIntegrationAlertPolicyRevisionRebindValidation(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "rebind-create"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	proof := alertManagementCommand(f, "second-slo").Proof
	_, second, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "revision-second", DisplayName: "Second SLO", ServiceID: definition.ServiceID.String(), SliID: f.sliID, Target: "0.5", RollingWindowSeconds: 3600, EvaluationValidFrom: &f.historyStart, IdempotencyKey: "second-slo", RequestID: "second-slo", Now: f.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	bad := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "3"}}
	input := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "bad-rebind"), ID: created.Policy.ID, ExpectedVersion: 1, SLOID: &second.ID, Conditions: &bad, Reason: "Rebind", Limits: f.limits}
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, alert.ErrPolicy) && !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("new target did not reject impossible burn: %v", err)
	}
	input.AlertCommandInput = alertManagementCommand(f, "good-rebind")
	input.Conditions = nil
	updated, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil || updated.Policy.SLOID != second.ID || updated.Policy.EvaluationRevision != 2 {
		t.Fatalf("rebind: %+v %v", updated, err)
	}
	var oldSLO string
	if err := f.conn.QueryRow(t.Context(), `SELECT slo_id FROM almanac_alert_policy_revisions WHERE policy_id=$1 AND revision=1`, created.Policy.ID).Scan(&oldSLO); err != nil || oldSLO != f.sloID {
		t.Fatalf("old immutable binding changed: %s %v", oldSLO, err)
	}
}

func TestIntegrationAlertPolicyRevisionConcurrentCrossSourceRebind(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	secondSLI := newAlertID().String()
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO almanac_slis(id,name,display_name,created_at,updated_at) SELECT $2,'revision-second-sli','Second SLI',created_at,updated_at FROM almanac_slis WHERE id=$1`, f.sliID, secondSLI); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO almanac_sli_measurements(sli_id,revision,datasource_id,agent_id,measurement_kind,component_mode,good_query,total_query,effective_from,activated_at,created_at) SELECT $2,1,datasource_id,agent_id,measurement_kind,component_mode,good_query,total_query,effective_from,activated_at,created_at FROM almanac_sli_measurements WHERE sli_id=$1 AND revision=1`, f.sliID, secondSLI)
		return err
	})
	secondSLO := alertRevisionTarget(t, f, "other-source", secondSLI, 86400)
	first, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "cross-first"))
	if err != nil {
		t.Fatal(err)
	}
	input := alertManagementCreate(f, "cross-second")
	input.Name = "Other policy"
	input.SLOID = secondSLO
	second, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result       AlertCommandResult
		err          error
		sloID, sliID string
	}
	done := make(chan outcome, 2)
	for _, change := range []struct{ key, id, target, sliID string }{{"swap-first", first.Policy.ID, secondSLO, secondSLI}, {"swap-second", second.Policy.ID, f.sloID, f.sliID}} {
		go func() {
			result, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, change.key), ID: change.id, ExpectedVersion: 1, SLOID: &change.target, Reason: "Swap targets", Limits: f.limits})
			done <- outcome{result, err, change.target, change.sliID}
		}()
	}
	for range 2 {
		got := <-done
		if got.err != nil || got.result.Policy.SLOID != got.sloID || got.result.Policy.EvaluationRevision != 2 {
			t.Fatalf("cross-source rebind: %+v %v", got.result, got.err)
		}
		binding, err := dbgen.New(f.conn).ReadAlertBinding(t.Context(), mustPGUUID(got.result.Policy.ID))
		if err != nil || binding.SliID.String() != got.sliID {
			t.Fatalf("cross-source authority/binding mismatch: %+v %v", binding, err)
		}
	}
}

func TestIntegrationAlertPolicyRevisionCommandGuards(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "guards-create"))
	if err != nil {
		t.Fatal(err)
	}
	name := "Guarded metadata"
	input := UpdateAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "guarded-update"), ID: created.Policy.ID, ExpectedVersion: 1, Name: &name, Limits: f.limits}
	setIntegrationUserRole(t, f.f, "viewer")
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAgentForbidden) {
		t.Fatalf("viewer mutation: %v", err)
	}
	setIntegrationUserRole(t, f.f, "administrator")
	updated, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := ReadAlertCommand(t.Context(), f.f.databaseURL, ReadAlertCommandInput{Runtime: f.runtime, Proof: input.Proof, Operation: "alert-policy.update", IdempotencyKey: input.IdempotencyKey, Now: f.clock})
	if err != nil || !lookup.Found || lookup.Result.Policy.Version != updated.Policy.Version {
		t.Fatalf("command reconciliation: %+v %v", lookup, err)
	}
	changed := input
	changed.Name = nil
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, changed); !errors.Is(err, ErrAlertKeyConflict) {
		t.Fatalf("changed body replay: %v", err)
	}
	f.now = f.at.Add(time.Hour)
	if _, err := UpdateAlertPolicy(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertReceiptExpired) {
		t.Fatalf("expired replay: %v", err)
	}
	var version int64
	var audits int
	if err := f.conn.QueryRow(t.Context(), `SELECT version,(SELECT count(*) FROM almanac_audit_events WHERE action='alert-policy.updated') FROM almanac_alert_policies WHERE id=$1`, created.Policy.ID).Scan(&version, &audits); err != nil || version != 2 || audits != 1 {
		t.Fatalf("rejected command mutated state: version=%d audits=%d %v", version, audits, err)
	}
}

func TestIntegrationAlertPolicyRevisionEnableRetainsTerminalOccurrence(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	created, err := CreateAlertPolicy(t.Context(), f.f.databaseURL, alertManagementCreate(f, "terminal-create"))
	if err != nil {
		t.Fatal(err)
	}
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, func() time.Time { return f.at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(2 * time.Second)
	stopped, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "terminal-disable"), ID: created.Policy.ID, ExpectedVersion: 1, Reason: "Pause"})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(25*time.Minute + time.Second)
	enable := EnableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "terminal-enable"), ID: created.Policy.ID, ExpectedVersion: stopped.Policy.Version, Reason: "Resume", Limits: f.limits}
	resumed, err := EnableAlertPolicy(t.Context(), f.f.databaseURL, enable)
	if err != nil || resumed.Policy.EvaluationRevision != 2 || resumed.Alert != nil {
		t.Fatalf("enable reused occurrence: %+v %v", resumed, err)
	}
	current, err := ReadAlert(t.Context(), f.f.databaseURL, ReadAlertInput{Runtime: f.runtime, Proof: enable.Proof, ID: opened.Occurrence.ID, Now: f.clock})
	if err != nil || current.Occurrence.State != alert.Disabled || current.Version != stopped.Alert.Version || !current.Occurrence.ClosedAt.Equal(f.at.Add(2*time.Second)) {
		t.Fatalf("terminal occurrence reopened: %+v %v", current, err)
	}
	var start, end time.Time
	if err := f.conn.QueryRow(t.Context(), `SELECT starts_at,ends_at FROM almanac_alert_ranges WHERE policy_id=$1`, created.Policy.ID).Scan(&start, &end); err == nil {
		t.Fatalf("unexpected range when next work was not yet due: %s..%s", start, end)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	claims, err := f.runtime.claimAlertWork(t.Context(), f.now, f.now, time.Minute, 10)
	if err != nil || len(claims) != 0 {
		t.Fatalf("disabled interval replayed: %+v %v", claims, err)
	}
	f.now = f.at.Add(26 * time.Minute)
	claim = f.claim(t, f.now)
	if claim.Revision != 2 || !claim.ScheduledAt.Equal(f.now) {
		t.Fatalf("fresh activation: %+v", claim)
	}
}
