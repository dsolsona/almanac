package alert

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func decisionFixture() (Input, Limits) {
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	snapshot := SnapshotIdentity{SLOID: "checkout", SLORevision: 1, SLIRevision: 1, SourceGeneration: 3, CalculationVersion: "occurrences-operational-v1", Cutoff: at}
	input := Input{
		Policy: PolicyRevision{ID: "checkout-burn", Revision: 1, Conditions: []Condition{
			{ID: "fast", Short: 5 * time.Minute, Long: time.Hour, Threshold: "14.4"},
			{ID: "sustained", Short: 30 * time.Minute, Long: 6 * time.Hour, Threshold: "6"},
		}},
		Objective: Objective{Target: "0.999", Cadence: time.Minute, DefinitionValidFrom: at.Add(-48 * time.Hour), MeasurementValidFrom: at.Add(-48 * time.Hour), RetentionStart: at.Add(-30 * 24 * time.Hour)},
		Snapshot:  snapshot,
	}
	for _, duration := range []time.Duration{5 * time.Minute, time.Hour, 30 * time.Minute, 6 * time.Hour} {
		input.Windows = append(input.Windows, WindowEvidence{Snapshot: snapshot, Start: at.Add(-duration), End: at, Quality: Complete, Good: "10000", Total: "10000"})
	}
	return input, Limits{MaxConditions: 8, MaxDecimalBytes: 96}
}

func TestEvaluateExactThresholds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, good, total, threshold string
		want                         Truth
	}{
		{"equal", "9856", "10000", "14.4", Breaching},
		{"below", "9857", "10000", "14.4", Clear},
		{"same rounded burn below", "9856.000000000001", "10000", "14.4", Clear},
		{"same rounded burn above", "9855.999999999999", "10000", "14.4", Breaching},
		{"fractional counts", "0.9856", "1", "14.4", Breaching},
		{"decimal leading zeros", "09856", "010000", "014.4", Breaching},
		{"large sum beyond interval decimal", "985600000000000000000000000000000", "1000000000000000000000000000000000", "14.4", Breaching},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, limits := decisionFixture()
			input.Policy.Conditions = input.Policy.Conditions[:1]
			input.Policy.Conditions[0].Threshold = test.threshold
			for i := range input.Windows {
				input.Windows[i].Good, input.Windows[i].Total = test.good, test.total
			}
			got, err := Evaluate(input, limits)
			if err != nil || got.Truth != test.want {
				t.Fatalf("decision = %+v, %v; want %s", got, err, test.want)
			}
		})
	}
}

func TestEvaluateConditionTruthAndFullWindows(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		change   func(*Input)
		want     Truth
		outcomes []ConditionOutcome
	}{
		{"all clear", func(*Input) {}, Clear, []ConditionOutcome{NotMatching, NotMatching}},
		{"AND needs both", func(in *Input) { in.Windows[0].Good = "9856" }, Clear, []ConditionOutcome{NotMatching, NotMatching}},
		{"OR matches sustained", func(in *Input) { in.Windows[2].Good = "9940"; in.Windows[3].Good = "9940" }, Breaching, []ConditionOutcome{NotMatching, Matching}},
		{"unavailable prevents recovery", func(in *Input) { in.Windows[3].Quality = Missing; in.Windows[3].Good = ""; in.Windows[3].Total = "" }, Indeterminate, []ConditionOutcome{NotMatching, Unavailable}},
		{"fast wins with unavailable sustained", func(in *Input) {
			in.Windows[0].Good = "9856"
			in.Windows[1].Good = "9856"
			in.Windows[3].Quality = Missing
		}, Breaching, []ConditionOutcome{Matching, Unavailable}},
		{"shortened canonical result cannot fire full alert", func(in *Input) {
			in.Objective.DefinitionValidFrom = in.Snapshot.Cutoff.Add(-30 * time.Minute)
			in.Windows[0].Good = "0"
			in.Windows[1].Good = "0"
		}, Indeterminate, []ConditionOutcome{Unavailable, Unavailable}},
		{"retention cannot shorten", func(in *Input) { in.Objective.RetentionStart = in.Snapshot.Cutoff.Add(-30 * time.Minute) }, Indeterminate, []ConditionOutcome{Unavailable, Unavailable}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, limits := decisionFixture()
			test.change(&input)
			got, err := Evaluate(input, limits)
			if err != nil || got.Truth != test.want {
				t.Fatalf("decision = %+v, %v; want %s", got, err, test.want)
			}
			for i, want := range test.outcomes {
				if got.Conditions[i].Outcome != want {
					t.Errorf("condition %d = %s; want %s", i, got.Conditions[i].Outcome, want)
				}
			}
		})
	}
	for _, quality := range []Quality{Missing, Partial, Stale, ZeroTraffic, Warmup, Expired, Excluded, CalculationFailed} {
		t.Run(string(quality), func(t *testing.T) {
			in, limits := decisionFixture()
			in.Windows[0].Quality = quality
			got, err := Evaluate(in, limits)
			if err != nil || got.Truth != Indeterminate || got.Conditions[0].Short.Quality != quality {
				t.Fatalf("quality lost: %+v %v", got, err)
			}
		})
	}
	input, limits := decisionFixture()
	input.Windows[0].Good, input.Windows[0].Total = "0", "0"
	got, err := Evaluate(input, limits)
	if err != nil || got.Truth != Indeterminate || got.Conditions[0].Short.Quality != ZeroTraffic {
		t.Fatalf("zero traffic = %+v %v", got, err)
	}
}

func TestEvaluateRejectsCorruptOrMixedEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*Input)
	}{
		{"missing window", func(in *Input) { in.Windows = in.Windows[1:] }},
		{"duplicate", func(in *Input) { in.Windows = append(in.Windows, in.Windows[0]) }},
		{"mixed source", func(in *Input) { in.Windows[3].Snapshot.SourceGeneration++ }},
		{"mixed exclusion", func(in *Input) { in.Windows[3].Snapshot.ExclusionRevision++ }},
		{"mixed definition", func(in *Input) { in.Windows[3].Snapshot.SLORevision++ }},
		{"mixed measurement", func(in *Input) { in.Windows[3].Snapshot.SLIRevision++ }},
		{"mixed calculation", func(in *Input) { in.Windows[3].Snapshot.CalculationVersion += "-other" }},
		{"wrong SLO", func(in *Input) { in.Windows[3].Snapshot.SLOID = "other" }},
		{"wrong cutoff", func(in *Input) { in.Windows[3].End = in.Windows[3].End.Add(time.Minute) }},
		{"negative counts", func(in *Input) { in.Windows[0].Good = "-1" }},
		{"good exceeds total", func(in *Input) { in.Windows[0].Good = "10001" }},
		{"fraction syntax", func(in *Input) { in.Windows[0].Good = "1/2" }},
		{"exponent syntax", func(in *Input) { in.Windows[0].Good = "1e2" }},
		{"missing numeric", func(in *Input) { in.Windows[0].Good = "" }},
		{"pending is not truth", func(in *Input) { in.Windows[0].Quality = Quality("pending") }},
		{"corruption after matching condition", func(in *Input) { in.Windows[0].Good = "0"; in.Windows[1].Good = "0"; in.Windows[3].Good = "NaN" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, limits := decisionFixture()
			test.change(&input)
			if got, err := Evaluate(input, limits); !errors.Is(err, ErrEvidence) {
				t.Fatalf("accepted corrupt evidence: %+v %v", got, err)
			}
		})
	}
}

func TestPolicyValidationAndSharedCustomWindows(t *testing.T) {
	t.Parallel()
	in, limits := decisionFixture()
	in.Policy.Conditions = append(in.Policy.Conditions, Condition{ID: "custom", Short: time.Hour, Long: 6 * time.Hour, Threshold: "3"})
	before := append([]WindowEvidence(nil), in.Windows...)
	got, err := Evaluate(in, limits)
	if err != nil || len(got.Conditions) != 3 || !reflect.DeepEqual(before, in.Windows) {
		t.Fatalf("three-condition shared inputs: %+v %v", got, err)
	}
	got.Conditions[0].Short.Good = "changed"
	if !reflect.DeepEqual(before, in.Windows) {
		t.Fatal("decision aliases caller evidence")
	}
	// A third condition can require two additional unique windows; four windows
	// were the experimental fixture, not a policy capability limit.
	in.Policy.Conditions[2].Short, in.Policy.Conditions[2].Long = 2*time.Hour, 12*time.Hour
	for _, duration := range []time.Duration{2 * time.Hour, 12 * time.Hour} {
		in.Windows = append(in.Windows, WindowEvidence{Snapshot: in.Snapshot, Start: in.Snapshot.Cutoff.Add(-duration), End: in.Snapshot.Cutoff, Quality: Complete, Good: "10000", Total: "10000"})
	}
	if result, err := Evaluate(in, limits); err != nil || len(result.Conditions) != 3 || result.Truth != Clear {
		t.Fatalf("six distinct windows: %+v %v", result, err)
	}
	for _, change := range []func(*Input){
		func(in *Input) { in.Policy.Conditions = nil }, func(in *Input) { in.Policy.Conditions[1].ID = "fast" },
		func(in *Input) { in.Policy.Conditions[0].Threshold = "0" }, func(in *Input) { in.Policy.Conditions[0].Threshold = "1000.000000000001" },
		func(in *Input) { in.Policy.Conditions[0].Short = 61 * time.Second }, func(in *Input) { in.Policy.Conditions[0].Long = 31 * 24 * time.Hour },
		func(in *Input) { in.Policy.Conditions[0].Long = time.Minute }, func(in *Input) { in.Objective.Target = "1" },
		func(in *Input) { in.Objective.Cadence = 70 * time.Second },
	} {
		input, _ := decisionFixture()
		change(&input)
		if err := Validate(input.Policy, input.Objective, limits); !errors.Is(err, ErrPolicy) {
			t.Fatalf("invalid policy accepted: %+v %v", input, err)
		}
	}
	in, _ = decisionFixture()
	in.Objective.Target = "0"
	for i := range in.Policy.Conditions {
		in.Policy.Conditions[i].Threshold = "1"
	}
	if err := Validate(in.Policy, in.Objective, limits); err != nil {
		t.Fatalf("zero target and reachable1x: %v", err)
	}
	limits.MaxConditions = 1
	if err := Validate(in.Policy, in.Objective, limits); !errors.Is(err, ErrPolicy) {
		t.Fatal("explicit caller condition limit ignored")
	}
}
