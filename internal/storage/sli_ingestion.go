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
func IngestAgentObservation(ctx context.Context, databaseURL, credentialID string, credentialVerifier [32]byte, item sli.ObservationItem, clock func() time.Time, runtimes ...*Runtime) (SliIngestResult, error) {
	result := SliIngestResult{DeliveryID: item.DeliveryID, Status: "rejected"}
	credential, valid := canonicalUUID(credentialID)
	if !valid || clock == nil {
		return result, ErrObservationInvalid
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credential, credentialVerifier, runtimes...)
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
	if measurement.CurrentAgentID != state.agentID || configurationRevision < measurement.AssignmentConfigurationRevision ||
		(measurement.CurrentOwnershipFrom.Valid && item.IntervalStart.Before(measurement.CurrentOwnershipFrom.Time)) ||
		!measurement.IntervalAgentID.Valid || measurement.IntervalAgentID != state.agentID ||
		measurement.MeasurementKind != string(sli.MeasurementKindEventCounts) || measurement.ComponentMode != string(sli.ComponentModeIntervalCount) {
		return result, ErrObservationDenied
	}
	if err := item.Validate(int(measurement.PollingIntervalSeconds), measurement.EffectiveFrom.Time.UTC(),
		time.Duration(measurement.SettlingDelaySeconds)*time.Second, now); err != nil {
		result.ErrorCode = observationValidationCode(err)
		return result, nil
	}
	result.ReplayAgeSeconds = max(0, int(now.Sub(item.CreatedAt).Seconds()))
	payloadDigest, err := sliObservationPayloadDigest(item)
	if err != nil {
		return result, ErrObservationInvalid
	}
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
		if coverage.ObservationID.Valid {
			observationID = coverage.ObservationID.Bytes
		} else {
			observationID = canonicalObservationID(sliID.String(), measurementRevision, item.IntervalStart)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return result, operationError(ctx, "storage: canonical SLI coverage lookup failed", err)
	} else {
		var good, total pgtype.Text
		if item.Outcome.State == sli.OutcomeObserved {
			goodValue, totalValue, err := sli.ValidateCounts(item.Outcome.GoodCount, item.Outcome.TotalCount)
			if err != nil {
				return result, ErrObservationInvalid
			}
			good, total = nullableString(goodValue), nullableString(totalValue)
		}
		sourceGeneration, err := state.queries.AdvanceSliSourceGeneration(ctx, dbgen.AdvanceSliSourceGenerationParams{
			SliID: sliID, MeasurementRevision: measurementRevision, RecordedAt: timestamp(now),
		})
		if err != nil {
			return result, operationError(ctx, "storage: SLI source generation update failed", err)
		}
		observationID = canonicalObservationID(sliID.String(), measurementRevision, item.IntervalStart)
		if err := state.queries.InsertSliObservation(ctx, dbgen.InsertSliObservationParams{
			IntervalStart: timestamp(item.IntervalStart), SliID: sliID, MeasurementRevision: measurementRevision,
			Outcome: string(item.Outcome.State), ResultDigest: resultDigest[:], GoodCount: good, TotalCount: total,
			ErrorCode: nullableString(item.Outcome.ErrorCode), MissingComponents: missingComponents,
			ExecutionID: mustPGUUID(item.ExecutionID), DeliveryID: deliveryID,
			AgentID: state.agentID, SessionGeneration: generation, ConfigurationRevision: configurationRevision,
			LeaseID: item.LeaseID, AgentCreatedAt: timestamp(item.CreatedAt), RecordedAt: timestamp(now),
			IngestionGeneration: sourceGeneration,
		}); err != nil {
			return result, operationError(ctx, "storage: SLI observation insert failed", err)
		}
		if err := state.queries.MarkSliObservation(ctx, dbgen.MarkSliObservationParams{
			SliID: sliID, MeasurementRevision: measurementRevision, Generation: sourceGeneration,
			IntervalStart: timestamp(item.IntervalStart), IntervalEnd: intervalEnd, RecordedAt: timestamp(now),
		}); err != nil {
			return result, operationError(ctx, "storage: SLI observation marking failed", err)
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
	month := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, value := range []time.Time{now.Add(-sli.ObservationReplayWindow), now, month.AddDate(0, 1, 0)} {
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
		partition := "almanac_sli_observations_" + suffix
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "almanac-sli-partition:"+partition); err != nil {
			return err
		}
		statement := fmt.Sprintf("CREATE TABLE IF NOT EXISTS public.%s PARTITION OF public.almanac_sli_observations FOR VALUES FROM ('%s') TO ('%s')",
			partition, start.Format("2006-01-02 15:04:05Z07:00"), end.Format("2006-01-02 15:04:05Z07:00"))
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
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

// The canonical key survives receipt expiry without a second stored identifier.
func canonicalObservationID(sliID string, revision int64, start time.Time) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, fmt.Appendf(nil, "%s:%d:%d", sliID, revision, start.UnixMicro()))
}
