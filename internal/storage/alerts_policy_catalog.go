package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"slices"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

type ListAlertPoliciesInput struct {
	Runtime                         *Runtime
	Proof                           DatasourceProof
	Now                             func() time.Time
	Limit                           int
	Cursor, SLOID, Severity, Search string
	Enabled                         *bool
}
type AlertPolicyCatalogItem struct {
	Policy                    AlertPolicyRecord
	SLOName, OpenOccurrenceID string
	Processing                AlertProcessingRecord
}
type AlertPolicyPage struct {
	Items      []AlertPolicyCatalogItem
	Total      int64
	NextCursor string
}

func ListAlertPolicies(ctx context.Context, databaseURL string, input ListAlertPoliciesInput) (AlertPolicyPage, error) {
	page := AlertPolicyPage{Items: []AlertPolicyCatalogItem{}}
	if input.Limit < 1 || input.Limit > 200 || input.Severity != "" && !slices.Contains([]string{"low", "medium", "high"}, input.Severity) {
		return page, ErrAlertInvalid
	}
	filter := dbgen.CountAlertPoliciesParams{Severity: input.Severity, Search: strings.ToLower(strings.TrimSpace(input.Search))}
	if input.SLOID != "" {
		id, valid := canonicalUUID(input.SLOID)
		if !valid {
			return page, ErrAlertInvalid
		}
		filter.SloID = id
	}
	if input.Enabled != nil {
		filter.Enabled = pgtype.Bool{Bool: *input.Enabled, Valid: true}
	}
	encoded, err := json.Marshal(filter, json.Deterministic(true))
	if err != nil {
		return page, err
	}
	digest := sha256.Sum256(encoded)
	identity := hex.EncodeToString(digest[:])
	cursor, err := decodeAlertReadCursor(input.Cursor, "alert-policies", identity)
	if err != nil {
		return page, err
	}
	err = alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		var err error
		page.Total, err = q.CountAlertPolicies(ctx, filter)
		if err != nil {
			return err
		}
		rows, err := q.ListAlertPolicies(ctx, dbgen.ListAlertPoliciesParams{SloID: filter.SloID, Enabled: filter.Enabled, Severity: filter.Severity, Search: filter.Search, HasCursor: input.Cursor != "", AfterCreated: timestamp(time.UnixMicro(cursor.Time)), AfterID: uuidOrZero(cursor.ID), PageLimit: int32(input.Limit + 1)})
		if err != nil {
			return err
		}
		for _, row := range rows[:min(input.Limit, len(rows))] {
			var conditions []alert.Condition
			if err := unmarshalAlertData(row.Conditions, &conditions); err != nil {
				return err
			}
			item := AlertPolicyCatalogItem{Policy: AlertPolicyRecord{ID: row.ID.String(), Name: row.Name, SLOID: row.SloID.String(), Severity: row.Severity, Version: row.Version, EvaluationRevision: row.EvaluationRevision, Enabled: row.Enabled, Conditions: conditions, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()}, SLOName: row.SloName, Processing: alertProcessingRecord(dbgen.AlmanacAlertCursor{PolicyID: row.ID, Revision: row.EvaluationRevision, Schedulable: row.Schedulable, NextScheduledAt: row.NextScheduledAt, AvailableAt: row.AvailableAt, PendingCutoff: row.PendingCutoff, LastCutoff: row.LastCutoff, LastEvaluationID: row.LastEvaluationID, WaitingReason: row.WaitingReason, FailureAttempts: row.FailureAttempts, Exhausted: row.Exhausted}, normalizedAgentNow(input.Now))}
			if row.OpenOccurrenceID.Valid {
				item.OpenOccurrenceID = row.OpenOccurrenceID.String()
			}
			page.Items = append(page.Items, item)
		}
		if len(rows) > input.Limit {
			last := rows[input.Limit-1]
			page.NextCursor, err = encodeAlertReadCursor(alertReadCursor{Version: 1, Kind: "alert-policies", Filter: identity, ID: last.ID.String(), Time: last.CreatedAt.Time.UnixMicro()})
		}
		return err
	})
	if err != nil {
		return AlertPolicyPage{}, err
	}
	return page, nil
}
