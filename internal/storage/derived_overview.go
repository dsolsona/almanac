package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func generationStamp(id pgtype.UUID, source, definition, measurement int64, version string, at, published, retention time.Time) api.DerivedGeneration {
	return api.DerivedGeneration{GenerationId: openapi_types.UUID(id.Bytes), SourceGeneration: strconv.FormatInt(source, 10), DefinitionRevision: strconv.FormatInt(definition, 10),
		MeasurementRevision: strconv.FormatInt(measurement, 10), ExclusionRevision: "0", CalculationVersion: version, EvaluatedThrough: at.UTC(), PublishedAt: published.UTC(), RetentionStart: new(retention.UTC()), CalculationLagSeconds: 0}
}

func attachLatestBudget(ctx context.Context, q *dbgen.Queries, item *api.SloOperationalOverviewItem, row dbgen.GetSloRow, source int64, at, now time.Time) error {
	state := api.SloOperationalOverviewItemBudgetHistoryState("preparing")
	item.BudgetHistoryState = &state
	failed, err := q.BudgetPreparationExhausted(ctx, dbgen.BudgetPreparationExhaustedParams{SloID: row.ID,
		DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion})
	if err != nil {
		return err
	}
	if failed {
		state = "unavailable"
	}
	start := floorSliBoundary(at.Add(-7*24*time.Hour), time.Duration(row.CadenceSeconds)*time.Second)
	cached, err := q.LatestPreparedBudgetSparkline(ctx, dbgen.LatestPreparedBudgetSparklineParams{RetentionStart: timestamp(ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second)), SloID: row.ID, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SpanSeconds: int64(at.Sub(start) / time.Second), At: timestamp(at), CalculationVersion: operationalCalculationVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(cached.BudgetSparkline) == 0 {
		state = "unavailable"
		return nil
	}
	if json.Unmarshal(cached.BudgetSparkline, &item.BudgetSparkline) != nil || len(item.BudgetSparkline) > 120 {
		item.BudgetSparkline = []api.SloOperationalSample{}
		_, err := q.DeleteCorruptPreparedSloHistory(ctx, cached.ID)
		return err
	}
	stamp := generationStamp(cached.ID, cached.SourceGeneration, cached.DefinitionRevision, cached.MeasurementRevision, cached.CalculationVersion, cached.EvaluatedThrough.Time, cached.PublishedAt.Time, ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second))
	stamp.CalculationLagSeconds = float32(max(0, at.Sub(cached.EvaluatedThrough.Time).Seconds()))
	item.BudgetHistoryGeneration = &stamp
	if cached.SourceGeneration == source && cached.EvaluatedThrough.Time.Equal(at) {
		state = "ready"
	}
	return nil
}

func queueVisibleBudgetHistory(ctx context.Context, q *dbgen.Queries, items []api.SloOperationalOverviewItem, now time.Time) error {
	ids := make([]pgtype.UUID, 0, len(items))
	for _, item := range items {
		if item.BudgetHistoryState != nil && *item.BudgetHistoryState == "ready" {
			continue
		}
		ids = append(ids, pgtype.UUID{Bytes: [16]byte(item.Slo.Id), Valid: true})
	}
	if len(ids) == 0 {
		return nil
	}
	if err := q.LockHistoryAdmission(ctx); err != nil {
		return err
	}
	_, err := q.QueueVisibleBudgetHistory(ctx, dbgen.QueueVisibleBudgetHistoryParams{CalculationVersion: operationalCalculationVersion, SloIds: ids, Now: timestamp(now)})
	return err
}

func overviewScopeFilters(input OperationalOverviewInput) (pgtype.UUID, pgtype.Text) {
	var serviceID pgtype.UUID
	if input.ServiceID != "" {
		serviceID, _ = canonicalUUID(input.ServiceID)
	}
	return serviceID, pgtype.Text{String: input.Urgency, Valid: input.Urgency != ""}
}

func findReusableOverview(ctx context.Context, q *dbgen.Queries, input OperationalOverviewInput, now time.Time, actorID pgtype.UUID, role string) (pgtype.UUID, bool, error) {
	if input.Runtime == nil {
		return pgtype.UUID{}, false, nil
	}
	fingerprint, err := overviewScopeFingerprint(ctx, q, input, now)
	if err != nil || !fingerprint.Current || fingerprint.DefinitionCount <= int64(input.Limit) {
		return pgtype.UUID{}, false, err
	}
	serviceID, urgency := overviewScopeFilters(input)
	id, err := q.FindReusableSloOverviewSnapshot(ctx, dbgen.FindReusableSloOverviewSnapshotParams{
		ActorID: actorID, SessionVerifier: input.Proof.SessionVerifier[:], Role: role, QueryText: input.Query,
		ServiceID: serviceID, Urgency: urgency, PageLimit: int32(input.Limit), Now: timestamp(now), Fingerprint: fingerprint.Fingerprint,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, false, nil
	}
	return id, err == nil, err
}

func overviewScopeFingerprint(ctx context.Context, q *dbgen.Queries, input OperationalOverviewInput, now time.Time) (dbgen.OperationalScopeFingerprintRow, error) {
	serviceID, _ := overviewScopeFilters(input)
	result, err := q.OperationalScopeFingerprint(ctx, dbgen.OperationalScopeFingerprintParams{
		CalculationVersion: operationalCalculationVersion, Now: timestamp(now), ServiceID: serviceID,
		QueryText: pgtype.Text{String: input.Query, Valid: input.Query != ""},
	})
	if err == nil && result.DefinitionCount > sloOverviewDefinitionLimit {
		return result, ErrOperationalResource
	}
	return result, err
}
