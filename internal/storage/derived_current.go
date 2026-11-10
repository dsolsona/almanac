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
	if !row.Current || !row.GenerationID.Valid {
		exhausted, err := q.CurrentPreparationExhausted(ctx, dbgen.CurrentPreparationExhaustedParams{SloID: id, CalculationVersion: operationalCalculationVersion})
		if err != nil {
			return api.SloCurrentStatus{}, err
		}
		if exhausted {
			return api.SloCurrentStatus{}, ErrDerivedFailed
		}
		definition, err := q.GetSlo(ctx, id)
		if err != nil {
			return api.SloCurrentStatus{}, err
		}
		failed, err := hasFailedSourceAt(ctx, q, definition, now, floorSliBoundary(now, time.Duration(definition.CadenceSeconds)*time.Second))
		if err != nil {
			return api.SloCurrentStatus{}, err
		}
		if failed {
			return api.SloCurrentStatus{}, ErrDerivedSourceFailed
		}
		// A clock boundary must not turn a bounded current read into a job wait.
		result, err := readCurrentSloFromAggregates(ctx, q, definition, now)
		if err != nil {
			return api.SloCurrentStatus{}, err
		}
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return api.SloCurrentStatus{}, err
		}
		return result, nil
	}
	var item api.SloOperationalOverviewItem
	if err := json.Unmarshal(row.Payload, &item); err != nil {
		return api.SloCurrentStatus{}, errors.New("storage: invalid current projection")
	}
	result := api.SloCurrentStatus{Slo: item.Slo, Current: item.Current, Urgency: api.SloCurrentStatusUrgency(item.Urgency), LeadingReason: item.LeadingReason, Reasons: item.Reasons,
		Burn1h: item.Burn1h, Burn6h: item.Burn6h, Exhaustion: item.Exhaustion, Generation: api.DerivedGeneration{
			GenerationId: openapi_types.UUID(row.GenerationID.Bytes), SourceGeneration: strconv.FormatInt(row.SourceGeneration.Int64, 10),
			MeasurementRevision: strconv.FormatInt(row.MeasurementRevision.Int64, 10), DefinitionRevision: strconv.FormatInt(row.DefinitionRevision.Int64, 10),
			ExclusionRevision: strconv.FormatInt(row.ExclusionRevision.Int64, 10), CalculationVersion: row.CalculationVersion.String,
			EvaluatedThrough: row.EvaluatedThrough.Time.UTC(), PublishedAt: row.PublishedAt.Time.UTC(), RetentionStart: new(row.RetentionStart.Time.UTC()), CalculationLagSeconds: 0,
		}}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloCurrentStatus{}, err
	}
	return result, nil
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
	generation, err := q.GetDerivedSourceGeneration(ctx, dbgen.GetDerivedSourceGenerationParams{SliID: row.SliID, MeasurementRevision: row.MeasurementRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		generation = 0
	} else if err != nil {
		return api.SloCurrentStatus{}, err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return api.SloCurrentStatus{}, err
	}
	stamp := generationStamp(pgtype.UUID{Bytes: id, Valid: true}, generation, row.DefinitionRevision, row.MeasurementRevision,
		detail.CalculationVersion, detail.EffectiveAt, now, ceilSliBoundary(now.Add(-sli.ObservationRetention), time.Duration(row.CadenceSeconds)*time.Second))
	return api.SloCurrentStatus{Slo: detail.Objective, Current: detail.Current, Urgency: api.SloCurrentStatusUrgency(detail.Urgency),
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
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
