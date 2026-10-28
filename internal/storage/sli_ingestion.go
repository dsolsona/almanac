package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrObservationDenied   = errors.New("storage: observation execution is not authorized")
	ErrObservationInvalid  = errors.New("storage: observation item is invalid")
	ErrObservationConflict = errors.New("storage: observation conflicts with canonical history")
)

type SliIngestResult struct {
	DeliveryID       string
	Status           string
	ObservationID    string
	ErrorCode        string
	ReplayAgeSeconds int
}

// IngestAgentObservation validates a result against the retained, durable lease
// that authorized its execution. The observation interval is validated
// separately from the execution timestamp so renewal does not erase authority
// for an already running interval.
func IngestAgentObservation(ctx context.Context, databaseURL, credentialID string, credentialVerifier [32]byte, item sli.ObservationItem, clock func() time.Time) (SliIngestResult, error) {
	result := SliIngestResult{DeliveryID: item.DeliveryID, Status: "rejected"}
	credential, valid := canonicalUUID(credentialID)
	if !valid || clock == nil {
		return result, ErrObservationInvalid
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credential, credentialVerifier)
	if err != nil {
		return result, err
	}
	defer closeAgentOwnershipTransaction(state)
	now := normalizedAgentNow(clock)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, now) {
		return result, ErrInvalidAgentCredential
	}
	owner, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrObservationDenied
	}
	if err != nil {
		return result, operationError(ctx, "storage: SLI execution owner lookup failed", err)
	}
	generation, generationErr := parsePositiveInt64(item.SessionGeneration)
	configurationRevision, configurationErr := parsePositiveInt64(item.ConfigurationRevision)
	measurementRevision, measurementErr := parsePositiveInt64(item.MeasurementRevision)
	sliID, sliValid := canonicalUUID(item.SLIID)
	if generationErr != nil || configurationErr != nil || measurementErr != nil || !sliValid || generation != owner.Generation {
		return result, ErrObservationDenied
	}
	grant, err := state.queries.GetAgentExecutionGrantByLease(ctx, item.LeaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrObservationDenied
	}
	if err != nil {
		return result, operationError(ctx, "storage: SLI execution grant lookup failed", err)
	}
	if grant.AgentID != state.agentID || grant.Generation != generation || grant.ConfigurationRevision != configurationRevision ||
		!grant.RetainUntil.Valid || !now.Before(grant.RetainUntil.Time) || !grant.ValidFrom.Valid || !grant.ValidUntil.Valid ||
		item.CreatedAt.Before(grant.ValidFrom.Time) || !item.CreatedAt.Before(grant.ValidUntil.Time) ||
		item.CreatedAt.After(now.Add(5*time.Second)) {
		return result, ErrObservationDenied
	}
	var allowed []agentprotocol.MeasurementGrant
	if json.Unmarshal(grant.AllowedMeasurements, &allowed) != nil || allowed == nil || !hasMeasurementGrant(allowed, sliID.String(), measurementRevision) {
		return result, ErrObservationDenied
	}
	measurement, err := state.queries.LockSliMeasurementForObservation(ctx, dbgen.LockSliMeasurementForObservationParams{SliID: sliID, Revision: measurementRevision, IntervalStart: timestamp(item.IntervalStart)})
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrObservationDenied
	}
	if err != nil {
		return result, operationError(ctx, "storage: SLI measurement authorization lookup failed", err)
	}
	if !measurement.IntervalAgentID.Valid || measurement.IntervalAgentID != state.agentID ||
		measurement.MeasurementKind != string(sli.MeasurementKindEventCounts) || measurement.ComponentMode != string(sli.ComponentModeIntervalCount) {
		return result, ErrObservationDenied
	}
	if err := item.Validate(int(measurement.PollingIntervalSeconds), measurement.EffectiveFrom.Time.UTC(),
		time.Duration(measurement.SettlingDelaySeconds)*time.Second, now); err != nil {
		result.ErrorCode = observationValidationCode(err)
		return result, nil
	}
	result.ReplayAgeSeconds = max(0, int(now.Sub(item.CreatedAt).Seconds()))
	payload := struct {
		Version               int                  `json:"version"`
		DeliveryID            string               `json:"delivery_id"`
		ExecutionID           string               `json:"execution_id"`
		CreatedAt             time.Time            `json:"created_at"`
		LeaseID               string               `json:"lease_id"`
		ConfigurationRevision string               `json:"configuration_revision"`
		SessionGeneration     string               `json:"session_generation"`
		SLIID                 string               `json:"sli_id"`
		MeasurementRevision   string               `json:"measurement_revision"`
		IntervalStart         time.Time            `json:"interval_start"`
		IntervalEnd           time.Time            `json:"interval_end"`
		Outcome               sli.ExecutionOutcome `json:"outcome"`
	}{1, item.DeliveryID, item.ExecutionID, item.CreatedAt.UTC(), item.LeaseID,
		item.ConfigurationRevision, item.SessionGeneration, item.SLIID, item.MeasurementRevision,
		item.IntervalStart.UTC(), item.IntervalEnd.UTC(), sli.CanonicalOutcome(item.Outcome)}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return result, ErrObservationInvalid
	}
	payloadDigest := sha256.Sum256(payloadBytes)
	clear(payloadBytes)
	canonical := struct {
		Version             int                  `json:"version"`
		SLIID               string               `json:"sli_id"`
		MeasurementRevision string               `json:"measurement_revision"`
		IntervalStart       time.Time            `json:"interval_start"`
		IntervalEnd         time.Time            `json:"interval_end"`
		Outcome             sli.ExecutionOutcome `json:"outcome"`
	}{1, item.SLIID, item.MeasurementRevision, item.IntervalStart.UTC(), item.IntervalEnd.UTC(), sli.CanonicalOutcome(item.Outcome)}
	canonicalBytes, err := json.Marshal(canonical)
	if err != nil {
		return result, ErrObservationInvalid
	}
	resultDigest := sha256.Sum256(canonicalBytes)
	clear(canonicalBytes)
	deliveryID, _ := canonicalUUID(item.DeliveryID)
	existingReceipt, err := state.queries.FindSliDeliveryReceipt(ctx, dbgen.FindSliDeliveryReceiptParams{AgentID: state.agentID, DeliveryID: deliveryID})
	if err == nil {
		if !existingReceipt.ReplayUntil.Valid || !now.Before(existingReceipt.ReplayUntil.Time) {
			result.ErrorCode = "replay_expired"
			return result, nil
		}
		if !equalDigest(existingReceipt.PayloadDigest, payloadDigest[:]) {
			result.Status = "conflicting"
			result.ErrorCode = "delivery_id_reused"
			return result, nil
		}
		result.Status = existingReceipt.DeliveryStatus
		result.ObservationID = existingReceipt.ObservationID.String()
		if err := state.tx.Commit(ctx); err != nil {
			return result, operationError(ctx, "storage: SLI delivery replay commit failed", err)
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, operationError(ctx, "storage: SLI delivery receipt lookup failed", err)
	}
	intervalEnd := timestamp(item.IntervalEnd)
	missingComponents := item.Outcome.MissingComponents
	if missingComponents == nil {
		missingComponents = []string{}
	}
	coverage, err := state.queries.GetSliCoverageByCanonicalInterval(ctx, dbgen.GetSliCoverageByCanonicalIntervalParams{
		IntervalStart: timestamp(item.IntervalStart), SliID: sliID, MeasurementRevision: measurementRevision,
	})
	observationID, idErr := uuid.NewRandom()
	if idErr != nil {
		return result, errors.New("storage: observation identifier unavailable")
	}
	status := "accepted"
	if item.Outcome.State == sli.OutcomeFailed || item.Outcome.State == sli.OutcomeIncomplete {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return result, operationError(ctx, "storage: canonical SLI coverage lookup failed", err)
		}
		exists, err := state.queries.HasSliExecutionAttempt(ctx, dbgen.HasSliExecutionAttemptParams{AgentID: state.agentID, ExecutionID: mustPGUUID(item.ExecutionID)})
		if err != nil {
			return result, operationError(ctx, "storage: SLI execution attempt lookup failed", err)
		}
		if exists {
			result.Status = "conflicting"
			result.ErrorCode = "execution_id_reused"
			return result, nil
		}
		if err := state.queries.InsertSliExecutionAttempt(ctx, dbgen.InsertSliExecutionAttemptParams{
			AgentID: state.agentID, ExecutionID: mustPGUUID(item.ExecutionID), ObservationID: uuidToPG(observationID),
			SliID: sliID, MeasurementRevision: measurementRevision, IntervalStart: timestamp(item.IntervalStart),
			IntervalEnd: intervalEnd, Outcome: string(item.Outcome.State), ErrorCode: nullableString(item.Outcome.ErrorCode),
			MissingComponents: missingComponents, LeaseID: item.LeaseID, RecordedAt: timestamp(now),
		}); err != nil {
			return result, operationError(ctx, "storage: SLI execution attempt insert failed", err)
		}
	} else if err == nil {
		if !equalDigest(coverage.ResultDigest, resultDigest[:]) {
			result.Status = "conflicting"
			result.ErrorCode = "canonical_interval_conflict"
			return result, nil
		}
		status = "duplicate"
		observationID, err = uuid.Parse(coverage.ObservationID.String())
		if err != nil {
			return result, errors.New("storage: canonical observation identifier is invalid")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return result, operationError(ctx, "storage: canonical SLI coverage lookup failed", err)
	} else {
		if err := state.queries.InsertSliCoverage(ctx, dbgen.InsertSliCoverageParams{
			IntervalStart: timestamp(item.IntervalStart), SliID: sliID, MeasurementRevision: measurementRevision,
			IntervalEnd: intervalEnd, Outcome: string(item.Outcome.State), ResultDigest: resultDigest[:],
			ErrorCode: nullableString(item.Outcome.ErrorCode), MissingComponents: missingComponents,
			ExecutionID: mustPGUUID(item.ExecutionID), DeliveryID: deliveryID, ObservationID: uuidToPG(observationID),
			AgentID: state.agentID, SessionGeneration: generation, ConfigurationRevision: configurationRevision,
			LeaseID: item.LeaseID, AgentCreatedAt: timestamp(item.CreatedAt), RecordedAt: timestamp(now),
		}); err != nil {
			return result, operationError(ctx, "storage: SLI coverage insert failed", err)
		}
		if item.Outcome.State == sli.OutcomeObserved {
			good, total, countErr := sli.ValidateCounts(item.Outcome.GoodCount, item.Outcome.TotalCount)
			if countErr != nil {
				return result, ErrObservationInvalid
			}
			if err := state.queries.InsertSliObservationValue(ctx, dbgen.InsertSliObservationValueParams{
				IntervalStart: timestamp(item.IntervalStart), SliID: sliID, MeasurementRevision: measurementRevision,
				GoodCount: good, TotalCount: total,
			}); err != nil {
				return result, operationError(ctx, "storage: SLI values insert failed", err)
			}
		}
		if err := state.queries.InsertSliDirtyInterval(ctx, dbgen.InsertSliDirtyIntervalParams{
			SliID: sliID, MeasurementRevision: measurementRevision, IntervalStart: timestamp(item.IntervalStart),
			IntervalEnd: intervalEnd, CreatedAt: timestamp(now),
		}); err != nil {
			return result, operationError(ctx, "storage: SLI dirty interval insert failed", err)
		}
	}
	if err := state.queries.InsertSliDeliveryReceipt(ctx, dbgen.InsertSliDeliveryReceiptParams{
		DeliveryID: deliveryID, PayloadDigest: payloadDigest[:], SliID: sliID, MeasurementRevision: measurementRevision,
		IntervalStart: timestamp(item.IntervalStart), DeliveryStatus: status, ObservationID: uuidToPG(observationID),
		ReplayUntil: timestamp(now.Add(sli.ObservationReplayWindow)), AgentID: state.agentID, SessionGeneration: generation,
		LeaseID: item.LeaseID, ConfigurationRevision: configurationRevision, CreatedAt: timestamp(now),
	}); err != nil {
		return result, operationError(ctx, "storage: SLI delivery receipt insert failed", err)
	}
	if err := state.queries.AdvanceSliLastAttemptedInterval(ctx, dbgen.AdvanceSliLastAttemptedIntervalParams{
		SliID: sliID, Revision: measurementRevision, IntervalEnd: intervalEnd,
	}); err != nil {
		return result, operationError(ctx, "storage: SLI attempt cursor update failed", err)
	}
	if err := state.tx.Commit(ctx); err != nil {
		return result, operationError(ctx, "storage: SLI observation commit failed", err)
	}
	result.Status = status
	result.ObservationID = observationID.String()
	return result, nil
}

func prepareSliPartitions(ctx context.Context, tx pgx.Tx, now time.Time) error {
	months := map[string]time.Time{}
	for _, value := range []time.Time{now.Add(-sli.ObservationReplayWindow), now, now.AddDate(0, 1, 0)} {
		start := time.Date(value.UTC().Year(), value.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
		months[start.Format("200601")] = start
	}
	suffixes := make([]string, 0, len(months))
	for suffix := range months {
		suffixes = append(suffixes, suffix)
	}
	slices.Sort(suffixes)
	for _, suffix := range suffixes {
		start := months[suffix]
		end := start.AddDate(0, 1, 0)
		for _, table := range []string{"almanac_sli_coverage", "almanac_sli_observation_values"} {
			partition := table + "_" + suffix
			if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "almanac-sli-partition:"+partition); err != nil {
				return err
			}
			statement := fmt.Sprintf("CREATE TABLE IF NOT EXISTS public.%s PARTITION OF public.%s FOR VALUES FROM ('%s') TO ('%s')",
				partition, table, start.Format("2006-01-02 15:04:05Z07:00"), end.Format("2006-01-02 15:04:05Z07:00"))
			if _, err := tx.Exec(ctx, statement); err != nil {
				return err
			}
		}
	}
	return nil
}

func parsePositiveInt64(value string) (int64, error) {
	var parsed int64
	if _, err := fmt.Sscan(value, &parsed); err != nil || parsed <= 0 || fmt.Sprint(parsed) != value {
		return 0, ErrObservationInvalid
	}
	return parsed, nil
}

func hasMeasurementGrant(grants []agentprotocol.MeasurementGrant, sliID string, revision int64) bool {
	for _, grant := range grants {
		if grant.SLIID == sliID && grant.MeasurementRevision == fmt.Sprint(revision) {
			return true
		}
	}
	return false
}

func observationValidationCode(err error) string {
	if errors.Is(err, sli.ErrReplayExpired) {
		return "replay_expired"
	}
	return "invalid_observation"
}

func equalDigest(left, right []byte) bool {
	return len(left) == sha256.Size && len(right) == sha256.Size && string(left) == string(right)
}

func nullableString(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func mustPGUUID(value string) pgtype.UUID {
	parsed, _ := canonicalUUID(value)
	return parsed
}
