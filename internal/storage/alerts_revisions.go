package storage

import (
	"context"
	"math"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// The public command supplies no activation time. Explicit boundaries remain
// available to internal callers, but cannot precede authority or source activation.
func alertActivationBoundary(requested, now, sourceActivation time.Time) (time.Time, error) {
	if now.IsZero() {
		return time.Time{}, ErrAlertInvalid
	}
	minimum := now
	if sourceActivation.After(minimum) {
		minimum = sourceActivation
	}
	if requested.IsZero() {
		requested = floorSliBoundary(minimum, time.Minute)
		if requested.Before(minimum) {
			requested = requested.Add(time.Minute)
		}
	}
	if requested.Before(minimum) || requested.Nanosecond() != 0 || requested.Unix()%60 != 0 {
		return time.Time{}, ErrAlertInvalid
	}
	return requested.UTC(), nil
}

func UpdateAlertPolicy(ctx context.Context, databaseURL string, input UpdateAlertPolicyInput) (AlertCommandResult, error) {
	request := struct {
		ID                     string
		ExpectedVersion        int64
		Name, Severity, SLOID  *string
		Conditions             *[]alert.Condition
		Reason                 string
		FirstBoundary          time.Time
		ConfirmNon30DayPresets bool
	}{input.ID, input.ExpectedVersion, input.Name, input.Severity, input.SLOID, input.Conditions, input.Reason, input.FirstBoundary, input.ConfirmNon30DayPresets}
	return executeAlertCommand(ctx, databaseURL, input.AlertCommandInput, "alert-policy.update", request, func(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, actor string) (AlertCommandResult, error) {
		return changeAlertPolicy(ctx, q, input, actor, false)
	})
}

func EnableAlertPolicy(ctx context.Context, databaseURL string, input EnableAlertPolicyInput) (AlertCommandResult, error) {
	request := struct {
		ID              string
		ExpectedVersion int64
		Reason          string
		FirstBoundary   time.Time
	}{input.ID, input.ExpectedVersion, input.Reason, input.FirstBoundary}
	return executeAlertCommand(ctx, databaseURL, input.AlertCommandInput, "alert-policy.enable", request, func(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, actor string) (AlertCommandResult, error) {
		return changeAlertPolicy(ctx, q, UpdateAlertPolicyInput{AlertCommandInput: input.AlertCommandInput, ID: input.ID, ExpectedVersion: input.ExpectedVersion, Reason: input.Reason, FirstBoundary: input.FirstBoundary, Limits: input.Limits}, actor, true)
	})
}

// Lock every source authority in one order, including a rebind's old authority.
// The pre-lock binding is only a discovery hint; it is rechecked under the policy
// lock, and target revisions are checked against the locked current authorities.
func lockAlertPolicyTargets(ctx context.Context, q *dbgen.Queries, id pgtype.UUID, targetID *string, expected int64) (dbgen.AlmanacAlertPolicy, dbgen.GetSloRow, error) {
	var empty dbgen.AlmanacAlertPolicy
	var noTarget dbgen.GetSloRow
	binding, err := q.ReadAlertBinding(ctx, id)
	if err != nil {
		return empty, noTarget, err
	}
	target := binding.SloID
	if targetID != nil {
		var valid bool
		target, valid = canonicalUUID(*targetID)
		if !valid {
			return empty, noTarget, ErrAlertInvalid
		}
	}
	row, err := q.GetSlo(ctx, target)
	if err != nil {
		return empty, noTarget, err
	}
	uniqueIDs := func(a, b pgtype.UUID) []string {
		ids := []string{a.String(), b.String()}
		slices.Sort(ids)
		return slices.Compact(ids)
	}
	var measurement int64
	for _, value := range uniqueIDs(binding.SliID, row.SliID) {
		key, _ := canonicalUUID(value)
		version, err := q.LockAlertSli(ctx, key)
		if err != nil {
			return empty, noTarget, err
		}
		if key == row.SliID {
			measurement = version
		}
	}
	var definition int64
	for _, value := range uniqueIDs(binding.SloID, target) {
		key, _ := canonicalUUID(value)
		version, err := q.LockAlertSlo(ctx, key)
		if err != nil {
			return empty, noTarget, err
		}
		if key == target {
			definition = version
		}
	}
	policy, err := q.LockAlertPolicy(ctx, id)
	if err != nil {
		return empty, noTarget, err
	}
	if policy.Version != expected || policy.SloID != binding.SloID || policy.EvaluationRevision != binding.EvaluationRevision || row.MeasurementRevision != measurement || row.DefinitionRevision != definition {
		return empty, noTarget, ErrAlertStale
	}
	return policy, row, nil
}

func sameAlertConditions(a, b []alert.Condition) bool {
	return slices.EqualFunc(a, b, func(a, b alert.Condition) bool {
		if a.ID != b.ID || a.Short != b.Short || a.Long != b.Long {
			return false
		}
		// Full policy validation has already bounded these decimal strings. Their
		// spelling (including leading/trailing zeros) is not an evaluation change.
		return sameAlertThreshold(a.Threshold, b.Threshold)
	})
}

func sameAlertThreshold(a, b string) bool {
	left, leftOK := new(big.Rat).SetString(a)
	right, rightOK := new(big.Rat).SetString(b)
	return leftOK && rightOK && left.Cmp(right) == 0
}

// Called only after complete bounded policy validation and target-authority locks.
func validateAlertPresetConfirmation(target dbgen.GetSloRow, conditions []alert.Condition, confirmed bool) error {
	if confirmed || target.RollingWindowSeconds == 30*24*60*60 {
		return nil
	}
	for _, condition := range conditions {
		fast := condition.Short == 5*time.Minute && condition.Long == time.Hour && sameAlertThreshold(condition.Threshold, "14.4")
		sustained := condition.Short == 30*time.Minute && condition.Long == 6*time.Hour && sameAlertThreshold(condition.Threshold, "6")
		if fast || sustained {
			return ErrAlertPresetConfirmation
		}
	}
	return nil
}

func changeAlertPolicy(ctx context.Context, q *dbgen.Queries, input UpdateAlertPolicyInput, actor string, enable bool) (AlertCommandResult, error) {
	id, ok := canonicalUUID(input.ID)
	if !ok || input.ExpectedVersion <= 0 {
		return AlertCommandResult{}, ErrAlertInvalid
	}
	policy, target, err := lockAlertPolicyTargets(ctx, q, id, input.SLOID, input.ExpectedVersion)
	if err != nil {
		return AlertCommandResult{}, err
	}
	current, err := readAlertPolicyRecord(ctx, q, input.ID)
	if err != nil {
		return AlertCommandResult{}, err
	}
	name, severity, conditions := policy.Name, policy.Severity, current.Conditions
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if input.Severity != nil {
		severity = *input.Severity
	}
	if input.Conditions != nil {
		conditions = slices.Clone(*input.Conditions)
	}
	if name == "" || (severity != "low" && severity != "medium" && severity != "high") {
		return AlertCommandResult{}, ErrAlertInvalid
	}
	now := normalizedAgentNow(input.Now)
	if now.IsZero() || now.Before(policy.UpdatedAt.Time) {
		return AlertCommandResult{}, ErrAlertInvalid
	}
	if err := alert.Validate(alert.PolicyRevision{ID: input.ID, Revision: policy.EvaluationRevision, Conditions: conditions}, alertObjective(target, now), input.Limits); err != nil {
		return AlertCommandResult{}, err
	}
	semantic := target.ID != policy.SloID || !sameAlertConditions(conditions, current.Conditions)
	if semantic {
		if err := validateAlertPresetConfirmation(target, conditions, input.ConfirmNon30DayPresets); err != nil {
			return AlertCommandResult{}, err
		}
	}
	replace := semantic || (enable && !policy.Enabled)
	if replace && strings.TrimSpace(input.Reason) == "" {
		return AlertCommandResult{}, ErrAlertInvalid
	}
	if !replace && name == policy.Name && severity == policy.Severity {
		return AlertCommandResult{Policy: &current}, nil
	}
	if policy.Version == math.MaxInt64 || (replace && policy.EvaluationRevision == math.MaxInt64) {
		return AlertCommandResult{}, ErrAlertInvalid
	}
	revision, enabled := policy.EvaluationRevision, policy.Enabled
	var stored alertStoredOccurrence
	if replace {
		cursor, err := q.LockAlertCursor(ctx, dbgen.LockAlertCursorParams{PolicyID: id, Revision: revision})
		if err != nil {
			return AlertCommandResult{}, err
		}
		if cursor.OpenOccurrenceID.Valid {
			row, err := q.LockAlertOccurrence(ctx, cursor.OpenOccurrenceID)
			if err != nil {
				return AlertCommandResult{}, err
			}
			stored, err = decodeAlertOccurrence(row)
			if err != nil {
				return AlertCommandResult{}, err
			}
		}
		now = normalizedAgentNow(input.Now)
		if now.IsZero() || now.Before(policy.UpdatedAt.Time) {
			return AlertCommandResult{}, ErrAlertInvalid
		}
		boundary, err := alertActivationBoundary(input.FirstBoundary, now, target.ActivatedAt.Time)
		if err != nil {
			return AlertCommandResult{}, err
		}
		actorID, _ := canonicalUUID(actor)
		stored, err = cancelAlertRevision(ctx, q, policy, cursor, stored, actorID, actor, input.Reason, input.RequestID, alert.DefinitionSuperseded, now, now)
		if err != nil {
			return AlertCommandResult{}, err
		}
		revision++
		enabled = enabled || enable
		encoded, err := marshalAlertData(conditions)
		if err != nil {
			return AlertCommandResult{}, err
		}
		if err := q.InsertAlertPolicyRevision(ctx, dbgen.InsertAlertPolicyRevisionParams{PolicyID: id, Revision: revision, SloID: target.ID, DefinitionRevision: target.DefinitionRevision, CalculationVersion: operationalCalculationVersion, Conditions: encoded, CreatedAt: timestamp(now)}); err != nil {
			return AlertCommandResult{}, err
		}
		if err := q.InsertAlertCursor(ctx, dbgen.InsertAlertCursorParams{PolicyID: id, Revision: revision, OriginAt: timestamp(boundary), Schedulable: enabled}); err != nil {
			return AlertCommandResult{}, err
		}
	}
	n, err := q.UpdateAlertPolicyIdentity(ctx, dbgen.UpdateAlertPolicyIdentityParams{ID: id, Version: input.ExpectedVersion, Name: name, Severity: severity, SloID: target.ID, EvaluationRevision: revision, Enabled: enabled, UpdatedAt: timestamp(now)})
	if err != nil {
		return AlertCommandResult{}, err
	}
	if n != 1 {
		return AlertCommandResult{}, ErrAlertStale
	}
	action := "alert-policy.updated"
	if enable {
		action = "alert-policy.enabled"
	}
	if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: actor, SubjectID: id, Action: action, OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return AlertCommandResult{}, err
	}
	updated, err := readAlertPolicyRecord(ctx, q, input.ID)
	if err != nil {
		return AlertCommandResult{}, err
	}
	result := AlertCommandResult{Policy: &updated}
	if stored.Occurrence != nil {
		record, err := readAlertRecord(ctx, q, stored.Occurrence.ID)
		if err != nil {
			return AlertCommandResult{}, err
		}
		result.Alert = &record
	}
	return result, nil
}

// The caller holds policy, cursor and any open occurrence locks in that order.
// Cancellation includes each due grid boundary through effective, never future
// work or observed recovery. Old claims and a pinned canonical cutoff are fenced.
func cancelAlertRevision(ctx context.Context, q *dbgen.Queries, policy dbgen.AlmanacAlertPolicy, cursor dbgen.AlmanacAlertCursor, stored alertStoredOccurrence, actorID pgtype.UUID, actor, reason, requestID string, cause alert.Cause, effective, detected time.Time) (alertStoredOccurrence, error) {
	if stored.Occurrence != nil {
		transition, err := alert.ApplyAdministrativeChange(stored.Occurrence, cause, actor, reason, effective, detected)
		if err != nil {
			return alertStoredOccurrence{}, err
		}
		previous := stored.Version
		stored.Occurrence = transition.Next
		stored.Version++
		stored.Sequence++
		if err := writeAlertOccurrence(ctx, q, stored, previous); err != nil {
			return alertStoredOccurrence{}, err
		}
		if err := insertAlertTransition(ctx, q, stored, transition, pgtype.UUID{}, requestID); err != nil {
			return alertStoredOccurrence{}, err
		}
	}
	end := floorSliBoundary(effective, time.Minute).Add(time.Minute)
	next := cursor.NextScheduledAt
	if cursor.Schedulable && next.Time.Before(end) {
		if err := q.InsertAlertRange(ctx, dbgen.InsertAlertRangeParams{ID: newAlertID(), PolicyID: policy.ID, Revision: policy.EvaluationRevision, StartsAt: next, EndsAt: timestamp(end), CadenceSeconds: 60, Kind: "administrative_cancellation", Reason: reason, RecordedAt: timestamp(detected), ReferenceEvaluationID: cursor.LastEvaluationID, ActorID: actorID, RequestID: requestID}); err != nil {
			return alertStoredOccurrence{}, err
		}
		next = timestamp(end)
	}
	n, err := q.CancelAlertCursor(ctx, dbgen.CancelAlertCursorParams{PolicyID: policy.ID, Revision: policy.EvaluationRevision, NextScheduledAt: next, Version: cursor.Version, WaitingReason: string(cause)})
	if err != nil {
		return alertStoredOccurrence{}, err
	}
	if n != 1 {
		return alertStoredOccurrence{}, ErrAlertStale
	}
	return stored, nil
}
