package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"go.opentelemetry.io/otel/attribute"
)

type CurrentSloInput struct {
	Runtime *Runtime
	Proof   DatasourceProof
	ID      string
	Now     func() time.Time
}

func ReadCurrentSloStatus(ctx context.Context, databaseURL string, input CurrentSloInput) (api.SloCurrentStatus, error) {
	id, valid := canonicalUUID(input.ID)
	if !valid || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return api.SloCurrentStatus{}, ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloCurrentStatus{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	now := normalizedAgentNow(input.Now)
	row, err := q.GetPublishedCurrentProjection(ctx, dbgen.GetPublishedCurrentProjectionParams{SloID: id, Now: timestamp(now), CalculationVersion: operationalCalculationVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SloCurrentStatus{}, ErrObjectiveNotFound
	}
	if err != nil {
		return api.SloCurrentStatus{}, err
	}
	var result api.SloCurrentStatus
	if row.Current && row.GenerationID.Valid {
		result, err = publishedCurrentStatus(row)
		if err != nil {
			definition, readErr := q.GetSlo(ctx, id)
			if readErr != nil {
				return api.SloCurrentStatus{}, readErr
			}
			row.Usable = false
			result, err = currentStatusDuringPreparation(ctx, q, row, definition, now, ErrDerivedFailed)
		}
	} else {
		definition, readErr := q.GetSlo(ctx, id)
		if readErr != nil {
			return api.SloCurrentStatus{}, readErr
		}
		exhausted, readErr := q.CurrentPreparationExhausted(ctx, dbgen.CurrentPreparationExhaustedParams{SloID: id, CalculationVersion: operationalCalculationVersion})
		if readErr != nil {
			return api.SloCurrentStatus{}, readErr
		}
		failed, readErr := hasFailedSourceAt(ctx, q, definition, now, floorSliBoundary(now, time.Duration(definition.CadenceSeconds)*time.Second))
		if readErr != nil {
			return api.SloCurrentStatus{}, readErr
		}
		switch {
		case exhausted:
			err = ErrDerivedFailed
		case failed:
			err = ErrDerivedSourceFailed
		default:
			// Clock-only changes still use exact bounded reads when available.
			result, err = readCurrentSloFromAggregates(ctx, q, definition, now)
		}
		if errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedFailed) || errors.Is(err, ErrDerivedSourceFailed) {
			result, err = currentStatusDuringPreparation(ctx, q, row, definition, now, err)
		}
	}
	if err != nil {
		return api.SloCurrentStatus{}, err
	}

	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloCurrentStatus{}, err
	}
	return result, nil
}

func publishedCurrentStatus(row dbgen.GetPublishedCurrentProjectionRow) (api.SloCurrentStatus, error) {
	var item api.SloOperationalOverviewItem
	if err := json.Unmarshal(row.Payload, &item); err != nil || !validPublishedCurrentItem(item, row) {
		return api.SloCurrentStatus{}, errors.New("storage: invalid current projection")
	}
	return api.SloCurrentStatus{CurrentState: new(api.SloCurrentStatusCurrentState("ready")), Slo: item.Slo, Current: item.Current,
		Urgency: api.SloCurrentStatusUrgency(item.Urgency), LeadingReason: item.LeadingReason, Reasons: item.Reasons,
		Burn1h: item.Burn1h, Burn6h: item.Burn6h, Exhaustion: item.Exhaustion, Generation: api.DerivedGeneration{
			GenerationId: openapi_types.UUID(row.GenerationID.Bytes), SourceGeneration: strconv.FormatInt(row.SourceGeneration.Int64, 10),
			MeasurementRevision: strconv.FormatInt(row.MeasurementRevision.Int64, 10), DefinitionRevision: strconv.FormatInt(row.DefinitionRevision.Int64, 10),
			ExclusionRevision: strconv.FormatInt(row.ExclusionRevision.Int64, 10), CalculationVersion: row.CalculationVersion.String,
			EvaluatedThrough: row.EvaluatedThrough.Time.UTC(), PublishedAt: row.PublishedAt.Time.UTC(), RetentionStart: new(row.RetentionStart.Time.UTC()),
		}}, nil
}

// A JSON object can decode successfully while missing required nested values.
// Validate its identity and provenance before treating it as retained evidence.
func validPublishedCurrentItem(item api.SloOperationalOverviewItem, row dbgen.GetPublishedCurrentProjectionRow) bool {
	g := item.Generation
	if !item.Urgency.Valid() || item.Slo.Id != openapi_types.UUID(row.SloID.Bytes) ||
		item.Slo.DefinitionRevision != strconv.FormatInt(row.DefinitionRevision.Int64, 10) ||
		g == nil || g.GenerationId != openapi_types.UUID(row.GenerationID.Bytes) ||
		g.DefinitionRevision != item.Slo.DefinitionRevision || g.MeasurementRevision != strconv.FormatInt(row.MeasurementRevision.Int64, 10) ||
		g.ExclusionRevision != strconv.FormatInt(row.ExclusionRevision.Int64, 10) || g.SourceGeneration != strconv.FormatInt(row.SourceGeneration.Int64, 10) ||
		g.CalculationVersion != row.CalculationVersion.String || !g.EvaluatedThrough.Equal(row.EvaluatedThrough.Time) ||
		!timestamp(g.PublishedAt).Time.Equal(row.PublishedAt.Time) || !item.Current.EvaluationAt.Equal(g.EvaluatedThrough) ||
		!item.LeadingReason.Code.Valid() || len(item.Reasons) == 0 || len(item.Reasons) > 14 || !item.Exhaustion.State.Valid() || item.Exhaustion.Assumptions == nil {
		return false
	}
	for _, reason := range item.Reasons {
		if !reason.Code.Valid() {
			return false
		}
	}
	w := item.Current.Window
	if w.RequestedStart.IsZero() || w.EffectiveStart.IsZero() || w.End.IsZero() || w.EligibleStart.IsZero() ||
		w.RetentionStart.IsZero() || w.ActivatedAt.IsZero() || w.EvaluationValidFrom.IsZero() {
		return false
	}
	for _, quality := range []api.SloOperationalQuality{item.Current.Quality, item.Burn1h.Quality, item.Burn6h.Quality, item.Exhaustion.CurrentQuality, item.Exhaustion.OneHourQuality} {
		if !quality.State.Valid() || !quality.Freshness.Valid() {
			return false
		}
	}
	for _, performance := range []api.SloEvaluationPerformance{item.Current.Performance, item.Burn1h.Performance, item.Burn6h.Performance} {
		if !performance.State.Valid() || !performance.BudgetUnit.Valid() {
			return false
		}
	}
	return item.Burn1h.WindowSeconds == 3600 && item.Burn6h.WindowSeconds == 21600 &&
		!item.Burn1h.Start.IsZero() && !item.Burn1h.End.IsZero() && !item.Burn6h.Start.IsZero() && !item.Burn6h.End.IsZero()
}

func currentStatusDuringPreparation(ctx context.Context, q *dbgen.Queries, row dbgen.GetPublishedCurrentProjectionRow, definition dbgen.GetSloRow, now time.Time, preparationErr error) (api.SloCurrentStatus, error) {
	state, reason := api.SloCurrentStatusCurrentState("stale"), "current_calculation_pending"
	if errors.Is(preparationErr, ErrDerivedSourceFailed) {
		state, reason = "failed", "source_aggregate_failed"
	} else if errors.Is(preparationErr, ErrDerivedFailed) {
		state, reason = "failed", "current_calculation_failed"
	}
	if row.Usable && row.GenerationID.Valid {
		result, err := publishedCurrentStatus(row)
		if err == nil {
			result.CurrentState = &state
			result.Generation.CalculationLagSeconds = float32(max(0, now.Sub(result.Generation.EvaluatedThrough).Seconds()))
			if result.Urgency != "needs_attention" {
				result.Urgency = "watch"
			}
			return result, nil
		}
		reason = "current_calculation_failed"
	}
	// There is no compatible prior evaluation. Return explicit unavailable values
	// for this read, not values from a different definition or a successful zero.
	item := unavailableOverviewItem(api.SloOperationalOverviewItem{Slo: sloRecordDTO(sloRecord(definition))}, now, reason)
	generation, err := currentReadGeneration(ctx, q, definition, item.Current.EvaluationAt, now)
	if err != nil {
		return api.SloCurrentStatus{}, err
	}
	return api.SloCurrentStatus{CurrentState: new(api.SloCurrentStatusCurrentState("unavailable")), Slo: item.Slo, Current: item.Current,
		Urgency: api.SloCurrentStatusUrgency(item.Urgency), LeadingReason: item.LeadingReason, Reasons: item.Reasons,
		Burn1h: item.Burn1h, Burn6h: item.Burn6h, Exhaustion: item.Exhaustion, Generation: generation}, nil
}

func currentReadGeneration(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, at, now time.Time) (api.DerivedGeneration, error) {
	generation, err := q.GetDerivedSourceGeneration(ctx, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		generation = 0
	} else if err != nil {
		return api.DerivedGeneration{}, err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return api.DerivedGeneration{}, err
	}
	return generationStamp(pgtype.UUID{Bytes: id, Valid: true}, generation, row.DefinitionRevision, row.MeasurementRevision,
		operationalCalculationVersion, at, now, ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second)), nil
}

func readCurrentSloFromAggregates(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, now time.Time) (api.SloCurrentStatus, error) {
	source, err := q.GetSloOperationalContext(ctx, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return api.SloCurrentStatus{}, err
	}
	detail, err := buildSloOperationalDetail(ctx, q, row, source, OperationalDetailInput{currentOnly: true, Now: func() time.Time { return now }, BurnWindow: 3600})
	if err != nil {
		return api.SloCurrentStatus{}, err
	}
	stamp, err := currentReadGeneration(ctx, q, row, detail.EffectiveAt, now)
	if err != nil {
		return api.SloCurrentStatus{}, err
	}
	return api.SloCurrentStatus{CurrentState: new(api.SloCurrentStatusCurrentState("ready")), Slo: detail.Objective, Current: detail.Current, Urgency: api.SloCurrentStatusUrgency(detail.Urgency),
		LeadingReason: detail.LeadingReason, Reasons: detail.Reasons, Burn1h: detail.Burn1h, Burn6h: detail.Burn6h, Exhaustion: detail.Exhaustion, Generation: stamp}, nil
}

func (r *Runtime) refreshCurrentProjections(ctx context.Context, clock func() time.Time) (resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "derived.current.scan")
	defer func() { finishDerivedTrace(ctx, span, resultErr, "scanned") }()
	conn, err := r.Acquire(ctx, DerivedTraffic)
	if err != nil {
		return err
	}
	ids, err := dbgen.New(conn).ListDueDerivedObjectives(ctx, dbgen.ListDueDerivedObjectivesParams{Now: timestamp(clock()), BatchLimit: 100, CalculationVersion: operationalCalculationVersion})
	conn.Release()
	if err != nil {
		return err
	}
	span.SetAttributes(attribute.Int("almanac.batch.count", len(ids)))
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := r.refreshCurrentProjection(ctx, id, clock); err != nil && !errors.Is(err, ErrDerivedPending) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (r *Runtime) refreshCurrentProjection(ctx context.Context, id pgtype.UUID, clock func() time.Time) (resultErr error) {
	ctx, span := telemetry.Tracer().Start(ctx, "derived.current.evaluate")
	recordCurrentWork(span, id)
	defer func() { finishDerivedTrace(ctx, span, resultErr, "published") }()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.BeginTx(bounded, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer rollbackTransaction(tx)
	if _, err := tx.Exec(bounded, "SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off"); err != nil {
		return err
	}
	q := dbgen.New(tx)
	row, err := q.GetSlo(bounded, id)
	if err != nil {
		return err
	}
	defer func() {
		if resultErr == nil || ctx.Err() != nil {
			return
		}
		cleanup, cancel := currentPreparationCleanupContext(ctx)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, err)
			return
		}
		now := clock()
		facts := derivedFactsRange{sli: row.SliID, revision: row.MeasurementRevision,
			from: now.Add(-max(time.Duration(row.RollingWindowSeconds)*time.Second, 6*time.Hour)), to: now}
		resultErr = errors.Join(resultErr, facts.rebuildIfInconsistent(cleanup, dbgen.New(conn), resultErr, now))
		if derivedTemporary(ctx, resultErr) {
			err := dbgen.New(conn).DeferCurrentPreparation(cleanup, dbgen.DeferCurrentPreparationParams{
				SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
				CalculationVersion: operationalCalculationVersion, Column5: timestamp(clock()),
			})
			if !errors.Is(resultErr, ErrDerivedPending) {
				resultErr = errors.Join(ErrDerivedPending, resultErr)
			}
			resultErr = errors.Join(resultErr, err)
			return
		}
		err := dbgen.New(conn).RecordCurrentPreparationFailure(cleanup, dbgen.RecordCurrentPreparationFailureParams{
			SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
			CalculationVersion: operationalCalculationVersion, Column5: timestamp(clock()), Reason: derivedFailureReason(resultErr, "current_preparation_failed"),
		})
		resultErr = errors.Join(resultErr, err)
	}()
	contextRow, err := q.GetSloOperationalContext(bounded, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return err
	}
	now := clock().UTC()
	detail, err := buildSloOperationalDetail(bounded, q, row, contextRow, OperationalDetailInput{currentOnly: true, Now: func() time.Time { return now }, BurnWindow: 3600})
	if err != nil {
		return err
	}
	sourceGeneration, err := q.GetDerivedSourceGeneration(bounded, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		sourceGeneration = 0
	} else if err != nil {
		return err
	}
	generationID, err := q.InsertDerivedGeneration(bounded, dbgen.InsertDerivedGenerationParams{
		SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, SourceGeneration: sourceGeneration,
		CalculationVersion: detail.CalculationVersion, EvaluatedThrough: timestamp(detail.EffectiveAt), RetentionStart: timestamp(ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second)), PublishedAt: timestamp(now),
	})
	if err != nil {
		return err
	}
	item := api.SloOperationalOverviewItem{Slo: detail.Objective, Current: detail.Current, Urgency: api.SloOperationalOverviewItemUrgency(detail.Urgency),
		LeadingReason: detail.LeadingReason, Reasons: detail.Reasons, Burn1h: detail.Burn1h, Burn6h: detail.Burn6h, Exhaustion: detail.Exhaustion, BudgetSparkline: []api.SloOperationalSample{}}
	stamp := generationStamp(generationID, sourceGeneration, row.DefinitionRevision, row.MeasurementRevision, detail.CalculationVersion, detail.EffectiveAt, now, ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second))
	item.Generation = &stamp
	budgetCache, err := attachExtremaBudget(bounded, q, &item, row, contextRow, now)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		api.SloOperationalOverviewItem
		ValidUntil time.Time `json:"_current_valid_until"`
	}{SloOperationalOverviewItem: item, ValidUntil: currentCacheDeadline(row, contextRow, now, detail.Current.Quality, detail.Burn1h.Quality, detail.Burn6h.Quality)})
	if err != nil {
		return err
	}
	_, err = q.PublishDerivedCurrentProjection(bounded, dbgen.PublishDerivedCurrentProjectionParams{SloID: id, GenerationID: generationID, ServiceID: row.ServiceID,
		SortName: strings.ToLower(row.Name), Urgency: string(detail.Urgency), Payload: payload, BudgetCache: budgetCache})
	if err != nil {
		return err
	}
	if err := q.ClearCurrentPreparationFailure(bounded, id); err != nil {
		return err
	}
	if err := tx.Commit(bounded); err != nil {
		return err
	}
	recordCurrentPublication(span, generationID.String(), sourceGeneration, detail.EffectiveAt)
	return nil
}
