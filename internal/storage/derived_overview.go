package storage

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func generationStamp(id pgtype.UUID, source, definition, measurement int64, version string, at, published, retention time.Time) api.DerivedGeneration {
	return api.DerivedGeneration{GenerationId: openapi_types.UUID(id.Bytes), SourceGeneration: strconv.FormatInt(source, 10), DefinitionRevision: strconv.FormatInt(definition, 10),
		MeasurementRevision: strconv.FormatInt(measurement, 10), ExclusionRevision: "0", CalculationVersion: version, EvaluatedThrough: at.UTC(), PublishedAt: published.UTC(), RetentionStart: new(retention.UTC()), CalculationLagSeconds: 0}
}

func overviewScopeFilters(input OperationalOverviewInput) (pgtype.UUID, pgtype.Text) {
	var serviceID pgtype.UUID
	if input.ServiceID != "" {
		serviceID, _ = canonicalUUID(input.ServiceID)
	}
	return serviceID, pgtype.Text{String: input.Urgency, Valid: input.Urgency != ""}
}

func canReserveOverview(usage dbgen.SloOverviewSnapshotUsageRow) bool {
	// Preparing results reserve their maximum size; ready pages charge actual bytes.
	return usage.SessionBuilds < sloOverviewSessionLimit && usage.InstallationBuilds < sloOverviewInstallationLimit &&
		usage.SessionBytes <= sloOverviewSessionByteLimit-sloOverviewProjectionByteLimit &&
		usage.InstallationBytes <= sloOverviewInstallationBytes-sloOverviewProjectionByteLimit
}

func findReusableOverview(ctx context.Context, q *dbgen.Queries, input OperationalOverviewInput, now time.Time, actorID pgtype.UUID, role string) (pgtype.UUID, bool, error) {
	if input.Runtime == nil {
		return pgtype.UUID{}, false, nil
	}
	fingerprint, err := overviewScopeFingerprint(ctx, q, input, now)
	if err != nil || fingerprint.DefinitionCount <= int64(input.Limit) {
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

func unavailableOverviewItem(previous api.SloOperationalOverviewItem, now time.Time, reason string) api.SloOperationalOverviewItem {
	objective := previous.Slo
	at := floorSliBoundary(now, time.Duration(objective.CadenceSeconds)*time.Second)
	start := at.Add(-time.Duration(objective.RollingWindowSeconds) * time.Second)
	eligible := maxTime(start, objective.EvaluationValidFrom)
	if eligible.After(at) {
		eligible = at
	}
	history := api.SloOperationalOverviewItemBudgetHistoryState("preparing")
	if reason != "current_calculation_pending" {
		history = "unavailable"
	}
	value := slo.WindowResult{End: at, RequestedStart: start, EffectiveStart: start, EligibleStart: eligible,
		RetentionStart: maxTime(eligible, now.Add(-sli.ObservationRetention)), State: "unavailable", Freshness: "unknown", Performance: "unknown", Reason: reason, Provisional: true}
	current := operationalValue(value)
	current.Window.ActivatedAt, current.Window.EvaluationValidFrom = objective.ActivatedAt, objective.EvaluationValidFrom
	current.Window.HistoricalRequestedBy, current.Window.HistoricalRequestedAt = objective.HistoricalRequestedBy, objective.HistoricalRequestedAt
	burn1h, burn6h := unavailableBurn(3600, at, reason), unavailableBurn(21600, at, reason)
	urgency := slo.ClassifyOperational(slo.OperationalState{QualityState: "unavailable", Freshness: "unknown", PerformanceState: "unknown", Provisional: true})
	reasons, leading := operationalReasons(urgency.Reasons)
	return api.SloOperationalOverviewItem{Slo: objective, CurrentState: new(api.SloOperationalOverviewItemCurrentState("unavailable")), Current: current, Burn1h: burn1h, Burn6h: burn6h,
		Urgency: api.SloOperationalOverviewItemUrgency(urgency.Group), Reasons: reasons, LeadingReason: leading,
		Exhaustion: exhaustionValue(value, burn1h, nil), BudgetHistoryState: &history,
		BudgetSparkline: previous.BudgetSparkline, BudgetHistoryGeneration: previous.BudgetHistoryGeneration}
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
