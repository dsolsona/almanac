//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func alertCatalogInput(f alertTestFixture) ListAlertsInput {
	command := alertManagementCommand(f, "catalog-read")
	return ListAlertsInput{Runtime: f.runtime, Proof: command.Proof, Now: f.clock, Limit: 2, Filter: AlertFilter{Scope: "active"}, GroupBy: "none", Sort: "newest"}
}

func TestIntegrationAlertCatalogPaginationAndGrouping(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	for _, name := range []string{"A_0", "A1", "A2"} {
		f.policy(t, name)
	}
	definition, err := dbgen.New(f.conn).GetSlo(t.Context(), mustPGUUID(f.sloID))
	if err != nil {
		t.Fatal(err)
	}
	proof := alertManagementCommand(f, "second-slo").Proof
	_, second, err := CreateObjective(t.Context(), f.f.databaseURL, CreateObjectiveInput{Proof: proof, Name: "catalog-second", DisplayName: "Second SLO", ServiceID: definition.ServiceID.String(), SliID: f.sliID, Target: "0.99", RollingWindowSeconds: 3600, EvaluationValidFrom: &f.historyStart, IdempotencyKey: "second-slo", RequestID: "second-slo", Now: f.clock}, true)
	if err != nil {
		t.Fatal(err)
	}
	f.sloID = second.ID
	for _, name := range []string{"100% burn", "B1", "B2"} {
		f.policy(t, name)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_alert_policies SET severity=CASE name WHEN 'A1' THEN 'low' WHEN 'B1' THEN 'medium' ELSE severity END`); err != nil {
		t.Fatal(err)
	}
	f.drain(t, "catalog counts")
	if got, err := f.runtime.processAlertTick(t.Context(), f.at, alertProcessorTestOptions(f), f.clock); err != nil || got.Evaluations != 6 {
		t.Fatalf("seed alerts: %+v %v", got, err)
	}
	input := alertCatalogInput(f)
	seen := map[string]bool{}
	for pageNumber := range 4 {
		page, err := ListAlerts(t.Context(), f.f.databaseURL, input)
		if err != nil || page.Total != 6 || len(page.Items) > 2 {
			t.Fatalf("global list page %d (cursor length %d): %+v %v", pageNumber, len(input.Cursor), page, err)
		}
		for _, item := range page.Items {
			if seen[item.Alert.Occurrence.ID] {
				t.Fatal("duplicate paginated occurrence")
			}
			seen[item.Alert.Occurrence.ID] = true
			if item.Evaluation.Truth != alert.Breaching || len(item.Evaluation.Conditions) != 1 || item.SLOID == "" || item.PolicyName == "" {
				t.Fatalf("row lacks actual reason/link: %+v", item)
			}
		}
		if page.NextCursor == "" {
			break
		}
		input.Cursor = page.NextCursor
	}
	if len(seen) != 6 {
		t.Fatalf("lost rows: %d", len(seen))
	}
	input = alertCatalogInput(f)
	input.GroupBy = "slo"
	page, err := ListAlerts(t.Context(), f.f.databaseURL, input)
	if err != nil || page.Total != 6 || len(page.Groups) != 1 || page.Groups[0].Total != 3 || page.Items[0].SLOID != page.Items[1].SLOID {
		t.Fatalf("group count is page-local or split source grouping: %+v %v", page, err)
	}
	input.Cursor = page.NextCursor
	input.Filter.SLOID = f.sloID
	if _, err := ListAlerts(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("cursor reused with changed filter: %v", err)
	}
	input = alertCatalogInput(f)
	input.GroupBy, input.Sort = "slo", "severity"
	grouped := map[string]bool{}
	groupSizes := map[string]int{}
	priority := map[string]int{"high": 0, "medium": 1, "low": 2}
	var previousSLO string
	previousPriority := -1
	for pageNumber := range 3 {
		page, err := ListAlerts(t.Context(), f.f.databaseURL, input)
		if err != nil || page.Total != 6 || len(page.Items) != 2 {
			t.Fatalf("grouped severity page %d: %+v %v", pageNumber, page, err)
		}
		pageSLOs := map[string]bool{}
		for _, item := range page.Items {
			id := item.Alert.Occurrence.ID
			if grouped[id] || !seen[id] {
				t.Fatalf("grouped pagination duplicated or invented occurrence %s", id)
			}
			grouped[id] = true
			if item.SLOID < previousSLO || item.SLOID == previousSLO && priority[item.Severity] < previousPriority {
				t.Fatalf("grouped severity order regressed: previous=%s/%d current=%s/%s", previousSLO, previousPriority, item.SLOID, item.Severity)
			}
			previousSLO, previousPriority = item.SLOID, priority[item.Severity]
			groupSizes[item.SLOID]++
			pageSLOs[item.SLOID] = true
		}
		if len(page.Groups) != len(pageSLOs) || pageNumber == 1 && len(page.Groups) != 2 {
			t.Fatalf("cross-group page lost group metadata: %+v", page)
		}
		for _, group := range page.Groups {
			if !pageSLOs[group.SLOID] || group.Total != 3 {
				t.Fatalf("group total is not global filtered total: %+v", group)
			}
			delete(pageSLOs, group.SLOID)
		}
		if pageNumber < 2 && page.NextCursor == "" || pageNumber == 2 && page.NextCursor != "" {
			t.Fatalf("incorrect grouped continuation on page %d: %q", pageNumber, page.NextCursor)
		}
		input.Cursor = page.NextCursor
	}
	if len(grouped) != 6 || len(groupSizes) != 2 {
		t.Fatalf("grouped pagination omitted rows: occurrences=%d groups=%v", len(grouped), groupSizes)
	}
	for id, count := range groupSizes {
		if count != 3 {
			t.Fatalf("SLO %s lost occurrences: %d", id, count)
		}
	}
	for _, search := range []string{"%", "_"} {
		input = alertCatalogInput(f)
		input.Filter.Search = search
		page, err := ListAlerts(t.Context(), f.f.databaseURL, input)
		if err != nil || page.Total != 1 || len(page.Items) != 1 {
			t.Fatalf("literal search %q became wildcard: %+v %v", search, page, err)
		}
	}
	input = alertCatalogInput(f)
	input.Filter.SLOID = f.sloID
	input.Filter.Severities = []string{"medium"}
	page, err = ListAlerts(t.Context(), f.f.databaseURL, input)
	if err != nil || page.Total != 1 || page.Items[0].PolicyName != "B1" {
		t.Fatalf("combined column filters: %+v %v", page, err)
	}
	input = alertCatalogInput(f)
	input.Sort = "severity"
	input.Limit = 20
	page, err = ListAlerts(t.Context(), f.f.databaseURL, input)
	if err != nil || page.Total != 6 || page.Items[4].Severity != "medium" || page.Items[5].Severity != "low" {
		t.Fatalf("global severity sort: %+v %v", page, err)
	}
	input = alertCatalogInput(f)
	input.Limit = 1
	first, err := ListAlerts(t.Context(), f.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Cursor = first.NextCursor
	input.Limit = 20
	resized, err := ListAlerts(t.Context(), f.f.databaseURL, input)
	if err != nil || resized.Total != 6 || len(resized.Items) != 5 {
		t.Fatalf("page size invalidated cursor: %+v %v", resized, err)
	}
	input.Cursor = "not-a-valid-cursor"
	if _, err := ListAlerts(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("malformed cursor accepted: %v", err)
	}
}

func TestIntegrationAlertCatalogLifecycleAndRanges(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "History burn")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Second)
	if _, err := AcknowledgeAlert(t.Context(), f.f.databaseURL, AcknowledgeAlertInput{AlertCommandInput: alertManagementCommand(f, "history-ack"), ID: opened.Occurrence.ID, ExpectedVersion: opened.Version}); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Minute)
	claim = f.claim(t, f.now)
	if err := f.runtime.recordAlertRepeatedCutoff(t.Context(), claim, f.at, f.clock); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(2 * time.Minute)
	claim = f.claim(t, f.now)
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, false), f.limits, f.clock); err != nil {
		t.Fatal(err)
	}
	input := alertCatalogInput(f)
	input.Filter.Scope = "history"
	page, err := ListAlerts(t.Context(), f.f.databaseURL, input)
	if err != nil || page.Total != 1 || page.Items[0].Alert.Occurrence.State != alert.Resolved {
		t.Fatalf("history lifecycle: %+v %v", page, err)
	}
	transitions := ListAlertTransitionsInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: opened.Occurrence.ID, Now: f.clock}, Limit: 1}
	var sequence int64
	for range 4 {
		page, err := ListAlertTransitions(t.Context(), f.f.databaseURL, transitions)
		if err != nil || len(page.Items) != 1 || page.Items[0].Sequence != sequence+1 {
			t.Fatalf("transition sequence: %+v %v", page, err)
		}
		sequence = page.Items[0].Sequence
		if sequence == 2 && (page.Items[0].ActorID != f.f.userID || page.Items[0].RequestID != "history-ack") {
			t.Fatalf("lost ack provenance: %+v", page.Items[0])
		}
		if page.NextCursor == "" {
			break
		}
		transitions.Cursor = page.NextCursor
	}
	if sequence != 3 {
		t.Fatalf("missing transitions: %d", sequence)
	}
	ranges, err := ListAlertHistoryRanges(t.Context(), f.f.databaseURL, ListAlertRangesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: policy, Now: f.clock}, Revision: 1, Limit: 2})
	if err != nil || len(ranges.Items) != 1 || ranges.Items[0].Kind != "repeated_cutoff" || !ranges.Items[0].Start.Equal(f.at.Add(time.Minute)) || ranges.Items[0].EvaluationID != opened.EvaluationID {
		t.Fatalf("missing exact range: %+v %v", ranges, err)
	}
	if _, err := ListAlertHistoryRanges(t.Context(), f.f.databaseURL, ListAlertRangesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: policy, Now: f.clock}, Revision: 2, Limit: 2}); !errors.Is(err, ErrAlertNotFound) {
		t.Fatalf("unknown revision appears quiet: %v", err)
	}
}

func TestIntegrationAlertCatalogProcessingHealth(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Waiting burn")
	if got, err := f.runtime.processAlertTick(t.Context(), f.at, alertProcessorTestOptions(f), f.clock); err != nil || got.Pending != 1 {
		t.Fatalf("seed pending: %+v %v", got, err)
	}
	f.now = f.at.Add(30 * time.Second)
	input := alertCatalogInput(f)
	processing, err := ReadAlertProcessing(t.Context(), f.f.databaseURL, ReadAlertInput{Runtime: f.runtime, Proof: input.Proof, ID: policy, Now: f.clock})
	if err != nil || processing.WaitingReason != "aggregate_pending" || processing.LagSeconds != 30 || processing.LastCutoff != nil || processing.PendingCutoff == nil || !processing.PendingCutoff.Equal(f.at) {
		t.Fatalf("waiting appeared completed: %+v %v", processing, err)
	}
	page, err := ListAlerts(t.Context(), f.f.databaseURL, input)
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("pending fabricated lifecycle row: %+v %v", page, err)
	}
}

func TestIntegrationAlertCatalogPoliciesBeforeFiring(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Saved one")
	f.policy(t, "Saved two")
	disabled := f.policy(t, "Disabled policy")
	if _, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "disable-saved"), ID: disabled, ExpectedVersion: 1, EffectiveAt: f.at, Reason: "No longer needed"}); err != nil {
		t.Fatal(err)
	}
	input := ListAlertPoliciesInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "policy-list").Proof, Now: f.clock, Limit: 2}
	page, err := ListAlertPolicies(t.Context(), f.f.databaseURL, input)
	if err != nil || page.Total != 3 || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("saved policy list: %+v %v", page, err)
	}
	for _, item := range page.Items {
		if item.OpenOccurrenceID != "" || item.Processing.LastCutoff != nil {
			t.Fatalf("saving fabricated evaluation: %+v", item)
		}
	}
	input.Cursor = page.NextCursor
	last, err := ListAlertPolicies(t.Context(), f.f.databaseURL, input)
	if err != nil || last.Total != 3 || len(last.Items) != 1 || last.NextCursor != "" {
		t.Fatalf("policy cursor: %+v %v", last, err)
	}
	input.Cursor = ""
	input.Enabled = new(false)
	page, err = ListAlertPolicies(t.Context(), f.f.databaseURL, input)
	if err != nil || page.Total != 1 || page.Items[0].Policy.ID != disabled || page.Items[0].Policy.Enabled || page.Items[0].Processing.Schedulable {
		t.Fatalf("disabled policy filter: %+v %v", page, err)
	}
}

func TestIntegrationAlertCatalogReadAuthority(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Viewer catalogue")
	f.drain(t, "viewer catalogue")
	if got, err := f.runtime.processAlertTick(t.Context(), f.at, alertProcessorTestOptions(f), f.clock); err != nil || got.Evaluations != 1 {
		t.Fatalf("seed catalogue: %+v %v", got, err)
	}
	input := alertCatalogInput(f)
	policies := ListAlertPoliciesInput{Runtime: f.runtime, Proof: input.Proof, Now: f.clock, Limit: 2}
	setIntegrationUserRole(t, f.f, "viewer")
	page, err := ListAlerts(t.Context(), f.f.databaseURL, input)
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("viewer cannot read occurrence catalogue: %+v %v", page, err)
	}
	saved, err := ListAlertPolicies(t.Context(), f.f.databaseURL, policies)
	if err != nil || saved.Total != 1 || len(saved.Items) != 1 {
		t.Fatalf("viewer cannot read policy catalogue: %+v %v", saved, err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sessions SET restricted=true WHERE verifier=$1`, input.Proof.SessionVerifier[:]); err != nil {
		t.Fatal(err)
	}
	page, err = ListAlerts(t.Context(), f.f.databaseURL, input)
	if !errors.Is(err, ErrAgentForbidden) || len(page.Items) != 0 || page.Total != 0 {
		t.Fatalf("restricted session received occurrence catalogue: %+v %v", page, err)
	}
	saved, err = ListAlertPolicies(t.Context(), f.f.databaseURL, policies)
	if !errors.Is(err, ErrAgentForbidden) || len(saved.Items) != 0 || saved.Total != 0 {
		t.Fatalf("restricted session received policy catalogue: %+v %v", saved, err)
	}
	if _, err := f.conn.Exec(t.Context(), `UPDATE almanac_sessions SET restricted=false WHERE verifier=$1`, input.Proof.SessionVerifier[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := RevokeSession(t.Context(), f.f.databaseURL, input.Proof.SessionVerifier, f.now, "revoke-catalogue"); err != nil {
		t.Fatal(err)
	}
	page, err = ListAlerts(t.Context(), f.f.databaseURL, input)
	if !errors.Is(err, ErrInvalidSession) || len(page.Items) != 0 || page.Total != 0 {
		t.Fatalf("revoked session received occurrence catalogue: %+v %v", page, err)
	}
	saved, err = ListAlertPolicies(t.Context(), f.f.databaseURL, policies)
	if !errors.Is(err, ErrInvalidSession) || len(saved.Items) != 0 || saved.Total != 0 {
		t.Fatalf("revoked session received policy catalogue: %+v %v", saved, err)
	}
}
