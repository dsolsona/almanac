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
)

var (
	ErrReassignmentPending          = errors.New("storage: agent has a pending datasource reassignment")
	ErrReassignmentSettingsConflict = errors.New("storage: datasource reassignment must not change settings")
)

// handleDatasourceReassignment runs only with installation, human session and
// source/target agent and datasource rows already locked by UpdateDatasource.
func handleDatasourceReassignment(ctx context.Context, q *dbgen.Queries, tx pgx.Tx, current dbgen.AlmanacDatasource, input UpdateDatasourceInput, user dbgen.LockSessionUserRow, installation dbgen.LockSessionInstallationRow, session dbgen.AlmanacSession, now time.Time, secrets *sourcesecret.Adapter) (DatasourceRecord, bool, error) {
	target, _ := canonicalUUID(input.Settings.AgentID)
	pending, pendingErr := q.LockDatasourceReassignment(ctx, current.ID)
	if pendingErr != nil && !errors.Is(pendingErr, pgx.ErrNoRows) {
		return DatasourceRecord{}, true, pendingErr
	}
	hasSLIs, err := q.HasSLIsForDatasource(ctx, current.ID)
	if err != nil {
		return DatasourceRecord{}, true, err
	}
	if errors.Is(pendingErr, pgx.ErrNoRows) && (!hasSLIs || target == current.AgentID) {
		return DatasourceRecord{}, false, nil
	}
	if pendingErr == nil && target != pending.SourceAgentID {
		return DatasourceRecord{}, true, ErrReassignmentPending
	}
	stored := datasourceRecord(current).Settings
	requested := input.Settings
	requested.AgentID = stored.AgentID
	actions, err := datasource.ResolveSecretActions(current.AuthenticationMode, input.Settings.AuthenticationMode, len(current.PasswordEnvelope) > 0, len(current.TokenEnvelope) > 0, input.Password, input.Token)
	if err != nil {
		return DatasourceRecord{}, true, ErrInvalidAgentInput
	}
	if requested != stored || actions.Password.RevisionDelta != 0 || actions.Token.RevisionDelta != 0 {
		return DatasourceRecord{}, true, ErrReassignmentSettingsConflict
	}
	installationID, err := loadInstallationID(ctx, tx)
	if err != nil {
		return DatasourceRecord{}, true, err
	}
	if pendingErr == nil {
		if _, err := q.DeleteDatasourceReassignment(ctx, current.ID); err != nil {
			return DatasourceRecord{}, true, err
		}
		updated, err := q.BumpDatasourceVersion(ctx, dbgen.BumpDatasourceVersionParams{ID: current.ID, UpdatedAt: timestamp(now)})
		if err != nil {
			return DatasourceRecord{}, true, err
		}
		revision, err := advanceAgentConfiguration(ctx, q, current.AgentID, now)
		if err != nil {
			return DatasourceRecord{}, true, err
		}
		if err := q.UpdateSourceRemovalRevisions(ctx, dbgen.UpdateSourceRemovalRevisionsParams{SourceAgentID: current.AgentID, RemovalRevision: revision}); err != nil {
			return DatasourceRecord{}, true, err
		}
		if pending.UsesV2Removal {
			if err := q.SetAgentHandoffConfiguration(ctx, dbgen.SetAgentHandoffConfigurationParams{AgentID: current.AgentID, Revision: revision}); err != nil {
				return DatasourceRecord{}, true, err
			}
		}
		if _, err := buildDatasourceConfigurationSnapshot(ctx, q, installationID, current.AgentID, revision, secrets); err != nil {
			return DatasourceRecord{}, true, err
		}
		if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: current.ID, Action: "datasource.reassignment.cancelled", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
			return DatasourceRecord{}, true, err
		}
		if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
			return DatasourceRecord{}, true, err
		}
		result := datasourceRecord(updated)
		result.ConfigurationRevision = revision
		return result, true, nil
	}
	if guarded, err := q.AgentHasPendingReassignments(ctx, target); err != nil {
		return DatasourceRecord{}, true, err
	} else if guarded {
		return DatasourceRecord{}, true, ErrReassignmentPending
	}
	capability, err := q.GetDatasourceAgentCapabilities(ctx, target)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, true, ErrAgentNotFound
	}
	if err != nil {
		return DatasourceRecord{}, true, err
	}
	if !capability.Enabled || !datasource.SupportsOperation(decodeCapabilities(capability.Capabilities), "collect") {
		return DatasourceRecord{}, true, ErrAgentConflict
	}
	deadline, err := q.LatestDatasourceGrantExpiry(ctx, dbgen.LatestDatasourceGrantExpiryParams{Now: timestamp(now), AgentID: current.AgentID, DatasourceID: current.ID})
	if err != nil || !deadline.Valid {
		return DatasourceRecord{}, true, errors.Join(err, ErrAgentConflict)
	}
	runtime, runtimeErr := q.GetAgentRuntimeStatus(ctx, current.AgentID)
	if runtimeErr != nil && !errors.Is(runtimeErr, pgx.ErrNoRows) {
		return DatasourceRecord{}, true, runtimeErr
	}
	usesV2 := runtimeErr == nil && runtime.Protocol == agentprotocol.ProtocolV2
	if usesV2 {
		marker, markerErr := q.GetAgentHandoffConfiguration(ctx, current.AgentID)
		if markerErr != nil && !errors.Is(markerErr, pgx.ErrNoRows) {
			return DatasourceRecord{}, true, markerErr
		}
		latest, latestErr := q.GetLatestAgentConfigurationRevision(ctx, current.AgentID)
		if latestErr != nil {
			return DatasourceRecord{}, true, latestErr
		}
		if errors.Is(markerErr, pgx.ErrNoRows) && runtime.AppliedConfigurationRevision != latest {
			live, err := q.AgentHasLiveOwnership(ctx, dbgen.AgentHasLiveOwnershipParams{AgentID: current.AgentID, Now: timestamp(now)})
			if err != nil {
				return DatasourceRecord{}, true, err
			}
			if live {
				return DatasourceRecord{}, true, ErrDiagnosticConfigurationPending
			}
			// No live authority can acknowledge a safe removal snapshot. Preserve
			// the existing configuration and use the expiry-only handoff path.
			usesV2 = false
		}
		if usesV2 && errors.Is(markerErr, pgx.ErrNoRows) {
			unsettled, err := q.AgentHasUnsettledCurrentLease(ctx, dbgen.AgentHasUnsettledCurrentLeaseParams{AgentID: current.AgentID, Now: timestamp(now), ConfigurationRevision: latest})
			if err != nil {
				return DatasourceRecord{}, true, err
			}
			if unsettled {
				return DatasourceRecord{}, true, ErrDiagnosticConfigurationPending
			}
		}
		if markerErr == nil && marker != latest {
			return DatasourceRecord{}, true, ErrReassignmentPending
		}
	}
	var revision int64
	if usesV2 {
		revision, err = advanceAgentConfiguration(ctx, q, current.AgentID, now)
	} else {
		revision, err = q.GetLatestAgentConfigurationRevision(ctx, current.AgentID)
	}
	if err != nil {
		return DatasourceRecord{}, true, err
	}
	if err := q.InsertDatasourceReassignment(ctx, dbgen.InsertDatasourceReassignmentParams{DatasourceID: current.ID, SourceAgentID: current.AgentID, TargetAgentID: target, RequestedAt: timestamp(now), AuthorityExpiresAt: deadline, RemovalRevision: revision, SourceConfigVersion: current.Version, UsesV2Removal: usesV2, ActorID: user.ID, RequestID: input.RequestID}); err != nil {
		return DatasourceRecord{}, true, err
	}
	updated, err := q.BumpDatasourceVersion(ctx, dbgen.BumpDatasourceVersionParams{ID: current.ID, UpdatedAt: timestamp(now)})
	if err != nil {
		return DatasourceRecord{}, true, err
	}
	if usesV2 {
		if err := q.UpdateSourceRemovalRevisions(ctx, dbgen.UpdateSourceRemovalRevisionsParams{SourceAgentID: current.AgentID, RemovalRevision: revision}); err != nil {
			return DatasourceRecord{}, true, err
		}
		if err := q.SetAgentHandoffConfiguration(ctx, dbgen.SetAgentHandoffConfigurationParams{AgentID: current.AgentID, Revision: revision}); err != nil {
			return DatasourceRecord{}, true, err
		}
		if _, err := buildDatasourceConfigurationSnapshot(ctx, q, installationID, current.AgentID, revision, secrets); err != nil {
			return DatasourceRecord{}, true, err
		}
	}
	if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: current.ID, Action: "datasource.reassignment.requested", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return DatasourceRecord{}, true, err
	}
	result, err := datasourceRecordWithReassignment(ctx, q, updated)
	if err != nil {
		return DatasourceRecord{}, true, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return DatasourceRecord{}, true, err
	}
	if usesV2 {
		result.ConfigurationRevision = revision
	}
	return result, true, nil
}

// ReassignmentNotice reports a committed configuration revision to notify an agent.
type ReassignmentNotice struct {
	AgentID  string
	Revision int64
}

// ReconcileDatasourceReassignments processes a bounded page. Each transition has
// its own transaction so an unavailable replacement does not block other moves.
func ReconcileDatasourceReassignments(ctx context.Context, databaseURL string, secrets *sourcesecret.Adapter, clock func() time.Time) ([]ReassignmentNotice, error) {
	if clock == nil {
		return nil, ErrInvalidAgentInput
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() {
		return nil, ErrInvalidAgentInput
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	defer closeInitialOwnerConnection(conn)
	rows, err := dbgen.New(conn).ListReadyDatasourceReassignments(ctx, timestamp(now))
	if err != nil {
		return nil, operationError(ctx, "storage: ready datasource reassignment query failed", err)
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

func reconcileDatasourceReassignment(ctx context.Context, conn *pgx.Conn, candidate dbgen.ListReadyDatasourceReassignmentsRow, secrets *sourcesecret.Adapter, clock func() time.Time) ([]ReassignmentNotice, error) {
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
	if err := lockDatasourceAgents(ctx, q, candidate.SourceAgentID, candidate.TargetAgentID); err != nil {
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
	now := normalizedAgentNow(clock)
	if now.IsZero() || pending.SourceAgentID != candidate.SourceAgentID || pending.TargetAgentID != candidate.TargetAgentID || current.AgentID != pending.SourceAgentID {
		return nil, ErrDatasourceConflict
	}
	if !pending.DrainedAt.Valid && now.Before(pending.AuthorityExpiresAt.Time) {
		return nil, nil
	}
	capability, err := q.GetDatasourceAgentCapabilities(ctx, pending.TargetAgentID)
	if err != nil {
		return nil, err
	}
	guarded, err := q.AgentHasPendingReassignments(ctx, pending.TargetAgentID)
	if err != nil {
		return nil, err
	}
	targetRuntime, targetRuntimeErr := q.GetAgentRuntimeStatus(ctx, pending.TargetAgentID)
	if targetRuntimeErr != nil && !errors.Is(targetRuntimeErr, pgx.ErrNoRows) {
		return nil, targetRuntimeErr
	}
	targetLatest, targetLatestErr := q.GetLatestAgentConfigurationRevision(ctx, pending.TargetAgentID)
	if targetLatestErr != nil && !errors.Is(targetLatestErr, pgx.ErrNoRows) {
		return nil, targetLatestErr
	}
	targetReady := targetRuntimeErr == nil && targetLatestErr == nil && targetRuntime.AppliedConfigurationRevision == targetLatest
	if targetReady && targetRuntime.Protocol == agentprotocol.ProtocolV2 {
		unsettled, err := q.AgentHasUnsettledCurrentLease(ctx, dbgen.AgentHasUnsettledCurrentLeaseParams{AgentID: pending.TargetAgentID, Now: timestamp(now), ConfigurationRevision: targetLatest})
		if err != nil {
			return nil, err
		}
		targetReady = !unsettled
	}
	if guarded || !targetReady || !capability.Enabled || !datasource.SupportsOperation(decodeCapabilities(capability.Capabilities), "collect") {
		_, err := q.SetDatasourceReassignmentUnavailable(ctx, dbgen.SetDatasourceReassignmentUnavailableParams{DatasourceID: pending.DatasourceID, RetryAt: timestamp(now.Add(10 * time.Second))})
		if err != nil {
			return nil, err
		}
		if pending.State != "replacement_unavailable" {
			if _, err := q.BumpDatasourceVersion(ctx, dbgen.BumpDatasourceVersionParams{ID: pending.DatasourceID, UpdatedAt: timestamp(now)}); err != nil {
				return nil, err
			}
			if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: pending.ActorID.String(), SubjectID: pending.DatasourceID, Action: "datasource.reassignment.unavailable", OccurredAt: timestamp(now), RequestID: pending.RequestID}); err != nil {
				return nil, err
			}
		}
		return nil, tx.Commit(ctx)
	}
	measurements, err := q.ListDatasourceSliMeasurements(ctx, pending.DatasourceID)
	if err != nil {
		return nil, err
	}
	for _, m := range measurements {
		cutoff := nextSliActivationBoundary(now, int(current.PollingIntervalSeconds))
		if m.EffectiveFrom.Time.After(cutoff) {
			cutoff = m.EffectiveFrom.Time
		}
		epoch, err := q.GetCurrentSliOwnershipEpoch(ctx, dbgen.GetCurrentSliOwnershipEpochParams{SliID: m.SliID, MeasurementRevision: m.Revision})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			if epoch.AgentID != pending.SourceAgentID {
				return nil, ErrDatasourceConflict
			}
			if !cutoff.After(epoch.StartsAt.Time) {
				cutoff = epoch.StartsAt.Time.Add(time.Duration(current.PollingIntervalSeconds) * time.Second)
			}
			if _, err := q.CloseSliOwnershipEpoch(ctx, dbgen.CloseSliOwnershipEpochParams{SliID: m.SliID, MeasurementRevision: m.Revision, EndsAt: timestamp(cutoff)}); err != nil {
				return nil, err
			}
		} else if cutoff.After(m.EffectiveFrom.Time) {
			if err := q.InsertSliOwnershipEpoch(ctx, dbgen.InsertSliOwnershipEpochParams{SliID: m.SliID, MeasurementRevision: m.Revision, AgentID: pending.SourceAgentID, StartsAt: m.EffectiveFrom, EndsAt: timestamp(cutoff)}); err != nil {
				return nil, err
			}
		}
		if err := q.InsertSliOwnershipEpoch(ctx, dbgen.InsertSliOwnershipEpochParams{SliID: m.SliID, MeasurementRevision: m.Revision, AgentID: pending.TargetAgentID, StartsAt: timestamp(cutoff)}); err != nil {
			return nil, err
		}
	}
	if _, err := q.ActivateDatasourceAgent(ctx, dbgen.ActivateDatasourceAgentParams{ID: pending.DatasourceID, AgentID: pending.TargetAgentID, UpdatedAt: timestamp(now)}); err != nil {
		return nil, err
	}
	if _, err := q.DeleteDatasourceReassignment(ctx, pending.DatasourceID); err != nil {
		return nil, err
	}
	if pending.UsesV2Removal && !pending.DrainedAt.Valid {
		if err := q.DeleteExpiredSourceHandoffConfiguration(ctx, pending.SourceAgentID); err != nil {
			return nil, err
		}
	}
	var sourceRevision int64
	if pending.UsesV2Removal {
		sourceRevision, err = q.GetLatestAgentConfigurationRevision(ctx, pending.SourceAgentID)
	} else {
		sourceRevision, err = advanceAgentConfiguration(ctx, q, pending.SourceAgentID, now)
	}
	if err != nil {
		return nil, err
	}
	targetRevision, err := advanceAgentConfiguration(ctx, q, pending.TargetAgentID, now)
	if err != nil {
		return nil, err
	}
	if err := q.SetDatasourceEpochConfigurationRevision(ctx, dbgen.SetDatasourceEpochConfigurationRevisionParams{DatasourceID: pending.DatasourceID, ConfigurationRevision: targetRevision}); err != nil {
		return nil, err
	}
	if targetRuntimeErr == nil && targetRuntime.Protocol == agentprotocol.ProtocolV2 && targetRuntime.AppliedConfigurationRevision > 0 {
		if err := q.SetAgentHandoffConfiguration(ctx, dbgen.SetAgentHandoffConfigurationParams{AgentID: pending.TargetAgentID, Revision: targetRevision}); err != nil {
			return nil, err
		}
	}
	installationID, err := loadInstallationID(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err := buildDatasourceConfigurationSnapshot(ctx, q, installationID, pending.SourceAgentID, sourceRevision, secrets); err != nil {
		return nil, err
	}
	if _, err := buildDatasourceConfigurationSnapshot(ctx, q, installationID, pending.TargetAgentID, targetRevision, secrets); err != nil {
		return nil, err
	}
	if err := q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: pending.ActorID.String(), SubjectID: pending.DatasourceID, Action: "datasource.reassignment.completed", OccurredAt: timestamp(now), RequestID: pending.RequestID}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return []ReassignmentNotice{{AgentID: pending.SourceAgentID.String(), Revision: sourceRevision}, {AgentID: pending.TargetAgentID.String(), Revision: targetRevision}}, nil
}
