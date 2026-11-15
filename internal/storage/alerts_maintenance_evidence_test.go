package storage

import (
	"bytes"
	"crypto/sha256"
	"reflect"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestAlertMaintenanceLegacyDigest(t *testing.T) {
	t.Parallel()
	// Independent pre-extension format, including the old field order. Reencoding
	// a saved input must retain the journal's exact SHA-256 input bytes.
	type legacyWindow struct {
		Snapshot            alert.SnapshotIdentity
		Start, End          time.Time
		Quality             alert.Quality
		Good, Total, Reason string
	}
	type legacyRecovery struct {
		ExclusionRevision int64
		Windows           []legacyWindow
	}
	type legacyInput struct {
		Policy           alert.PolicyRevision
		Objective        alert.Objective
		Snapshot         alert.SnapshotIdentity
		Windows          []legacyWindow
		RecoveryBaseline *legacyRecovery
	}
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	snapshot := alert.SnapshotIdentity{SLOID: "legacy", SLORevision: 1, SLIRevision: 1, SourceGeneration: 5, ExclusionRevision: 1, CalculationVersion: "v1", Cutoff: at}
	window := legacyWindow{Snapshot: snapshot, Start: at.Add(-time.Hour), End: at, Quality: alert.Complete, Good: "123.456789012345", Total: "124", Reason: ""}
	old := legacyInput{Policy: alert.PolicyRevision{ID: "legacy", Revision: 1, Conditions: []alert.Condition{{ID: "fast", Short: time.Minute, Long: time.Hour, Threshold: "14.4"}}}, Objective: alert.Objective{Target: "0.999", Cadence: time.Minute}, Snapshot: snapshot, Windows: []legacyWindow{window}, RecoveryBaseline: &legacyRecovery{ExclusionRevision: 0, Windows: []legacyWindow{window}}}
	data, err := marshalAlertData(old)
	if err != nil {
		t.Fatal(err)
	}
	var decoded alert.Input
	if err := unmarshalAlertData(data, &decoded); err != nil {
		t.Fatal(err)
	}
	encoded, err := marshalAlertData(decoded)
	if err != nil || !bytes.Equal(data, encoded) || sha256.Sum256(data) != sha256.Sum256(encoded) {
		t.Fatalf("legacy bytes changed:\n%s\n%s\n%v", data, encoded, err)
	}
}

func TestAlertMaintenanceSeriesComparison(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	raw := alert.WindowEvidence{Snapshot: alert.SnapshotIdentity{Cutoff: at, ExclusionRevision: 2}, Start: at.Add(-time.Hour), End: at, Quality: alert.Complete, Good: "1", Total: "1", Maintenance: &alert.MaintenanceEvidence{RequestedRevision: 2, ComputedRevision: 0, Proof: &alert.MaintenanceProof{Kind: "indexed_absence", RequestedRevision: 2, IndexedThrough: 2, Start: at.Add(-time.Hour), End: at}}}
	data, _ := marshalAlertData(raw)
	var saved alert.WindowEvidence
	if err := unmarshalAlertData(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !alertSeriesWindowMatches(alert.Objective{}, raw, saved) {
		t.Fatal("independently decoded equal provenance rejected")
	}
	saved.Maintenance.Proof.IndexedThrough++
	if alertSeriesWindowMatches(alert.Objective{}, raw, saved) {
		t.Fatal("changed provenance accepted")
	}
}

func TestAlertMaintenanceCompactionCodec(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	first := alert.SnapshotIdentity{SLOID: "6f6805d3-a6f2-41c1-8188-c593af5245b3", SLORevision: 1, SLIRevision: 1, SourceGeneration: 4, ExclusionRevision: 3, CalculationVersion: "v1", Cutoff: at}
	last := first
	last.Cutoff = at.Add(time.Minute)
	last.SourceGeneration++
	evidence := func(s alert.SnapshotIdentity, d time.Duration) *alert.MaintenanceEvidence {
		return &alert.MaintenanceEvidence{RequestedRevision: 3, ComputedRevision: 0, Proof: &alert.MaintenanceProof{Kind: "indexed_absence", RequestedRevision: 3, IndexedThrough: 3, Start: s.Cutoff.Add(-d), End: s.Cutoff}}
	}
	summary := AlertCompactionSummary{FormatVersion: 1, First: first, Last: last, Truth: alert.Clear, Conditions: []alert.ConditionCompactionSummary{{ID: "fast", Threshold: "1", Outcome: alert.NotMatching, ShortDuration: time.Minute, LongDuration: time.Hour, ShortQuality: alert.Complete, LongQuality: alert.Complete, FirstShortMaintenance: evidence(first, time.Minute), LastShortMaintenance: evidence(last, time.Minute), FirstLongMaintenance: evidence(first, time.Hour), LastLongMaintenance: evidence(last, time.Hour)}}}
	data, err := marshalAlertData(summary)
	if err != nil {
		t.Fatal(err)
	}
	row := dbgen.AlmanacAlertRange{Kind: "compacted_evaluations", CadenceSeconds: 60, StartsAt: timestamp(at), EndsAt: timestamp(at.Add(2 * time.Minute)), Summary: data}
	got, err := decodeAlertCompactionSummary(row)
	if err != nil || !reflect.DeepEqual(got, &summary) {
		t.Fatalf("roundtrip=%+v error=%v", got, err)
	}
	if bytes.Contains(data, []byte(`"Good"`)) || bytes.Contains(data, []byte(`"Total"`)) {
		t.Fatal("invented numerical summary")
	}
	summary.Conditions[0].LastLongMaintenance.Proof.End = first.Cutoff
	row.Summary, err = marshalAlertData(summary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeAlertCompactionSummary(row); err == nil {
		t.Fatal("invalid endpoint proof accepted")
	}
}
