package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

type AlertFilter struct {
	Scope, SLOID, ServiceID, Search string
	States, Severities              []string
}
type ListAlertsInput struct {
	Runtime               *Runtime
	Proof                 DatasourceProof
	Now                   func() time.Time
	Limit                 int
	Cursor, GroupBy, Sort string
	Filter                AlertFilter
}
type AlertCatalogItem struct {
	Alert                                                                      AlertRecord
	PolicyName, Severity, SLOID, SLOName, ServiceID, ServiceName, EvaluationID string
	Evaluation                                                                 alert.Evaluation
	ScheduledAt, CommittedAt                                                   time.Time
	Processing                                                                 AlertProcessingRecord
}
type AlertGroup struct {
	SLOID, SLOName string
	Total          int64
}
type AlertPage struct {
	Items      []AlertCatalogItem
	Groups     []AlertGroup
	Total      int64
	NextCursor string
}

type alertReadCursor struct {
	Version                 int
	Kind, Filter, Group, ID string
	Priority                int32
	Time, Sequence          int64
}

func decodeAlertReadCursor(value, kind, filter string) (alertReadCursor, error) {
	if value == "" {
		return alertReadCursor{Version: 1, Kind: kind, Filter: filter}, nil
	}
	if len(value) > 1024 || !isASCII(value) {
		return alertReadCursor{}, ErrAlertInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != value {
		return alertReadCursor{}, ErrAlertInvalid
	}
	var cursor alertReadCursor
	if err := json.Unmarshal(raw, &cursor, json.RejectUnknownMembers(true)); err != nil || cursor.Version != 1 || cursor.Kind != kind || cursor.Filter != filter || !canonicalUUIDCheck(cursor.ID) {
		return alertReadCursor{}, ErrAlertInvalid
	}
	return cursor, nil
}
func encodeAlertReadCursor(cursor alertReadCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 1024 {
		return "", ErrAlertInvalid
	}
	return encoded, nil
}

func alertCatalogFilter(input ListAlertsInput) (dbgen.CountAlertCatalogParams, string, error) {
	var result dbgen.CountAlertCatalogParams
	if input.Limit < 1 || input.Limit > 200 || (input.GroupBy != "none" && input.GroupBy != "slo") || (input.Sort != "newest" && input.Sort != "severity") {
		return result, "", ErrAlertInvalid
	}
	allowed := []string{"firing", "acknowledged", "resolved", "disabled", "superseded"}
	switch input.Filter.Scope {
	case "active":
		allowed = allowed[:2]
	case "history":
		allowed = allowed[2:]
	case "all":
	default:
		return result, "", ErrAlertInvalid
	}
	if len(input.Filter.States) == 0 {
		result.States = slices.Clone(allowed)
	} else {
		for _, state := range input.Filter.States {
			if !slices.Contains([]string{"firing", "acknowledged", "resolved", "disabled", "superseded"}, state) {
				return result, "", ErrAlertInvalid
			}
			if slices.Contains(allowed, state) && !slices.Contains(result.States, state) {
				result.States = append(result.States, state)
			}
		}
	}
	result.Severities = slices.Clone(input.Filter.Severities)
	if len(result.Severities) == 0 {
		result.Severities = []string{"low", "medium", "high"}
	}
	for _, severity := range result.Severities {
		if !slices.Contains([]string{"low", "medium", "high"}, severity) {
			return result, "", ErrAlertInvalid
		}
	}
	slices.Sort(result.States)
	slices.Sort(result.Severities)
	result.Severities = slices.Compact(result.Severities)
	for _, target := range []struct {
		value string
		out   *pgtype.UUID
	}{{input.Filter.SLOID, &result.SloID}, {input.Filter.ServiceID, &result.ServiceID}} {
		if target.value != "" {
			id, valid := canonicalUUID(target.value)
			if !valid {
				return result, "", ErrAlertInvalid
			}
			*target.out = id
		}
	}
	result.Search = strings.ToLower(strings.TrimSpace(input.Filter.Search))
	encoded, err := json.Marshal(struct {
		Filter             dbgen.CountAlertCatalogParams
		Group, Sort, Scope string
	}{result, input.GroupBy, input.Sort, input.Filter.Scope}, json.Deterministic(true))
	if err != nil {
		return result, "", err
	}
	digest := sha256.Sum256(encoded)
	return result, hex.EncodeToString(digest[:]), nil
}

func ListAlerts(ctx context.Context, databaseURL string, input ListAlertsInput) (AlertPage, error) {
	page := AlertPage{Items: []AlertCatalogItem{}, Groups: []AlertGroup{}}
	filter, identity, err := alertCatalogFilter(input)
	if err != nil {
		return page, fmt.Errorf("storage: alert catalogue filter: %w", err)
	}
	cursor, err := decodeAlertReadCursor(input.Cursor, "alerts", identity)
	if err != nil {
		return page, fmt.Errorf("storage: alert catalogue cursor: %w", err)
	}
	groupValid := canonicalUUIDCheck(cursor.Group)
	if input.GroupBy == "none" {
		groupValid = cursor.Group == "00000000-0000-0000-0000-000000000000"
	}
	if input.Cursor != "" && (!groupValid || cursor.Priority < 0 || cursor.Priority > 2 || input.Sort == "newest" && cursor.Priority != 0) {
		return page, fmt.Errorf("storage: alert catalogue cursor values: %w", ErrAlertInvalid)
	}
	err = alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		var err error
		page.Total, err = q.CountAlertCatalog(ctx, filter)
		if err != nil {
			return err
		}
		rows, err := q.ListAlertCatalog(ctx, dbgen.ListAlertCatalogParams{GroupSlo: input.GroupBy == "slo", SortSeverity: input.Sort == "severity", States: filter.States, Severities: filter.Severities, SloID: filter.SloID, ServiceID: filter.ServiceID, Search: filter.Search, HasCursor: input.Cursor != "", AfterGroup: uuidOrZero(cursor.Group), AfterPriority: cursor.Priority, AfterTime: cursor.Time, AfterID: uuidOrZero(cursor.ID), PageLimit: int32(input.Limit + 1)})
		if err != nil {
			return err
		}
		var groupIDs []pgtype.UUID
		for _, row := range rows[:min(input.Limit, len(rows))] {
			var occurrence alert.Occurrence
			if err := unmarshalAlertData(row.Payload, &occurrence); err != nil {
				return err
			}
			if occurrence.ID != row.ID.String() || occurrence.PolicyID != row.PolicyID.String() || occurrence.Revision != row.Revision || string(occurrence.State) != row.State {
				return fmt.Errorf("storage: alert catalogue occurrence identity mismatch: %w", ErrAlertInvalid)
			}
			var original AlertOriginalPolicy
			if err := unmarshalAlertData(row.PolicySnapshot, &original); err != nil {
				return err
			}
			var saved struct {
				FormatVersion int
				Evaluation    alert.Evaluation
			}
			if err := unmarshalAlertData(row.Evidence, &saved); err != nil {
				return err
			}
			if saved.FormatVersion != 1 || saved.Evaluation.PolicyID != occurrence.PolicyID || saved.Evaluation.Revision != occurrence.Revision || saved.Evaluation.Snapshot != occurrence.LatestSnapshot {
				return fmt.Errorf("storage: alert catalogue evidence identity mismatch: %w", ErrAlertInvalid)
			}
			processing := alertProcessingRecord(dbgen.AlmanacAlertCursor{PolicyID: row.PolicyID, Revision: row.Revision, Schedulable: row.Schedulable, NextScheduledAt: row.NextScheduledAt, AvailableAt: row.AvailableAt, PendingCutoff: row.PendingCutoff, LastCutoff: row.LastCutoff, LastEvaluationID: row.LastEvaluationID, WaitingReason: row.WaitingReason, FailureAttempts: row.FailureAttempts, Exhausted: row.Exhausted}, normalizedAgentNow(input.Now))
			page.Items = append(page.Items, AlertCatalogItem{Alert: AlertRecord{Occurrence: occurrence, Version: row.Version, Sequence: row.TransitionSequence, OriginalPolicy: original}, PolicyName: row.PolicyName, Severity: row.Severity, SLOID: row.SloID.String(), SLOName: row.SloName, ServiceID: row.ServiceID.String(), ServiceName: row.ServiceName, EvaluationID: row.EvaluationID.String(), Evaluation: saved.Evaluation, ScheduledAt: row.ScheduledAt.Time.UTC(), CommittedAt: row.CommittedAt.Time.UTC(), Processing: processing})
			if !slices.Contains(groupIDs, row.SloID) {
				groupIDs = append(groupIDs, row.SloID)
			}
		}
		if input.GroupBy == "slo" && len(groupIDs) > 0 {
			groups, err := q.GroupAlertCatalog(ctx, dbgen.GroupAlertCatalogParams{States: filter.States, Severities: filter.Severities, SloID: filter.SloID, ServiceID: filter.ServiceID, Search: filter.Search, PageSlos: groupIDs})
			if err != nil {
				return err
			}
			for _, group := range groups {
				page.Groups = append(page.Groups, AlertGroup{SLOID: group.SloID.String(), SLOName: group.SloName, Total: group.Total})
			}
		}
		if len(rows) > input.Limit {
			last := rows[input.Limit-1]
			page.NextCursor, err = encodeAlertReadCursor(alertReadCursor{Version: 1, Kind: "alerts", Filter: identity, Group: last.GroupKey.String(), Priority: last.PriorityKey, Time: last.TimeKey, ID: last.ID.String()})
		}
		return err
	})
	if err != nil {
		return AlertPage{}, err
	}
	return page, nil
}

type AlertProcessingRecord struct {
	PolicyID                        string
	Revision                        int64
	Schedulable, Exhausted          bool
	NextBoundary, AvailableAt       time.Time
	PendingCutoff, LastCutoff       *time.Time
	LastEvaluationID, WaitingReason string
	FailureAttempts                 int32
	LagSeconds                      int64
}

func alertProcessingRecord(row dbgen.AlmanacAlertCursor, now time.Time) AlertProcessingRecord {
	result := AlertProcessingRecord{PolicyID: row.PolicyID.String(), Revision: row.Revision, Schedulable: row.Schedulable, Exhausted: row.Exhausted, NextBoundary: row.NextScheduledAt.Time.UTC(), AvailableAt: row.AvailableAt.Time.UTC(), WaitingReason: row.WaitingReason, FailureAttempts: row.FailureAttempts}
	if row.PendingCutoff.Valid {
		result.PendingCutoff = new(row.PendingCutoff.Time.UTC())
	}
	if row.LastCutoff.Valid {
		result.LastCutoff = new(row.LastCutoff.Time.UTC())
	}
	if row.LastEvaluationID.Valid {
		result.LastEvaluationID = row.LastEvaluationID.String()
	}
	if row.Schedulable {
		result.LagSeconds = max(0, int64(now.Sub(row.NextScheduledAt.Time)/time.Second))
	}
	return result
}
func ReadAlertProcessing(ctx context.Context, databaseURL string, input ReadAlertInput) (AlertProcessingRecord, error) {
	var result AlertProcessingRecord
	err := alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		policy, err := readAlertPolicyRecord(ctx, q, input.ID)
		if err != nil {
			return err
		}
		id, _ := canonicalUUID(policy.ID)
		row, err := q.ReadAlertCursor(ctx, dbgen.ReadAlertCursorParams{PolicyID: id, Revision: policy.EvaluationRevision})
		if err != nil {
			return err
		}
		result = alertProcessingRecord(row, normalizedAgentNow(input.Now))
		return nil
	})
	if err != nil {
		return AlertProcessingRecord{}, err
	}
	return result, nil
}
