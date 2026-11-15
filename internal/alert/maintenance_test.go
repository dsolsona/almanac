package alert

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

const evidenceBuild = "25023fee-32cf-4566-b13a-c22febd42b38"
const evidenceComparison = "f92fc08f-5aae-4395-93da-1b1bb8b3f393"

func maintenanceFor(w WindowEvidence, computed int64) *MaintenanceEvidence {
	m := &MaintenanceEvidence{RequestedRevision: w.Snapshot.ExclusionRevision, ComputedRevision: computed, BuildID: evidenceBuild}
	if computed != m.RequestedRevision {
		m.Proof = &MaintenanceProof{Kind: "indexed_absence", ComputedRevision: computed, RequestedRevision: m.RequestedRevision, IndexedThrough: m.RequestedRevision, Start: w.Start, End: w.End}
	}
	return m
}

func TestEvaluateMaintenanceProvenance(t *testing.T) {
	t.Parallel()
	input, limits := decisionFixture()
	input.Snapshot.ExclusionRevision = 3
	for i := range input.Windows {
		input.Windows[i].Snapshot = input.Snapshot
		input.Windows[i].Maintenance = maintenanceFor(input.Windows[i], 1)
	}
	baseline := make([]WindowEvidence, len(input.Windows))
	for i, w := range input.Windows {
		w.Snapshot.ExclusionRevision = 2
		w.Maintenance = maintenanceFor(w, 0)
		w.Maintenance.BuildID = ""
		baseline[i] = w
	}
	input.RecoveryBaseline = &RecoveryInput{ExclusionRevision: 2, Windows: baseline}
	got, err := Evaluate(input, limits)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Conditions[0].Short.Maintenance, input.Windows[0].Maintenance) || !reflect.DeepEqual(got.RecoveryComparison.Conditions[0].Short.Maintenance, baseline[0].Maintenance) {
		t.Fatalf("lost provenance: %+v", got)
	}
	got.Conditions[0].Short.Maintenance.Proof.Kind = "changed"
	got.RecoveryComparison.Conditions[0].Short.Maintenance.BuildID = "changed"
	if input.Windows[0].Maintenance.Proof.Kind != "indexed_absence" || baseline[0].Maintenance.BuildID != "" {
		t.Fatal("output aliases input")
	}
}

func TestEvaluateMaintenanceInvalid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*MaintenanceEvidence)
	}{
		{"request mismatch", func(m *MaintenanceEvidence) { m.RequestedRevision++ }},
		{"future computed", func(m *MaintenanceEvidence) { m.ComputedRevision = 4 }},
		{"missing proof", func(m *MaintenanceEvidence) { m.Proof = nil }},
		{"bad build", func(m *MaintenanceEvidence) { m.BuildID = "not-uuid" }},
		{"missing build", func(m *MaintenanceEvidence) { m.BuildID = "" }},
		{"short proof", func(m *MaintenanceEvidence) { m.Proof.Start = m.Proof.Start.Add(time.Minute) }},
		{"unknown proof", func(m *MaintenanceEvidence) { m.Proof.Kind = "changed" }},
		{"unindexed", func(m *MaintenanceEvidence) { m.Proof.IndexedThrough = 1 }},
		{"missing comparison", func(m *MaintenanceEvidence) { m.Proof.Kind = "equal_union" }},
		{"extra comparison", func(m *MaintenanceEvidence) { m.Proof.ComparisonID = evidenceComparison }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, limits := decisionFixture()
			input.Snapshot.ExclusionRevision = 3
			for i := range input.Windows {
				input.Windows[i].Snapshot = input.Snapshot
			}
			input.Windows[0].Maintenance = maintenanceFor(input.Windows[0], 1)
			tc.change(input.Windows[0].Maintenance)
			if _, err := Evaluate(input, limits); !errors.Is(err, ErrEvidence) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCompactionMaintenanceEndpoints(t *testing.T) {
	t.Parallel()
	entries, before := retentionEntries()
	for i := range entries {
		changeRetentionIdentity(&entries[i], func(s *SnapshotIdentity) { s.ExclusionRevision = 5 })
		for _, w := range []*WindowEvidence{&entries[i].Evaluation.Conditions[0].Short, &entries[i].Evaluation.Conditions[0].Long} {
			w.Maintenance = maintenanceFor(*w, int64(i%3))
		}
	}
	runs, err := PlanCompaction(entries, before)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v error=%v", runs, err)
	}
	c := runs[0].Conditions[0]
	if !reflect.DeepEqual(c.FirstShortMaintenance, entries[0].Evaluation.Conditions[0].Short.Maintenance) || !reflect.DeepEqual(c.LastLongMaintenance, entries[5].Evaluation.Conditions[0].Long.Maintenance) {
		t.Fatalf("endpoints=%+v", c)
	}
	c.FirstShortMaintenance.Proof.Kind = "changed"
	if entries[0].Evaluation.Conditions[0].Short.Maintenance.Proof.Kind != "indexed_absence" {
		t.Fatal("summary aliases evidence")
	}
}

func TestEvaluateMaintenanceReusedHour(t *testing.T) {
	t.Parallel()
	input, limits := decisionFixture()
	input.Snapshot.ExclusionRevision = 4
	for i := range input.Windows {
		input.Windows[i].Snapshot = input.Snapshot
	}
	w := &input.Windows[3]
	w.Maintenance = maintenanceFor(*w, 3)
	hour := w.Start.Add(time.Hour)
	h := MaintenanceHourEvidence{SLOID: input.Snapshot.SLOID, SLIID: evidenceComparison, BuildID: evidenceBuild, SLORevision: 1, SLIRevision: 1, SourceGeneration: 1, ComputedRevision: 1, HourStart: hour, Proof: MaintenanceProof{Kind: "equal_union", ComputedRevision: 1, RequestedRevision: 3, IndexedThrough: 4, Start: hour.Add(20 * time.Second), End: hour.Add(time.Hour + 40*time.Second), ComparisonID: evidenceComparison}}
	w.Maintenance.ReusedHours = []MaintenanceHourEvidence{h}
	got, err := Evaluate(input, limits)
	if err != nil {
		t.Fatal(err)
	}
	got.Conditions[1].Long.Maintenance.ReusedHours[0].SourceGeneration = 2
	if w.Maintenance.ReusedHours[0].SourceGeneration != 1 {
		t.Fatal("hour slice aliases input")
	}
	for _, tc := range []struct {
		name   string
		change func(*MaintenanceHourEvidence)
	}{
		{"other objective", func(h *MaintenanceHourEvidence) { h.SLOID = "other" }},
		{"other definition", func(h *MaintenanceHourEvidence) { h.SLORevision++ }},
		{"other measurement", func(h *MaintenanceHourEvidence) { h.SLIRevision++ }},
		{"future generation", func(h *MaintenanceHourEvidence) { h.SourceGeneration = 4 }},
		{"future mask", func(h *MaintenanceHourEvidence) { h.ComputedRevision = 4 }},
		{"bad sli", func(h *MaintenanceHourEvidence) { h.SLIID = "unknown" }},
		{"outside window", func(h *MaintenanceHourEvidence) { h.HourStart = input.Snapshot.Cutoff }},
		{"wrong target", func(h *MaintenanceHourEvidence) { h.Proof.RequestedRevision = 4 }},
		{"shortened proof", func(h *MaintenanceHourEvidence) { h.Proof.End = h.HourStart.Add(time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modified := h
			tc.change(&modified)
			w.Maintenance.ReusedHours = []MaintenanceHourEvidence{modified}
			if _, err := Evaluate(input, limits); !errors.Is(err, ErrEvidence) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	w.Maintenance.ReusedHours = []MaintenanceHourEvidence{h, h}
	if _, err := Evaluate(input, limits); !errors.Is(err, ErrEvidence) {
		t.Fatalf("duplicate hour error=%v", err)
	}
}

func TestEvaluateMaintenanceExactAndSemanticTimes(t *testing.T) {
	t.Parallel()
	for _, build := range []string{"", evidenceBuild} {
		input, limits := decisionFixture()
		w := &input.Windows[0]
		w.Maintenance = &MaintenanceEvidence{BuildID: build, Proof: &MaintenanceProof{Kind: "same_revision", Start: w.Start.In(time.FixedZone("proof", 3600)), End: w.End}}
		if _, err := Evaluate(input, limits); err != nil {
			t.Fatalf("genesis build %q: %v", build, err)
		}
	}
	input, limits := decisionFixture()
	input.Snapshot.ExclusionRevision = 3
	for i := range input.Windows {
		input.Windows[i].Snapshot = input.Snapshot
	}
	w := &input.Windows[0]
	w.Maintenance = maintenanceFor(*w, 3)
	w.Maintenance.Proof = &MaintenanceProof{Kind: "same_revision", ComputedRevision: 3, RequestedRevision: 3, IndexedThrough: 0, Start: w.Start, End: w.End}
	if _, err := Evaluate(input, limits); err != nil {
		t.Fatalf("same revision wrongly requires index catchup: %v", err)
	}
}
