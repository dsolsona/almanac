package alertclient

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func sameSeriesSnapshot(a, b api.AlertSnapshotIdentity) bool {
	return a.SloId == b.SloId && a.SloRevision == b.SloRevision && a.SliRevision == b.SliRevision && a.SourceGeneration == b.SourceGeneration && a.ExclusionRevision == b.ExclusionRevision && a.CalculationVersion == b.CalculationVersion && a.Cutoff.Equal(b.Cutoff)
}

func validSeriesResponse(data []byte, in Input) bool {
	var value api.AlertSeries
	if json.Unmarshal(data, &value) != nil || !present(data, "policy_id", "evaluation_revision", "objective", "condition", "start", "end", "points", "gaps", "sampling") || !validID(value.PolicyId.String()) || !counter(value.EvaluationRevision, true) || !validConditions([]api.AlertCondition{value.Condition}) || value.Condition.Id != in.ConditionID || !validDecimal(value.Objective.Target) || value.Objective.CadenceSeconds <= 0 || value.Objective.DefinitionValidFrom.IsZero() || value.Objective.MeasurementValidFrom.IsZero() || value.Objective.RetentionStart.IsZero() || value.Start.IsZero() || value.End.IsZero() || value.End.Before(value.Start) || value.End.Sub(value.Start) > 24*time.Hour {
		return false
	}
	if in.Start != "" {
		start, _ := time.Parse(time.RFC3339Nano, in.Start)
		if !start.Equal(value.Start) {
			return false
		}
	}
	if in.End != "" {
		end, _ := time.Parse(time.RFC3339Nano, in.End)
		if !end.Equal(value.End) {
			return false
		}
	}
	if in.Start == "" && !value.Start.Equal(value.End.Add(-2*time.Hour)) {
		return false
	}
	var raw struct {
		Sampling jsontext.Value   `json:"sampling"`
		Points   []jsontext.Value `json:"points"`
		Gaps     []jsontext.Value `json:"gaps"`
	}
	if json.Unmarshal(data, &raw) != nil || !present(raw.Sampling, "total", "returned", "reduced", "omitted_extrema", "method") {
		return false
	}
	sample := value.Sampling
	if sample.Total < 0 || sample.Total > 1441 || sample.Returned != len(value.Points) || sample.Returned > in.Limit || sample.Total < sample.Returned || sample.Reduced != (sample.Total > sample.Returned) || sample.OmittedExtrema && !sample.Reduced || !sample.Method.Valid() || sample.Reduced != (sample.Method == "extrema_priority") {
		return false
	}
	// This upper bound follows the frozen bounded storage reader: at most one
	// unavailable gap per source point, one interior gap per adjacent pair, one
	// per scoped work range, and four boundary/cursor intervals. Never truncate.
	if len(value.Gaps) > 3*1441+4 {
		return false
	}
	seen := map[string]bool{}
	for i, p := range value.Points {
		var numeric struct {
			Condition struct {
				Short map[string]jsontext.Value `json:"short"`
				Long  map[string]jsontext.Value `json:"long"`
			} `json:"condition"`
		}
		if json.Unmarshal(raw.Points[i], &numeric) != nil {
			return false
		}
		for _, window := range []map[string]jsontext.Value{numeric.Condition.Short, numeric.Condition.Long} {
			for _, key := range []string{"good", "total", "bad", "observed_error_rate", "allowed_error_rate", "burn"} {
				if raw, exists := window[key]; exists && string(raw) == "null" {
					return false
				}
			}
		}
		if !present(raw.Points[i], "evaluation_id", "snapshot", "scheduled_at", "committed_at", "condition") || !validID(p.EvaluationId.String()) || seen[p.EvaluationId.String()] || !validSnapshot(p.Snapshot) || p.Snapshot.Cutoff.Before(value.Start) || p.Snapshot.Cutoff.After(value.End) || i > 0 && !p.Snapshot.Cutoff.After(value.Points[i-1].Snapshot.Cutoff) || p.ScheduledAt.IsZero() || p.ScheduledAt.Before(p.Snapshot.Cutoff) || p.CommittedAt.Before(p.Snapshot.Cutoff) || !validResults([]api.AlertConditionResult{p.Condition}) || p.Condition.Id != value.Condition.Id || p.Condition.Threshold != value.Condition.Threshold || !sameSeriesSnapshot(p.Snapshot, p.Condition.Short.Snapshot) || !sameSeriesSnapshot(p.Snapshot, p.Condition.Long.Snapshot) || p.Condition.Short.End.Sub(p.Condition.Short.Start) != time.Duration(value.Condition.ShortWindowSeconds)*time.Second || p.Condition.Long.End.Sub(p.Condition.Long.Start) != time.Duration(value.Condition.LongWindowSeconds)*time.Second {
			return false
		}
		if i > 0 {
			first := value.Points[0].Snapshot
			if p.Snapshot.SloId != first.SloId || p.Snapshot.SloRevision != first.SloRevision || p.Snapshot.SliRevision != first.SliRevision || p.Snapshot.CalculationVersion != first.CalculationVersion {
				return false
			}
		}
		for _, w := range []api.AlertWindowEvidence{p.Condition.Short, p.Condition.Long} {
			for _, numeric := range []*string{w.Bad, w.ObservedErrorRate, w.AllowedErrorRate, w.Burn} {
				if numeric != nil && !validDecimal(*numeric) {
					return false
				}
			}
		}
		seen[p.EvaluationId.String()] = true
	}
	for i, g := range value.Gaps {
		if !present(raw.Gaps[i], "start", "end", "start_inclusive", "end_inclusive", "basis", "kind", "reason") || g.Start.IsZero() || g.End.IsZero() || g.End.Before(g.Start) || !g.Basis.Valid() || !g.Kind.Valid() || g.RangeId != nil && !validID(g.RangeId.String()) {
			return false
		}
		// Zero-duration unavailable samples are meaningful only as closed points.
		if g.Start.Equal(g.End) && (!g.StartInclusive || !g.EndInclusive) {
			return false
		}
		switch g.Kind {
		case "not_retained", "unavailable", "compacted_evaluations":
			if g.Basis != "effective_cutoff" {
				return false
			}
		default:
			if g.Basis != "scheduled_boundary" {
				return false
			}
		}
	}
	return true
}
