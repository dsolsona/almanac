//go:build integration

package storage

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationAlertSeriesReducerNormalizedEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"zero traffic", "warmup", "expired overrides warmup", "equivalent time zones"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			policy := uuid.New().String()
			coldDuration := 2 * time.Hour
			if mode == "warmup" || mode == "expired overrides warmup" {
				coldDuration = 30 * 24 * time.Hour
			}
			conditions := []alert.Condition{{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "1"}, {ID: "cold", Short: 5 * time.Minute, Long: coldDuration, Threshold: "1"}}
			alertTestTx(t, f.conn, func(tx pgx.Tx) error {
				return registerAlertPolicy(t.Context(), tx, alertRegistration{Policy: alert.PolicyRevision{ID: policy, Revision: 1, Conditions: conditions}, SLOID: f.sloID, Name: "Normalized series", Severity: "high", FirstBoundary: f.at, Now: f.now, Limits: f.limits})
			})
			var occurrence string
			for i := range 2 {
				f.now = f.at.Add(time.Duration(i) * time.Minute)
				claim := f.claim(t, f.now)
				input := f.input(t, claim, alert.Complete, true)
				cold := input.Windows[0]
				cold.Start = cold.End.Add(-coldDuration)
				input.Windows = append(input.Windows, cold)
				if i == 1 {
					switch mode {
					case "zero traffic":
						for j := range input.Windows {
							input.Windows[j].Good, input.Windows[j].Total = "0", "0"
						}
					case "expired overrides warmup":
						input.Objective.RetentionStart = f.now.Add(-time.Minute)
					case "equivalent time zones":
						zone := time.FixedZone("accepted offset", 3600)
						for j := range input.Windows {
							input.Windows[j].Snapshot.Cutoff = input.Windows[j].Snapshot.Cutoff.In(zone)
							input.Windows[j].Start = input.Windows[j].Start.In(zone)
							input.Windows[j].End = input.Windows[j].End.In(zone)
						}
					}
				}
				// Feed raw complete evidence through the real reducer/journal. The
				// stored result must differ from raw input only where specified.
				got, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, f.clock)
				if err != nil || got.Occurrence == nil {
					t.Fatalf("commit raw evidence: %+v %v", got, err)
				}
				occurrence = got.Occurrence.ID
			}
			condition, quality, reason, good := "fast", alert.Complete, "", "9800"
			switch mode {
			case "zero traffic":
				quality, reason, good = alert.ZeroTraffic, "zero_traffic", "0"
			case "warmup":
				condition, quality, reason = "cold", alert.Warmup, "full_window_warmup"
			case "expired overrides warmup":
				condition, quality, reason = "cold", alert.Expired, "full_window_not_retained"
			}
			request := ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "normalized").Proof, ID: occurrence, Now: f.clock}, ConditionID: condition, Start: f.now, End: f.now, Limit: 120}
			got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Points) != 1 || got.Points[0].Condition.Long.Quality != quality || got.Points[0].Condition.Long.Reason != reason || got.Points[0].Condition.Long.Good != good {
				t.Fatalf("normalized immutable evidence lost: %+v", got.Points)
			}
		})
	}
}

func TestIntegrationAlertSeriesDelayedScheduledRepeats(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	cutoff := f.at
	f.at, f.now = cutoff.Add(10*time.Minute), cutoff.Add(10*time.Minute)
	f.policy(t, "Delayed series")
	var occurrence string
	for i := range 3 {
		f.now = f.at.Add(time.Duration(i) * time.Minute)
		claim := f.claim(t, f.now)
		if i == 1 {
			if err := f.runtime.recordAlertRepeatedCutoff(t.Context(), claim, cutoff, f.clock); err != nil {
				t.Fatal(err)
			}
			continue
		}
		input := f.input(t, claim, alert.Complete, true)
		input.Snapshot.Cutoff = cutoff.Add(time.Duration(i) * time.Minute)
		for j := range input.Windows {
			duration := input.Windows[j].End.Sub(input.Windows[j].Start)
			input.Windows[j].Snapshot = input.Snapshot
			input.Windows[j].End = input.Snapshot.Cutoff
			input.Windows[j].Start = input.Snapshot.Cutoff.Add(-duration)
		}
		got, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, f.clock)
		if err != nil || got.Occurrence == nil {
			t.Fatalf("commit delayed: %+v %v", got, err)
		}
		occurrence = got.Occurrence.ID
	}
	request := ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "delayed").Proof, ID: occurrence, Now: f.clock}, ConditionID: "fast", Start: cutoff, End: cutoff.Add(2 * time.Minute), Limit: 120}
	got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil || len(got.Points) != 2 {
		t.Fatalf("delayed series: %+v %v", got, err)
	}
	if slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool { return g.Kind == "not_retained" && g.Basis == "effective_cutoff" }) {
		t.Fatalf("proved repeated cutoff became missing: %+v", got.Gaps)
	}
	if !slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool {
		return g.Kind == "repeated_cutoff" && g.Basis == "scheduled_boundary" && g.Start.Equal(f.at.Add(time.Minute)) && g.End.Equal(f.at.Add(2*time.Minute))
	}) {
		t.Fatalf("scheduled provenance omitted: %+v", got.Gaps)
	}
}

func TestIntegrationAlertSeriesEndpointsAndImmutableEvidence(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policyThreshold(t, "Series spike", "3")
	var occurrence string
	var ids []string
	for i, good := range []string{"9900", "9500", "9900"} {
		f.now = f.at.Add(time.Duration(i) * time.Minute)
		claim := f.claim(t, f.now)
		input := f.input(t, claim, alert.Complete, false)
		for j := range input.Windows {
			input.Windows[j].Good = good
		}
		got, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, f.clock)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, got.EvaluationID)
		if got.Occurrence != nil {
			occurrence = got.Occurrence.ID
		}
	}
	request := ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "series").Proof, ID: occurrence, Now: f.clock}, ConditionID: "fast", Start: f.at, End: f.now, Limit: 2}
	got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 2 || got.Points[0].EvaluationID != ids[0] || got.Points[1].EvaluationID != ids[2] || !got.Sampling.OmittedExtrema || !got.Sampling.Reduced || got.Sampling.Total != 3 {
		t.Fatalf("spike sampling: %+v", got)
	}
	if got.Points[0].Condition.Short.Good != "9900" || got.Points[1].Condition.Long.Good != "9900" || got.Objective.Target != "0.990000000000" {
		t.Fatalf("immutable values changed: %+v", got)
	}
	request.Limit = 3
	got, err = ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil || len(got.Points) != 3 || got.Points[1].EvaluationID != ids[1] || got.Sampling.OmittedExtrema || got.Sampling.Reduced {
		t.Fatalf("full exact series: %+v %v", got, err)
	}
	setIntegrationUserRole(t, f.f, "viewer")
	if _, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request); err != nil {
		t.Fatalf("viewer: %v", err)
	}
	request.ConditionID = "absent"
	if _, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("unknown condition: %v", err)
	}
	request.ConditionID = "fast"
	request.End = request.Start.Add(24*time.Hour + time.Nanosecond)
	if _, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request); !errors.Is(err, ErrAlertInvalid) {
		t.Fatalf("oversize: %v", err)
	}
	request.Start, request.End = time.Time{}, time.Time{}
	got, err = ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil || !got.End.Equal(f.now) || !got.Start.Equal(f.now.Add(-2*time.Hour)) {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	if _, err := RevokeSession(t.Context(), f.f.databaseURL, request.Proof.SessionVerifier, f.now, "series-revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked read: %v", err)
	}
}

func TestIntegrationAlertSeriesConditionQualityAndWorkAxes(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := uuid.New().String()
	conditions := []alert.Condition{{ID: "first", Short: 5 * time.Minute, Long: time.Hour, Threshold: "1"}, {ID: "second", Short: 5 * time.Minute, Long: time.Hour, Threshold: "2"}, {ID: "third", Short: 10 * time.Minute, Long: 2 * time.Hour, Threshold: "3"}}
	alertTestTx(t, f.conn, func(tx pgx.Tx) error {
		return registerAlertPolicy(t.Context(), tx, alertRegistration{Policy: alert.PolicyRevision{ID: policy, Revision: 1, Conditions: conditions}, SLOID: f.sloID, Name: "Three conditions", Severity: "high", FirstBoundary: f.at, Now: f.now, Limits: f.limits})
	})
	var occurrence string
	var firstID string
	for i := range 4 {
		f.now = f.at.Add(time.Duration(i) * time.Minute)
		claim := f.claim(t, f.now)
		if i == 1 {
			if err := f.runtime.recordAlertRepeatedCutoff(t.Context(), claim, f.at, f.clock); err != nil {
				t.Fatal(err)
			}
			continue
		}
		input := f.input(t, claim, alert.Complete, true)
		for _, d := range []time.Duration{10 * time.Minute, 2 * time.Hour} {
			w := input.Windows[0]
			w.Start = w.End.Add(-d)
			w.Good = "9500"
			if i == 2 {
				w.Quality = alert.Partial
			}
			if i == 3 {
				w.Quality = alert.Missing
				w.Good = ""
				w.Total = ""
			}
			input.Windows = append(input.Windows, w)
		}
		got, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, f.clock)
		if err != nil {
			t.Fatal(err)
		}
		occurrence = got.Occurrence.ID
		if i == 0 {
			firstID = got.EvaluationID
		}
	}
	request := ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "series").Proof, ID: occurrence, Now: f.clock}, ConditionID: "third", Start: f.at.Add(-time.Minute), End: f.now.Add(2 * time.Minute), Limit: 2}
	got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil {
		t.Fatal(err)
	}
	if got.Condition.ID != "third" || got.Condition.Long != 2*time.Hour || got.Sampling.Total != 3 || len(got.Points) != 2 || got.Points[0].EvaluationID != firstID || got.Points[1].Condition.Long.Quality != alert.Missing || got.Points[1].Condition.Long.Good != "" {
		t.Fatalf("selected paired condition/evidence: %+v", got)
	}
	for _, want := range []struct{ kind, basis string }{{"repeated_cutoff", "scheduled_boundary"}, {"outside_revision", "scheduled_boundary"}, {"pending", "scheduled_boundary"}, {"unavailable", "effective_cutoff"}} {
		if !slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool { return g.Kind == want.kind && g.Basis == want.basis }) {
			t.Fatalf("missing %+v gap: %+v", want, got.Gaps)
		}
	}
	if !slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool { return g.Kind == "unavailable" && g.Start.Equal(f.at.Add(2*time.Minute)) }) {
		t.Fatalf("sampled-out partial quality lost: %+v", got.Gaps)
	}
	request.Start = f.at.Add(time.Nanosecond)
	request.End = f.at.Add(3 * time.Minute)
	request.Limit = 120
	got, err = ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil || len(got.Points) != 2 || got.Points[0].EvaluationID == firstID {
		t.Fatalf("inclusive exact requested bounds: %+v %v", got, err)
	}
	request.Start = f.at.Add(2*time.Minute - time.Nanosecond)
	got, err = ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil || !slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool { return g.Kind == "repeated_cutoff" }) {
		t.Fatalf("sub-microsecond range overlap lost: %+v %v", got.Gaps, err)
	}

}

func TestIntegrationAlertSeriesCompactedInterior(t *testing.T) {
	t.Parallel()
	for _, delay := range []time.Duration{0, 10 * time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			t.Parallel()
			f := newAlertTestFixture(t)
			cutoffStart := f.at.Add(-delay)
			f.policy(t, "Compacted series")
			var occurrence string
			for i := range 8 {
				f.now = f.at.Add(time.Duration(i) * time.Minute)
				claim := f.claim(t, f.now)
				input := f.input(t, claim, alert.Complete, i == 0)
				input.Snapshot.Cutoff = input.Snapshot.Cutoff.Add(-delay)
				for j := range input.Windows {
					input.Windows[j].Snapshot = input.Snapshot
					input.Windows[j].Start = input.Windows[j].Start.Add(-delay)
					input.Windows[j].End = input.Windows[j].End.Add(-delay)
				}
				got, err := f.runtime.commitAlertEvaluation(t.Context(), claim, input, f.limits, f.clock)
				if err != nil {
					t.Fatal(err)
				}
				if got.Occurrence != nil {
					occurrence = got.Occurrence.ID
				}
			}
			f.now = f.at.Add(3 * 24 * time.Hour)
			compacted, err := f.runtime.maintainAlertEvidence(t.Context(), alertRetentionTestOptions(f.now, 20), f.clock)
			if err != nil || compacted.CompactedEvaluations == 0 {
				t.Fatalf("fixture compaction: %+v %v", compacted, err)
			}
			var verifier [32]byte
			verifier[0] = 99
			if _, err := IssueSession(t.Context(), f.f.databaseURL, IssueSessionInput{UserID: f.f.userID, ExpectedCredentialEpoch: f.f.epoch, ExpectedCredentialRevision: f.f.revision, Verifier: verifier, Now: f.now, RequestID: "series-history"}); err != nil {
				t.Fatal(err)
			}
			got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, ID: occurrence, Now: f.clock}, ConditionID: "fast", Start: cutoffStart, End: cutoffStart.Add(7 * time.Minute), Limit: 120})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool {
				return g.Kind == "compacted_evaluations" && g.Basis == "effective_cutoff" && g.RangeID != "" && g.Start.Equal(cutoffStart.Add(2*time.Minute)) && g.End.Equal(cutoffStart.Add(6*time.Minute))
			}) {
				t.Fatalf("compacted cutoff gap: %+v", got.Gaps)
			}
			if len(got.Points) != 3 || !got.Points[0].Snapshot.Cutoff.Equal(cutoffStart) || !got.Points[1].Snapshot.Cutoff.Equal(cutoffStart.Add(time.Minute)) || !got.Points[2].Snapshot.Cutoff.Equal(cutoffStart.Add(7*time.Minute)) {
				t.Fatalf("pinned history changed: %+v", got.Points)
			}
		})
	}
}

func TestIntegrationAlertSeriesMissingInteriorAndCancellation(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Skipped series")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(10 * time.Minute)
	claim = f.claim(t, f.now)
	skipped, err := f.runtime.recordAlertGap(t.Context(), claim, f.clock, 2*time.Minute)
	if err != nil || !skipped {
		t.Fatalf("gap: %v %v", skipped, err)
	}
	// Gap commit has dispatched this tick; the next deterministic turn is later.
	f.now = f.now.Add(time.Second)
	claim = f.claim(t, f.now)
	if _, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock); err != nil {
		t.Fatal(err)
	}
	request := ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "series").Proof, ID: opened.Occurrence.ID, Now: f.clock}, ConditionID: "fast", Start: f.at, End: f.at.Add(8 * time.Minute), Limit: 120}
	got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool {
		return g.Basis == "effective_cutoff" && g.Kind == "not_retained" && g.Start.Equal(f.at) && g.End.Equal(f.at.Add(8*time.Minute)) && !g.StartInclusive && !g.EndInclusive
	}) {
		t.Fatalf("line bridges unevaluated interior: %+v", got.Gaps)
	}
	f.now = f.at.Add(11 * time.Minute)
	if _, err := DisableAlertPolicy(t.Context(), f.f.databaseURL, DisableAlertPolicyInput{AlertCommandInput: alertManagementCommand(f, "series-disable"), ID: policy, Reason: "Retired", ExpectedVersion: 1, EffectiveAt: f.now}); err != nil {
		t.Fatal(err)
	}
	request.End = f.at.Add(13 * time.Minute)
	got, err = ReadAlertSeries(t.Context(), f.f.databaseURL, request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool {
		return g.Kind == "administrative_cancellation" && g.Basis == "scheduled_boundary"
	}) || !slices.ContainsFunc(got.Gaps, func(g AlertSeriesGap) bool { return g.Kind == "outside_revision" && g.Reason == "after_inactivation" }) {
		t.Fatalf("cancelled/future work confused: %+v", got.Gaps)
	}
}

func TestIntegrationAlertSeriesIgnoresUnrelatedOldRanges(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Old range isolation")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_alert_ranges(id,policy_id,revision,starts_at,ends_at,cadence_seconds,kind,reason,recorded_at)
 SELECT gen_random_uuid(),$1,1,$2::timestamptz-i*interval '2 minutes',$2::timestamptz-i*interval '2 minutes'+interval '1 minute',60,'unreconstructed_gap','old_fixture',$2 FROM generate_series(1000,3000) i`, policy, f.at); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "series").Proof, ID: opened.Occurrence.ID, Now: f.clock}, ConditionID: "fast", Start: f.at, End: f.at, Limit: 2})
	if err != nil || len(got.Points) != 1 || len(got.Gaps) != 0 {
		t.Fatalf("unrelated old ranges blocked exact point: %+v %v", got, err)
	}
}

func TestIntegrationAlertSeriesRejectsCorruptInteriorEvidence(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	f.policy(t, "Series integrity")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.at.Add(time.Minute)
	claim = f.claim(t, f.now)
	input := f.input(t, claim, alert.Complete, true)
	evaluation, err := alert.Evaluate(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := marshalAlertData(struct {
		FormatVersion int
		Input         alert.Input
		Evaluation    alert.Evaluation
	}{1, input, evaluation})
	if err != nil {
		t.Fatal(err)
	}
	// Append a structurally consistent row with a wrong digest, preserving all
	// immutable triggers. It is not the occurrence latest pointer: every selected
	// evaluation must be checked, not only the known pinned endpoint.
	err = dbgen.New(f.conn).InsertAlertEvaluation(t.Context(), dbgen.InsertAlertEvaluationParams{ID: mustPGUUID(uuid.New().String()), PolicyID: mustPGUUID(claim.PolicyID), Revision: claim.Revision, ScheduledAt: timestamp(claim.ScheduledAt), EffectiveCutoff: timestamp(input.Snapshot.Cutoff), Truth: string(evaluation.Truth), SourceGeneration: input.Snapshot.SourceGeneration, ExclusionRevision: input.Snapshot.ExclusionRevision, ClaimEpoch: claim.Epoch, ClaimToken: claim.Token, InputDigest: make([]byte, 32), Evidence: evidence, Result: []byte("{}"), CommittedAt: timestamp(f.now)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "series").Proof, ID: opened.Occurrence.ID, Now: f.clock}, ConditionID: "fast", Start: f.at, End: f.now, Limit: 120})
	if !errors.Is(err, ErrAlertInvalid) || len(got.Points) != 0 {
		t.Fatalf("corrupt interior exposed partial success: %+v %v", got, err)
	}
}

func TestIntegrationAlertSeriesRefusesOversizedRequestedHistory(t *testing.T) {
	t.Parallel()
	f := newAlertTestFixture(t)
	policy := f.policy(t, "Series admission")
	claim := f.claim(t, f.at)
	opened, err := f.runtime.commitAlertEvaluation(t.Context(), claim, f.input(t, claim, alert.Complete, true), f.limits, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	// The database permits generic cadence. If internal history exceeds public
	// minute-grid density, the reader must refuse rather than silently omit it.
	if _, err := f.conn.Exec(t.Context(), `INSERT INTO almanac_alert_ranges(id,policy_id,revision,starts_at,ends_at,cadence_seconds,kind,reason,recorded_at)
 SELECT gen_random_uuid(),$1,1,$2::timestamptz+i*interval '2 seconds',$2::timestamptz+i*interval '2 seconds'+interval '1 second',1,'unreconstructed_gap','dense_fixture',$2 FROM generate_series(1,1450) i`, policy, f.at); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAlertSeries(t.Context(), f.f.databaseURL, ReadAlertSeriesInput{ReadAlertInput: ReadAlertInput{Runtime: f.runtime, Proof: alertManagementCommand(f, "series").Proof, ID: opened.Occurrence.ID, Now: f.clock}, ConditionID: "fast", Start: f.at, End: f.at.Add(time.Hour), Limit: 120})
	if !errors.Is(err, ErrOperationalResource) || len(got.Points) != 0 {
		t.Fatalf("oversized history became partial success: %+v %v", got, err)
	}
}
