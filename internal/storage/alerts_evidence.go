package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"time"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

type ReadAlertEvidenceInput struct {
	ReadAlertInput
	Selection string // firing or latest; never a live recalculation
}

type AlertEvidenceRecord struct {
	ID                       string
	ScheduledAt, CommittedAt time.Time
	Input                    alert.Input
	Evaluation               alert.Evaluation
}

func ReadAlertEvidence(ctx context.Context, databaseURL string, input ReadAlertEvidenceInput) (AlertEvidenceRecord, error) {
	var result AlertEvidenceRecord
	if input.Selection != "firing" && input.Selection != "latest" {
		return result, ErrAlertInvalid
	}
	err := alertReadTransaction(ctx, databaseURL, input.Runtime, input.Proof, input.Now, func(q *dbgen.Queries, _ string) error {
		id, valid := canonicalUUID(input.ID)
		if !valid {
			return ErrAlertInvalid
		}
		occurrence, err := q.ReadAlertOccurrence(ctx, id)
		if err != nil {
			return err
		}
		evaluationID := occurrence.FiringEvaluationID
		if input.Selection == "latest" {
			evaluationID = occurrence.LatestEvaluationID
		}
		row, err := q.ReadAlertEvaluation(ctx, evaluationID)
		if err != nil {
			return err
		}
		if row.PolicyID != occurrence.PolicyID || row.Revision != occurrence.Revision {
			return ErrAlertInvalid
		}
		var saved struct {
			FormatVersion int
			Input         alert.Input
			Evaluation    alert.Evaluation
		}
		if err := unmarshalAlertData(row.Evidence, &saved); err != nil {
			return err
		}
		if saved.FormatVersion != 1 || saved.Input.Policy.ID != row.PolicyID.String() || saved.Input.Policy.Revision != row.Revision || saved.Evaluation.PolicyID != saved.Input.Policy.ID || saved.Evaluation.Revision != row.Revision || saved.Input.Snapshot != saved.Evaluation.Snapshot || !saved.Input.Snapshot.Cutoff.Equal(row.EffectiveCutoff.Time) || string(saved.Evaluation.Truth) != row.Truth {
			return ErrAlertInvalid
		}
		encoded, err := marshalAlertData(saved.Input)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		if !bytes.Equal(digest[:], row.InputDigest) {
			return ErrAlertInvalid
		}
		result = AlertEvidenceRecord{ID: row.ID.String(), ScheduledAt: row.ScheduledAt.Time.UTC(), CommittedAt: row.CommittedAt.Time.UTC(), Input: saved.Input, Evaluation: saved.Evaluation}
		return nil
	})
	if err != nil {
		return AlertEvidenceRecord{}, err
	}
	return result, nil
}
