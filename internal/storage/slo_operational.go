package storage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/dsolsona/almanac/internal/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const (
	operationalCalculationVersion  = "occurrences-operational-v1"
	sloOverviewTTL                 = 2 * time.Minute
	sloOverviewSessionLimit        = 4
	sloOverviewInstallationLimit   = 64
	sloOverviewCleanupBatch        = 100
	sloOperationalFactsLimit       = 2_500_000
	sloOverviewDefinitionLimit     = 10_000
	sloOverviewProjectionByteLimit = 16 << 20
	sloOverviewMinimumBytes        = 16 << 10
	sloOverviewSessionByteLimit    = sloOverviewSessionLimit * sloOverviewProjectionByteLimit
	sloOverviewInstallationBytes   = sloOverviewInstallationLimit * sloOverviewProjectionByteLimit
	sloOperationalSeriesLimit      = 1500
	sloOperationalReadAttempts     = 3
)

var (
	ErrOperationalRange       = errors.New("storage: invalid operational range")
	ErrOperationalRangeDense  = errors.New("storage: operational range too dense")
	ErrOperationalResource    = errors.New("storage: operational resource limit")
	ErrOperationalSnapshot    = errors.New("storage: invalid operational snapshot cursor")
	ErrOperationalSnapshotOld = errors.New("storage: operational snapshot expired")
)

func validOperationalQuery(query string) bool {
	return utf8.ValidString(query) && utf8.RuneCountInString(query) <= 128
}

func addProjectionPayloadBytes(total, added int) (int, error) {
	if added < 0 || total < 0 || total > sloOverviewProjectionByteLimit-added {
		return total, ErrOperationalResource
	}
	return total + added, nil
}

func retryOperationalRead(ctx context.Context, attempt func() error) error {
	for range sloOperationalReadAttempts {
		if err := ctx.Err(); err != nil {
			return operationError(ctx, "storage: operational read cancelled", err)
		}
		err := attempt()
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "40001" {
			err = errReadSerialization
		}
		if !errors.Is(err, errReadSerialization) {
			return err
		}
	}
	return errReadSerialization
}

type OperationalOverviewInput struct {
	Runtime          *Runtime
	Proof            DatasourceProof
	Query, ServiceID string
	Urgency, Cursor  string
	snapshotID       string
	Limit            int
	Now              func() time.Time
}

type OperationalDetailInput struct {
	retainedReader     *retainedSeriesReader
	historyPublication *retainedHistoryPublication
	releaseSnapshot    func() error
	currentOnly        bool
	Runtime            *Runtime
	Proof              DatasourceProof
	ID                 string
	At, Start, End     *time.Time
	BurnWindow         int
	Period             string
	Now                func() time.Time
}

// ReadSloOperationalDetail loads one authorized repeatable-read snapshot, then
// reduces retained history without holding its connection and rechecks authority.
func ReadSloOperationalDetail(ctx context.Context, databaseURL string, input OperationalDetailInput) (api.SloOperationalDetail, error) {
	var result api.SloOperationalDetail
	err := retryOperationalRead(ctx, func() error {
		var attemptErr error
		result, attemptErr = readSloOperationalDetailAttempt(ctx, databaseURL, input)
		return attemptErr
	})
	return result, err
}

func readSloOperationalDetailAttempt(ctx context.Context, databaseURL string, input OperationalDetailInput) (api.SloOperationalDetail, error) {
	if input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) || !canonicalUUIDCheck(input.ID) {
		return api.SloOperationalDetail{}, ErrObjectiveInvalid
	}
	if input.BurnWindow == 0 {
		input.BurnWindow = 3600
	}
	if input.BurnWindow != 3600 && input.BurnWindow != 21600 || (input.Start == nil) != (input.End == nil) {
		return api.SloOperationalDetail{}, ErrOperationalRange
	}
	id, _ := canonicalUUID(input.ID)
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if input.Runtime != nil {
		// The bounded 20-MiB payload must fit each materialization/sort workspace.
		if _, err := tx.Exec(ctx, "SET LOCAL work_mem = '32MB'"); err != nil {
			return api.SloOperationalDetail{}, readTransactionError(ctx, "storage: operational read memory setup failed", err)
		}
	}
	row, err := q.GetSlo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SloOperationalDetail{}, ErrObjectiveNotFound
	}
	if err != nil {
		return api.SloOperationalDetail{}, readTransactionError(ctx, "storage: operational SLO definition read failed", err)
	}
	contextRow, err := q.GetSloOperationalContext(ctx, dbgen.GetSloOperationalContextParams{SliID: row.SliID, Revision: row.MeasurementRevision})
	if err != nil {
		return api.SloOperationalDetail{}, readTransactionError(ctx, "storage: operational SLI context read failed", err)
	}
	if contextRow.PollingIntervalSeconds != row.CadenceSeconds || contextRow.MeasurementRevision != row.MeasurementRevision {
		return api.SloOperationalDetail{}, ErrObjectiveInvalid
	}
	released := false
	input.releaseSnapshot = func() error {
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return err
		}
		closeInitialOwnerConnection(conn)
		released = true
		return nil
	}
	input.historyPublication = new(retainedHistoryPublication)
	result, err := loadRetainedSloHistory(ctx, q, row, contextRow, input)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	result.Context.SliDisplayName = contextRow.SliDisplayName
	result.Context.DatasourceDisplayName = contextRow.DatasourceDisplayName
	publication := input.historyPublication != nil && input.historyPublication.generation.SloID.Valid
	if released || publication {
		readStarted := started
		if !released {
			if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
				return api.SloOperationalDetail{}, err
			}
			closeInitialOwnerConnection(conn)
		}
		// Recheck authority after assembling the detached snapshot.
		conn, q, tx, user, installation, session, started, err = beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, "administrator", "editor", "viewer")
		if err != nil {
			return api.SloOperationalDetail{}, err
		}
		defer closeInitialOwnerConnection(conn)
		defer rollbackTransaction(tx)
		if started.Before(readStarted) {
			return api.SloOperationalDetail{}, ErrInvalidSession
		}
	}
	if publication {
		result, err = publishRetainedHistory(input.Runtime.Bind(ctx), q, result, *input.historyPublication)
		if err != nil {
			return api.SloOperationalDetail{}, err
		}
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloOperationalDetail{}, err
	}
	return result, nil
}

func sampleBucketLimit(start, end time.Time, cadence time.Duration) int {
	ticks := int(end.Sub(start) / cadence)
	if ticks > sloOperationalSeriesLimit {
		return 375
	}
	return max(1, ticks)
}

func operationalComplianceDay(result slo.WindowResult, end, validFrom time.Time, sloID uuid.UUID, revision, target string, window, cadence time.Duration) api.SloDailyComplianceDay {
	status, explanation := dailyComplianceStatus(result, result.End, validFrom, window, cadence)
	quality, performance := operationalQuality(result), operationalPerformance(result)
	if status == api.SloDailyComplianceDayStatusUnknown {
		if performance.ObservedState == nil && (performance.State == "met" || performance.State == "violated") {
			observed := api.SloEvaluationPerformanceObservedState(performance.State)
			performance.ObservedState = &observed
		}
		performance.State, performance.Provisional = "unknown", true
		performance.Reason, quality.Reason = &explanation, &explanation
	}
	return api.SloDailyComplianceDay{UtcDate: openapi_types.Date{Time: end.Add(-24 * time.Hour)},
		DayStart: end.Add(-24 * time.Hour), DayEnd: end, RequestedAt: end, EffectiveAt: result.End, SloId: sloID,
		DefinitionRevision: revision, Target: sli.NormalizeDecimal(target), Quality: quality, Performance: performance,
		Status: status, Explanation: explanation}
}

func utcDayStart(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func dailyComplianceStatus(result slo.WindowResult, dayEnd, validFrom time.Time, window, cadence time.Duration) (api.SloDailyComplianceDayStatus, string) {
	if !dayEnd.After(validFrom) || result.Performance == "not_applicable" && result.Expected == 0 {
		return api.SloDailyComplianceDayStatusNotApplicable, "before_evaluation_validity"
	}
	if result.EligibleStart.After(result.RequestedStart) || result.Expected != int64(window/cadence) {
		return api.SloDailyComplianceDayStatusUnknown, "shortened_window"
	}
	if result.State == "zero_traffic" {
		return api.SloDailyComplianceDayStatusUnknown, "zero_traffic"
	}
	if result.KnownEmpty > 0 {
		return api.SloDailyComplianceDayStatusUnknown, "known_empty"
	}
	if result.Expired > 0 || result.State == "expired_history" {
		return api.SloDailyComplianceDayStatusUnknown, "expired_history"
	}
	if result.Missing > 0 || result.State == "partial" || result.State == "no_data" {
		return api.SloDailyComplianceDayStatusUnknown, "incomplete_measurements"
	}
	if result.State == "unavailable" || result.Provisional {
		if result.Reason != "" {
			return api.SloDailyComplianceDayStatusUnknown, result.Reason
		}
		return api.SloDailyComplianceDayStatusUnknown, "unavailable"
	}
	if result.Performance == "met" {
		return api.SloDailyComplianceDayStatusMet, "target_met"
	}
	if result.Performance == "violated" {
		return api.SloDailyComplianceDayStatusViolated, "target_violated"
	}
	if result.Reason != "" {
		return api.SloDailyComplianceDayStatusUnknown, result.Reason
	}
	return api.SloDailyComplianceDayStatusUnknown, "performance_unknown"
}

func bucketBoundaries(start, end time.Time, ticks, buckets int, cadence time.Duration) []time.Time {
	boundaries := make([]time.Time, buckets+1)
	for index := range boundaries {
		boundaries[index] = start.Add(time.Duration(ticks*index/buckets) * cadence)
	}
	boundaries[len(boundaries)-1] = end
	return boundaries
}

func sloRecordDTO(value SloRecord) api.Slo {
	result := api.Slo{Id: uuid.MustParse(value.ID), Name: value.Name, DisplayName: value.DisplayName, Version: strconv.FormatInt(value.Version, 10),
		DefinitionRevision: strconv.FormatInt(value.DefinitionRevision, 10), ServiceId: uuid.MustParse(value.ServiceID), ServiceDisplayName: value.ServiceDisplayName,
		SliId: uuid.MustParse(value.SliID), SliDisplayName: value.SliDisplayName, Target: value.Target, RollingWindowSeconds: value.RollingWindowSeconds,
		CadenceSeconds: int(value.CadenceSeconds), ActivatedAt: value.ActivatedAt, EvaluationValidFrom: value.EvaluationValidFrom,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
	if value.HistoricalRequestedBy != nil {
		result.HistoricalRequestedBy = new(uuid.MustParse(*value.HistoricalRequestedBy))
	}
	if value.HistoricalRequestedAt != nil {
		result.HistoricalRequestedAt = value.HistoricalRequestedAt
	}
	return result
}

func unavailableOperationalSamples(start, end time.Time, cadence time.Duration, revision string, buckets int, reason string) []api.SloOperationalSample {
	ticks := int(floorSliBoundary(end, cadence).Sub(floorSliBoundary(start, cadence)) / cadence)
	if ticks <= 0 {
		return []api.SloOperationalSample{}
	}
	buckets = min(max(1, buckets), ticks)
	boundaries := bucketBoundaries(start, end, ticks, buckets, cadence)
	samples := make([]api.SloOperationalSample, 0, buckets)
	for index := range buckets {
		reasonValue := reason
		samples = append(samples, api.SloOperationalSample{BucketStart: boundaries[index], BucketEnd: boundaries[index+1], EvaluationAt: boundaries[index+1],
			State: "missing", Quality: api.SloOperationalQuality{State: "unavailable", Freshness: "unknown", Reason: &reasonValue}, Revision: revision,
			RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{}})
	}
	return samples
}

type overviewCursor struct {
	SnapshotID string `json:"s"`
	Offset     int    `json:"o"`
	MAC        string `json:"m"`
}

type overviewSnapshot struct {
	ID              pgtype.UUID
	ActorID         pgtype.UUID
	SessionVerifier []byte
	Role            string
	QueryText       string
	ServiceID       pgtype.UUID
	Urgency         pgtype.Text
	PageLimit       int32
	GroupCounts     []byte
	State           string
	ExpiresAt       pgtype.Timestamptz
}

// ReadSloOperationalOverview returns a fixed-expiry, actor/session-bound page.
func ReadSloOperationalOverview(ctx context.Context, databaseURL string, input OperationalOverviewInput) (api.SloOperationalOverviewPage, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "slo.overview.read")
	defer span.End()
	if input.Now == nil || input.Proof.SessionVerifier == ([32]byte{}) || input.Limit < 0 || input.Limit > 200 || !validOperationalQuery(input.Query) || len(input.Cursor) > 512 {
		return api.SloOperationalOverviewPage{}, ErrOperationalRange
	}
	if input.Limit == 0 {
		input.Limit = 50
	}
	input.Query = strings.ToLower(strings.TrimSpace(input.Query))
	if input.Urgency != "" && input.Urgency != "needs_attention" && input.Urgency != "watch" && input.Urgency != "healthy" {
		return api.SloOperationalOverviewPage{}, ErrOperationalRange
	}
	if input.ServiceID != "" {
		if _, ok := canonicalUUID(input.ServiceID); !ok {
			return api.SloOperationalOverviewPage{}, ErrOperationalRange
		}
	}
	var reservedID uuid.UUID
	cleanupFailedRead := func(readErr error) error {
		if reservedID == uuid.Nil {
			return readErr
		}
		// A cancelled browser request must not occupy a session snapshot slot
		// until expiry. Release only the reservation created by this request.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		return errors.Join(readErr, deleteSloOverviewSnapshot(cleanupCtx, databaseURL, input, reservedID))
	}
	if input.Cursor == "" && input.snapshotID == "" {
		var id uuid.UUID
		var role string
		var reused bool
		var err error
		err = retryOperationalRead(ctx, func() error {
			id, role, reused, err = reserveSloOverviewSnapshot(ctx, databaseURL, input)
			return err
		})
		if err != nil {
			return api.SloOperationalOverviewPage{}, err
		}
		if !reused {
			reservedID = id
			err = retryOperationalRead(ctx, func() error {
				return materializeSloOverviewSnapshot(ctx, databaseURL, input, id, role)
			})
			if err != nil {
				return api.SloOperationalOverviewPage{}, cleanupFailedRead(err)
			}
		}
		input.snapshotID = id.String()
	}
	var result api.SloOperationalOverviewPage
	err := retryOperationalRead(ctx, func() error {
		var attemptErr error
		result, attemptErr = readSloOverviewPage(ctx, databaseURL, input)
		return attemptErr
	})
	if err != nil {
		return result, cleanupFailedRead(err)
	}
	return result, nil
}

func reserveSloOverviewSnapshot(ctx context.Context, databaseURL string, input OperationalOverviewInput) (uuid.UUID, string, bool, error) {
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{}, "administrator", "editor", "viewer")
	if err != nil {
		return uuid.Nil, "", false, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	now := normalizedAgentNow(input.Now)
	// Ready reads do not consume admission. Check before the global admission
	// lock so ten independent users are not serialized behind each other's reads.
	if id, reused, err := findReusableOverview(ctx, q, input, now, user.ID, user.Role); err != nil {
		return uuid.Nil, "", false, err
	} else if reused {
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return uuid.Nil, "", false, err
		}
		return uuid.UUID(id.Bytes), user.Role, true, nil
	}
	// Filter changes may overlap briefly while the preceding HTTP request is
	// being cancelled. Serialize admission without mistaking that overlap for
	// an exhausted snapshot budget; keep the wait bounded and cancellable.
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout = '250ms'"); err != nil {
		return uuid.Nil, "", false, readTransactionError(ctx, "storage: overview admission timeout setup failed", err)
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('slo_overview_snapshot_admission', 0))"); err != nil {
		if failure, ok := errors.AsType[*pgconn.PgError](err); ok && failure.Code == "55P03" {
			return uuid.Nil, "", false, ErrOperationalResource
		}
		return uuid.Nil, "", false, readTransactionError(ctx, "storage: overview admission lock failed", err)
	}
	if err = q.DeleteExpiredSloOverviewSnapshots(ctx, dbgen.DeleteExpiredSloOverviewSnapshotsParams{ExpiresAt: timestamp(now), Limit: sloOverviewCleanupBatch}); err != nil {
		return uuid.Nil, "", false, readTransactionError(ctx, "storage: overview snapshot cleanup failed", err)
	}
	if id, reused, err := findReusableOverview(ctx, q, input, now, user.ID, user.Role); err != nil {
		return uuid.Nil, "", false, err
	} else if reused {
		if err := finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
			return uuid.Nil, "", false, err
		}
		return uuid.UUID(id.Bytes), user.Role, true, nil
	}
	usage, err := q.SloOverviewSnapshotUsage(ctx, dbgen.SloOverviewSnapshotUsageParams{
		ActorID: user.ID, SessionVerifier: input.Proof.SessionVerifier[:], Now: timestamp(now),
		ReservedBytes: sloOverviewProjectionByteLimit, MinimumBytes: sloOverviewMinimumBytes,
	})
	if err != nil {
		return uuid.Nil, "", false, readTransactionError(ctx, "storage: overview resource accounting failed", err)
	}
	if !canReserveOverview(usage) {
		return uuid.Nil, "", false, ErrOperationalResource
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return uuid.Nil, "", false, errors.New("storage: overview snapshot identifier unavailable")
	}
	cursorSecret := make([]byte, 32)
	if _, err = rand.Read(cursorSecret); err != nil {
		return uuid.Nil, "", false, errors.New("storage: overview cursor secret unavailable")
	}
	var serviceID pgtype.UUID
	if input.ServiceID != "" {
		parsed, _ := canonicalUUID(input.ServiceID)
		serviceID = parsed
	}
	var urgency pgtype.Text
	if input.Urgency != "" {
		urgency = pgtype.Text{String: input.Urgency, Valid: true}
	}
	counts, _ := json.Marshal(api.SloUrgencyCounts{})
	if err = q.InsertSloOverviewSnapshot(ctx, dbgen.InsertSloOverviewSnapshotParams{
		ID: uuidToPG(id), CursorSecret: cursorSecret, ActorID: user.ID, SessionVerifier: input.Proof.SessionVerifier[:], Role: user.Role,
		QueryText: strings.TrimSpace(input.Query), ServiceID: serviceID, Urgency: urgency, PageLimit: int32(input.Limit),
		GroupCounts: counts, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(sloOverviewTTL)),
	}); err != nil {
		return uuid.Nil, "", false, readTransactionError(ctx, "storage: overview snapshot reservation failed", err)
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return uuid.Nil, "", false, err
	}
	return id, user.Role, false, nil
}

func materializeSloOverviewSnapshot(ctx context.Context, databaseURL string, input OperationalOverviewInput, snapshotID uuid.UUID, expectedRole string) error {
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if user.Role != expectedRole {
		return ErrAgentForbidden
	}
	now := normalizedAgentNow(input.Now)
	reservation, err := q.GetSloOverviewSnapshot(ctx, uuidToPG(snapshotID))
	if err != nil {
		return readTransactionError(ctx, "storage: overview reservation read failed", err)
	}
	if reservation.ActorID != user.ID || !slices.Equal(reservation.SessionVerifier, input.Proof.SessionVerifier[:]) || reservation.Role != user.Role || !now.Before(reservation.ExpiresAt.Time) || reservation.State != "preparing" {
		return ErrOperationalSnapshotOld
	}
	serviceID, urgency := overviewScopeFilters(input)
	stats, err := q.MaterializeSloOverviewReferences(ctx, dbgen.MaterializeSloOverviewReferencesParams{SnapshotID: uuidToPG(snapshotID),
		CalculationVersion: operationalCalculationVersion, Now: timestamp(now), ServiceID: serviceID,
		QueryText: pgtype.Text{String: input.Query, Valid: input.Query != ""}, Urgency: urgency})
	if err != nil {
		if failure, ok := errors.AsType[*pgconn.PgError](err); ok && failure.Code == "23503" && failure.ConstraintName == "almanac_slo_overview_snapshot_items_projection_digest_fkey" {
			return errReadSerialization
		}
		return readTransactionError(ctx, "storage: overview reference publication failed", err)
	}
	if stats.Definitions > sloOverviewDefinitionLimit || stats.PayloadBytes > sloOverviewProjectionByteLimit || stats.Finished != 1 {
		return ErrOperationalResource
	}
	if stats.SelectedCount > int64(input.Limit) {
		fingerprint, err := overviewScopeFingerprint(ctx, q, input, now)
		if err != nil {
			return err
		}
		if err := q.SetSloOverviewFingerprint(ctx, dbgen.SetSloOverviewFingerprintParams{ID: uuidToPG(snapshotID), PublicationFingerprint: fingerprint.Fingerprint}); err != nil {
			return readTransactionError(ctx, "storage: overview fingerprint persistence failed", err)
		}
	}
	return finishAgentRead(ctx, tx, input.Now, started, user, installation, session)
}

func readSloOverviewPage(ctx context.Context, databaseURL string, input OperationalOverviewInput) (api.SloOperationalOverviewPage, error) {
	cursor := overviewCursor{SnapshotID: input.snapshotID, Offset: 0}
	if input.Cursor != "" {
		var valid bool
		cursor, valid = decodeOverviewCursor(input.Cursor)
		if !valid {
			return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
		}
	}
	if cursor.Offset < 0 || cursor.Offset > sloOverviewDefinitionLimit {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	id, valid := canonicalUUID(cursor.SnapshotID)
	if !valid {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	conn, q, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, "administrator", "editor", "viewer")
	if err != nil {
		return api.SloOperationalOverviewPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	now := normalizedAgentNow(input.Now)
	if err = q.DeleteExpiredSloOverviewSnapshots(ctx, dbgen.DeleteExpiredSloOverviewSnapshotsParams{ExpiresAt: timestamp(now), Limit: sloOverviewCleanupBatch}); err != nil {
		return api.SloOperationalOverviewPage{}, readTransactionError(ctx, "storage: overview snapshot cleanup failed", err)
	}
	snapshotRow, err := q.GetSloOverviewSnapshot(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshotOld
	}
	if err != nil {
		return api.SloOperationalOverviewPage{}, readTransactionError(ctx, "storage: overview snapshot read failed", err)
	}
	if !now.Before(snapshotRow.ExpiresAt.Time) {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshotOld
	}
	if snapshotRow.ActorID != user.ID || !slices.Equal(snapshotRow.SessionVerifier, input.Proof.SessionVerifier[:]) || snapshotRow.Role != user.Role || snapshotRow.QueryText != strings.TrimSpace(input.Query) || snapshotRow.PageLimit != int32(input.Limit) || cursor.Offset%input.Limit != 0 {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	if snapshotRow.ServiceID.Valid != (input.ServiceID != "") || snapshotRow.ServiceID.Valid && snapshotRow.ServiceID.String() != input.ServiceID || snapshotRow.Urgency.Valid != (input.Urgency != "") || snapshotRow.Urgency.Valid && snapshotRow.Urgency.String != input.Urgency {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	if snapshotRow.State != "ready" {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshotOld
	}
	if input.Cursor != "" && !validOverviewCursor(snapshotRow.CursorSecret, cursor) {
		return api.SloOperationalOverviewPage{}, ErrOperationalSnapshot
	}
	var counts api.SloUrgencyCounts
	if json.Unmarshal(snapshotRow.GroupCounts, &counts) != nil {
		return api.SloOperationalOverviewPage{}, errors.New("storage: invalid overview snapshot counts")
	}
	rows, err := q.ListSloOverviewSnapshotItems(ctx, dbgen.ListSloOverviewSnapshotItemsParams{SnapshotID: id, Ordinal: int32(cursor.Offset), Limit: int32(input.Limit + 1)})
	if err != nil {
		return api.SloOperationalOverviewPage{}, readTransactionError(ctx, "storage: overview page read failed", err)
	}
	more := len(rows) > input.Limit
	if more {
		rows = rows[:input.Limit]
	}
	items := make([]api.SloOperationalOverviewItem, 0, len(rows))
	for _, row := range rows {
		var item api.SloOperationalOverviewItem
		if json.Unmarshal(row.Payload, &item) != nil || !item.Urgency.Valid() {
			return api.SloOperationalOverviewPage{}, errors.New("storage: invalid overview snapshot item")
		}
		item.Slo.Target = sli.NormalizeDecimal(item.Slo.Target)
		if row.ProjectionCurrent {
			if item.Generation != nil {
				item.CurrentState = new(api.SloOperationalOverviewItemCurrentState("ready"))
			}
		} else if row.ProjectionValid && item.Generation != nil && item.CurrentState != nil && (*item.CurrentState == "stale" || *item.CurrentState == "failed") {
			boundary := floorSliBoundary(snapshotRow.CreatedAt.Time.UTC(), time.Duration(item.Slo.CadenceSeconds)*time.Second)
			item.Generation.CalculationLagSeconds = float32(max(0, boundary.Sub(item.Generation.EvaluatedThrough).Seconds()))
			history := api.SloOperationalOverviewItemBudgetHistoryState("preparing")
			if *item.CurrentState == "failed" {
				history = "unavailable"
			}
			item.BudgetHistoryState = &history
		} else {
			reason := "current_calculation_pending"
			if row.PreparationFailed {
				reason = "current_calculation_failed"
			} else if row.SourceFailed {
				reason = "source_aggregate_failed"
			}
			item = unavailableOverviewItem(item, snapshotRow.CreatedAt.Time.UTC(), reason)
		}
		items = append(items, item)
	}
	next := ""
	previous := ""
	if cursor.Offset > 0 {
		previousOffset := max(0, cursor.Offset-input.Limit)
		previous = encodeOverviewCursor(snapshotRow.CursorSecret, overviewCursor{SnapshotID: id.String(), Offset: previousOffset})
	}
	if more {
		next = encodeOverviewCursor(snapshotRow.CursorSecret, overviewCursor{SnapshotID: id.String(), Offset: cursor.Offset + len(items)})
	} else if input.Cursor == "" {
		// A single page has no continuation to pin. Release it so ordinary
		// filter changes cannot exhaust the cursor-snapshot admission budget.
		if err = q.DeleteSloOverviewSnapshot(ctx, id); err != nil {
			return api.SloOperationalOverviewPage{}, readTransactionError(ctx, "storage: single-page overview snapshot cleanup failed", err)
		}
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return api.SloOperationalOverviewPage{}, err
	}
	return api.SloOperationalOverviewPage{SnapshotId: uuid.UUID(id.Bytes), Items: items, GroupCounts: counts, NextCursor: next, PreviousCursor: previous}, nil
}

func deleteSloOverviewSnapshot(ctx context.Context, databaseURL string, input OperationalOverviewInput, id uuid.UUID) error {
	conn, queries, tx, user, installation, session, started, err := beginPooledAgentRead(ctx, databaseURL, input.Runtime, input.Proof.SessionVerifier, input.Now, pgx.TxOptions{}, "administrator", "editor", "viewer")
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	if _, err = tx.Exec(ctx, "DELETE FROM public.almanac_slo_overview_snapshots WHERE id=$1 AND actor_id=$2 AND session_verifier=$3", uuidToPG(id), user.ID, input.Proof.SessionVerifier[:]); err != nil {
		return readTransactionError(ctx, "storage: failed overview reservation cleanup", err)
	}
	if err = finishAgentRead(ctx, tx, input.Now, started, user, installation, session); err != nil {
		return err
	}
	_ = queries
	return nil
}

func encodeOverviewCursor(secret []byte, value overviewCursor) string {
	value.MAC = overviewCursorMAC(secret, value)
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeOverviewCursor(value string) (overviewCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) > 256 {
		return overviewCursor{}, false
	}
	var cursor overviewCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.SnapshotID == "" || cursor.MAC == "" {
		return overviewCursor{}, false
	}
	return cursor, true
}

func validOverviewCursor(secret []byte, cursor overviewCursor) bool {
	return hmac.Equal([]byte(cursor.MAC), []byte(overviewCursorMAC(secret, cursor)))
}

func overviewCursorMAC(secret []byte, cursor overviewCursor) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(cursor.SnapshotID + ":" + strconv.Itoa(cursor.Offset)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func evaluateOperationalAt(ctx context.Context, base slo.WindowInput, at time.Time, duration time.Duration) (slo.WindowResult, error) {
	base.At, base.Duration = at, duration
	// Short burn windows must not validate and walk months of history loaded
	// for the budget chart. Eligibility and missing counts remain arithmetic.
	first, _ := slices.BinarySearchFunc(base.Observations, maxTime(at.Add(-duration), base.ValidFrom, base.SliValidFrom, base.RetentionStart), func(fact slo.Observation, at time.Time) int { return fact.Start.Compare(at) })
	base.Observations = base.Observations[first:]
	return slo.EvaluateOperationalWindowContext(ctx, base)
}

func operationalQuality(result slo.WindowResult) api.SloOperationalQuality {
	return api.SloOperationalQuality{State: api.SloOperationalQualityState(result.State), Freshness: api.SloOperationalQualityFreshness(result.Freshness),
		ExpectedIntervals: result.Expected, ObservedIntervals: result.Observed, KnownEmptyIntervals: result.KnownEmpty,
		MissingIntervals: result.Missing, ExpiredIntervals: result.Expired, ZeroTrafficIntervals: result.ZeroTraffic,
		Coverage: result.Coverage, FirstObservationAt: result.FirstObservation, LastObservationAt: result.LastObservation,
		LatestReceiptAt: result.LatestReceipt, Reason: stringPointer(result.Reason)}
}

func operationalPerformance(result slo.WindowResult) api.SloEvaluationPerformance {
	performance := api.SloEvaluationPerformance{State: api.SloEvaluationPerformanceState(result.Performance), Provisional: result.Provisional,
		Reason: stringPointer(result.Reason), Good: result.Good, Total: result.Total, Bad: result.Bad, Attainment: result.Attainment,
		AllowedBudget: result.Allowed, RemainingBudget: result.Remaining, ConsumedFraction: result.Consumed,
		RemainingFraction: result.RemainingFraction, Burn: result.Burn, BudgetUnit: "events"}
	if result.ObservedPerformance != "" {
		state := api.SloEvaluationPerformanceObservedState(result.ObservedPerformance)
		performance.ObservedState = &state
	}
	return performance
}

func operationalValue(result slo.WindowResult) api.SloOperationalValue {
	return api.SloOperationalValue{EvaluationAt: result.End, Quality: operationalQuality(result), Performance: operationalPerformance(result), Window: api.SloEvaluationWindow{RequestedStart: result.RequestedStart,
		EffectiveStart: result.EffectiveStart, End: result.End, EligibleStart: result.EligibleStart, RetentionStart: result.RetentionStart,
		ActivatedAt: result.End, EvaluationValidFrom: result.EligibleStart}}
}

func applyOperationalDefinitionWindow(value *api.SloOperationalValue, activatedAt, validFrom time.Time, requestedBy pgtype.UUID, requestedAt pgtype.Timestamptz) {
	value.Window.ActivatedAt = activatedAt
	value.Window.EvaluationValidFrom = validFrom
	if requestedAt.Valid {
		value.Window.HistoricalRequestedAt = optionalTime(requestedAt)
	}
	if requestedBy.Valid {
		value.Window.HistoricalRequestedBy = new(uuid.UUID(requestedBy.Bytes))
	}
}

func burnValue(result slo.WindowResult, seconds int) api.SloBurnValue {
	quality, performance := operationalQuality(result), operationalPerformance(result)
	if result.EligibleStart.After(result.EffectiveStart) {
		reason := "incomplete_measurements"
		quality.Reason, performance.Reason, performance.Provisional = &reason, &reason, true
	}
	return api.SloBurnValue{WindowSeconds: api.SloBurnValueWindowSeconds(seconds), Start: result.RequestedStart, End: result.End, Quality: quality, Performance: performance}
}

func unavailableBurn(seconds int, at time.Time, reason string) api.SloBurnValue {
	quality := api.SloOperationalQuality{State: "unavailable", Freshness: "unknown", Reason: &reason}
	performance := api.SloEvaluationPerformance{State: "unknown", BudgetUnit: "events", Reason: &reason}
	return api.SloBurnValue{WindowSeconds: api.SloBurnValueWindowSeconds(seconds), Start: at.Add(-time.Duration(seconds) * time.Second), End: at, Quality: quality, Performance: performance}
}

func exhaustionValue(current slo.WindowResult, burn api.SloBurnValue, oneHour *slo.WindowResult) api.SloExhaustionEstimate {
	assumptions := []api.SloExhaustionEstimateAssumptions{"freezes_current_allowed_budget", "continues_observed_one_hour_bad_event_rate", "rolling_window_expiry_can_change_result", "future_traffic_can_change_result"}
	result := api.SloExhaustionEstimate{State: "unavailable", CurrentQuality: operationalQuality(current), OneHourQuality: burn.Quality, Assumptions: assumptions}
	if current.ExactRemaining == nil || oneHour == nil || oneHour.ExactBad == nil || current.State != "complete" || current.Provisional || current.Freshness != "fresh" || current.EligibleStart.After(current.EffectiveStart) || oneHour.State != "complete" || oneHour.Provisional || oneHour.Freshness != "fresh" || oneHour.EligibleStart.After(oneHour.EffectiveStart) {
		reason := "incomplete_or_stale_inputs"
		result.Reason = &reason
		return result
	}
	remaining := new(big.Rat).Set(current.ExactRemaining)
	rate := new(big.Rat).Set(oneHour.ExactBad)
	remainingText := slo.FormatRational(remaining)
	result.RemainingEvents = &remainingText
	rateText := slo.FormatRational(rate)
	result.BadEventsPerHour = &rateText
	if remaining.Sign() <= 0 {
		result.State = "exhausted"
		zero := "0.000000000000"
		result.HoursUntilExhaustion = &zero
		return result
	}
	if rate.Sign() == 0 {
		reason := "zero_bad_event_rate"
		result.Reason = &reason
		return result
	}
	result.State = "estimated"
	hours := new(big.Rat).Quo(remaining, rate)
	hoursText := slo.FormatRational(hours)
	result.HoursUntilExhaustion = &hoursText
	return result
}

func exactBurn(result *slo.WindowResult) *big.Rat {
	if result == nil || result.ExactBurn == nil {
		return nil
	}
	return new(big.Rat).Set(result.ExactBurn)
}

func operationalReasons(values []slo.OperationalReason) ([]api.SloUrgencyReason, api.SloUrgencyReason) {
	reasons := make([]api.SloUrgencyReason, 0, len(values))
	for _, value := range values {
		reasons = append(reasons, api.SloUrgencyReason{Code: api.SloUrgencyReasonCode(value.Code), Provisional: value.Provisional})
	}
	if len(reasons) == 0 {
		reasons = append(reasons, api.SloUrgencyReason{Code: "on_track"})
	}
	return reasons, reasons[0]
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func urgencyRank(value string) int {
	switch value {
	case "needs_attention":
		return 0
	case "watch":
		return 1
	case "healthy":
		return 2
	default:
		return 3
	}
}

func operationalSample(result slo.WindowResult, bucketStart, bucketEnd time.Time, revision string) api.SloOperationalSample {
	state := api.SloOperationalSampleState("observed")
	switch result.State {
	case "unavailable":
		state = "missing"
	case "zero_traffic":
		state = "zero_traffic"
	case "complete":
		if result.Observed == 0 && result.KnownEmpty > 0 {
			state = "known_empty"
		} else if result.Observed > 0 && (result.KnownEmpty > 0 || result.Expired > 0 || result.Missing > 0) {
			state = "partial"
		}
	case "no_data":
		state = "missing"
	case "expired_history":
		if result.Observed > 0 {
			state = "partial"
		} else {
			state = "expired"
		}
	case "not_applicable":
		state = "not_applicable"
	case "partial":
		state = "partial"
	}
	if state == "observed" && result.Freshness == "stale" {
		state = "stale"
	}
	return api.SloOperationalSample{BucketStart: bucketStart, BucketEnd: bucketEnd, EvaluationAt: result.End, State: state,
		Quality: operationalQuality(result), Revision: revision, Good: result.Good, Total: result.Total,
		Attainment: result.Attainment, AllowedBudget: result.Allowed, RemainingBudget: result.Remaining,
		RemainingFraction: result.RemainingFraction, Burn: result.Burn, ReceivedAt: result.LatestReceipt,
		RetainedExtrema: []api.SloOperationalSampleRetainedExtrema{}}
}
func buildSloOperationalDetail(ctx context.Context, q *dbgen.Queries, row dbgen.GetSloRow, contextRow dbgen.GetSloOperationalContextRow, input OperationalDetailInput) (api.SloOperationalDetail, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "slo.operational.assemble")
	defer span.End()
	now := normalizedAgentNow(input.Now)
	cadence := time.Duration(row.CadenceSeconds) * time.Second
	bound, err := resolveOperationalBounds(input, cadence, row.RollingWindowSeconds)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	requestedAt, effectiveAt, rangeStart, rangeEnd := bound.requestedAt, bound.effectiveAt, bound.start, bound.end
	window := time.Duration(row.RollingWindowSeconds) * time.Second
	base := slo.WindowInput{Now: now, ValidFrom: row.EvaluationValidFrom.Time.UTC(), SliValidFrom: row.SliValidFrom.Time.UTC(),
		RetentionStart: now.Add(-sli.ObservationRetention), Cadence: cadence,
		FreshnessGrace: cadence + time.Duration(contextRow.SettlingDelaySeconds)*time.Second + sli.DispatchPhase(row.SliID.String(), int(row.CadenceSeconds)),
		Target:         sli.NormalizeDecimal(row.Target)}
	rolling, attainment, daily := input.retainedReader.rolling, input.retainedReader.attainment, input.retainedReader.daily
	var currentWindows map[time.Duration]slo.WindowResult
	if input.currentOnly || input.retainedReader != nil {
		currentWindows = make(map[time.Duration]slo.WindowResult)
		durations := []time.Duration{window}
		if time.Hour%cadence == 0 {
			durations = append(durations, time.Hour)
		}
		if 6*time.Hour%cadence == 0 {
			durations = append(durations, 6*time.Hour)
		}
		var inputs []slo.WindowInput
		for _, duration := range durations {
			if _, exists := currentWindows[duration]; exists {
				continue
			}
			value := base
			value.At, value.Duration = effectiveAt, duration
			inputs = append(inputs, value)
			currentWindows[duration] = slo.WindowResult{}
		}
		values, err := evaluateDerivedWindows(ctx, q, row.SliID, row.MeasurementRevision, inputs)
		if input.retainedReader != nil && (errors.Is(err, ErrDerivedPending) || errors.Is(err, ErrDerivedSourceFailed)) {
			input.retainedReader.pending = true
			input.retainedReader.failed = errors.Is(err, ErrDerivedSourceFailed)
			values = make([]slo.WindowResult, len(inputs))
			for index, value := range inputs {
				values[index] = slo.WindowResult{End: value.At, RequestedStart: value.At.Add(-value.Duration),
					EffectiveStart: value.At.Add(-value.Duration), EligibleStart: maxTime(value.At.Add(-value.Duration), value.ValidFrom, value.SliValidFrom), RetentionStart: value.RetentionStart,
					State: "unavailable", Freshness: "unknown", Performance: "unknown", Reason: "history_preparing"}
			}
			err = nil
		}
		if err != nil {
			return api.SloOperationalDetail{}, err
		}
		for index, value := range values {
			currentWindows[inputs[index].Duration] = value
		}
	}
	evaluate := func(duration time.Duration) (slo.WindowResult, error) {
		if currentWindows != nil {
			return currentWindows[duration], nil
		}
		return evaluateOperationalAt(ctx, base, effectiveAt, duration)
	}
	currentResult, err := evaluate(window)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	burn1h := unavailableBurn(3600, effectiveAt, "cadence_mismatch")
	var burn1hResult *slo.WindowResult
	if time.Hour%cadence == 0 {
		result, evalErr := evaluate(time.Hour)
		if evalErr != nil {
			return api.SloOperationalDetail{}, evalErr
		}
		burn1h = burnValue(result, 3600)
		burn1hResult = &result
	}
	burn6h := unavailableBurn(21600, effectiveAt, "cadence_mismatch")
	if 6*time.Hour%cadence == 0 {
		result, evalErr := evaluate(6 * time.Hour)
		if evalErr != nil {
			return api.SloOperationalDetail{}, evalErr
		}
		burn6h = burnValue(result, 21600)
	}
	current := operationalValue(currentResult)
	current.Window.RequestedStart = requestedAt.Add(-time.Duration(row.RollingWindowSeconds) * time.Second)
	applyOperationalDefinitionWindow(&current, row.ActivatedAt.Time.UTC(), row.EvaluationValidFrom.Time.UTC(), row.HistoricalRequestedBy, row.HistoricalRequestedAt)
	urgency := slo.ClassifyOperational(slo.OperationalState{QualityState: currentResult.State, Freshness: currentResult.Freshness, PerformanceState: currentResult.Performance,
		Provisional: currentResult.Provisional, ShortenedEligibility: currentResult.EligibleStart.After(currentResult.EffectiveStart), RemainingBudget: currentResult.Remaining,
		RemainingFraction: currentResult.RemainingFraction, Burn1h: burn1h.Performance.Burn, Burn1hAvailable: burn1h.Performance.Burn != nil,
		Burn1hQualityState: string(burn1h.Quality.State), Burn1hFreshness: string(burn1h.Quality.Freshness), Burn1hProvisional: burn1h.Performance.Provisional,
		Burn1hShortenedEligibility: burn1hResult != nil && burn1hResult.EligibleStart.After(burn1hResult.EffectiveStart),
		RemainingExact:             currentResult.ExactRemaining, RemainingFractionExact: currentResult.ExactRemainingFraction,
		Burn1hExact: exactBurn(burn1hResult)})
	reasons, leading := operationalReasons(urgency.Reasons)
	if input.currentOnly {
		return api.SloOperationalDetail{Objective: sloRecordDTO(sloRecord(row)), RequestedAt: requestedAt, EffectiveAt: effectiveAt, CalculationVersion: operationalCalculationVersion,
			Current: current, Urgency: api.SloOperationalDetailUrgency(urgency.Group), LeadingReason: leading, Reasons: reasons,
			Burn1h: burn1h, Burn6h: burn6h, Exhaustion: exhaustionValue(currentResult, burn1h, burn1hResult)}, nil
	}
	if reader := input.retainedReader; reader != nil && input.releaseSnapshot != nil {
		days, summary, err := daily(ctx, base, rangeStart, rangeEnd, effectiveAt,
			uuid.UUID(row.ID.Bytes), strconv.FormatInt(row.DefinitionRevision, 10), sli.NormalizeDecimal(row.Target), window, cadence)
		if err != nil {
			return api.SloOperationalDetail{}, err
		}
		reader.days, reader.daySummary = &days, summary
		if err := input.releaseSnapshot(); err != nil {
			return api.SloOperationalDetail{}, err
		}
	}
	budgetSamples, err := rolling(ctx, base, rangeStart, rangeEnd, time.Duration(row.RollingWindowSeconds)*time.Second, cadence, strconv.FormatInt(row.DefinitionRevision, 10), "remaining_fraction", sampleBucketLimit(rangeStart, rangeEnd, cadence))
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	selectedBurn := time.Duration(input.BurnWindow) * time.Second
	var burnSamples []api.SloOperationalSample
	if selectedBurn%cadence != 0 {
		burnSamples = unavailableOperationalSamples(rangeStart, rangeEnd, cadence, strconv.FormatInt(row.DefinitionRevision, 10), sampleBucketLimit(rangeStart, rangeEnd, cadence), "cadence_mismatch")
	} else {
		burnSamples, err = rolling(ctx, base, rangeStart, rangeEnd, selectedBurn, cadence, strconv.FormatInt(row.DefinitionRevision, 10), "burn", sampleBucketLimit(rangeStart, rangeEnd, cadence))
		if err != nil {
			return api.SloOperationalDetail{}, err
		}
	}
	attainmentSamples, resolution, reduced, err := attainment(ctx, base, rangeStart, rangeEnd, cadence, strconv.FormatInt(row.DefinitionRevision, 10))
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	complianceSeries, complianceSummary, err := daily(ctx, base, rangeStart, rangeEnd, effectiveAt,
		uuid.UUID(row.ID.Bytes), strconv.FormatInt(row.DefinitionRevision, 10), sli.NormalizeDecimal(row.Target), window, cadence)
	if err != nil {
		return api.SloOperationalDetail{}, err
	}
	evaluationID, err := uuid.NewRandom()
	if err != nil {
		return api.SloOperationalDetail{}, errors.New("storage: operational evaluation identifier unavailable")
	}
	result := api.SloOperationalDetail{RequestedAt: requestedAt, EffectiveAt: effectiveAt, EvaluationId: evaluationID,
		CalculationVersion: operationalCalculationVersion, SnapshotKind: "current_operational_read", Objective: sloRecordDTO(sloRecord(row)),
		Context: api.SloOperationalContext{SliId: uuid.UUID(contextRow.SliID.Bytes), SliName: contextRow.SliName, SliDisplayName: contextRow.SliDisplayName,
			DatasourceId: uuid.UUID(contextRow.DatasourceID.Bytes), DatasourceDisplayName: contextRow.DatasourceDisplayName,
			MeasurementRevision: strconv.FormatInt(contextRow.MeasurementRevision, 10), PollingIntervalSeconds: int(contextRow.PollingIntervalSeconds),
			SettlingDelaySeconds: int(contextRow.SettlingDelaySeconds), GoodQuery: contextRow.GoodQuery, TotalQuery: contextRow.TotalQuery},
		Current: current, Urgency: api.SloOperationalDetailUrgency(urgency.Group), LeadingReason: leading, Reasons: reasons,
		Burn1h: burn1h, Burn6h: burn6h, Exhaustion: exhaustionValue(currentResult, burn1h, burn1hResult),
		Range: api.SloOperationalRange{RequestedStart: rangeStart, RequestedEnd: rangeEnd, EffectiveStart: rangeStart, EffectiveEnd: rangeEnd,
			SourceCadenceSeconds: int(row.CadenceSeconds), ResolutionSeconds: int(resolution / time.Second), Reduced: reduced,
			MaxSamplesPerSeries: 1500, BucketBounds: "half_open"},
		AttainmentSeries: api.SloOperationalSeries{Kind: "attainment", Samples: attainmentSamples},
		BudgetSeries:     api.SloOperationalSeries{Kind: "rolling_budget", Samples: budgetSamples},
		BurnSeries:       api.SloOperationalSeries{Kind: "burn", BurnWindowSeconds: new(api.SloOperationalSeriesBurnWindowSeconds(input.BurnWindow)), Samples: burnSamples},
		ComplianceSeries: complianceSeries, ComplianceSummary: complianceSummary}
	if reader := input.retainedReader; reader != nil && reader.pending {
		result.HistoryPreparation = &struct {
			Failed       bool  `json:"failed"`
			ReadySeconds int64 `json:"ready_seconds"`
			TotalSeconds int64 `json:"total_seconds"`
		}{Failed: reader.failed, ReadySeconds: reader.readySeconds, TotalSeconds: max(1, reader.totalSeconds)}
	}
	return result, nil
}
