package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrObjectiveInvalid         = errors.New("storage: invalid objective request")
	ErrObjectiveNotFound        = errors.New("storage: objective resource not found")
	ErrObjectiveConflict        = errors.New("storage: objective resource conflict")
	ErrObjectiveTarget          = errors.New("storage: invalid SLO target")
	ErrObjectiveWindow          = errors.New("storage: invalid SLO rolling window")
	ErrObjectiveHistoricalStart = errors.New("storage: invalid historical evaluation start")
	ErrObjectiveEvaluationTime  = errors.New("storage: invalid evaluation time")
	ErrObjectiveTimelineRange   = errors.New("storage: invalid timeline range")
)

type ServiceRecord struct {
	ID, Name, DisplayName string
	Version               int64
	CreatedAt, UpdatedAt  time.Time
}

type SloRecord struct {
	ID, Name, DisplayName                                string
	Version, DefinitionRevision                          int64
	ServiceID, ServiceDisplayName, SliID, SliDisplayName string
	MeasurementRevision                                  int64
	Target                                               string
	RollingWindowSeconds                                 int64
	CadenceSeconds                                       int32
	ActivatedAt, EvaluationValidFrom, SliValidFrom       time.Time
	HistoricalRequestedBy                                *string
	HistoricalRequestedAt                                *time.Time
	CreatedAt, UpdatedAt                                 time.Time
}

type CreateObjectiveInput struct {
	Proof                                       DatasourceProof
	Name, DisplayName, ServiceID, SliID, Target string
	RollingWindowSeconds                        int64
	EvaluationValidFrom                         *time.Time
	IdempotencyKey, RequestID                   string
	Now                                         func() time.Time
}

func serviceRecord(v dbgen.AlmanacService) ServiceRecord {
	return ServiceRecord{ID: v.ID.String(), Name: v.Name, DisplayName: v.DisplayName, Version: v.Version, CreatedAt: v.CreatedAt.Time.UTC(), UpdatedAt: v.UpdatedAt.Time.UTC()}
}

func sloRecord(v dbgen.GetSloRow) SloRecord {
	r := SloRecord{ID: v.ID.String(), Name: v.Name, DisplayName: v.DisplayName, Version: v.Version, DefinitionRevision: v.DefinitionRevision, ServiceID: v.ServiceID.String(), ServiceDisplayName: v.ServiceDisplayName, SliID: v.SliID.String(), SliDisplayName: v.SliDisplayName, MeasurementRevision: v.MeasurementRevision, Target: sli.NormalizeDecimal(v.Target), RollingWindowSeconds: v.RollingWindowSeconds, CadenceSeconds: v.CadenceSeconds, ActivatedAt: v.ActivatedAt.Time.UTC(), EvaluationValidFrom: v.EvaluationValidFrom.Time.UTC(), SliValidFrom: v.SliValidFrom.Time.UTC(), CreatedAt: v.CreatedAt.Time.UTC(), UpdatedAt: v.UpdatedAt.Time.UTC()}
	if v.HistoricalRequestedBy.Valid {
		id := v.HistoricalRequestedBy.String()
		r.HistoricalRequestedBy = &id
	}
	if v.HistoricalRequestedAt.Valid {
		at := v.HistoricalRequestedAt.Time.UTC()
		r.HistoricalRequestedAt = &at
	}
	return r
}

func validateObjectiveCreate(input CreateObjectiveInput, isSlo bool) bool {
	if input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) || input.RequestID == "" || !validPrintableASCII(input.IdempotencyKey, 1, 128) || !sli.ValidName(input.Name) || !validSliDisplayName(input.DisplayName) {
		return false
	}
	if !isSlo {
		return true
	}
	_, serviceValid := canonicalUUID(input.ServiceID)
	_, sliValid := canonicalUUID(input.SliID)
	return serviceValid && sliValid
}

// CreateObjective creates a service or an SLO and persists the original replay response.
func CreateObjective(ctx context.Context, databaseURL string, input CreateObjectiveInput, isSlo bool) (ServiceRecord, SloRecord, error) {
	if !validateObjectiveCreate(input, isSlo) {
		return ServiceRecord{}, SloRecord{}, ErrObjectiveInvalid
	}
	if isSlo {
		if _, err := slo.CalculateOccurrences("1", "1", input.Target, false); err != nil {
			return ServiceRecord{}, SloRecord{}, ErrObjectiveTarget
		}
		if input.RollingWindowSeconds <= 0 || input.RollingWindowSeconds > 2147483647 {
			return ServiceRecord{}, SloRecord{}, ErrObjectiveWindow
		}
	}
	operation := "service.create"
	if isSlo {
		operation = "slo.create"
	}
	request := struct {
		Name, DisplayName, ServiceID, SliID, Target string
		RollingWindowSeconds                        int64
		EvaluationValidFrom                         *time.Time
	}{input.Name, input.DisplayName, input.ServiceID, input.SliID, input.Target, input.RollingWindowSeconds, input.EvaluationValidFrom}
	raw, err := json.Marshal(request)
	if err != nil {
		return ServiceRecord{}, SloRecord{}, ErrObjectiveInvalid
	}
	digest := sha256.Sum256(raw)
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor")
	if err != nil {
		return ServiceRecord{}, SloRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	now := normalizedAgentNow(input.Now)
	// Serialize a given actor/operation/key even when its receipt does not exist yet.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", user.ID.String()+":"+operation+":"+input.IdempotencyKey); err != nil {
		return ServiceRecord{}, SloRecord{}, operationError(ctx, "storage: objective receipt lock failed", err)
	}
	key := dbgen.FindObjectiveCreateReceiptParams{ActorID: user.ID, Operation: operation, IdempotencyKey: input.IdempotencyKey}
	receipt, err := q.FindObjectiveCreateReceipt(ctx, key)
	if err == nil && now.Before(receipt.ExpiresAt.Time) {
		if !bytes.Equal(digest[:], receipt.RequestDigest) {
			return ServiceRecord{}, SloRecord{}, ErrObjectiveConflict
		}
		var service ServiceRecord
		var objective SloRecord
		if isSlo {
			err = json.Unmarshal(receipt.Result, &objective)
		} else {
			err = json.Unmarshal(receipt.Result, &service)
		}
		if err != nil {
			return ServiceRecord{}, SloRecord{}, errors.New("storage: corrupt objective receipt")
		}
		if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return ServiceRecord{}, SloRecord{}, err
		}
		return service, objective, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ServiceRecord{}, SloRecord{}, operationError(ctx, "storage: objective receipt read failed", err)
	}
	if err == nil {
		if err := q.DeleteObjectiveCreateReceipt(ctx, dbgen.DeleteObjectiveCreateReceiptParams{ActorID: user.ID, Operation: operation, IdempotencyKey: input.IdempotencyKey, ExpiresAt: timestamp(now)}); err != nil {
			return ServiceRecord{}, SloRecord{}, err
		}
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return ServiceRecord{}, SloRecord{}, err
	}
	pgID := uuidToPG(id)
	var service ServiceRecord
	var objective SloRecord
	if !isSlo {
		row, e := q.InsertService(ctx, dbgen.InsertServiceParams{ID: pgID, Name: input.Name, DisplayName: input.DisplayName, CreatedAt: timestamp(now)})
		if isUniqueViolation(e) {
			return service, objective, ErrObjectiveConflict
		}
		if e != nil {
			return service, objective, operationError(ctx, "storage: service create failed", e)
		}
		service = serviceRecord(row)
		raw, err = json.Marshal(service)
	} else {
		serviceID, _ := canonicalUUID(input.ServiceID)
		sliID, _ := canonicalUUID(input.SliID)
		if _, e := q.GetService(ctx, serviceID); errors.Is(e, pgx.ErrNoRows) {
			return service, objective, ErrObjectiveNotFound
		} else if e != nil {
			return service, objective, e
		}
		measurement, e := q.GetSli(ctx, sliID)
		if errors.Is(e, pgx.ErrNoRows) {
			return service, objective, ErrObjectiveNotFound
		}
		if e != nil {
			return service, objective, e
		}
		if measurement.MeasurementKind != "event_counts" || measurement.ComponentMode != "interval_count" {
			return service, objective, ErrObjectiveInvalid
		}
		if input.RollingWindowSeconds%int64(measurement.PollingIntervalSeconds) != 0 {
			return service, objective, ErrObjectiveWindow
		}
		validFrom := now
		var actor pgtype.UUID
		var requestedAt pgtype.Timestamptz
		if input.EvaluationValidFrom != nil {
			_, offset := input.EvaluationValidFrom.Zone()
			validFrom = input.EvaluationValidFrom.UTC()
			cadence := time.Duration(measurement.PollingIntervalSeconds) * time.Second
			if offset != 0 || validFrom.IsZero() || validFrom.Nanosecond()%1000 != 0 || !floorSliBoundary(validFrom, cadence).Equal(validFrom) || validFrom.Before(measurement.EffectiveFrom.Time) || validFrom.Before(ceilSliBoundary(now.Add(-sli.ObservationRetention), cadence)) || validFrom.After(now) {
				return service, objective, ErrObjectiveHistoricalStart
			}
			actor = user.ID
			requestedAt = timestamp(now)
		}
		e = q.InsertSlo(ctx, dbgen.InsertSloParams{ID: pgID, Name: input.Name, DisplayName: input.DisplayName, CreatedAt: timestamp(now)})
		if isUniqueViolation(e) {
			return service, objective, ErrObjectiveConflict
		}
		if e != nil {
			return service, objective, operationError(ctx, "storage: SLO create failed", e)
		}
		e = q.InsertSloDefinition(ctx, dbgen.InsertSloDefinitionParams{SloID: pgID, ServiceID: serviceID, SliID: sliID, MeasurementRevision: measurement.MeasurementRevision, Target: input.Target, RollingWindowSeconds: input.RollingWindowSeconds, CadenceSeconds: measurement.PollingIntervalSeconds, ActivatedAt: timestamp(now), EvaluationValidFrom: timestamp(validFrom), HistoricalRequestedBy: actor, HistoricalRequestedAt: requestedAt})
		if e != nil {
			return service, objective, operationError(ctx, "storage: SLO definition create failed", e)
		}
		row, e := q.GetSlo(ctx, pgID)
		if e != nil {
			return service, objective, e
		}
		objective = sloRecord(row)
		raw, err = json.Marshal(objective)
	}
	if err != nil {
		return service, objective, err
	}
	err = q.InsertObjectiveCreateReceipt(ctx, dbgen.InsertObjectiveCreateReceiptParams{ActorID: user.ID, Operation: operation, IdempotencyKey: input.IdempotencyKey, RequestDigest: digest[:], ResultID: pgID, Result: raw, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(24 * time.Hour))})
	if err != nil {
		return service, objective, operationError(ctx, "storage: objective receipt create failed", err)
	}
	if err = q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: pgID, Action: operation, OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return service, objective, operationError(ctx, "storage: objective audit failed", err)
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return service, objective, err
	}
	return service, objective, nil
}

type ObjectivePage struct {
	Services   []ServiceRecord
	Slos       []SloRecord
	NextCursor string
}

func ReadObjective(ctx context.Context, databaseURL string, proof DatasourceProof, idText string, now func() time.Time, isSlo bool) (ServiceRecord, SloRecord, error) {
	id, valid := canonicalUUID(idText)
	if !valid || now == nil || proof.SessionVerifier == ([32]byte{}) {
		return ServiceRecord{}, SloRecord{}, ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return ServiceRecord{}, SloRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	var service ServiceRecord
	var objective SloRecord
	if isSlo {
		var row dbgen.GetSloRow
		row, err = q.GetSlo(ctx, id)
		objective = sloRecord(row)
	} else {
		var row dbgen.AlmanacService
		row, err = q.GetService(ctx, id)
		service = serviceRecord(row)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return service, objective, ErrObjectiveNotFound
	}
	if err != nil {
		return service, objective, operationError(ctx, "storage: objective read failed", err)
	}
	if err = finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return ServiceRecord{}, SloRecord{}, err
	}
	return service, objective, nil
}

func ListObjectives(ctx context.Context, databaseURL string, proof DatasourceProof, limit int, cursor, serviceID string, now func() time.Time, isSlo bool) (ObjectivePage, error) {
	kind := "services"
	if isSlo {
		kind = "slos"
	}
	if !isSlo && serviceID != "" {
		return ObjectivePage{}, ErrObjectiveInvalid
	}
	filter := pgtype.UUID{}
	if serviceID != "" {
		var ok bool
		filter, ok = canonicalUUID(serviceID)
		if !ok {
			return ObjectivePage{}, ErrObjectiveInvalid
		}
	}
	bounded, position, err := validateAgentPage(kind, serviceID, limit, cursor)
	if err != nil || now == nil || proof.SessionVerifier == ([32]byte{}) {
		return ObjectivePage{}, ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return ObjectivePage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	page := ObjectivePage{Services: []ServiceRecord{}, Slos: []SloRecord{}}
	if isSlo {
		rows, e := q.ListSlos(ctx, dbgen.ListSlosParams{CreatedAt: timestamp(position.Time), Column2: uuidOrZero(position.ID), Limit: int32(bounded + 1), ServiceID: filter})
		if e != nil {
			return page, operationError(ctx, "storage: SLO list failed", e)
		}
		for _, row := range rows[:min(len(rows), bounded)] {
			page.Slos = append(page.Slos, sloRecord(dbgen.GetSloRow(row)))
		}
		if len(rows) > bounded {
			last := page.Slos[len(page.Slos)-1]
			page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: kind, AgentID: serviceID, Time: last.CreatedAt, ID: last.ID})
		}
	} else {
		rows, e := q.ListServices(ctx, dbgen.ListServicesParams{CreatedAt: timestamp(position.Time), Column2: uuidOrZero(position.ID), Limit: int32(bounded + 1)})
		if e != nil {
			return page, operationError(ctx, "storage: service list failed", e)
		}
		for _, row := range rows[:min(len(rows), bounded)] {
			page.Services = append(page.Services, serviceRecord(row))
		}
		if len(rows) > bounded {
			last := page.Services[len(page.Services)-1]
			page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: kind, Time: last.CreatedAt, ID: last.ID})
		}
	}
	if err != nil {
		return ObjectivePage{}, err
	}
	if err = finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return ObjectivePage{}, err
	}
	return page, nil
}

func UpdateObjectiveMetadata(ctx context.Context, databaseURL string, proof DatasourceProof, idText, displayName, requestID string, version int64, now func() time.Time, isSlo bool) (ServiceRecord, SloRecord, error) {
	id, valid := canonicalUUID(idText)
	if !valid || !validSliDisplayName(displayName) || version <= 0 || requestID == "" || now == nil || proof.SessionVerifier == ([32]byte{}) {
		return ServiceRecord{}, SloRecord{}, ErrObjectiveInvalid
	}
	conn, q, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor")
	if err != nil {
		return ServiceRecord{}, SloRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	current := normalizedAgentNow(now)
	var service ServiceRecord
	var objective SloRecord
	// Take the row lock before comparing the resource version, including no-op updates.
	table := "almanac_services"
	action := "service.metadata.updated"
	if isSlo {
		table = "almanac_slos"
		action = "slo.metadata.updated"
	}
	var existing string
	var currentVersion int64
	err = tx.QueryRow(ctx, "SELECT display_name, version FROM public."+table+" WHERE id=$1 FOR UPDATE", id).Scan(&existing, &currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return service, objective, ErrObjectiveNotFound
	}
	if err != nil {
		return service, objective, operationError(ctx, "storage: objective metadata lock failed", err)
	}
	if currentVersion != version {
		return service, objective, ErrObjectiveConflict
	}
	if existing != displayName {
		if isSlo {
			_, err = q.UpdateSloMetadata(ctx, dbgen.UpdateSloMetadataParams{ID: id, Version: version, DisplayName: displayName, UpdatedAt: timestamp(current)})
		} else {
			_, err = q.UpdateServiceMetadata(ctx, dbgen.UpdateServiceMetadataParams{ID: id, Version: version, DisplayName: displayName, UpdatedAt: timestamp(current)})
		}
		if err != nil {
			return service, objective, operationError(ctx, "storage: objective metadata update failed", err)
		}
		if err = q.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: id, Action: action, OccurredAt: timestamp(current), RequestID: requestID}); err != nil {
			return service, objective, err
		}
	}
	if isSlo {
		var row dbgen.GetSloRow
		row, err = q.GetSlo(ctx, id)
		objective = sloRecord(row)
	} else {
		var row dbgen.AlmanacService
		row, err = q.GetService(ctx, id)
		service = serviceRecord(row)
	}
	if err != nil {
		return service, objective, err
	}
	if err = finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return ServiceRecord{}, SloRecord{}, err
	}
	return service, objective, nil
}
