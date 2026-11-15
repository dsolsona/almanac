package alert

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func evaluated(t *testing.T, truth Truth, minute int) Evaluation {
	t.Helper()
	return evaluatedAtExclusion(t, truth, minute, 0)
}

func evaluatedAtExclusion(t *testing.T, truth Truth, minute int, exclusion int64) Evaluation {
	t.Helper()
	in, limits := decisionFixture()
	in.Snapshot.ExclusionRevision = exclusion
	in.Snapshot.Cutoff = in.Snapshot.Cutoff.Add(time.Duration(minute) * time.Minute)
	for i := range in.Windows {
		in.Windows[i].Start = in.Windows[i].Start.Add(time.Duration(minute) * time.Minute)
		in.Windows[i].End = in.Snapshot.Cutoff
		in.Windows[i].Snapshot = in.Snapshot
	}
	if truth == Breaching {
		in.Windows[0].Good = "9856"
		in.Windows[1].Good = "9856"
	}
	if truth == Indeterminate {
		in.Windows[3].Quality = Missing
	}
	result, err := Evaluate(in, limits)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func opened(t *testing.T) *Occurrence {
	t.Helper()
	ev := evaluated(t, Breaching, 0)
	result, err := Reduce(nil, ev, ev.Snapshot.Cutoff.Add(time.Minute))
	if err != nil || result.Action != Create || result.Next == nil || result.Next.State != Firing {
		t.Fatalf("open = %+v %v", result, err)
	}
	result.Next.ID = "occurrence-1" // Persistence assigns identity, never the reducer.
	return result.Next
}

func TestLifecycleGlobalAcknowledgementAndRecovery(t *testing.T) {
	t.Parallel()
	current := opened(t)
	original := *current
	ackAt := current.OpenedDetectedAt.Add(time.Minute)
	ack, err := Acknowledge(*current, "ari", ackAt)
	if err != nil || ack.Action != AcknowledgeAction || ack.Next.State != Acknowledged || ack.Next.AcknowledgedBy != "ari" || !ack.Next.AcknowledgedAt.Equal(ackAt) {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	if !reflect.DeepEqual(*current, original) {
		t.Fatal("ack mutated input")
	}
	duplicate, err := Acknowledge(*ack.Next, "dan", ackAt.Add(time.Minute))
	if err != nil || duplicate.Action != Unchanged || !reflect.DeepEqual(*duplicate.Next, *ack.Next) {
		t.Fatalf("first global actor changed: %+v %v", duplicate, err)
	}
	for minute, truth := range []Truth{Breaching, Indeterminate, Breaching, Clear} {
		ev := evaluated(t, truth, minute+1)
		next, err := Reduce(ack.Next, ev, ackAt.Add(time.Duration(minute+1)*time.Minute))
		if err != nil || next.Next.ID != "occurrence-1" || next.Next.AcknowledgedBy != "ari" {
			t.Fatalf("continuity: %+v %v", next, err)
		}
		want := Acknowledged
		if truth == Clear {
			want = Resolved
		}
		if next.Next.State != want {
			t.Fatalf("state=%s want %s", next.Next.State, want)
		}
		ack = next
	}
	if _, err := Acknowledge(*ack.Next, "dan", ackAt.Add(time.Hour)); !errors.Is(err, ErrTransition) {
		t.Fatal("terminal occurrence acknowledged")
	}
	if _, err := Reduce(ack.Next, evaluated(t, Breaching, 10), ackAt.Add(time.Hour)); !errors.Is(err, ErrTransition) {
		t.Fatal("terminal occurrence reopened")
	}
	fresh, err := Reduce(nil, evaluated(t, Breaching, 10), ackAt.Add(time.Hour))
	if err != nil || fresh.Action != Create || fresh.Next.ID != "" || fresh.Next.AcknowledgedBy != "" {
		t.Fatalf("fresh occurrence retains terminal identity: %+v %v", fresh, err)
	}
}

func TestLifecycleHandoffAndIndeterminate(t *testing.T) {
	t.Parallel()
	for _, truth := range []Truth{Clear, Indeterminate} {
		ev := evaluated(t, truth, 0)
		result, err := Reduce(nil, ev, ev.Snapshot.Cutoff.Add(time.Minute))
		if err != nil || result.Action != Unchanged || result.Next != nil {
			t.Fatalf("manufactured normal occurrence: %+v %v", result, err)
		}
	}
	current := opened(t)
	in, limits := decisionFixture()
	in.Windows[2].Good = "9940"
	in.Windows[3].Good = "9940"
	in.Snapshot.Cutoff = in.Snapshot.Cutoff.Add(time.Minute)
	for i := range in.Windows {
		in.Windows[i].Start = in.Windows[i].Start.Add(time.Minute)
		in.Windows[i].End = in.Snapshot.Cutoff
		in.Windows[i].Snapshot = in.Snapshot
	}
	ev, err := Evaluate(in, limits)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := Reduce(current, ev, ev.Snapshot.Cutoff.Add(time.Minute))
	if err != nil || handoff.Action != AdvanceEvidence || handoff.Next.ID != current.ID || handoff.Next.State != Firing || !handoff.Next.FiringSnapshot.Cutoff.Equal(current.FiringSnapshot.Cutoff) {
		t.Fatalf("handoff split occurrence: %+v %v", handoff, err)
	}
	next, err := Reduce(handoff.Next, evaluated(t, Indeterminate, 2), ev.Snapshot.Cutoff.Add(2*time.Minute))
	if err != nil || next.Next.State != Firing {
		t.Fatalf("unknown resolved: %+v %v", next, err)
	}
}

func TestLifecycleAdministrativeAndStaleTransitions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		cause Cause
		state State
	}{{PolicyDisabled, Disabled}, {DefinitionSuperseded, Superseded}, {SLOArchived, Disabled}} {
		current := opened(t)
		before := *current
		at := current.OpenedDetectedAt.Add(time.Minute)
		result, err := ApplyAdministrativeChange(current, test.cause, "dan", "planned change", at, at)
		if err != nil || result.Next.State != test.state || result.Cause != test.cause || result.Actor != "dan" || result.Reason != "planned change" || result.Next.ID != current.ID {
			t.Fatalf("admin: %+v %v", result, err)
		}
		if !reflect.DeepEqual(*current, before) {
			t.Fatal("admin mutated input")
		}
		if _, err := ApplyAdministrativeChange(result.Next, test.cause, "dan", "again", at, at); !errors.Is(err, ErrTransition) {
			t.Fatal("terminal record changed")
		}
	}
	for _, change := range []func(*Evaluation){
		func(ev *Evaluation) { ev.PolicyID = "other" }, func(ev *Evaluation) { ev.Revision++ },
		func(ev *Evaluation) { ev.Snapshot.SLORevision++ }, func(ev *Evaluation) { ev.Snapshot.SLIRevision++ },
		func(ev *Evaluation) { ev.Truth = Truth("pending") }, func(ev *Evaluation) { ev.Snapshot.Cutoff = ev.Snapshot.Cutoff.Add(-time.Hour) },
	} {
		current := opened(t)
		ev := evaluated(t, Breaching, 1)
		change(&ev)
		if _, err := Reduce(current, ev, current.OpenedDetectedAt.Add(time.Hour)); !errors.Is(err, ErrTransition) {
			t.Fatalf("stale transition accepted: %+v %v", ev, err)
		}
	}
	current := opened(t)
	if _, err := Acknowledge(*current, "", current.OpenedDetectedAt); !errors.Is(err, ErrTransition) {
		t.Fatal("anonymous ack accepted")
	}
	if _, err := ApplyAdministrativeChange(current, Cause("manual_close"), "dan", "done", current.OpenedDetectedAt, current.OpenedDetectedAt); !errors.Is(err, ErrTransition) {
		t.Fatal("manual close accepted")
	}
}

func TestAdministrativeEffectiveAndDetectionClocks(t *testing.T) {
	t.Parallel()
	current := opened(t)
	effective := current.UpdatedAt.Add(time.Minute)
	detected := effective.Add(5 * time.Minute)
	got, err := ApplyAdministrativeChange(current, PolicyDisabled, "ari", "stop policy", effective, detected)
	if err != nil || !got.EffectiveAt.Equal(effective) || !got.DetectedAt.Equal(detected) || !got.Next.ClosedAt.Equal(effective) || !got.Next.UpdatedAt.Equal(detected) {
		t.Fatalf("administrative clocks: %+v %v", got, err)
	}
	if _, err := ApplyAdministrativeChange(current, PolicyDisabled, "ari", "stop policy", detected, effective); !errors.Is(err, ErrTransition) {
		t.Fatal("detection before effective boundary accepted")
	}
}

func TestMaintenanceRecoveryUsesSameFactsAndSurvivesIndeterminate(t *testing.T) {
	t.Parallel()
	for _, beforeTruth := range []Truth{Breaching, Clear, Indeterminate} {
		t.Run(string(beforeTruth), func(t *testing.T) {
			current := opened(t)
			unknown := evaluatedAtExclusion(t, Indeterminate, 1, 1)
			advanced, err := Reduce(current, unknown, current.UpdatedAt.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			in, limits := decisionFixture()
			in.Snapshot.ExclusionRevision = 1
			in.Snapshot.Cutoff = in.Snapshot.Cutoff.Add(2 * time.Minute)
			for i := range in.Windows {
				in.Windows[i].Start = in.Windows[i].Start.Add(2 * time.Minute)
				in.Windows[i].End = in.Snapshot.Cutoff
				in.Windows[i].Snapshot = in.Snapshot
			}
			clear, err := Evaluate(in, limits)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Reduce(advanced.Next, clear, current.UpdatedAt.Add(3*time.Minute)); !errors.Is(err, ErrTransition) {
				t.Fatal("changed mask recovery accepted without comparison")
			}
			old := make([]WindowEvidence, len(in.Windows))
			copy(old, in.Windows)
			for i := range old {
				old[i].Snapshot.ExclusionRevision = 0
			}
			if beforeTruth == Breaching {
				old[0].Good = "9856"
				old[1].Good = "9856"
			}
			if beforeTruth == Indeterminate {
				old[3].Quality = Missing
			}
			in.RecoveryBaseline = &RecoveryInput{ExclusionRevision: 0, Windows: old}
			clear, err = Evaluate(in, limits)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Reduce(advanced.Next, clear, current.UpdatedAt.Add(3*time.Minute))
			want := ResolvedAfterMaintenanceChange
			if beforeTruth == Clear {
				want = ConditionsCleared
			}
			if err != nil || got.Cause != want || got.Next.State != Resolved {
				t.Fatalf("recovery attribution: %+v %v", got, err)
			}
			in.RecoveryBaseline.Windows[0].Snapshot.SourceGeneration++
			if _, err := Evaluate(in, limits); !errors.Is(err, ErrEvidence) {
				t.Fatal("different facts accepted for maintenance comparison")
			}
		})
	}
}
