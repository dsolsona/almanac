package storage

import (
	"context"
	"encoding/json/v2"
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

func (r *Runtime) queueDueBudgetHistory(ctx context.Context, now time.Time) error {
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	q := dbgen.New(tx)
	if err := q.LockHistoryAdmission(bounded); err != nil {
		return err
	}
	if _, err := q.QueueDueBudgetHistory(bounded, dbgen.QueueDueBudgetHistoryParams{CalculationVersion: operationalCalculationVersion, Now: timestamp(now)}); err != nil {
		return err
	}
	return tx.Commit(bounded)
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
	return api.SloOperationalOverviewItem{Slo: objective, Current: current, Burn1h: burn1h, Burn6h: burn6h,
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
