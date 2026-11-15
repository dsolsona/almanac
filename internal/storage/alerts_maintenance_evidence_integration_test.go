//go:build integration

package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationAlertMaintenanceEvidenceRetention(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Maintenance provenance")
	build := "cfd5bc95-7f07-497b-8abc-07c458112f31"
	for i := range 4 {
		f.now = f.at.Add(time.Duration(i) * time.Minute)
		claim := f.claim(t, f.now)
		input := f.input(t, claim, alert.Complete, false)
		for j := range input.Windows {
			w := &input.Windows[j]
			// Explicit complete genesis identity is legitimate and differs from the
			// implicit genesis endpoint, despite identical calculation outcomes.
			w.Maintenance = &alert.MaintenanceEvidence{}
			if i%2 == 0 {
				w.Maintenance.BuildID = build
			}
		}
		result, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, f.clock)
		if err != nil {
			t.Fatal(err)
		}
		row, err := dbgen.New(f.conn).ReadAlertEvaluation(t.Context(), mustPGUUID(result.EvaluationID))
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Input      alert.Input
			Evaluation alert.Evaluation
		}
		if err := unmarshalAlertData(row.Evidence, &saved); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(saved.Input.Windows[0].Maintenance, input.Windows[0].Maintenance) || !reflect.DeepEqual(saved.Evaluation.Conditions[0].Short.Maintenance, input.Windows[0].Maintenance) {
			t.Fatal("persisted provenance changed")
		}
	}
	f.now = f.at.Add(3 * 24 * time.Hour)
	if _, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 10), f.clock); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 99
	if _, err := IssueSession(t.Context(), f.f.databaseURL, IssueSessionInput{UserID: f.f.userID, ExpectedCredentialEpoch: f.f.epoch, ExpectedCredentialRevision: f.f.revision, Verifier: verifier, Now: f.now, RequestID: "maintenance-evidence-read"}); err != nil {
		t.Fatal(err)
	}
	page, err := ListAlertHistoryRanges(t.Context(), f.f.databaseURL, ListAlertRangesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, ID: policy, Now: f.clock}, Revision: 1, Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ranges=%+v error=%v", page, err)
	}
	s := page.Items[0].Summary
	if s == nil || len(s.Conditions) != 1 {
		t.Fatalf("summary=%+v", s)
	}
	c := s.Conditions[0]
	if c.FirstShortMaintenance == nil || c.LastLongMaintenance == nil || c.FirstShortMaintenance.BuildID != build || c.LastLongMaintenance.BuildID != build || !s.First.Cutoff.Equal(f.at) || !s.Last.Cutoff.Equal(f.at.Add(2*time.Minute)) {
		t.Fatalf("wrong actual endpoints %+v", s)
	}
	// The different middle build identity is intentionally not assigned to either
	// endpoint or represented as a fabricated numerical middle sample.
}
