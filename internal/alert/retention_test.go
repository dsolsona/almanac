package alert

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func retentionEntries() ([]RetentionEntry, time.Time) {
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	entries := make([]RetentionEntry, 6)
	for i := range entries {
		cutoff := at.Add(time.Duration(i) * time.Minute)
		identity := SnapshotIdentity{SLOID: "slo", SLORevision: 1, SLIRevision: 1, SourceGeneration: int64(10 + i), CalculationVersion: "v1", Cutoff: cutoff}
		window := func(duration time.Duration) WindowEvidence {
			return WindowEvidence{Snapshot: identity, Start: cutoff.Add(-duration), End: cutoff, Quality: Complete, Good: strconv.Itoa(99 + i), Total: strconv.Itoa(100 + i)}
		}
		entries[i] = RetentionEntry{ID: strconv.Itoa(i), ScheduledAt: cutoff, CommittedAt: cutoff.Add(time.Second), Evaluation: Evaluation{PolicyID: "policy", Revision: 1, Snapshot: identity, Truth: Clear, Conditions: []ConditionResult{{ID: "fast", Threshold: "1", Outcome: NotMatching, Short: window(5 * time.Minute), Long: window(time.Hour)}}}}
	}
	return entries, at.Add(time.Hour)
}

func changeRetentionIdentity(entry *RetentionEntry, change func(*SnapshotIdentity)) {
	change(&entry.Evaluation.Snapshot)
	for i := range entry.Evaluation.Conditions {
		for _, window := range []*WindowEvidence{&entry.Evaluation.Conditions[i].Short, &entry.Evaluation.Conditions[i].Long} {
			duration := window.End.Sub(window.Start)
			window.Snapshot = entry.Evaluation.Snapshot
			window.End = entry.Evaluation.Snapshot.Cutoff
			window.Start = window.End.Add(-duration)
		}
	}
}

func TestCompactionExactSummary(t *testing.T) {
	t.Parallel()
	entries, before := retentionEntries()
	original, _ := retentionEntries()
	runs, err := PlanCompaction(entries, before)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v error=%v", runs, err)
	}
	want := CompactionRun{EvaluationIDs: []string{"0", "1", "2", "3", "4", "5"}, Start: entries[0].ScheduledAt, End: entries[5].ScheduledAt.Add(time.Minute), First: entries[0].Evaluation.Snapshot, Last: entries[5].Evaluation.Snapshot, Truth: Clear, Conditions: []ConditionCompactionSummary{{ID: "fast", Threshold: "1", Outcome: NotMatching, ShortDuration: 5 * time.Minute, LongDuration: time.Hour, ShortQuality: Complete, LongQuality: Complete}}}
	if !reflect.DeepEqual(runs[0], want) || !reflect.DeepEqual(entries, original) {
		t.Fatalf("lost endpoints/summary or mutated input: %+v", runs)
	}
	runs[0].EvaluationIDs[0] = "changed"
	runs[0].Conditions[0].ID = "changed"
	if !reflect.DeepEqual(entries, original) {
		t.Fatal("returned run aliases input")
	}
}

func TestCompactionRunBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*RetentionEntry)
	}{
		{"policy", func(e *RetentionEntry) { e.Evaluation.PolicyID = "policy-two" }},
		{"revision", func(e *RetentionEntry) { e.Evaluation.Revision = 2 }},
		{"slo", func(e *RetentionEntry) { changeRetentionIdentity(e, func(s *SnapshotIdentity) { s.SLOID = "another" }) }},
		{"slo revision", func(e *RetentionEntry) { changeRetentionIdentity(e, func(s *SnapshotIdentity) { s.SLORevision++ }) }},
		{"sli revision", func(e *RetentionEntry) { changeRetentionIdentity(e, func(s *SnapshotIdentity) { s.SLIRevision++ }) }},
		{"exclusion", func(e *RetentionEntry) {
			changeRetentionIdentity(e, func(s *SnapshotIdentity) { s.ExclusionRevision++ })
		}},
		{"calculation", func(e *RetentionEntry) {
			changeRetentionIdentity(e, func(s *SnapshotIdentity) { s.CalculationVersion = "v2" })
		}},
		{"source regression", func(e *RetentionEntry) {
			changeRetentionIdentity(e, func(s *SnapshotIdentity) { s.SourceGeneration -= 10 })
		}},
		{"truth", func(e *RetentionEntry) { e.Evaluation.Truth = Breaching; e.Evaluation.Conditions[0].Outcome = Matching }},
		{"condition id", func(e *RetentionEntry) { e.Evaluation.Conditions[0].ID = "slow" }},
		{"threshold", func(e *RetentionEntry) { e.Evaluation.Conditions[0].Threshold = "2" }},
		{"short duration", func(e *RetentionEntry) {
			e.Evaluation.Conditions[0].Short.Start = e.Evaluation.Conditions[0].Short.Start.Add(-time.Minute)
		}},
		{"long duration", func(e *RetentionEntry) {
			e.Evaluation.Conditions[0].Long.Start = e.Evaluation.Conditions[0].Long.Start.Add(-time.Minute)
		}},
		{"short quality", func(e *RetentionEntry) {
			e.Evaluation.Truth = Indeterminate
			e.Evaluation.Conditions[0].Outcome = Unavailable
			e.Evaluation.Conditions[0].Short.Quality = Missing
		}},
		{"long quality", func(e *RetentionEntry) {
			e.Evaluation.Truth = Indeterminate
			e.Evaluation.Conditions[0].Outcome = Unavailable
			e.Evaluation.Conditions[0].Long.Quality = CalculationFailed
		}},
		{"short reason", func(e *RetentionEntry) { e.Evaluation.Conditions[0].Short.Reason = "different" }},
		{"long reason", func(e *RetentionEntry) { e.Evaluation.Conditions[0].Long.Reason = "different" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries, before := retentionEntries()
			for i := 2; i < len(entries); i++ {
				test.change(&entries[i])
			}
			runs, err := PlanCompaction(entries, before)
			if err != nil || len(runs) != 2 || !reflect.DeepEqual(runs[0].EvaluationIDs, []string{"0", "1"}) || !reflect.DeepEqual(runs[1].EvaluationIDs, []string{"2", "3", "4", "5"}) {
				t.Fatalf("merged meaning/quality boundary: %+v %v", runs, err)
			}
		})
	}
}

func TestCompactionBarriersAndHorizon(t *testing.T) {
	t.Parallel()
	for _, barrier := range []string{"pin", "committed equality", "scheduled equality", "hole", "repeated cutoff"} {
		t.Run(barrier, func(t *testing.T) {
			entries, before := retentionEntries()
			want := [][]string{{"0", "1"}, {"3", "4", "5"}}
			switch barrier {
			case "pin":
				entries[2].Pinned = true
			case "committed equality":
				entries[2].CommittedAt = before
			case "scheduled equality":
				before = entries[2].ScheduledAt
				want = [][]string{{"0", "1"}}
			case "hole":
				entries = append(entries[:2], entries[3:]...)
			case "repeated cutoff":
				changeRetentionIdentity(&entries[2], func(s *SnapshotIdentity) { s.Cutoff = entries[1].Evaluation.Snapshot.Cutoff })
				want = [][]string{{"0", "1"}, {"2", "3", "4", "5"}}
			}
			runs, err := PlanCompaction(entries, before)
			var ids [][]string
			for _, run := range runs {
				ids = append(ids, run.EvaluationIDs)
			}
			if err != nil || !reflect.DeepEqual(ids, want) {
				t.Fatalf("barrier lost: %+v %v", runs, err)
			}
		})
	}
}

func TestCompactionConditionHandoff(t *testing.T) {
	t.Parallel()
	entries, before := retentionEntries()
	for i := range entries {
		e := &entries[i].Evaluation
		e.Truth = Breaching
		second := e.Conditions[0]
		second.ID = "sustained"
		e.Conditions = append(e.Conditions, second)
		if i < 2 {
			e.Conditions[0].Outcome = Matching
		} else {
			e.Conditions[1].Outcome = Matching
		}
	}
	runs, err := PlanCompaction(entries, before)
	if err != nil || len(runs) != 2 || len(runs[0].EvaluationIDs) != 2 || len(runs[1].EvaluationIDs) != 4 {
		t.Fatalf("unchanged policy truth hid handoff: %+v %v", runs, err)
	}
}

func TestCompactionQualityWithinUnavailableTruth(t *testing.T) {
	t.Parallel()
	for _, side := range []string{"short", "long"} {
		t.Run(side, func(t *testing.T) {
			entries, before := retentionEntries()
			for i := range entries {
				e := &entries[i].Evaluation
				e.Truth, e.Conditions[0].Outcome = Indeterminate, Unavailable
				e.Conditions[0].Short.Quality, e.Conditions[0].Long.Quality = Missing, Partial
				if i >= 2 {
					if side == "short" {
						e.Conditions[0].Short.Quality = Stale
					} else {
						e.Conditions[0].Long.Quality = CalculationFailed
					}
				}
			}
			runs, err := PlanCompaction(entries, before)
			if err != nil || len(runs) != 2 || len(runs[0].EvaluationIDs) != 2 || len(runs[1].EvaluationIDs) != 4 {
				t.Fatalf("unchanged unavailable truth hid quality change: %+v %v", runs, err)
			}
		})
	}
}

func TestCompactionScheduledHorizonIndependentOfCommit(t *testing.T) {
	t.Parallel()
	entries, _ := retentionEntries()
	entries = entries[:2]
	before := entries[0].ScheduledAt
	for i := range entries {
		changeRetentionIdentity(&entries[i], func(s *SnapshotIdentity) { s.Cutoff = s.Cutoff.Add(-5 * time.Minute) })
		entries[i].CommittedAt = before.Add(-time.Microsecond)
	}
	if runs, err := PlanCompaction(entries, before); err != nil || len(runs) != 0 {
		t.Fatalf("old committed time bypassed scheduled horizon: %+v %v", runs, err)
	}
}

func TestCompactionRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func([]RetentionEntry)
	}{
		{"order", func(e []RetentionEntry) { e[4], e[5] = e[5], e[4] }},
		{"duplicate id", func(e []RetentionEntry) { e[5].ID = e[0].ID }},
		{"duplicate boundary", func(e []RetentionEntry) { e[5].ScheduledAt = e[4].ScheduledAt }},
		{"fractional boundary", func(e []RetentionEntry) { e[5].ScheduledAt = e[5].ScheduledAt.Add(time.Second) }},
		{"empty id", func(e []RetentionEntry) { e[5].ID = "" }},
		{"empty policy", func(e []RetentionEntry) { e[5].Evaluation.PolicyID = "" }},
		{"missing commit", func(e []RetentionEntry) { e[5].CommittedAt = time.Time{} }},
		{"bad truth", func(e []RetentionEntry) { e[5].Evaluation.Truth = "other" }},
		{"missing conditions", func(e []RetentionEntry) { e[5].Evaluation.Conditions = nil }},
		{"bad outcome", func(e []RetentionEntry) { e[5].Evaluation.Conditions[0].Outcome = "other" }},
		{"bad threshold", func(e []RetentionEntry) { e[5].Evaluation.Conditions[0].Threshold = "NaN" }},
		{"bad window", func(e []RetentionEntry) {
			e[5].Evaluation.Conditions[0].Short.End = e[5].Evaluation.Conditions[0].Short.Start
		}},
		{"wrong snapshot", func(e []RetentionEntry) { e[5].Evaluation.Conditions[0].Short.Snapshot.SLOID = "wrong" }},
		{"pending quality", func(e []RetentionEntry) { e[5].Evaluation.Conditions[0].Short.Quality = "pending" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries, before := retentionEntries()
			test.change(entries)
			runs, err := PlanCompaction(entries, before)
			if !errors.Is(err, ErrCompaction) || len(runs) != 0 {
				t.Fatalf("invalid input yielded deletable run: %+v %v", runs, err)
			}
		})
	}
	entries, before := retentionEntries()
	if _, err := PlanCompaction(entries, time.Time{}); !errors.Is(err, ErrCompaction) {
		t.Fatal("zero horizon accepted")
	}
	for _, input := range [][]RetentionEntry{nil, entries[:1]} {
		if runs, err := PlanCompaction(input, before); err != nil || len(runs) != 0 {
			t.Fatalf("singleton/empty compacted: %+v %v", runs, err)
		}
	}
}
