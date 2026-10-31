package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type RetrySloPreparationInput struct {
	Runtime   *Runtime
	Proof     DatasourceProof
	SloID     string
	JobID     string
	RequestID string
	Now       func() time.Time
}

// RetrySloPreparation gives the addressed exhausted job another finite budget.
// Unrelated objectives and source keys retain their failure state and counters.
func RetrySloPreparation(ctx context.Context, databaseURL string, input RetrySloPreparationInput) error {
	id, valid := canonicalUUID(input.SloID)
	jobID, jobValid := canonicalUUID(input.JobID)
	if !valid || !jobValid || input.Now == nil || input.RequestID == "" || input.Proof.SessionVerifier == ([32]byte{}) {
		return ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{}, "administrator", "editor")
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	row, err := q.GetSlo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrObjectiveNotFound
	} else if err != nil {
		return err
	}
	if err := q.LockHistoryAdmission(ctx); err != nil {
		return err
	}
	now := timestamp(normalizedAgentNow(input.Now))
	action := "slo.history.retry"
	var changed int64
	if jobID == id {
		action = "slo.current.retry"
		changed, err = q.RetryCurrentSloPreparation(ctx, dbgen.RetryCurrentSloPreparationParams{SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision, CalculationVersion: operationalCalculationVersion, Now: now})
	} else {
		changed, err = q.RetryFailedSloHistoryJob(ctx, dbgen.RetryFailedSloHistoryJobParams{ID: jobID, SloID: id, NextAttemptAt: now})
		if err == nil && changed == 0 {
			action = "sli.aggregate.retry"
			changed, err = q.RetrySourceAggregate(ctx, dbgen.RetrySourceAggregateParams{JobID: jobID, SliID: row.SliID, MeasurementRevision: row.MeasurementRevision, Now: now})
		}
	}
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrObjectiveConflict
	}
	if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: jobID,
		Action: action, OccurredAt: now, RequestID: input.RequestID}); err != nil {
		return err
	}
	return finishAgentRead(ctx, tx, input.Now, started, user, installation, session)
}

func ReadSloPreparation(ctx context.Context, databaseURL string, input CurrentSloInput) (api.SloPreparationPage, error) {
	id, valid := canonicalUUID(input.ID)
	if !valid || input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return api.SloPreparationPage{}, ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloPreparationPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if _, err := q.GetSlo(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		return api.SloPreparationPage{}, ErrObjectiveNotFound
	} else if err != nil {
		return api.SloPreparationPage{}, err
	}
	rows, err := q.ListSloPreparationJobs(ctx, dbgen.ListSloPreparationJobsParams{SloID: id, CalculationVersion: operationalCalculationVersion, Now: timestamp(normalizedAgentNow(input.Now))})
	if err != nil {
		return api.SloPreparationPage{}, err
	}
	page := api.SloPreparationPage{Items: []api.SloPreparationJob{}, Truncated: len(rows) > 100}
	for _, row := range rows[:min(len(rows), 100)] {
		page.Items = append(page.Items, api.SloPreparationJob{Id: row.ID.Bytes, Stage: api.SloPreparationJobStage(row.Stage), State: api.SloPreparationJobState(row.State), Attempts: int(row.Attempts), NextAttemptAt: row.NextAttemptAt.Time, ErrorCode: row.LastError})
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloPreparationPage{}, err
	}
	return page, nil
}
