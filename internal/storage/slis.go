package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const sliCreateReceiptTTL = 24 * time.Hour

var (
	ErrSliNotFound                   = errors.New("storage: SLI not found")
	ErrSliConflict                   = errors.New("storage: SLI conflict")
	ErrSliInvalid                    = errors.New("storage: invalid SLI input")
	ErrSliUnsupported                = errors.New("storage: unsupported SLI measurement semantics")
	ErrSliAgentUnavailable           = errors.New("storage: assigned agent cannot collect SLIs")
	ErrDatasourceMeasurementConflict = errors.New("storage: datasource update conflicts with active SLI measurements")
)

type CreateSliInput struct {
	Proof           DatasourceProof
	Secrets         *sourcesecret.Adapter
	Name            string
	DisplayName     string
	DatasourceID    string
	MeasurementKind sli.MeasurementKind
	ComponentMode   sli.ComponentMode
	GoodQuery       string
	TotalQuery      string
	IdempotencyKey  string
	RequestID       string
	Now             func() time.Time
}

type UpdateSliMetadataInput struct {
	Proof           DatasourceProof
	SliID           string
	ExpectedVersion int64
	DisplayName     string
	RequestID       string
	Now             func() time.Time
}

type SliRecord struct {
	ID                      string
	Name                    string
	DisplayName             string
	Version                 int64
	MeasurementRevision     int64
	DatasourceID            string
	DatasourceVersion       int64
	AgentID                 string
	AgentReachability       string
	CollectionProgress      string
	MeasurementKind         string
	ComponentMode           string
	GoodQuery               string
	TotalQuery              string
	PollingIntervalSeconds  int
	SettlingDelaySeconds    int
	EffectiveFrom           time.Time
	ActivatedAt             *time.Time
	ActivationState         string
	Freshness               string
	LatestObservation       *SliObservationRecord
	LastAttemptAt           *time.Time
	LastObservationAt       *time.Time
	NextExpectedExecutionAt *time.Time
	ListProjection          *SliListProjection
	ConfigurationRevision   int64
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type SliListProjection struct {
	DatasourceDisplayName        *string
	RecentMeasurementWindowStart *time.Time
	RecentMeasurementWindowEnd   *time.Time
	RecentMeasurements           []SliRecentMeasurement
	LinkedSLOs                   []SliLinkedObjective
	LinkedSLOCount               int
}

type SliRecentMeasurement struct {
	IntervalStart time.Time `json:"interval_start"`
	IntervalEnd   time.Time `json:"interval_end"`
	State         string    `json:"state"`
	GoodCount     *string   `json:"good_count"`
	TotalCount    *string   `json:"total_count"`
}

type SliLinkedObjective struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type sliListCursor struct {
	Version int       `json:"version"`
	Kind    string    `json:"kind"`
	Time    time.Time `json:"time"`
	ID      string    `json:"id"`
	Filter  string    `json:"filter,omitempty"`
}

type SliPage struct {
	Items       []SliRecord
	ItemCursors []string
	NextCursor  string
}

type SliObservationRecord struct {
	ObservationID       string
	MeasurementRevision int64
	IntervalStart       time.Time
	IntervalEnd         time.Time
	Outcome             string
	GoodCount           *string
	TotalCount          *string
	ErrorCode           *string
	MissingComponents   []string
	RecordedAt          time.Time
}

type SliObservationPage struct {
	Items                          []SliObservationRecord
	NextCursor                     string
	EarliestQueryableIntervalStart time.Time
	InspectedStart, InspectedEnd   *time.Time
	MissingIntervals               []SliMissingInterval
}

type SliMissingInterval struct{ IntervalStart, IntervalEnd time.Time }

type sliCreateReceipt struct {
	Version               int    `json:"version"`
	SliID                 string `json:"sli_id"`
	ConfigurationRevision int64  `json:"configuration_revision"`
}

func CreateSli(ctx context.Context, databaseURL string, input CreateSliInput) (SliRecord, error) {
	definition := sli.Definition{
		Name: input.Name, DisplayName: input.DisplayName, DatasourceID: input.DatasourceID,
		MeasurementKind: input.MeasurementKind, ComponentMode: input.ComponentMode,
		GoodQuery: input.GoodQuery, TotalQuery: input.TotalQuery,
	}
	validationErr := definition.Validate()
	if errors.Is(validationErr, sli.ErrUnsupportedMeasurementKind) || errors.Is(validationErr, sli.ErrUnsupportedComponentMode) {
		return SliRecord{}, ErrSliUnsupported
	}
	if input.Now == nil || input.RequestID == "" || !validPrintableASCII(input.IdempotencyKey, 1, 128) || validationErr != nil || input.Proof.SessionVerifier == ([32]byte{}) {
		return SliRecord{}, ErrSliInvalid
	}
	digest, err := digestSliCreate(input)
	if err != nil {
		return SliRecord{}, ErrSliInvalid
	}
	datasourceID, valid := canonicalUUID(input.DatasourceID)
	if !valid {
		return SliRecord{}, ErrSliInvalid
	}
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor")
	if err != nil {
		return SliRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	now := normalizedAgentNow(input.Now)
	receipt, err := queries.FindSliCreateReceipt(ctx, dbgen.FindSliCreateReceiptParams{ActorID: user.ID, IdempotencyKey: input.IdempotencyKey})
	if err == nil {
		if !now.Before(receipt.ExpiresAt.Time) {
			if err := queries.DeleteExpiredSliCreateReceipt(ctx, dbgen.DeleteExpiredSliCreateReceiptParams{ActorID: user.ID, IdempotencyKey: input.IdempotencyKey, ExpiresAt: timestamp(now)}); err != nil {
				return SliRecord{}, operationError(ctx, "storage: expired SLI receipt cleanup failed", err)
			}
		} else {
			if !bytes.Equal(receipt.RequestDigest, digest[:]) {
				return SliRecord{}, ErrSliConflict
			}
			var saved sliCreateReceipt
			if json.Unmarshal(receipt.Result, &saved) != nil || saved.Version != 1 || saved.ConfigurationRevision <= 0 || !canonicalUUIDCheck(saved.SliID) {
				return SliRecord{}, errors.New("storage: invalid SLI create receipt")
			}
			row, readErr := queries.GetSli(ctx, uuidToPG(uuid.MustParse(saved.SliID)))
			if errors.Is(readErr, pgx.ErrNoRows) {
				return SliRecord{}, errors.New("storage: SLI create receipt target is missing")
			}
			if readErr != nil {
				return SliRecord{}, operationError(ctx, "storage: SLI receipt result read failed", readErr)
			}
			record, err := sliRecordFromGet(ctx, queries, row, now)
			record.ConfigurationRevision = saved.ConfigurationRevision
			if err != nil {
				return SliRecord{}, err
			}
			if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
				return SliRecord{}, err
			}
			return record, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return SliRecord{}, operationError(ctx, "storage: SLI create receipt lookup failed", err)
	}

	observedDatasource, err := queries.GetDatasource(ctx, datasourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliRecord{}, ErrDatasourceNotFound
	}
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI datasource lookup failed", err)
	}
	if _, err := queries.LockAgentAccessAgent(ctx, observedDatasource.AgentID); errors.Is(err, pgx.ErrNoRows) {
		return SliRecord{}, ErrAgentNotFound
	} else if err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI agent lock failed", err)
	}
	if pending, err := queries.AgentHasPendingReassignments(ctx, observedDatasource.AgentID); err != nil {
		return SliRecord{}, err
	} else if pending {
		return SliRecord{}, ErrReassignmentPending
	}
	lockedDatasource, err := queries.LockDatasource(ctx, datasourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliRecord{}, ErrDatasourceNotFound
	}
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI datasource lock failed", err)
	}
	if lockedDatasource.AgentID != observedDatasource.AgentID || lockedDatasource.Version != observedDatasource.Version {
		return SliRecord{}, ErrDatasourceConflict
	}
	capability, err := queries.GetDatasourceAgentCapabilities(ctx, lockedDatasource.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliRecord{}, ErrAgentNotFound
	}
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI agent capability lookup failed", err)
	}
	if !capability.Enabled || !datasource.SupportsOperation(decodeCapabilities(capability.Capabilities), "collect") {
		return SliRecord{}, ErrSliAgentUnavailable
	}
	if err := prepareSliPartitions(ctx, tx, now); err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI partition preparation failed", err)
	}
	installationID, err := loadInstallationID(ctx, tx)
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI installation lookup failed", err)
	}
	if input.Secrets == nil && (len(lockedDatasource.PasswordEnvelope) > 0 || len(lockedDatasource.TokenEnvelope) > 0) {
		return SliRecord{}, ErrSourceSecretUnavailable
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return SliRecord{}, errors.New("storage: SLI identifier unavailable")
	}
	cadence := int(lockedDatasource.PollingIntervalSeconds)
	effectiveFrom := sli.LatestSettledEnd(now, cadence, int(lockedDatasource.SettlingDelaySeconds)).Add(-time.Duration(cadence) * time.Second)
	if err := queries.InsertSli(ctx, dbgen.InsertSliParams{ID: uuidToPG(id), Name: input.Name, DisplayName: input.DisplayName, CreatedAt: timestamp(now)}); err != nil {
		if isUniqueViolation(err) {
			return SliRecord{}, ErrSliConflict
		}
		return SliRecord{}, operationError(ctx, "storage: SLI create failed", err)
	}
	if err := queries.InsertSliMeasurement(ctx, dbgen.InsertSliMeasurementParams{
		SliID: uuidToPG(id), DatasourceID: lockedDatasource.ID, AgentID: lockedDatasource.AgentID,
		MeasurementKind: string(input.MeasurementKind), ComponentMode: string(input.ComponentMode),
		GoodQuery: input.GoodQuery, TotalQuery: input.TotalQuery,
		EffectiveFrom: timestamp(effectiveFrom), CreatedAt: timestamp(now),
	}); err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI measurement create failed", err)
	}
	configurationRevision, err := advanceAgentConfiguration(ctx, queries, lockedDatasource.AgentID, now)
	if err != nil {
		return SliRecord{}, err
	}
	if _, err := buildDatasourceConfigurationSnapshot(ctx, queries, installationID, lockedDatasource.AgentID, configurationRevision, input.Secrets); err != nil {
		return SliRecord{}, err
	}
	if err := queries.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: uuidToPG(id), Action: "sli.created", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI audit failed", err)
	}
	receiptResult, err := json.Marshal(sliCreateReceipt{Version: 1, SliID: id.String(), ConfigurationRevision: configurationRevision})
	if err != nil {
		return SliRecord{}, errors.New("storage: SLI receipt encoding failed")
	}
	if err := queries.InsertSliCreateReceipt(ctx, dbgen.InsertSliCreateReceiptParams{ActorID: user.ID, IdempotencyKey: input.IdempotencyKey, RequestDigest: digest[:], Result: receiptResult, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(sliCreateReceiptTTL))}); err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI create receipt insert failed", err)
	}
	row, err := queries.GetSli(ctx, uuidToPG(id))
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: created SLI read failed", err)
	}
	record, err := sliRecordFromGet(ctx, queries, row, now)
	record.ConfigurationRevision = configurationRevision
	if err != nil {
		return SliRecord{}, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return SliRecord{}, err
	}
	return record, err
}

func UpdateSliMetadata(ctx context.Context, databaseURL string, input UpdateSliMetadataInput) (SliRecord, error) {
	id, valid := canonicalUUID(input.SliID)
	if !valid || input.ExpectedVersion <= 0 || input.Now == nil || input.RequestID == "" || !validSliDisplayName(input.DisplayName) || input.Proof.SessionVerifier == ([32]byte{}) {
		return SliRecord{}, ErrSliInvalid
	}
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, "administrator", "editor")
	if err != nil {
		return SliRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	now := normalizedAgentNow(input.Now)
	current, err := queries.LockSliMetadata(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliRecord{}, ErrSliNotFound
	}
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI metadata lock failed", err)
	}
	if current.Version != input.ExpectedVersion {
		return SliRecord{}, ErrSliConflict
	}
	if current.DisplayName != input.DisplayName {
		if _, err := queries.UpdateSliDisplayName(ctx, dbgen.UpdateSliDisplayNameParams{DisplayName: input.DisplayName, UpdatedAt: timestamp(now), ID: id, Version: input.ExpectedVersion}); err != nil {
			return SliRecord{}, operationError(ctx, "storage: SLI metadata update failed", err)
		}
		if err := queries.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: id, Action: "sli.metadata.updated", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
			return SliRecord{}, operationError(ctx, "storage: SLI metadata audit failed", err)
		}
	}
	row, err := queries.GetSli(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliRecord{}, ErrSliNotFound
	}
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: updated SLI read failed", err)
	}
	record, err := sliRecordFromGet(ctx, queries, row, now)
	if err != nil {
		return SliRecord{}, err
	}
	if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return SliRecord{}, err
	}
	return record, nil
}

func validateSliListPage(limit int, cursor, query, datasourceID string) (int, agentCursor, string, string, pgtype.UUID, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 128 {
		return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
	}
	for _, character := range query {
		if unicode.IsControl(character) {
			return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
		}
	}
	normalizedQuery := strings.ToLower(strings.TrimSpace(query))
	filterID := ""
	var datasourceFilter pgtype.UUID
	if datasourceID != "" {
		parsed, valid := canonicalUUID(datasourceID)
		if !valid {
			return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
		}
		datasourceFilter = parsed
		filterID = datasourceID
	}
	filter := ""
	if normalizedQuery != "" || filterID != "" {
		digest := sha256.Sum256([]byte("almanac:sli-list:v1\x00" + normalizedQuery + "\x00" + filterID))
		filter = fmt.Sprintf("%x", digest)
	}
	if cursor == "" {
		return limit, agentCursor{Version: 1, Kind: "slis"}, filter, normalizedQuery, datasourceFilter, nil
	}
	if len(cursor) > 512 || !isASCII(cursor) {
		return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != cursor {
		return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
	}
	var decoded sliListCursor
	if json.Unmarshal(raw, &decoded, json.RejectUnknownMembers(true)) != nil || decoded.Kind != "slis" || decoded.Time.IsZero() || decoded.Time.Nanosecond()%1000 != 0 {
		return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
	}
	if _, valid := canonicalUUID(decoded.ID); !valid {
		return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
	}
	if filter == "" {
		if decoded.Version != 1 || decoded.Filter != "" {
			return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
		}
	} else if decoded.Version != 2 || decoded.Filter != filter {
		return 0, agentCursor{}, "", "", pgtype.UUID{}, ErrSliInvalid
	}
	position := agentCursor{Version: decoded.Version, Kind: decoded.Kind, Time: decoded.Time, ID: decoded.ID}
	return limit, position, filter, normalizedQuery, datasourceFilter, nil
}

func encodeSliListCursor(createdAt time.Time, id, filter string) (string, error) {
	if filter == "" {
		return encodeAgentCursor(agentCursor{Version: 1, Kind: "slis", Time: createdAt, ID: id})
	}
	raw, err := json.Marshal(sliListCursor{Version: 2, Kind: "slis", Time: createdAt, ID: id, Filter: filter})
	if err != nil {
		return "", errors.New("storage: SLI cursor encode failed")
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 512 {
		return "", ErrSliInvalid
	}
	return encoded, nil
}

func GetSli(ctx context.Context, databaseURL string, proof DatasourceProof, sliID string, now func() time.Time) (SliRecord, error) {
	id, valid := canonicalUUID(sliID)
	if !valid || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return SliRecord{}, ErrSliInvalid
	}
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return SliRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	row, err := queries.GetSli(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliRecord{}, ErrSliNotFound
	}
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: SLI read failed", err)
	}
	record, err := sliRecordFromGet(ctx, queries, row, normalizedAgentNow(now))
	if err != nil {
		return SliRecord{}, err
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return SliRecord{}, err
	}
	return record, nil
}

func ListSlis(ctx context.Context, databaseURL string, proof DatasourceProof, limit int, cursor, query, datasourceID string, now func() time.Time) (SliPage, error) {
	bounded, position, filter, normalizedQuery, datasourceFilter, err := validateSliListPage(limit, cursor, query, datasourceID)
	if err != nil || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return SliPage{}, ErrSliInvalid
	}
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return SliPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	listNow := normalizedAgentNow(now)
	rows, err := queries.ListSlis(ctx, dbgen.ListSlisParams{CreatedAt: timestamp(position.Time), CursorID: uuidOrZero(position.ID), LimitCount: int32(bounded + 1),
		RetentionCutoff: timestamp(listNow.Add(-sli.ObservationRetention)), ListNow: timestamp(listNow), Query: normalizedQuery, DatasourceFilter: datasourceFilter})
	if err != nil {
		return SliPage{}, operationError(ctx, "storage: SLI list failed", err)
	}
	page := SliPage{Items: []SliRecord{}}
	page.ItemCursors = make([]string, 0, min(len(rows), bounded))
	for index, row := range rows {
		if index == bounded {
			break
		}
		record, err := sliRecordFromListRow(ctx, row, listNow)
		if err != nil {
			return SliPage{}, err
		}
		page.Items = append(page.Items, record)
		itemCursor, err := encodeSliListCursor(record.CreatedAt, record.ID, filter)
		if err != nil {
			return SliPage{}, err
		}
		page.ItemCursors = append(page.ItemCursors, itemCursor)
	}
	if len(rows) > bounded {
		last := page.Items[len(page.Items)-1]
		page.NextCursor, err = encodeSliListCursor(last.CreatedAt, last.ID, filter)
		if err != nil {
			return SliPage{}, err
		}
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return SliPage{}, err
	}
	return page, nil
}

func ListSliObservations(ctx context.Context, databaseURL string, proof DatasourceProof, sliID string, limit int, cursor string, now func() time.Time) (SliObservationPage, error) {
	id, valid := canonicalUUID(sliID)
	bounded, position, err := validateAgentPage("sli-observations", sliID, limit, cursor)
	if !valid || err != nil || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return SliObservationPage{}, ErrSliInvalid
	}
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return SliObservationPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	definition, err := queries.GetSli(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SliObservationPage{}, ErrSliNotFound
	} else if err != nil {
		return SliObservationPage{}, operationError(ctx, "storage: SLI history owner read failed", err)
	}
	current := normalizedAgentNow(now)
	cadence := time.Duration(definition.PollingIntervalSeconds) * time.Second
	cutoff := current.Add(-sli.ObservationRetention)
	boundary := ceilSliBoundary(cutoff, cadence)
	if boundary.Before(definition.EffectiveFrom.Time) {
		boundary = definition.EffectiveFrom.Time.UTC()
	}
	page := SliObservationPage{Items: []SliObservationRecord{}, MissingIntervals: []SliMissingInterval{}, EarliestQueryableIntervalStart: boundary}
	upper := floorSliBoundary(current.Add(-time.Duration(definition.SettlingDelaySeconds)*time.Second), cadence)
	if cursor != "" {
		upper = position.Time
		if upper.After(floorSliBoundary(current.Add(-time.Duration(definition.SettlingDelaySeconds)*time.Second), cadence)) || !upper.Equal(floorSliBoundary(upper, cadence)) {
			return SliObservationPage{}, ErrSliInvalid
		}
	}
	if !definition.ActivatedAt.Valid || !upper.After(boundary) {
		if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
			return SliObservationPage{}, err
		}
		return page, nil
	}
	lower := upper.Add(-time.Duration(bounded) * cadence)
	if lower.Before(boundary) {
		lower = boundary
	}
	page.InspectedStart, page.InspectedEnd = &lower, &upper
	rows, err := queries.ListSliObservations(ctx, dbgen.ListSliObservationsParams{SliID: id, Column2: timestamp(upper), Limit: int32(bounded), RetentionCutoff: timestamp(cutoff), RangeStart: timestamp(lower)})
	if err != nil {
		return SliObservationPage{}, operationError(ctx, "storage: SLI history read failed", err)
	}
	covered := make(map[int64]bool, len(rows))
	for _, row := range rows {
		page.Items = append(page.Items, sliObservationFromListRow(row))
		if row.Outcome == string(sli.OutcomeObserved) || row.Outcome == string(sli.OutcomeCompleteEmpty) {
			covered[row.IntervalStart.Time.UnixMicro()] = true
		}
	}
	for at := upper.Add(-cadence); !at.Before(lower); at = at.Add(-cadence) {
		if covered[at.UnixMicro()] {
			continue
		}
		if n := len(page.MissingIntervals); n > 0 && page.MissingIntervals[n-1].IntervalStart.Equal(at.Add(cadence)) {
			page.MissingIntervals[n-1].IntervalStart = at
		} else {
			page.MissingIntervals = append(page.MissingIntervals, SliMissingInterval{IntervalStart: at, IntervalEnd: at.Add(cadence)})
		}
	}
	if lower.After(boundary) {
		page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: "sli-observations", AgentID: sliID, Time: lower, ID: sliID})
		if err != nil {
			return SliObservationPage{}, err
		}
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return SliObservationPage{}, err
	}
	return page, nil
}

func floorSliBoundary(value time.Time, cadence time.Duration) time.Time {
	return time.UnixMicro(value.UTC().UnixMicro() / cadence.Microseconds() * cadence.Microseconds()).UTC()
}

func ceilSliBoundary(value time.Time, cadence time.Duration) time.Time {
	boundary := floorSliBoundary(value, cadence)
	if boundary.Before(value) {
		boundary = boundary.Add(cadence)
	}
	return boundary
}

func sliRecordFromGet(ctx context.Context, queries *dbgen.Queries, row dbgen.GetSliRow, now time.Time) (SliRecord, error) {
	rows, err := queries.ListSliObservations(ctx, dbgen.ListSliObservationsParams{SliID: row.ID, Column2: pgtype.Timestamptz{}, Limit: 1, RetentionCutoff: timestamp(now.Add(-sli.ObservationRetention)), RangeStart: timestamp(now.Add(-sli.ObservationRetention))})
	if err != nil {
		return SliRecord{}, operationError(ctx, "storage: latest SLI observation read failed", err)
	}
	var latest *SliObservationRecord
	if len(rows) > 0 {
		value := sliObservationFromListRow(rows[0])
		latest = &value
	}
	acceptedAt, err := latestSliAcceptedAt(ctx, queries, row.ID, now)
	if err != nil {
		return SliRecord{}, err
	}
	return sliRecordFromFields(row.ID, row.Name, row.DisplayName, row.Version, row.MeasurementRevision,
		row.DatasourceID, row.DatasourceVersion, row.AgentID, row.MeasurementKind, row.ComponentMode,
		row.GoodQuery, row.TotalQuery, row.PollingIntervalSeconds, row.SettlingDelaySeconds,
		row.EffectiveFrom, row.ActivatedAt, row.CreatedAt, row.UpdatedAt, row.AgentEnabled, row.LastHeartbeatAt,
		row.RuntimeUpdatedAt, row.CollectionProgress, row.DesiredConfigurationRevision, row.AppliedConfigurationRevision,
		row.ConfigurationFailureCode, row.IncompatibleAttemptedAt, row.LastAttemptAt,
		row.NextExpectedExecutionAt, latest, acceptedAt, now), nil
}

func sliRecordFromListRow(ctx context.Context, row dbgen.ListSlisRow, now time.Time) (SliRecord, error) {
	var latest *SliObservationRecord
	if row.LatestObservationID.Valid {
		value := SliObservationRecord{ObservationID: row.LatestObservationID.String(), MeasurementRevision: row.LatestMeasurementRevision,
			IntervalStart: row.LatestIntervalStart.Time.UTC(), IntervalEnd: row.LatestIntervalEnd.Time.UTC(),
			Outcome: row.LatestOutcome, GoodCount: nullableSliCount(row.LatestGoodCount), TotalCount: nullableSliCount(row.LatestTotalCount),
			ErrorCode: nullableText(row.LatestErrorCode), MissingComponents: append([]string(nil), row.LatestMissingComponents...), RecordedAt: row.LatestRecordedAt.Time.UTC()}
		latest = &value
	}
	var recent []SliRecentMeasurement
	if err := json.Unmarshal([]byte(row.RecentMeasurements), &recent); err != nil {
		return SliRecord{}, operationError(ctx, "storage: malformed recent SLI list projection", err)
	}
	for index := range recent {
		if recent[index].GoodCount != nil {
			value := sli.NormalizeDecimal(*recent[index].GoodCount)
			recent[index].GoodCount = &value
		}
		if recent[index].TotalCount != nil {
			value := sli.NormalizeDecimal(*recent[index].TotalCount)
			recent[index].TotalCount = &value
		}
	}
	var linked []SliLinkedObjective
	if err := json.Unmarshal([]byte(row.LinkedSlos), &linked); err != nil {
		return SliRecord{}, operationError(ctx, "storage: malformed linked SLO list projection", err)
	}
	if len(recent) > 12 || len(linked) > 5 || row.LinkedSloCount < int32(len(linked)) {
		return SliRecord{}, operationError(ctx, "storage: invalid bounded SLI list projection", errors.New("projection exceeded its declared bound"))
	}
	lastObservation := nullableTimestamp(row.LastObservationAt)
	record := sliRecordFromFields(row.ID, row.Name, row.DisplayName, row.Version, row.MeasurementRevision,
		row.DatasourceID, row.DatasourceVersion, row.AgentID, row.MeasurementKind, row.ComponentMode,
		row.GoodQuery, row.TotalQuery, row.PollingIntervalSeconds, row.SettlingDelaySeconds,
		row.EffectiveFrom, row.ActivatedAt, row.CreatedAt, row.UpdatedAt, row.AgentEnabled, row.LastHeartbeatAt,
		row.RuntimeUpdatedAt, row.CollectionProgress, row.DesiredConfigurationRevision, row.AppliedConfigurationRevision,
		row.ConfigurationFailureCode, row.IncompatibleAttemptedAt, row.LastAttemptAt,
		row.NextExpectedExecutionAt, latest, lastObservation, now)
	datasourceName := row.DatasourceDisplayName
	record.ListProjection = &SliListProjection{
		DatasourceDisplayName:        &datasourceName,
		RecentMeasurementWindowStart: nullableTimestamp(row.RecentMeasurementWindowStart),
		RecentMeasurementWindowEnd:   nullableTimestamp(row.RecentMeasurementWindowEnd),
		RecentMeasurements:           recent, LinkedSLOs: linked, LinkedSLOCount: int(row.LinkedSloCount),
	}
	return record, nil
}

func latestSliAcceptedAt(ctx context.Context, queries *dbgen.Queries, id pgtype.UUID, now time.Time) (*time.Time, error) {
	value, err := queries.LatestSliAcceptedAt(ctx, dbgen.LatestSliAcceptedAtParams{SliID: id, IntervalStart: timestamp(now.Add(-sli.ObservationRetention))})
	if err != nil {
		return nil, operationError(ctx, "storage: latest accepted SLI observation read failed", err)
	}
	return nullableTimestamp(value), nil
}

func sliRecordFromFields(id pgtype.UUID, name, displayName string, version, measurementRevision int64,
	datasourceID pgtype.UUID, datasourceVersion int64, agentID pgtype.UUID, measurementKind, componentMode,
	goodQuery, totalQuery string, cadence, settling int32, effectiveFrom, activatedAt, createdAt, updatedAt pgtype.Timestamptz,
	agentEnabled bool, lastHeartbeatAt, runtimeUpdatedAt pgtype.Timestamptz, collectionProgress pgtype.Text,
	desiredRevision, appliedRevision pgtype.Int8,
	configurationFailureCode pgtype.Text, incompatibleAt, lastAttemptAt, nextExpectedAt pgtype.Timestamptz,
	latest *SliObservationRecord, lastObservation *time.Time, now time.Time) SliRecord {
	reachability := sliAgentReachability(agentEnabled, lastHeartbeatAt, runtimeUpdatedAt, incompatibleAt, now)
	progress := "not_started"
	_ = collectionProgress
	_ = lastAttemptAt
	_ = nextExpectedAt
	if latest != nil {
		progress = "idle"
	}
	if desiredRevision.Valid && appliedRevision.Valid && appliedRevision.Int64 < desiredRevision.Int64 {
		progress = "blocked"
	}
	if configurationFailureCode.Valid {
		progress = "blocked"
	}
	activation := "active"
	if !agentEnabled || configurationFailureCode.Valid || reachability == "incompatible" {
		activation = "blocked"
	} else if !activatedAt.Valid || !desiredRevision.Valid || !appliedRevision.Valid || appliedRevision.Int64 < desiredRevision.Int64 {
		activation = "preparing"
	} else if now.Before(effectiveFrom.Time) {
		activation = "scheduled"
	}
	freshness := "unknown"
	var lastAttempt *time.Time
	var nextExpected *time.Time
	phase := sli.DispatchPhase(id.String(), int(cadence))
	if activatedAt.Valid {
		if latest == nil {
			nextExpected = new(now)
		} else {
			base := latest.IntervalEnd.Add(time.Duration(int(cadence)+int(settling)) * time.Second)
			if now.Sub(base) >= time.Duration(cadence)*time.Second {
				base = base.Add((now.Sub(base) / (time.Duration(cadence) * time.Second)) * time.Duration(cadence) * time.Second)
			}
			nextExpected = new(base.Add(phase))
		}
	}
	if latest != nil {
		lastAttempt = new(latest.RecordedAt)
		if latest.Outcome == string(sli.OutcomeObserved) || latest.Outcome == string(sli.OutcomeCompleteEmpty) {
			freshUntil := latest.IntervalEnd.Add(time.Duration(int(cadence)+int(settling))*time.Second + phase)
			if !now.After(freshUntil) {
				freshness = "fresh"
			} else {
				freshness = "stale"
			}
		} else {
			freshness = "stale"
		}
	} else if activatedAt.Valid && now.After(activatedAt.Time.Add(time.Duration(cadence)*time.Second)) {
		freshness = "stale"
	}
	return SliRecord{ID: id.String(), Name: name, DisplayName: displayName, Version: version,
		MeasurementRevision: measurementRevision, DatasourceID: datasourceID.String(), DatasourceVersion: datasourceVersion,
		AgentID: agentID.String(), AgentReachability: reachability, CollectionProgress: progress,
		MeasurementKind: measurementKind, ComponentMode: componentMode, GoodQuery: goodQuery, TotalQuery: totalQuery,
		PollingIntervalSeconds: int(cadence), SettlingDelaySeconds: int(settling), EffectiveFrom: effectiveFrom.Time.UTC(), ActivatedAt: nullableTimestamp(activatedAt),
		ActivationState: activation, Freshness: freshness, LatestObservation: latest,
		LastAttemptAt: lastAttempt, LastObservationAt: lastObservation,
		NextExpectedExecutionAt: nextExpected, CreatedAt: createdAt.Time.UTC(), UpdatedAt: updatedAt.Time.UTC()}
}

func sliAgentReachability(enabled bool, lastHeartbeatAt, runtimeUpdatedAt, incompatibleAt pgtype.Timestamptz, now time.Time) string {
	if !enabled {
		return "disabled"
	}
	activityAt := nullableTimestamp(lastHeartbeatAt)
	if activityAt == nil {
		activityAt = nullableTimestamp(runtimeUpdatedAt)
	}
	if incompatibleAt.Valid && (activityAt == nil || !incompatibleAt.Time.Before(*activityAt)) {
		return "incompatible"
	}
	if activityAt == nil {
		return "unreported"
	}
	if !now.After(activityAt.Add(30 * time.Second)) {
		return "connected"
	}
	return "unreachable"
}

func sliObservationFromListRow(row dbgen.ListSliObservationsRow) SliObservationRecord {
	return SliObservationRecord{ObservationID: row.ObservationID.String(), MeasurementRevision: row.MeasurementRevision,
		IntervalStart: row.IntervalStart.Time.UTC(), IntervalEnd: row.IntervalEnd.Time.UTC(), Outcome: row.Outcome,
		GoodCount: nullableSliCount(row.GoodCount), TotalCount: nullableSliCount(row.TotalCount),
		ErrorCode: nullableText(row.ErrorCode), MissingComponents: append([]string(nil), row.MissingComponents...), RecordedAt: row.RecordedAt.Time.UTC()}
}

func nullableSliCount(value any) *string {
	if value == nil {
		return nil
	}
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case []byte:
		text = string(typed)
	default:
		text = fmt.Sprint(typed)
	}
	if text == "" {
		return nil
	}
	text = sli.NormalizeDecimal(text)
	return &text
}

func nextCanonicalBoundary(after time.Time, cadenceSeconds int) time.Time {
	cadence := int64(cadenceSeconds)
	unix := after.UTC().Unix()
	remainder := unix % cadence
	if remainder != 0 {
		unix += cadence - remainder
	} else if after.Nanosecond() != 0 {
		unix += cadence
	}
	return time.Unix(unix, 0).UTC()
}

func nextSliActivationBoundary(now time.Time, cadenceSeconds int) time.Time {
	return nextCanonicalBoundary(now.Add(time.Duration(cadenceSeconds)*time.Second), cadenceSeconds)
}

func reconcilePendingSliActivation(ctx context.Context, queries *dbgen.Queries, agentID pgtype.UUID, configurationRevision int64, now time.Time) error {
	if pending, err := queries.AgentHasPendingReassignments(ctx, agentID); err != nil {
		return err
	} else if pending {
		return nil
	}
	configuration, err := queries.GetAgentConfiguration(ctx, agentID)
	if err != nil {
		return err
	}
	if configuration.Revision != configurationRevision {
		return ErrAgentOwnershipDenied
	}
	pending, err := queries.ListPendingSlisForAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	rescheduled := false
	for _, measurement := range pending {
		if measurement.Revision == 1 {
			continue
		}
		if measurement.EffectiveFrom.Time.After(now) {
			continue
		}
		cadence := int(measurement.PollingIntervalSeconds)
		next := nextSliActivationBoundary(now, cadence)
		updated, err := queries.UpdateSliEffectiveFrom(ctx, dbgen.UpdateSliEffectiveFromParams{
			EffectiveFrom: timestamp(next), SliID: measurement.SliID, Revision: measurement.Revision,
			EffectiveFrom_2: measurement.EffectiveFrom,
		})
		if err != nil {
			return err
		}
		if updated != 1 {
			return ErrSliConflict
		}
		rescheduled = true
	}
	if rescheduled {
		_, err := advanceAgentConfiguration(ctx, queries, agentID, now)
		return err
	}
	for _, measurement := range pending {
		if measurement.Revision != 1 && !measurement.EffectiveFrom.Time.After(now) {
			return ErrSliConflict
		}
		updated, err := queries.ActivateSliMeasurement(ctx, dbgen.ActivateSliMeasurementParams{
			SliID: measurement.SliID, Revision: measurement.Revision,
			EffectiveFrom: measurement.EffectiveFrom, ActivatedAt: timestamp(now),
		})
		if err != nil {
			return err
		}
		if updated != 1 {
			return ErrSliConflict
		}
	}
	return nil
}

func digestSliCreate(input CreateSliInput) ([32]byte, error) {
	value := struct {
		Version         int                 `json:"version"`
		Name            string              `json:"name"`
		DisplayName     string              `json:"display_name"`
		DatasourceID    string              `json:"datasource_id"`
		MeasurementKind sli.MeasurementKind `json:"measurement_kind"`
		ComponentMode   sli.ComponentMode   `json:"component_mode"`
		GoodQuery       string              `json:"good_query"`
		TotalQuery      string              `json:"total_query"`
	}{1, input.Name, input.DisplayName, input.DatasourceID, input.MeasurementKind, input.ComponentMode, input.GoodQuery, input.TotalQuery}
	encoded, err := json.Marshal(value)
	if err != nil {
		return [32]byte{}, err
	}
	defer clear(encoded)
	return sha256.Sum256(encoded), nil
}

func canonicalUUIDCheck(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value && parsed != uuid.Nil
}

func validSliDisplayName(value string) bool {
	definition := sli.Definition{Name: "valid-name", DisplayName: value, DatasourceID: "00000000-0000-4000-8000-000000000002",
		MeasurementKind: sli.MeasurementKindEventCounts, ComponentMode: sli.ComponentModeIntervalCount, GoodQuery: "good", TotalQuery: "total"}
	return definition.Validate() == nil
}
