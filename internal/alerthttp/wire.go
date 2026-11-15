package alerthttp

import (
	"strconv"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage"
)

func wireID(value string) api.AlertPolicyId {
	id, _ := uuid.Parse(value) // Storage records originate from UUID columns.
	return api.AlertPolicyId(id)
}

func optionalID(value string) *api.AlertPolicyId {
	if value == "" {
		return nil
	}
	return new(wireID(value))
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return new(value)
}

func snapshotDTO(value alert.SnapshotIdentity) api.AlertSnapshotIdentity {
	return api.AlertSnapshotIdentity{SloId: wireID(value.SLOID), SloRevision: strconv.FormatInt(value.SLORevision, 10), SliRevision: strconv.FormatInt(value.SLIRevision, 10), SourceGeneration: strconv.FormatInt(value.SourceGeneration, 10), ExclusionRevision: strconv.FormatInt(value.ExclusionRevision, 10), CalculationVersion: value.CalculationVersion, Cutoff: value.Cutoff.UTC()}
}

func windowDTO(value alert.WindowEvidence) api.AlertWindowEvidence {
	return api.AlertWindowEvidence{Snapshot: snapshotDTO(value.Snapshot), Start: value.Start.UTC(), End: value.End.UTC(), Quality: api.AlertQuality(value.Quality), Reason: optionalString(value.Reason), Good: optionalString(value.Good), Total: optionalString(value.Total)}
}

func conditionDTO(value alert.Condition) api.AlertCondition {
	return api.AlertCondition{Id: value.ID, ShortWindowSeconds: int64(value.Short / time.Second), LongWindowSeconds: int64(value.Long / time.Second), Threshold: value.Threshold}
}

func conditionResultsDTO(values []alert.ConditionResult, target string) []api.AlertConditionResult {
	result := make([]api.AlertConditionResult, 0, len(values))
	for _, value := range values {
		result = append(result, api.AlertConditionResult{Id: value.ID, Threshold: value.Threshold, Outcome: api.AlertConditionOutcome(value.Outcome), Short: windowRatesDTO(value.Short, target), Long: windowRatesDTO(value.Long, target)})
	}
	return result
}

func evaluationDTO(value alert.Evaluation, target string) api.AlertEvaluation {
	result := api.AlertEvaluation{PolicyId: wireID(value.PolicyID), EvaluationRevision: strconv.FormatInt(value.Revision, 10), Snapshot: snapshotDTO(value.Snapshot), Truth: api.AlertTruth(value.Truth), Conditions: conditionResultsDTO(value.Conditions, target)}
	if value.RecoveryComparison != nil {
		result.RecoveryComparison = &api.AlertRecoveryComparison{Snapshot: snapshotDTO(value.RecoveryComparison.Snapshot), Truth: api.AlertTruth(value.RecoveryComparison.Truth), Conditions: conditionResultsDTO(value.RecoveryComparison.Conditions, target)}
	}
	return result
}

func policyDTO(value storage.AlertPolicyRecord) api.AlertPolicy {
	conditions := make([]api.AlertCondition, 0, len(value.Conditions))
	for _, condition := range value.Conditions {
		conditions = append(conditions, conditionDTO(condition))
	}
	return api.AlertPolicy{Id: wireID(value.ID), Name: value.Name, SloId: wireID(value.SLOID), Severity: api.AlertSeverity(value.Severity), Enabled: value.Enabled, Version: strconv.FormatInt(value.Version, 10), EvaluationRevision: strconv.FormatInt(value.EvaluationRevision, 10), Conditions: conditions, CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC()}
}

func recordDTO(value storage.AlertRecord) api.AlertRecord {
	occurrence := value.Occurrence
	result := api.AlertRecord{Id: wireID(occurrence.ID), PolicyId: wireID(occurrence.PolicyID), EvaluationRevision: strconv.FormatInt(occurrence.Revision, 10), State: api.AlertLifecycle(occurrence.State), Version: strconv.FormatInt(value.Version, 10), Sequence: strconv.FormatInt(value.Sequence, 10), FiringSnapshot: snapshotDTO(occurrence.FiringSnapshot), LatestSnapshot: snapshotDTO(occurrence.LatestSnapshot), LatestBreachingSnapshot: snapshotDTO(occurrence.LatestBreachingSnapshot), OpenedDetectedAt: occurrence.OpenedDetectedAt.UTC(), UpdatedAt: occurrence.UpdatedAt.UTC(), AcknowledgedBy: optionalID(occurrence.AcknowledgedBy), OriginalPolicy: api.AlertOriginalPolicy{Name: value.OriginalPolicy.Name, Severity: api.AlertSeverity(value.OriginalPolicy.Severity), SloName: value.OriginalPolicy.SLOName, ServiceName: value.OriginalPolicy.ServiceName, Target: value.OriginalPolicy.Target, RollingWindowSeconds: value.OriginalPolicy.RollingWindowSeconds}}
	if !occurrence.AcknowledgedAt.IsZero() {
		result.AcknowledgedAt = new(occurrence.AcknowledgedAt.UTC())
	}
	if !occurrence.ClosedAt.IsZero() {
		result.ClosedAt = new(occurrence.ClosedAt.UTC())
	}
	return result
}

func evidenceDTO(value storage.AlertEvidenceRecord) api.AlertEvidence {
	objective := value.Input.Objective
	return api.AlertEvidence{Id: wireID(value.ID), ScheduledAt: value.ScheduledAt.UTC(), CommittedAt: value.CommittedAt.UTC(), Evaluation: evaluationDTO(value.Evaluation, objective.Target), Objective: api.AlertObjective{Target: objective.Target, CadenceSeconds: int64(objective.Cadence / time.Second), DefinitionValidFrom: objective.DefinitionValidFrom.UTC(), MeasurementValidFrom: objective.MeasurementValidFrom.UTC(), RetentionStart: objective.RetentionStart.UTC()}}
}

func processingDTO(value storage.AlertProcessingRecord) api.AlertProcessing {
	return api.AlertProcessing{PolicyId: wireID(value.PolicyID), Revision: strconv.FormatInt(value.Revision, 10), Schedulable: value.Schedulable, Exhausted: value.Exhausted, NextBoundary: value.NextBoundary.UTC(), AvailableAt: value.AvailableAt.UTC(), PendingCutoff: value.PendingCutoff, LastCutoff: value.LastCutoff, LastEvaluationId: optionalID(value.LastEvaluationID), WaitingReason: optionalString(value.WaitingReason), FailureAttempts: int(value.FailureAttempts), LagSeconds: value.LagSeconds}
}

func commandDTO(value storage.AlertCommandResult) api.AlertCommandResult {
	result := api.AlertCommandResult{Operation: api.AlertOperation(value.Operation), RequestId: value.RequestID}
	if value.Policy != nil {
		result.Policy = new(policyDTO(*value.Policy))
	}
	if value.Alert != nil {
		result.Alert = new(recordDTO(*value.Alert))
	}
	return result
}

func policyPageDTO(value storage.AlertPolicyPage) api.AlertPolicyPage {
	result := api.AlertPolicyPage{Items: make([]api.AlertPolicyCatalogItem, 0, len(value.Items)), Total: strconv.FormatInt(value.Total, 10), NextCursor: optionalString(value.NextCursor)}
	for _, item := range value.Items {
		result.Items = append(result.Items, api.AlertPolicyCatalogItem{Policy: policyDTO(item.Policy), SloName: item.SLOName, OpenOccurrenceId: optionalID(item.OpenOccurrenceID), Processing: processingDTO(item.Processing)})
	}
	return result
}

func alertPageDTO(value storage.AlertPage) api.AlertPage {
	result := api.AlertPage{Items: make([]api.AlertCatalogItem, 0, len(value.Items)), Groups: make([]api.AlertGroup, 0, len(value.Groups)), Total: strconv.FormatInt(value.Total, 10), NextCursor: optionalString(value.NextCursor)}
	for _, item := range value.Items {
		result.Items = append(result.Items, api.AlertCatalogItem{Alert: recordDTO(item.Alert), PolicyName: item.PolicyName, Severity: api.AlertSeverity(item.Severity), SloId: wireID(item.SLOID), SloName: item.SLOName, ServiceId: wireID(item.ServiceID), ServiceName: item.ServiceName, EvaluationId: wireID(item.EvaluationID), Evaluation: evaluationDTO(item.Evaluation, item.Alert.OriginalPolicy.Target), ScheduledAt: item.ScheduledAt.UTC(), CommittedAt: item.CommittedAt.UTC(), Processing: processingDTO(item.Processing)})
	}
	for _, group := range value.Groups {
		result.Groups = append(result.Groups, api.AlertGroup{SloId: wireID(group.SLOID), SloName: group.SLOName, Total: strconv.FormatInt(group.Total, 10), Firing: strconv.FormatInt(group.Firing, 10)})
	}
	return result
}

func transitionPageDTO(value storage.AlertTransitionPage) api.AlertTransitionPage {
	result := api.AlertTransitionPage{Items: make([]api.AlertTransition, 0, len(value.Items)), NextCursor: optionalString(value.NextCursor)}
	for _, item := range value.Items {
		record := api.AlertTransition{Id: wireID(item.ID), To: api.AlertLifecycle(item.To), Cause: api.AlertTransitionCause(item.Cause), ActorId: optionalID(item.ActorID), Reason: optionalString(item.Reason), EvaluationId: optionalID(item.EvaluationID), RequestId: item.RequestID, Sequence: strconv.FormatInt(item.Sequence, 10), EffectiveAt: item.EffectiveAt.UTC(), DetectedAt: item.DetectedAt.UTC()}
		if item.From != "" {
			record.From = new(api.AlertLifecycle(item.From))
		}
		result.Items = append(result.Items, record)
	}
	return result
}

func rangePageDTO(value storage.AlertRangePage) api.AlertRangePage {
	result := api.AlertRangePage{Items: make([]api.AlertRange, 0, len(value.Items)), NextCursor: optionalString(value.NextCursor)}
	for _, item := range value.Items {
		record := api.AlertRange{Id: wireID(item.ID), Kind: api.AlertRangeKind(item.Kind), Reason: item.Reason, ActorId: optionalID(item.ActorID), RequestId: optionalString(item.RequestID), EvaluationId: optionalID(item.EvaluationID), Start: item.Start.UTC(), End: item.End.UTC(), RecordedAt: item.RecordedAt.UTC(), CadenceSeconds: item.CadenceSeconds}
		if item.Summary != nil {
			summary := item.Summary
			record.Summary = &api.AlertCompactionSummary{First: snapshotDTO(summary.First), Last: snapshotDTO(summary.Last), Truth: api.AlertTruth(summary.Truth), Conditions: make([]api.AlertConditionCompactionSummary, 0, len(summary.Conditions))}
			for _, condition := range summary.Conditions {
				record.Summary.Conditions = append(record.Summary.Conditions, api.AlertConditionCompactionSummary{Id: condition.ID, Threshold: condition.Threshold, Outcome: api.AlertConditionOutcome(condition.Outcome), ShortWindowSeconds: int64(condition.ShortDuration / time.Second), LongWindowSeconds: int64(condition.LongDuration / time.Second), ShortQuality: api.AlertQuality(condition.ShortQuality), LongQuality: api.AlertQuality(condition.LongQuality), ShortReason: optionalString(condition.ShortReason), LongReason: optionalString(condition.LongReason)})
			}
		}
		result.Items = append(result.Items, record)
	}
	return result
}
