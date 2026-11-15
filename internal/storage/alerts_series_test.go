package storage

import (
	"slices"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
)

func TestAlertSeriesWindowNormalizationRejectsOtherChanges(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	objective := alert.Objective{DefinitionValidFrom: at.Add(-time.Hour), MeasurementValidFrom: at.Add(-time.Hour), RetentionStart: at.Add(-2 * time.Hour)}
	raw := alert.WindowEvidence{Snapshot: alert.SnapshotIdentity{SLOID: "immutable", SLORevision: 2, SLIRevision: 3, SourceGeneration: 4, ExclusionRevision: 5, CalculationVersion: "v1", Cutoff: at}, Start: at.Add(-time.Minute), End: at, Quality: alert.Complete, Good: "0", Total: "0"}
	saved := raw
	saved.Quality, saved.Reason = alert.ZeroTraffic, "zero_traffic"
	if !alertSeriesWindowMatches(objective, raw, saved) {
		t.Fatal("documented zero-traffic normalization rejected")
	}
	for _, change := range []struct {
		name   string
		mutate func(*alert.WindowEvidence)
	}{
		{"counts", func(w *alert.WindowEvidence) { w.Total = "1" }},
		{"window boundary", func(w *alert.WindowEvidence) { w.Start = w.Start.Add(time.Second) }},
		{"snapshot cutoff", func(w *alert.WindowEvidence) { w.Snapshot.Cutoff = w.Snapshot.Cutoff.Add(time.Second) }},
		{"source generation", func(w *alert.WindowEvidence) { w.Snapshot.SourceGeneration++ }},
		{"exclusion revision", func(w *alert.WindowEvidence) { w.Snapshot.ExclusionRevision++ }},
		{"quality", func(w *alert.WindowEvidence) { w.Quality = alert.Missing }},
		{"reason", func(w *alert.WindowEvidence) { w.Reason = "invented" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := saved
			change.mutate(&changed)
			if alertSeriesWindowMatches(objective, raw, changed) {
				t.Fatal("altered evidence accepted")
			}
		})
	}
	// Validity normalization also applies to non-complete source evidence, and
	// retention takes precedence. Counts and original input remain untouched.
	raw.Quality, raw.Good, raw.Total = alert.Missing, "", ""
	objective.DefinitionValidFrom, objective.RetentionStart = at, at
	saved = raw
	saved.Quality, saved.Reason = alert.Expired, "full_window_not_retained"
	if !alertSeriesWindowMatches(objective, raw, saved) {
		t.Fatal("retention precedence rejected")
	}
	if raw.Quality != alert.Missing || raw.Good != "" {
		t.Fatal("raw evidence mutated")
	}
}

func TestAlertSeriesSamplingExactExtrema(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var points []AlertSeriesPoint
	for i, counts := range [][2]string{{"9900", "9900"}, {"9500", "9600"}, {"9499.999999999999", "9900"}, {"9900", "9000"}, {"10000", "10000"}, {"9900", "9900"}} {
		quality := alert.Complete
		if i == 2 {
			quality = alert.Partial
		}
		points = append(points, AlertSeriesPoint{EvaluationID: string(rune('a' + i)), Snapshot: alert.SnapshotIdentity{Cutoff: at.Add(time.Duration(i) * time.Minute)}, Condition: alert.ConditionResult{Short: alert.WindowEvidence{Quality: quality, Good: counts[0], Total: "10000"}, Long: alert.WindowEvidence{Quality: quality, Good: counts[1], Total: "10000"}}})
	}
	// With limit five, b is still an omitted bucket extremum.
	// c is a provisional but actual numeric maximum, infinitesimally larger than
	// b despite both rounding to 5. Its condition remains unavailable; sampling
	// neither upgrades its quality nor rounds away its extremum.
	for _, scenario := range []struct {
		limit   int
		ids     []string
		omitted bool
	}{{2, []string{"a", "f"}, true}, {3, []string{"a", "c", "f"}, true}, {4, []string{"a", "c", "d", "f"}, true}, {5, []string{"a", "c", "d", "e", "f"}, true}, {6, []string{"a", "b", "c", "d", "e", "f"}, false}} {
		got, metadata, err := sampleAlertSeries(points, "0.99", at, at.Add(5*time.Minute), scenario.limit)
		var ids []string
		for _, point := range got {
			ids = append(ids, point.EvaluationID)
		}
		if err != nil || !slices.Equal(ids, scenario.ids) || metadata.OmittedExtrema != scenario.omitted {
			t.Errorf("limit%d: IDs%v metadata%+v error%v", scenario.limit, ids, metadata, err)
		}
	}
}
