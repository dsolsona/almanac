package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrReassignmentPending          = errors.New("storage: agent has a pending datasource reassignment")
	ErrReassignmentSettingsConflict = errors.New("storage: datasource reassignment must not change settings")
)

func datasourceAssignmentOnly(current dbgen.AlmanacDatasource, input UpdateDatasourceInput) bool {
	stored := datasourceRecord(current).Settings
	requested := input.Settings
	requested.AgentID = stored.AgentID
	actions, err := datasource.ResolveSecretActions(current.AuthenticationMode, input.Settings.AuthenticationMode, len(current.PasswordEnvelope) > 0, len(current.TokenEnvelope) > 0, input.Password, input.Token)
	return err == nil && requested == stored && actions.Password.RevisionDelta == 0 && actions.Token.RevisionDelta == 0
}

// The caller holds the installation, session, participant and datasource locks.
func handleDatasourceReassignment(ctx context.Context, q *dbgen.Queries, tx pgx.Tx, current dbgen.AlmanacDatasource, input UpdateDatasourceInput, user dbgen.LockSessionUserRow, installation dbgen.LockSessionInstallationRow, session dbgen.AlmanacSession, now time.Time, secrets *sourcesecret.Adapter) (DatasourceRecord, bool, error) {
	target, _ := canonicalUUID(input.Settings.AgentID)
	pending, pendingErr := q.GetDatasourceReassignment(ctx, current.ID)
	if pendingErr != nil && !errors.Is(pendingErr, pgx.ErrNoRows) {
		return DatasourceRecord{}, true, pendingErr
	}
	legacyCancellation := pendingErr == nil && pending.TargetConfigurationRevision == 0 && pending.SourceAgentID == current.AgentID && target == current.AgentID
	if input.CancelReassignment && !legacyCancellation && (pendingErr != nil || pending.TargetConfigurationRevision == 0 || pending.TargetAgentID != current.AgentID || pending.SourceAgentID != target) {
		return DatasourceRecord{}, true, ErrDatasourceConflict
	}
	if legacyCancellation {
		if !datasourceAssignmentOnly(current, input) {
			return DatasourceRecord{}, true, ErrReassignmentSettingsConflict
		}
		result, err := cancelLegacyDatasourceReassignment(ctx, q, tx, current, pending, user.ID, input.RequestID, now, secrets)
		if err != nil {
			return DatasourceRecord{}, true, err
		}
		if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
			return DatasourceRecord{}, true, err
		}
		return result, true, nil
	}
	if target == current.AgentID {
		return DatasourceRecord{}, false, nil
	}
	if !datasourceAssignmentOnly(current, input) {
		return DatasourceRecord{}, true, ErrReassignmentSettingsConflict
	}
	result, err := transferDatasourceAssignment(ctx, q, tx, current, target, user.ID, input.RequestID, now, secrets)
	if err != nil {
		return DatasourceRecord{}, true, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return DatasourceRecord{}, true, err
	}
	return result, true, nil
}

func transferDatasourceAssignment(ctx context.Context, q *dbgen.Queries, tx pgx.Tx, current dbgen.AlmanacDatasource, target, actor pgtype.UUID, requestID string, now time.Time, secrets *sourcesecret.Adapter) (DatasourceRecord, error) {
	previous, previousErr := q.GetDatasourceReassignment(ctx, current.ID)
	if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
		return DatasourceRecord{}, previousErr
	}
	canceled := previousErr == nil && previous.TargetConfigurationRevision > 0 && previous.TargetAgentID == current.AgentID && previous.SourceAgentID == target
	capability, err := q.GetDatasourceAgentCapabilities(ctx, target)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, ErrAgentNotFound
	}
	if err != nil {
		return DatasourceRecord{}, err
	}
	hasSLIs, err := q.HasSLIsForDatasource(ctx, current.ID)
	if err != nil {
		return DatasourceRecord{}, err
	}
	capabilities := decodeCapabilities(capability.Capabilities)
	supported := datasource.SupportsOperation(capabilities, "collect")
	if !hasSLIs {
		supported = datasource.SupportsOperation(capabilities, "connection_test") && datasource.SupportsOperation(capabilities, "query_preview")
	}
	if !capability.Enabled || !supported {
		return DatasourceRecord{}, ErrAgentConflict
	}
	if _, err := q.DeleteDatasourceReassignment(ctx, current.ID); err != nil {
		return DatasourceRecord{}, err
	}
	measurements, err := q.ListDatasourceSliMeasurements(ctx, current.ID)
	if err != nil {
		return DatasourceRecord{}, err
	}
	for _, m := range measurements {
		cutoff := nextCanonicalBoundary(now, int(current.PollingIntervalSeconds))
		if m.EffectiveFrom.Time.After(cutoff) {
			cutoff = m.EffectiveFrom.Time
		}
		epoch, err := q.GetCurrentSliOwnershipEpoch(ctx, dbgen.GetCurrentSliOwnershipEpochParams{SliID: m.SliID, MeasurementRevision: m.Revision})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return DatasourceRecord{}, err
		}
		if err == nil {
			if !epoch.StartsAt.Time.Before(cutoff) {
				// A second move can supersede an assignment before its first interval.
				if err := q.SupersedeFutureSliOwnershipEpochs(ctx, dbgen.SupersedeFutureSliOwnershipEpochsParams{SliID: m.SliID, MeasurementRevision: m.Revision, StartsAt: timestamp(cutoff)}); err != nil {
					return DatasourceRecord{}, err
				}
			} else if _, err := q.CloseSliOwnershipEpoch(ctx, dbgen.CloseSliOwnershipEpochParams{SliID: m.SliID, MeasurementRevision: m.Revision, EndsAt: timestamp(cutoff)}); err != nil {
				return DatasourceRecord{}, err
			}
		} else if cutoff.After(m.EffectiveFrom.Time) {
			if err := q.InsertSliOwnershipEpoch(ctx, dbgen.InsertSliOwnershipEpochParams{SliID: m.SliID, MeasurementRevision: m.Revision, AgentID: current.AgentID, StartsAt: m.EffectiveFrom, EndsAt: timestamp(cutoff)}); err != nil {
				return DatasourceRecord{}, err
			}
		}
		if err := q.InsertSliOwnershipEpoch(ctx, dbgen.InsertSliOwnershipEpochParams{SliID: m.SliID, MeasurementRevision: m.Revision, AgentID: target, StartsAt: timestamp(cutoff)}); err != nil {
			return DatasourceRecord{}, err
		}
	}
	sourceRevision, err := advanceAgentConfiguration(ctx, q, current.AgentID, now)
	if err != nil {
		return DatasourceRecord{}, err
	}
	targetRevision, err := advanceAgentConfiguration(ctx, q, target, now)
	if err != nil {
		return DatasourceRecord{}, err
	}
	updated, err := q.ActivateDatasourceAgent(ctx, dbgen.ActivateDatasourceAgentParams{ID: current.ID, AgentID: target, UpdatedAt: timestamp(now), AssignmentConfigurationRevision: targetRevision})
	if err != nil {
		return DatasourceRecord{}, err
	}
	deadline, err := q.LatestDatasourceGrantExpiry(ctx, dbgen.LatestDatasourceGrantExpiryParams{Now: timestamp(now), AgentID: current.AgentID, DatasourceID: current.ID})
	if err != nil {
		return DatasourceRecord{}, err
	}
	if err := q.SetDatasourceEpochConfigurationRevision(ctx, dbgen.SetDatasourceEpochConfigurationRevisionParams{DatasourceID: current.ID, ConfigurationRevision: targetRevision}); err != nil {
		return DatasourceRecord{}, err
	}
	if !canceled {
		if err := q.InsertDatasourceReassignment(ctx, dbgen.InsertDatasourceReassignmentParams{DatasourceID: current.ID, SourceAgentID: current.AgentID, TargetAgentID: target, RequestedAt: timestamp(now), AuthorityExpiresAt: deadline, RemovalRevision: sourceRevision, SourceConfigVersion: current.Version, ActorID: actor, RequestID: requestID, TargetConfigurationRevision: targetRevision}); err != nil {
			return DatasourceRecord{}, err
		}
	}
	if err := publishReassignmentConfigurations(ctx, q, tx, secrets, ReassignmentNotice{AgentID: current.AgentID.String(), Revision: sourceRevision}, ReassignmentNotice{AgentID: target.String(), Revision: targetRevision}); err != nil {
		return DatasourceRecord{}, err
	}
	action := "datasource.reassignment.requested"
	if canceled {
		action = "datasource.reassignment.canceled"
	}
	if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: actor.String(), SubjectID: current.ID, Action: action, OccurredAt: timestamp(now), RequestID: requestID}); err != nil {
		return DatasourceRecord{}, err
	}
	result, err := datasourceRecordWithReassignment(ctx, q, updated)
	result.ConfigurationRevision = targetRevision
	result.PreviousAgentID = current.AgentID.String()
	result.PreviousConfigurationRevision = sourceRevision
	return result, err
}

func cancelLegacyDatasourceReassignment(ctx context.Context, q *dbgen.Queries, tx pgx.Tx, current dbgen.AlmanacDatasource, pending dbgen.AlmanacDatasourceReassignment, actor pgtype.UUID, requestID string, now time.Time, secrets *sourcesecret.Adapter) (DatasourceRecord, error) {
	if _, err := q.DeleteDatasourceReassignment(ctx, current.ID); err != nil {
		return DatasourceRecord{}, err
	}
	sourceRevision, err := advanceAgentConfiguration(ctx, q, current.AgentID, now)
	if err != nil {
		return DatasourceRecord{}, err
	}
	targetRevision, err := advanceAgentConfiguration(ctx, q, pending.TargetAgentID, now)
	if err != nil {
		return DatasourceRecord{}, err
	}
	updated, err := q.ActivateDatasourceAgent(ctx, dbgen.ActivateDatasourceAgentParams{ID: current.ID, AgentID: current.AgentID, UpdatedAt: timestamp(now), AssignmentConfigurationRevision: sourceRevision})
	if err != nil {
		return DatasourceRecord{}, err
	}
	if err := q.SetDatasourceEpochConfigurationRevision(ctx, dbgen.SetDatasourceEpochConfigurationRevisionParams{DatasourceID: current.ID, ConfigurationRevision: sourceRevision}); err != nil {
		return DatasourceRecord{}, err
	}
	if err := publishReassignmentConfigurations(ctx, q, tx, secrets, ReassignmentNotice{AgentID: current.AgentID.String(), Revision: sourceRevision}, ReassignmentNotice{AgentID: pending.TargetAgentID.String(), Revision: targetRevision}); err != nil {
		return DatasourceRecord{}, err
	}
	if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: actor.String(), SubjectID: current.ID, Action: "datasource.reassignment.canceled", OccurredAt: timestamp(now), RequestID: requestID}); err != nil {
		return DatasourceRecord{}, err
	}
	result := datasourceRecord(updated)
	result.ConfigurationRevision = sourceRevision
	result.PreviousAgentID = pending.TargetAgentID.String()
	result.PreviousConfigurationRevision = targetRevision
	return result, nil
}

func publishReassignmentConfigurations(ctx context.Context, q *dbgen.Queries, tx pgx.Tx, secrets *sourcesecret.Adapter, configs ...ReassignmentNotice) error {
	installationID, err := loadInstallationID(ctx, tx)
	if err != nil {
		return err
	}
	for _, config := range configs {
		agentID, _ := canonicalUUID(config.AgentID)
		runtime, runtimeErr := q.GetAgentRuntimeStatus(ctx, agentID)
		if runtimeErr != nil && !errors.Is(runtimeErr, pgx.ErrNoRows) {
			return runtimeErr
		}
		if runtimeErr == nil && runtime.Protocol == agentprotocol.ProtocolV2 && runtime.AppliedConfigurationRevision > 0 {
			if err := q.SetAgentHandoffConfiguration(ctx, dbgen.SetAgentHandoffConfigurationParams{AgentID: agentID, Revision: config.Revision}); err != nil {
				return err
			}
		}
		if _, err := buildDatasourceConfigurationSnapshot(ctx, q, installationID, agentID, config.Revision, secrets); err != nil {
			return err
		}
	}
	return nil
}

type ReassignmentNotice struct {
	AgentID  string
	Revision int64
}

// ReconcileDatasourceReassignments adopts legacy moves and reports replacement readiness.
func ReconcileDatasourceReassignments(ctx context.Context, databaseURL string, secrets *sourcesecret.Adapter, clock func() time.Time) ([]ReassignmentNotice, error) {
	if clock == nil || normalizedAgentNow(clock).IsZero() {
		return nil, ErrInvalidAgentInput
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	defer closeInitialOwnerConnection(conn)
	rows, err := dbgen.New(conn).ListReadyDatasourceReassignments(ctx, timestamp(normalizedAgentNow(clock)))
	if err != nil {
		return nil, err
	}
	var notices []ReassignmentNotice
	for _, row := range rows {
		result, err := reconcileDatasourceReassignment(ctx, conn, row, secrets, clock)
		if err != nil {
			return notices, err
		}
		notices = append(notices, result...)
	}
	return notices, nil
}

func reconcileDatasourceReassignment(ctx context.Context, conn *readConnection, candidate dbgen.ListReadyDatasourceReassignmentsRow, secrets *sourcesecret.Adapter, clock func() time.Time) ([]ReassignmentNotice, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return nil, err
	}
	q := dbgen.New(tx)
	if _, err := q.LockSessionInstallation(ctx); err != nil {
		return nil, err
	}
	if _, err := q.LockDatasourceReassignmentAgents(ctx, []pgtype.UUID{candidate.SourceAgentID, candidate.TargetAgentID}); err != nil {
		return nil, err
	}
	current, err := q.LockDatasource(ctx, candidate.DatasourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pending, err := q.LockDatasourceReassignment(ctx, candidate.DatasourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pending.SourceAgentID != candidate.SourceAgentID || pending.TargetAgentID != candidate.TargetAgentID {
		return nil, nil
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return nil, ErrInvalidAgentInput
	}
	if pending.TargetConfigurationRevision == 0 {
		if current.AgentID != pending.SourceAgentID {
			return nil, ErrDatasourceConflict
		}
		result, err := transferDatasourceAssignment(ctx, q, tx, current, pending.TargetAgentID, pending.ActorID, pending.RequestID, now, secrets)
		if errors.Is(err, ErrAgentConflict) || errors.Is(err, ErrAgentNotFound) {
			if _, err := q.SetDatasourceReassignmentUnavailable(ctx, dbgen.SetDatasourceReassignmentUnavailableParams{DatasourceID: current.ID, RetryAt: timestamp(now.Add(10 * time.Second))}); err != nil {
				return nil, err
			}
			return nil, tx.Commit(ctx)
		}
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return []ReassignmentNotice{{AgentID: result.PreviousAgentID, Revision: result.PreviousConfigurationRevision}, {AgentID: result.Settings.AgentID, Revision: result.ConfigurationRevision}}, nil
	}
	if current.AgentID != pending.TargetAgentID {
		return nil, ErrDatasourceConflict
	}
	runtime, runtimeErr := q.GetAgentRuntimeStatus(ctx, pending.TargetAgentID)
	if runtimeErr != nil && !errors.Is(runtimeErr, pgx.ErrNoRows) {
		return nil, runtimeErr
	}
	capability, err := q.GetDatasourceAgentCapabilities(ctx, pending.TargetAgentID)
	if err != nil {
		return nil, err
	}
	hasSLIs, err := q.HasSLIsForDatasource(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	state := "waiting_for_configuration"
	if runtimeErr != nil || !capability.Enabled || (hasSLIs && !datasource.SupportsOperation(decodeCapabilities(capability.Capabilities), "collect")) || !runtime.LastHeartbeatAt.Valid || now.Sub(runtime.LastHeartbeatAt.Time) > 30*time.Second {
		state = "replacement_unavailable"
	} else if runtime.ConfigurationFailureRevision.Valid && runtime.ConfigurationFailureRevision.Int64 >= pending.TargetConfigurationRevision {
		state = "replacement_unavailable"
	} else if runtime.AppliedConfigurationRevision >= pending.TargetConfigurationRevision {
		if err := completeAcknowledgedDatasourceReassignments(ctx, q, current.AgentID, runtime.AppliedConfigurationRevision, runtime.Generation, now); err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if err := q.UpdateDatasourceReassignmentProgress(ctx, dbgen.UpdateDatasourceReassignmentProgressParams{DatasourceID: current.ID, State: state, RetryAt: timestamp(now.Add(2 * time.Second))}); err != nil {
		return nil, err
	}
	return nil, tx.Commit(ctx)
}

func completeAcknowledgedDatasourceReassignments(ctx context.Context, q *dbgen.Queries, agentID pgtype.UUID, revision, generation int64, now time.Time) error {
	completed, err := q.CompleteAcknowledgedDatasourceReassignments(ctx, dbgen.CompleteAcknowledgedDatasourceReassignmentsParams{AgentID: agentID, ConfigurationRevision: revision, Generation: generation})
	if err != nil {
		return err
	}
	for _, pending := range completed {
		if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: pending.ActorID.String(), SubjectID: pending.DatasourceID, Action: "datasource.reassignment.completed", OccurredAt: timestamp(now), RequestID: pending.RequestID}); err != nil {
			return err
		}
	}
	return nil
}
