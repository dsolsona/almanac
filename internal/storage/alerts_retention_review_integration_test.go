//go:build integration

package storage

import (
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationAlertRetentionOverlapBounds(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Range lookup")
	// Disjoint historical ranges, followed by a long gap and administrative range.
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_alert_ranges(id,policy_id,revision,starts_at,ends_at,cadence_seconds,kind,reason,recorded_at)
 SELECT gen_random_uuid(),$1,1,$2::timestamptz+i*interval '2 minutes',$2::timestamptz+i*interval '2 minutes'+interval '1 minute',60,'unreconstructed_gap','fixture',$2 FROM generate_series(0,999) i`, policy, f.at); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		start, end int
		kind       string
	}{{2000, 4000, "unreconstructed_gap"}, {4001, 5000, "administrative_cancellation"}} {
		if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_alert_ranges(id,policy_id,revision,starts_at,ends_at,cadence_seconds,kind,reason,recorded_at) VALUES(gen_random_uuid(),$1,1,$2,$3,60,$4,'fixture',$2)`, policy, f.at.Add(time.Duration(item.start)*time.Minute), f.at.Add(time.Duration(item.end)*time.Minute), item.kind); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name       string
		start, end int
		want       bool
	}{
		{"before first", -2, 0, false}, {"same range", 0, 1, true}, {"adjacent gap", 1, 2, false}, {"covers several", 1, 5, true},
		{"inside long gap", 3000, 3001, true}, {"after long gap", 4000, 4001, false}, {"inside administrative", 4500, 4501, true}, {"after last", 5000, 5001, false},
	} {
		got, err := dbgen.New(f.conn).AlertRangesOverlap(t.Context(), dbgen.AlertRangesOverlapParams{PolicyID: mustPGUUID(policy), Revision: 1, StartsAt: timestamp(f.at.Add(time.Duration(c.start) * time.Minute)), EndsAt: timestamp(f.at.Add(time.Duration(c.end) * time.Minute))})
		if err != nil || got != c.want {
			t.Fatalf("%s: overlap=%v want=%v error=%v", c.name, got, c.want, err)
		}
	}
	if _, err := f.conn.Exec(t.Context(), `ANALYZE almanac_alert_ranges`); err != nil {
		t.Fatal(err)
	}
	capture := &factsQueryCapture{}
	_, err := dbgen.New(capture).AlertRangesOverlap(t.Context(), dbgen.AlertRangesOverlapParams{PolicyID: mustPGUUID(policy), Revision: 1, StartsAt: timestamp(f.at.Add(5000 * time.Minute)), EndsAt: timestamp(f.at.Add(5001 * time.Minute))})
	if !errors.Is(err, errFactsQueryCaptured) {
		t.Fatal(err)
	}
	var raw []byte
	if err := f.conn.QueryRow(t.Context(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+capture.statement, capture.arguments...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan map[string]any }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatal(err)
	}
	var visits float64
	var inspect func(map[string]any)
	inspect = func(node map[string]any) {
		if node["Relation Name"] == "almanac_alert_ranges" {
			rows, _ := node["Actual Rows"].(float64)
			removed, _ := node["Rows Removed by Filter"].(float64)
			loops, _ := node["Actual Loops"].(float64)
			visits += (rows + removed) * loops
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			inspect(child.(map[string]any))
		}
	}
	inspect(plans[0].Plan)
	t.Logf("actual overlap plan: %s", raw)
	if visits > 1 {
		t.Fatalf("absence proof scanned historical ranges: %.0f row visits, want at most one predecessor", visits)
	}
}

func TestIntegrationAlertRetentionSummaryRead(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	seedAlertRetentionClear(t, f, 4, 0)
	var policy string
	if err := f.conn.QueryRow(t.Context(), `SELECT id::text FROM almanac_alert_policies`).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(3 * 24 * time.Hour)
	if _, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 10), f.clock); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 99
	if _, err := IssueSession(t.Context(), f.f.databaseURL, IssueSessionInput{UserID: f.f.userID, ExpectedCredentialEpoch: f.f.epoch, ExpectedCredentialRevision: f.f.revision, Verifier: verifier, Now: f.now, RequestID: "retention-read-session"}); err != nil {
		t.Fatal(err)
	}
	input := ListAlertRangesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, ID: policy, Now: f.clock}, Revision: 1, Limit: 10}
	page, err := ListAlertHistoryRanges(t.Context(), f.f.databaseURL, input)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("read compacted range: %+v %v", page, err)
	}
	body, err := marshalAlertData(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Summary *struct {
			FormatVersion int
			First, Last   alert.SnapshotIdentity
			Truth         alert.Truth
			Conditions    []alert.ConditionCompactionSummary
		}
	}
	if err := unmarshalAlertData(body, &record); err != nil {
		t.Fatal(err)
	}
	s := record.Summary
	if s == nil || s.FormatVersion != 1 || s.Truth != alert.Clear || len(s.Conditions) != 1 || s.Conditions[0].ID != "fast" || s.Conditions[0].ShortDuration != 5*time.Minute || s.Conditions[0].LongDuration != time.Hour || s.Conditions[0].Outcome != alert.NotMatching || !s.First.Cutoff.Equal(f.at) || !s.Last.Cutoff.Equal(f.at.Add(2*time.Minute)) || s.First.SLOID != f.sloID || s.Last.SLOID != f.sloID {
		t.Fatalf("lost exact compacted outcome/provenance: %s", body)
	}
	if strings.Contains(string(body), `"Good"`) || strings.Contains(string(body), `"Total"`) {
		t.Fatalf("invented compacted numerical evidence: %s", body)
	}
	// Corruption is an append-only fixture: never disable immutable-history guards.
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_alert_ranges(id,policy_id,revision,starts_at,ends_at,cadence_seconds,kind,reason,recorded_at,summary)
 SELECT gen_random_uuid(),policy_id,revision,starts_at+interval '10 minutes',ends_at+interval '10 minutes',cadence_seconds,kind,reason,recorded_at,jsonb_set(summary,'{Truth}','"breaching"'::jsonb)
 FROM almanac_alert_ranges WHERE policy_id=$1 AND kind='compacted_evaluations'`, policy); err != nil {
		t.Fatal(err)
	}
	if got, err := ListAlertHistoryRanges(t.Context(), f.f.databaseURL, input); !errors.Is(err, ErrAlertInvalid) || len(got.Items) != 0 {
		t.Fatalf("malformed persisted summary accepted or partial page leaked: %+v %v", got, err)
	}

}

func TestIntegrationAlertRetentionRepeatedRangePin(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Repeated cutoff evidence")
	claim := f.claim(t, f.at)
	first, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, false), f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Minute)
	claim = f.claim(t, f.now)
	if err := f.runtime.recordAlertRepeatedCutoff(t.Context(), claim, f.at, f.clock); err != nil {
		t.Fatal(err)
	}
	for i := 2; i < 7; i++ {
		f.now = f.at.Add(time.Duration(i) * time.Minute)
		claim = f.claim(t, f.now)
		if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, false), f.limits, f.clock); err != nil {
			t.Fatal(err)
		}
	}
	f.now = f.at.Add(3 * 24 * time.Hour)
	result, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 10), f.clock)
	if err != nil || result.CompactedEvaluations != 4 {
		t.Fatalf("compaction around repeated range: %+v %v", result, err)
	}
	var pinned bool
	if err := f.conn.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM almanac_alert_evaluations e JOIN almanac_alert_ranges r ON r.reference_evaluation_id=e.id WHERE e.id=$1 AND r.kind='repeated_cutoff')`, first.EvaluationID).Scan(&pinned); err != nil || !pinned {
		t.Fatalf("repeated cutoff lost original evidence: %v %v", pinned, err)
	}
}
