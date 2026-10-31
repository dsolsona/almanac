package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type SloHistoryValuesInput struct {
	Runtime      *Runtime
	Proof        DatasourceProof
	ID           string
	GenerationID string
	Lane         string
	Offset       int
	Limit        int
	Now          func() time.Time
}

func historySeriesPath(lane string) []string {
	switch lane {
	case "attainment":
		return []string{"attainment_series", "samples"}
	case "budget":
		return []string{"budget_series", "samples"}
	case "burn":
		return []string{"burn_series", "samples"}
	case "compliance":
		return []string{"compliance_series", "days"}
	default:
		return nil
	}
}

func ReadSloHistoryValues(ctx context.Context, databaseURL string, input SloHistoryValuesInput) (api.SloHistoryValuePage, error) {
	id, valid := canonicalUUID(input.ID)
	generation, generationValid := canonicalUUID(input.GenerationID)
	path := historySeriesPath(input.Lane)
	if !valid || !generationValid || len(path) == 0 || input.Offset < 0 || input.Offset > 1500 || input.Limit < 1 || input.Limit > 100 || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return api.SloHistoryValuePage{}, ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloHistoryValuePage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if _, err := q.GetSlo(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		return api.SloHistoryValuePage{}, ErrObjectiveNotFound
	} else if err != nil {
		return api.SloHistoryValuePage{}, err
	}
	row, err := q.ReadPreparedSloValuePage(ctx, dbgen.ReadPreparedSloValuePageParams{SloID: id, GenerationID: generation, SeriesPath: path,
		PageOffset: int32(input.Offset), PageLimit: int32(input.Limit), Now: timestamp(normalizedAgentNow(input.Now)), CalculationVersion: operationalCalculationVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SloHistoryValuePage{}, ErrOperationalSnapshotOld
	}
	if err != nil {
		return api.SloHistoryValuePage{}, err
	}
	if row.TotalValues < 0 || row.TotalValues > 1500 || input.Offset > int(row.TotalValues) {
		return api.SloHistoryValuePage{}, ErrObjectiveInvalid
	}
	page := api.SloHistoryValuePage{Lane: api.SloHistoryValuePageLane(input.Lane), Offset: input.Offset, TotalValues: int(row.TotalValues),
		Generation: generationStamp(row.ID, row.SourceGeneration, row.DefinitionRevision, row.MeasurementRevision, row.CalculationVersion, row.EvaluatedThrough.Time, row.PublishedAt.Time, row.RetentionStart.Time),
		Samples:    []api.SloOperationalSample{}, Days: []api.SloDailyComplianceDay{}}
	if input.Lane == "compliance" {
		err = json.Unmarshal(row.PageValues, &page.Days)
	} else {
		err = json.Unmarshal(row.PageValues, &page.Samples)
	}
	if err != nil || len(page.Samples)+len(page.Days) != min(input.Limit, page.TotalValues-input.Offset) {
		return api.SloHistoryValuePage{}, ErrDerivedFailed
	}
	if next := input.Offset + input.Limit; next < page.TotalValues {
		page.NextOffset = new(next)
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloHistoryValuePage{}, err
	}
	return page, nil
}
