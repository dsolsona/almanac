package storage

import (
	"context"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func normalizeMaintenanceSelector(kind MaintenanceScopeKind, sloIDs, serviceIDs []string) (MaintenanceScopeKind, []string, []string, error) {
	switch {
	case (kind == "" || kind == MaintenanceScopeSlos) && len(serviceIDs) == 0:
		ids, err := maintenanceScope(sloIDs)
		return MaintenanceScopeSlos, ids, nil, err
	case kind == MaintenanceScopeServices && len(sloIDs) == 0:
		ids, err := maintenanceScope(serviceIDs)
		return kind, nil, ids, err
	case kind == MaintenanceScopeAllServices && len(sloIDs) == 0 && len(serviceIDs) == 0:
		return kind, nil, nil, nil
	default:
		return "", nil, nil, ErrMaintenanceInvalid
	}
}

func isServiceMaintenanceScope(kind MaintenanceScopeKind) bool {
	return kind == MaintenanceScopeServices || kind == MaintenanceScopeAllServices
}

func validateServiceMaintenanceSelector(ctx context.Context, q *dbgen.Queries, input CreateMaintenanceInput, operation string) error {
	services := make([]pgtype.UUID, len(input.ServiceIDs))
	for i, id := range input.ServiceIDs {
		services[i], _ = canonicalUUID(id)
	}
	if input.ScopeKind == MaintenanceScopeServices && operation != "end" {
		count, err := q.CountActiveServiceIDs(ctx, services)
		if err != nil {
			return err
		}
		if count != int64(len(services)) {
			return ErrMaintenanceNotFound
		}
	}
	cadences, err := q.ListServiceMaintenanceCadences(ctx, dbgen.ListServiceMaintenanceCadencesParams{
		AllServices: input.ScopeKind == MaintenanceScopeAllServices, ServiceIds: services,
	})
	if err != nil {
		return err
	}
	alignment := int64(1)
	for _, cadence := range cadences {
		var ok bool
		alignment, ok = mergeMaintenanceCadence(alignment, int64(cadence))
		if !ok {
			return &MaintenanceAlignmentError{}
		}
	}
	if operation == "end" && input.End.Unix()%alignment == 0 {
		// A service may have joined since the window opened on another grid.
		// Only the prospective change needs to align with its current boundary.
		return nil
	}
	return maintenanceAlignment(alignment, input.Start, input.End, nil)
}

type maintenanceSuppressionEvidence struct {
	ID         string
	Revision   int64
	Start, End time.Time
}

// The caller already holds the SLO and exclusion state locks. Keep this SHARE
// lock until occurrence publication so a newer selector or assignment must wait.
func readMaintenanceSuppression(ctx context.Context, tx pgx.Tx, sloID pgtype.UUID, cutoff time.Time) (*maintenanceSuppressionEvidence, error) {
	q := dbgen.New(tx)
	if _, err := q.FenceServiceMaintenanceAuthority(ctx); err != nil {
		return nil, err
	}
	witness, err := q.ReadMaintenanceSuppressionWitness(ctx, dbgen.ReadMaintenanceSuppressionWitnessParams{
		SloID: sloID, Cutoff: timestamp(cutoff),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &maintenanceSuppressionEvidence{
		ID: witness.ExclusionID.String(), Revision: witness.Revision, Start: witness.StartsAt.Time.UTC(), End: witness.EndsAt.Time.UTC(),
	}, nil
}
