package alertclient

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"github.com/dsolsona/almanac/internal/api"
	"math/big"
)

func validPolicy(p api.AlertPolicy) bool {
	return validID(p.Id.String()) && validID(p.SloId.String()) && validName(p.Name) && contains(string(p.Severity), "low", "medium", "high") && counter(p.Version, true) && counter(p.EvaluationRevision, true) && !p.CreatedAt.IsZero() && !p.UpdatedAt.Before(p.CreatedAt) && validConditions(p.Conditions)
}
func validSnapshot(s api.AlertSnapshotIdentity) bool {
	return validID(s.SloId.String()) && counter(s.SloRevision, true) && counter(s.SliRevision, true) && counter(s.SourceGeneration, false) && counter(s.ExclusionRevision, false) && s.CalculationVersion != "" && !s.Cutoff.IsZero()
}
func validRecord(a api.AlertRecord) bool {
	if !validID(a.Id.String()) || !validID(a.PolicyId.String()) || !counter(a.Version, true) || !counter(a.Sequence, true) || !counter(a.EvaluationRevision, true) || !validSnapshot(a.FiringSnapshot) || !validSnapshot(a.LatestSnapshot) || !validSnapshot(a.LatestBreachingSnapshot) || a.OpenedDetectedAt.IsZero() || a.UpdatedAt.Before(a.OpenedDetectedAt) || !contains(string(a.State), "firing", "acknowledged", "resolved", "disabled", "superseded") || !validName(a.OriginalPolicy.Name) || !validDecimal(a.OriginalPolicy.Target) {
		return false
	}
	active := a.State == "firing" || a.State == "acknowledged"
	if active != (a.ClosedAt == nil) || (a.AcknowledgedAt == nil) != (a.AcknowledgedBy == nil) || a.State == "acknowledged" && a.AcknowledgedAt == nil {
		return false
	}
	return a.AcknowledgedBy == nil || validID(a.AcknowledgedBy.String())
}
func validDecimal(v string) bool { return len(v) <= 96 && decimal.MatchString(v) }
func validWindow(w api.AlertWindowEvidence) bool {
	if !validSnapshot(w.Snapshot) || !w.End.After(w.Start) || !w.End.Equal(w.Snapshot.Cutoff) || !contains(string(w.Quality), "complete", "missing", "partial", "stale", "zero_traffic", "warmup", "expired", "excluded", "calculation_failed") || (w.Good == nil) != (w.Total == nil) {
		return false
	}
	if w.Good == nil {
		return w.Quality != "complete"
	}
	if !validDecimal(*w.Good) || !validDecimal(*w.Total) {
		return false
	}
	g, _ := new(big.Rat).SetString(*w.Good)
	total, _ := new(big.Rat).SetString(*w.Total)
	return g.Cmp(total) <= 0 && (w.Quality != "complete" || total.Sign() > 0)
}
func validResults(values []api.AlertConditionResult) bool {
	if len(values) < 1 || len(values) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range values {
		if c.Id == "" || seen[c.Id] || !validThreshold(c.Threshold) || !contains(string(c.Outcome), "matching", "not_matching", "unavailable") || !validWindow(c.Short) || !validWindow(c.Long) {
			return false
		}
		seen[c.Id] = true
	}
	return true
}
func validEvaluation(e api.AlertEvaluation) bool {
	return validID(e.PolicyId.String()) && counter(e.EvaluationRevision, true) && validSnapshot(e.Snapshot) && contains(string(e.Truth), "breaching", "clear", "indeterminate") && validResults(e.Conditions) && (e.RecoveryComparison == nil || validSnapshot(e.RecoveryComparison.Snapshot) && validResults(e.RecoveryComparison.Conditions) && contains(string(e.RecoveryComparison.Truth), "breaching", "clear", "indeterminate"))
}
func validProcessing(p api.AlertProcessing) bool {
	return validID(p.PolicyId.String()) && counter(p.Revision, true) && !p.AvailableAt.IsZero() && !p.NextBoundary.IsZero() && p.FailureAttempts >= 0 && p.LagSeconds >= 0
}
func validCommand(r api.AlertCommandResult, operation, id string) bool {
	if string(r.Operation) != operation || !validRequestID(r.RequestId) {
		return false
	}
	if operation == "alert.acknowledge" {
		return r.Policy == nil && r.Alert != nil && validRecord(*r.Alert) && (id == "" || r.Alert.Id.String() == id)
	}
	return r.Policy != nil && validPolicy(*r.Policy) && (id == "" || r.Policy.Id.String() == id) && (r.Alert == nil || validRecord(*r.Alert) && r.Alert.PolicyId == r.Policy.Id)
}
func validCursor(v *string) bool { return v == nil || ascii(*v, 1, 1024, false) }
func present(data []byte, names ...string) bool {
	var fields map[string]jsontext.Value
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	for _, name := range names {
		v, ok := fields[name]
		if !ok || string(v) == "null" {
			return false
		}
	}
	return true
}

// Decode into generated string-typed evidence models and return original bytes;
// never round-trip exact counters through map[string]any/float64. Unknown additive
// response fields remain compatible. Required identities and envelope shape fail
// closed, including command receipts whose saved resource is older than now.
func validResponse(data []byte, in Input, tag string) bool {
	if !jsontext.Value(data).IsValid() {
		return false
	}
	decode := func(v any) bool { return json.Unmarshal(data, v) == nil }
	if in.Mutation() {
		var v api.AlertCommandResult
		if !decode(&v) || !validCommand(v, in.CommandOperation(), in.ID) {
			return false
		}
		if v.Policy != nil {
			return tag == `"alert-policy:`+v.Policy.Id.String()+":"+v.Policy.Version+`"`
		}
		return tag == `"alert-occurrence:`+v.Alert.Id.String()+":"+v.Alert.Version+`"`
	}
	switch in.Kind + " " + in.Action {
	case "alert-policies get":
		var v api.AlertPolicy
		return decode(&v) && present(data, "enabled", "conditions") && validPolicy(v) && v.Id.String() == in.ID && tag == `"alert-policy:`+in.ID+":"+v.Version+`"`
	case "alerts get":
		var v api.AlertRecord
		return decode(&v) && validRecord(v) && v.Id.String() == in.ID && tag == `"alert-occurrence:`+in.ID+":"+v.Version+`"`
	case "alert-policies list":
		var v api.AlertPolicyPage
		if !decode(&v) || !present(data, "items", "total") || !counter(v.Total, false) || !validCursor(v.NextCursor) || len(v.Items) > in.Limit {
			return false
		}
		for _, item := range v.Items {
			if !validPolicy(item.Policy) || !validProcessing(item.Processing) || item.Policy.Id != item.Processing.PolicyId {
				return false
			}
		}
		return true
	case "alerts list":
		var v api.AlertPage
		if !decode(&v) || !present(data, "items", "groups", "total") || !counter(v.Total, false) || !validCursor(v.NextCursor) || len(v.Items) > in.Limit {
			return false
		}
		for _, item := range v.Items {
			if !validRecord(item.Alert) || !validEvaluation(item.Evaluation) || !validProcessing(item.Processing) || !validID(item.EvaluationId.String()) || item.ScheduledAt.IsZero() || item.CommittedAt.IsZero() {
				return false
			}
		}
		for _, group := range v.Groups {
			if !validID(group.SloId.String()) || !counter(group.Total, false) || !counter(group.Firing, false) {
				return false
			}
			total, _ := new(big.Int).SetString(group.Total, 10)
			firing, _ := new(big.Int).SetString(group.Firing, 10)
			if firing.Cmp(total) > 0 {
				return false
			}
		}
		return true
	case "alerts processing":
		var v api.AlertProcessing
		return decode(&v) && present(data, "schedulable", "exhausted", "failure_attempts", "lag_seconds") && validProcessing(v) && v.PolicyId.String() == in.ID
	case "alerts evidence":
		var v api.AlertEvidence
		return decode(&v) && validID(v.Id.String()) && validEvaluation(v.Evaluation) && !v.ScheduledAt.IsZero() && !v.CommittedAt.IsZero() && validDecimal(v.Objective.Target) && v.Objective.CadenceSeconds > 0
	case "alerts command-result":
		var v api.AlertCommandLookup
		if !decode(&v) {
			return false
		}
		switch v.Status {
		case "unknown", "expired":
			return v.Result == nil
		case "committed":
			return v.Result != nil && validCommand(*v.Result, in.Operation, "")
		default:
			return false
		}
	case "alerts series":
		return validSeriesResponse(data, in)
	case "alerts transitions":
		var v api.AlertTransitionPage
		if !decode(&v) || !present(data, "items") || !validCursor(v.NextCursor) || len(v.Items) > in.Limit {
			return false
		}
		for _, item := range v.Items {
			if !validID(item.Id.String()) || !counter(item.Sequence, true) || item.EffectiveAt.IsZero() || item.DetectedAt.Before(item.EffectiveAt) || !contains(string(item.To), "firing", "acknowledged", "resolved", "disabled", "superseded") || !contains(string(item.Cause), "condition_matched", "acknowledged", "conditions_cleared", "resolved_after_maintenance_change", "policy_disabled", "slo_archived", "definition_superseded") {
				return false
			}
		}
		return true
	case "alert-policies ranges":
		var v api.AlertRangePage
		if !decode(&v) || !present(data, "items") || !validCursor(v.NextCursor) || len(v.Items) > in.Limit {
			return false
		}
		for _, item := range v.Items {
			if !validID(item.Id.String()) || !item.End.After(item.Start) || item.RecordedAt.IsZero() || item.CadenceSeconds != 60 || item.Reason == "" || !contains(string(item.Kind), "repeated_cutoff", "unreconstructed_gap", "administrative_cancellation", "compacted_evaluations") {
				return false
			}
			if item.Summary != nil && (!validSnapshot(item.Summary.First) || !validSnapshot(item.Summary.Last) || len(item.Summary.Conditions) == 0) {
				return false
			}
		}
		return true
	}
	return false
}
