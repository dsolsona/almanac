package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"
	"uuid"

	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
)

var (
	ErrAlertInvalid = errors.New("storage: invalid alert operation")
	ErrAlertStale   = errors.New("storage: stale alert work or version")
)

func newAlertID() pgtype.UUID { id, _ := canonicalUUID(uuid.New().String()); return id }

func alertObjective(row dbgen.GetSloRow, now time.Time) alert.Objective {
	return alert.Objective{Target: sli.NormalizeDecimal(row.Target), Cadence: time.Duration(row.CadenceSeconds) * time.Second, DefinitionValidFrom: row.EvaluationValidFrom.Time, MeasurementValidFrom: row.SliValidFrom.Time, RetentionStart: now.Add(-sli.ObservationRetention)}
}

type alertRegistration struct {
	Policy                alert.PolicyRevision
	SLOID, Name, Severity string
	FirstBoundary, Now    time.Time
	Limits                alert.Limits
}

// registerAlertPolicy participates in its caller's transaction. The eventual
// management caller must authenticate/authorize and write audit/receipt there.
// No route, CLI or worker calls this unwired foundation yet.
func registerAlertPolicy(ctx context.Context, tx pgx.Tx, input alertRegistration) error {
	id, idOK := canonicalUUID(input.Policy.ID)
	sloID, sloOK := canonicalUUID(input.SLOID)
	if !idOK || !sloOK || strings.TrimSpace(input.Name) == "" || input.Policy.Revision != 1 || input.Now.IsZero() || input.FirstBoundary.Before(input.Now) || input.FirstBoundary.Nanosecond() != 0 || input.FirstBoundary.Unix()%60 != 0 {
		return ErrAlertInvalid
	}
	if input.Severity != "low" && input.Severity != "medium" && input.Severity != "high" {
		return ErrAlertInvalid
	}
	q := dbgen.New(tx)
	row, err := q.GetSlo(ctx, sloID)
	if err != nil {
		return err
	}
	measurement, err := q.LockAlertSli(ctx, row.SliID)
	if err != nil {
		return err
	}
	definition, err := q.LockAlertSlo(ctx, sloID)
	if err != nil {
		return err
	}
	if measurement != row.MeasurementRevision || definition != row.DefinitionRevision {
		return ErrAlertStale
	}
	if err := alert.Validate(input.Policy, alertObjective(row, input.Now), input.Limits); err != nil {
		return err
	}
	conditions, err := marshalAlertData(input.Policy.Conditions)
	if err != nil {
		return err
	}
	if err := q.InsertAlertPolicy(ctx, dbgen.InsertAlertPolicyParams{ID: id, Name: strings.TrimSpace(input.Name), SloID: sloID, EvaluationRevision: 1, Severity: input.Severity, CreatedAt: timestamp(input.Now)}); err != nil {
		return err
	}
	if err := q.InsertAlertPolicyRevision(ctx, dbgen.InsertAlertPolicyRevisionParams{PolicyID: id, Revision: 1, SloID: sloID, DefinitionRevision: definition, CalculationVersion: operationalCalculationVersion, Conditions: conditions, CreatedAt: timestamp(input.Now)}); err != nil {
		return err
	}
	return q.InsertAlertCursor(ctx, dbgen.InsertAlertCursorParams{PolicyID: id, Revision: 1, OriginAt: timestamp(input.FirstBoundary)})
}

func (r *Runtime) alertTransaction(ctx context.Context, work func(context.Context, pgx.Tx, *dbgen.Queries) error) error {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
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
	if err := work(bounded, tx, dbgen.New(tx)); err != nil {
		return err
	}
	return tx.Commit(bounded)
}

type alertClaim struct {
	PolicyID                 string
	Revision, Epoch, Version int64
	Token                    pgtype.UUID
	ScheduledAt, Deadline    time.Time
}

func (r *Runtime) claimAlertWork(ctx context.Context, tick, now time.Time, lease time.Duration, limit int32) ([]alertClaim, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "alert.claim")
	defer span.End()
	// A bounded internal claim batch is not a policy-count capability limit.
	if tick.IsZero() || now.IsZero() || lease <= 0 || lease%time.Microsecond != 0 || limit <= 0 || limit > 200 {
		return nil, ErrAlertInvalid
	}
	var result []alertClaim
	err := r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
		rows, err := q.ClaimAlertCursors(ctx, dbgen.ClaimAlertCursorsParams{Token: newAlertID(), Until: timestamp(now.Add(lease)), Tick: timestamp(tick), Now: timestamp(now), BatchLimit: limit})
		if err != nil {
			return err
		}
		for _, row := range rows {
			result = append(result, alertClaim{PolicyID: row.PolicyID.String(), Revision: row.Revision, Epoch: row.Epoch, Version: row.Version, Token: row.ClaimToken, ScheduledAt: row.NextScheduledAt.Time, Deadline: row.ClaimUntil.Time})
		}
		return nil
	})
	span.SetAttributes(attribute.Int("alert.claim.count", len(result)))
	return result, err
}

func validAlertClaim(claim alertClaim) bool {
	return canonicalUUIDCheck(claim.PolicyID) && claim.Revision > 0 && claim.Epoch > 0 && claim.Version > 0 && claim.Token.Valid && !claim.ScheduledAt.IsZero() && !claim.Deadline.IsZero()
}

func (r *Runtime) deferAlertWork(ctx context.Context, claim alertClaim, clock func() time.Time, retry time.Duration, reason string) error {
	if !validAlertClaim(claim) || clock == nil || retry <= 0 || retry%time.Microsecond != 0 || strings.TrimSpace(reason) == "" {
		return ErrAlertInvalid
	}
	id, _ := canonicalUUID(claim.PolicyID)
	return r.alertTransaction(ctx, func(ctx context.Context, _ pgx.Tx, q *dbgen.Queries) error {
		cursor, err := q.LockAlertCursor(ctx, dbgen.LockAlertCursorParams{PolicyID: id, Revision: claim.Revision})
		if err != nil {
			return err
		}
		now := normalizedAgentNow(clock)
		if now.IsZero() {
			return ErrAlertInvalid
		}
		if !cursor.ClaimUntil.Valid || !cursor.ClaimUntil.Time.After(now) {
			return ErrAlertStale
		}
		available := now.Add(retry)
		if !available.After(claim.ScheduledAt) {
			return ErrAlertInvalid
		}
		n, err := q.DeferAlertCursor(ctx, dbgen.DeferAlertCursorParams{PolicyID: id, Revision: claim.Revision, AvailableAt: timestamp(available), WaitingReason: reason, Epoch: claim.Epoch, ClaimToken: claim.Token, Version: claim.Version, NextScheduledAt: timestamp(claim.ScheduledAt), ClaimUntil: timestamp(now)})
		if err != nil {
			return err
		}
		if n != 1 || !cursor.ClaimUntil.Time.After(normalizedAgentNow(clock)) {
			return ErrAlertStale
		}
		return nil
	})
}

type alertStoredOccurrence struct {
	Occurrence                             *alert.Occurrence
	Version, Sequence                      int64
	FiringEvaluationID, LatestEvaluationID pgtype.UUID
}

func decodeAlertOccurrence(row dbgen.AlmanacAlertOccurrence) (alertStoredOccurrence, error) {
	var occurrence alert.Occurrence
	if err := json.Unmarshal(row.Payload, &occurrence); err != nil {
		return alertStoredOccurrence{}, err
	}
	if occurrence.ID != row.ID.String() || occurrence.PolicyID != row.PolicyID.String() || occurrence.Revision != row.Revision || string(occurrence.State) != row.State {
		return alertStoredOccurrence{}, ErrAlertInvalid
	}
	return alertStoredOccurrence{Occurrence: &occurrence, Version: row.Version, Sequence: row.TransitionSequence, FiringEvaluationID: row.FiringEvaluationID, LatestEvaluationID: row.LatestEvaluationID}, nil
}

func writeAlertOccurrence(ctx context.Context, q *dbgen.Queries, stored alertStoredOccurrence, expectedVersion int64) error {
	id, ok := canonicalUUID(stored.Occurrence.ID)
	if !ok {
		return ErrAlertInvalid
	}
	payload, err := json.Marshal(stored.Occurrence)
	if err != nil {
		return err
	}
	var terminal pgtype.Timestamptz
	if !stored.Occurrence.ClosedAt.IsZero() {
		terminal = timestamp(stored.Occurrence.ClosedAt)
	}
	n, err := q.UpdateAlertOccurrence(ctx, dbgen.UpdateAlertOccurrenceParams{ID: id, State: string(stored.Occurrence.State), TransitionSequence: stored.Sequence, LatestEvaluationID: stored.LatestEvaluationID, Payload: payload, UpdatedAt: timestamp(stored.Occurrence.UpdatedAt), TerminalAt: terminal, Version: expectedVersion})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAlertStale
	}
	return nil
}

func insertAlertTransition(ctx context.Context, q *dbgen.Queries, stored alertStoredOccurrence, result alert.TransitionResult, evaluationID pgtype.UUID, requestID string) error {
	policy, _ := canonicalUUID(stored.Occurrence.PolicyID)
	id, _ := canonicalUUID(stored.Occurrence.ID)
	var from pgtype.Text
	if result.From != "" {
		from = pgtype.Text{String: string(result.From), Valid: true}
	}
	var actor pgtype.UUID
	if result.Actor != "" {
		var ok bool
		actor, ok = canonicalUUID(result.Actor)
		if !ok {
			return ErrAlertInvalid
		}
	}
	return q.InsertAlertTransition(ctx, dbgen.InsertAlertTransitionParams{ID: newAlertID(), PolicyID: policy, Revision: stored.Occurrence.Revision, OccurrenceID: id, Sequence: stored.Sequence, FromState: from, ToState: string(stored.Occurrence.State), Cause: string(result.Cause), EffectiveAt: timestamp(result.EffectiveAt), DetectedAt: timestamp(result.DetectedAt), ActorID: actor, Reason: result.Reason, EvaluationID: evaluationID, RequestID: requestID})
}
