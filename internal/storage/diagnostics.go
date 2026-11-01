package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	diagnosticReceiptTTL           = 24 * time.Hour
	maxActiveDatasourceDiagnostics = 4
)

var (
	ErrDiagnosticNotFound             = errors.New("storage: diagnostic not found")
	ErrDiagnosticConflict             = errors.New("storage: diagnostic conflict")
	ErrDiagnosticAgentUnavailable     = errors.New("storage: diagnostic agent unavailable")
	ErrDiagnosticConfigurationPending = errors.New("storage: diagnostic configuration pending")
	ErrDiagnosticConfigurationFailed  = errors.New("storage: diagnostic configuration failed")
	ErrDiagnosticUnsupported          = errors.New("storage: diagnostic operation unsupported")
)

type DiagnosticRecord struct {
	ID                string
	ActorID           string
	AgentID           string
	DatasourceID      string
	SessionGeneration int64
	Operation         string
	State             string
	Result            []byte
	ErrorCode         string
	Message           string
	CreatedAt         time.Time
	DeadlineAt        time.Time
	ExpiresAt         time.Time
	Replayed          bool
}

type BeginDiagnosticInput struct {
	Proof                     DatasourceProof
	DiagnosticID              string
	AgentID                   string
	AgentGeneration           int64
	DatasourceID              string
	ExpectedDatasourceVersion int64
	IdempotencyKey            string
	Operation                 string
	Query                     string
	ConnectionSettings        datasource.Settings
	Password                  string
	Token                     string
	Secrets                   *sourcesecret.Adapter
	RequestedAt               time.Time
	Now                       func() time.Time
}

func ReplayDatasourceConnectionTest(ctx context.Context, databaseURL string, proof DatasourceProof, datasourceID string, version int64, key string, now func() time.Time) (DiagnosticRecord, bool, error) {
	if _, valid := canonicalUUID(datasourceID); !valid || version <= 0 || !validPrintableASCII(key, 1, 128) || now == nil || proof.SessionVerifier == ([32]byte{}) {
		return DiagnosticRecord{}, false, ErrInvalidAgentInput
	}
	conn, queries, tx, user, installation, session, started, err := beginDatasourceAdminWrite(ctx, databaseURL, proof.SessionVerifier, now)
	if err != nil {
		return DiagnosticRecord{}, false, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	current := normalizedAgentNow(now)
	record, found, err := readDatasourceConnectionTestReceipt(ctx, queries, tx, user.ID, key, digestDatasourceConnectionTest(datasourceID, version), current)
	if err != nil {
		return DiagnosticRecord{}, false, err
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return DiagnosticRecord{}, false, err
	}
	record.Replayed = found
	return record, found, nil
}

func BeginDatasourceDiagnostic(ctx context.Context, databaseURL string, input BeginDiagnosticInput) (DiagnosticRecord, agentprotocol.DiagnosticRequest, error) {
	diagnosticID, diagnosticOK := canonicalUUID(input.DiagnosticID)
	agentID, agentOK := canonicalUUID(input.AgentID)
	savedConnectionTest := input.Operation == "connection_test" && input.DatasourceID != ""
	if !diagnosticOK || !agentOK || input.AgentGeneration <= 0 || input.Now == nil || !validAgentClock(input.RequestedAt) || input.Proof.SessionVerifier == ([32]byte{}) ||
		(input.Operation != "connection_test" && input.Operation != "query_preview") ||
		(input.Operation == "query_preview" && (input.DatasourceID == "" || len(input.Query) == 0 || len(input.Query) > 4096)) ||
		(input.Operation == "connection_test" && !savedConnectionTest && input.ConnectionSettings.Validate() != nil) ||
		(savedConnectionTest && (input.ExpectedDatasourceVersion <= 0 || !validPrintableASCII(input.IdempotencyKey, 1, 128) || input.Password != "" || input.Token != "")) {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrInvalidAgentInput
	}
	roles := []string{"administrator"}
	if input.Operation == "query_preview" {
		roles = append(roles, "editor")
	}
	conn, queries, tx, user, installation, session, now, err := beginAgentReadWithRoles(ctx, databaseURL, input.Proof.SessionVerifier, input.Now, roles...)
	if err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	var diagnosticReceiptDigest [32]byte
	if savedConnectionTest {
		diagnosticReceiptDigest = digestDatasourceConnectionTest(input.DatasourceID, input.ExpectedDatasourceVersion)
		replay, found, replayErr := readDatasourceConnectionTestReceipt(ctx, queries, tx, user.ID, input.IdempotencyKey, diagnosticReceiptDigest, now)
		if replayErr != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, replayErr
		}
		if found {
			replay.Replayed = true
			if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
				return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, err
			}
			return replay, agentprotocol.DiagnosticRequest{}, nil
		}
	}
	// Serialize admission for this agent before reading runtime readiness or
	// counting active diagnostics. Different actors have different user/session
	// locks, so only this shared row lock protects the per-agent capacity bound.
	agent, err := queries.LockAgentAccessAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrAgentNotFound
	}
	if err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic agent lookup failed", err)
	}
	if !agent.Enabled {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrAgentConflict
	}
	runtime, err := queries.GetAgentRuntimeStatus(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDiagnosticAgentUnavailable
	}
	if err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic runtime lookup failed", err)
	}
	if runtime.Generation != input.AgentGeneration {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDiagnosticAgentUnavailable
	}
	status, err := runtimeStatus(runtime, now)
	if err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic capabilities invalid", err)
	}
	if status.Reachability != "connected" {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDiagnosticAgentUnavailable
	}
	if status.ConfigurationFailureCode != nil && status.ConfigurationFailureRevision != nil && *status.ConfigurationFailureRevision == status.DesiredConfigurationRevision {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDiagnosticConfigurationFailed
	}
	if status.AppliedConfigurationRevision < status.DesiredConfigurationRevision {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDiagnosticConfigurationPending
	}
	if !datasource.SupportsOperation(status.Capabilities, input.Operation) {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDiagnosticUnsupported
	}
	createdAt := input.RequestedAt.UTC().Truncate(time.Microsecond)
	deadline := createdAt.Add(30 * time.Second)
	if !now.Before(deadline) {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDiagnosticAgentUnavailable
	}
	if _, err := tx.Exec(ctx, `UPDATE public.almanac_diagnostics SET state='timed_out', updated_at=$2
		WHERE agent_id=$1 AND state IN ('pending','running') AND deadline_at <= $2`, agentID, timestamp(now)); err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: expired diagnostic cleanup failed", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM public.almanac_diagnostics WHERE expires_at <= $1`, timestamp(now)); err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: expired diagnostic receipt cleanup failed", err)
	}
	var activeCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.almanac_diagnostics WHERE agent_id=$1 AND state IN ('pending','running')`, agentID).Scan(&activeCount); err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic capacity read failed", err)
	}
	if activeCount >= maxActiveDatasourceDiagnostics {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDiagnosticConflict
	}
	datasourceAssignment := agentprotocol.DatasourceAssignment{}
	var datasourceID pgtype.UUID
	safeRequest := struct {
		Operation    string `json:"operation"`
		DatasourceID string `json:"datasource_id,omitempty"`
		QuerySHA256  string `json:"query_sha256,omitempty"`
	}{Operation: input.Operation, DatasourceID: input.DatasourceID}
	if input.Query != "" {
		digest := sha256.Sum256([]byte(input.Query))
		safeRequest.QuerySHA256 = hex.EncodeToString(digest[:])
	}
	if input.Operation == "query_preview" {
		id, valid := canonicalUUID(input.DatasourceID)
		if !valid {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDatasourceNotFound
		}
		row, readErr := queries.GetDatasource(ctx, id)
		if errors.Is(readErr, pgx.ErrNoRows) {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDatasourceNotFound
		}
		if readErr != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic datasource read failed", readErr)
		}
		if row.AgentID != agentID {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrAgentConflict
		}
		installationID, loadErr := loadInstallationID(ctx, tx)
		if loadErr != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic installation lookup failed", loadErr)
		}
		datasourceAssignment, err = releaseDatasourceAssignment(installationID, row, input.Secrets)
		if err != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, err
		}
		datasourceID = id
	} else if savedConnectionTest {
		id, valid := canonicalUUID(input.DatasourceID)
		if !valid {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDatasourceNotFound
		}
		row, readErr := queries.GetDatasource(ctx, id)
		if errors.Is(readErr, pgx.ErrNoRows) {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDatasourceNotFound
		}
		if readErr != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic datasource read failed", readErr)
		}
		if row.AgentID != agentID || row.Version != input.ExpectedDatasourceVersion {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrDatasourceConflict
		}
		installationID, loadErr := loadInstallationID(ctx, tx)
		if loadErr != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic installation lookup failed", loadErr)
		}
		datasourceAssignment, err = releaseDatasourceAssignment(installationID, row, input.Secrets)
		if err != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, err
		}
		datasourceID = id
	} else {
		datasourceAssignment, err = assignmentForConnectionTest(input)
		if err != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, err
		}
	}
	request := agentprotocol.DiagnosticRequest{DiagnosticID: diagnosticID.String(), AgentID: agentID.String(), Operation: input.Operation, DeadlineAt: deadline, Datasource: datasourceAssignment, Query: input.Query}
	if err := request.Validate(); err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrInvalidAgentInput
	}
	requestJSON, err := json.Marshal(safeRequest)
	if err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, ErrInvalidAgentInput
	}
	_, err = tx.Exec(ctx, `INSERT INTO public.almanac_diagnostics
		(id, actor_id, agent_id, datasource_id, session_generation, operation, state, request, created_at, updated_at, deadline_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,'pending',$7::jsonb,$8,$8,$9,$10)`, diagnosticID, user.ID, agentID, datasourceID,
		input.AgentGeneration, input.Operation, requestJSON, timestamp(createdAt), timestamp(deadline), timestamp(deadline.Add(diagnosticReceiptTTL)))
	if err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic create failed", err)
	}
	if savedConnectionTest {
		receiptResult, marshalErr := json.Marshal(struct {
			DiagnosticID string `json:"diagnostic_id"`
		}{DiagnosticID: diagnosticID.String()})
		if marshalErr != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, errors.New("storage: diagnostic receipt encoding failed")
		}
		if err := queries.InsertDatasourceDiagnosticReceipt(ctx, dbgen.InsertDatasourceDiagnosticReceiptParams{ActorID: user.ID,
			IdempotencyKey: input.IdempotencyKey, Digest: diagnosticReceiptDigest[:], Result: receiptResult,
			CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(datasourceCreateReceiptTTL))}); err != nil {
			return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, operationError(ctx, "storage: diagnostic receipt insert failed", err)
		}
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return DiagnosticRecord{}, agentprotocol.DiagnosticRequest{}, err
	}
	return DiagnosticRecord{ID: diagnosticID.String(), ActorID: user.ID.String(), AgentID: agentID.String(), DatasourceID: input.DatasourceID,
		SessionGeneration: input.AgentGeneration, Operation: input.Operation, State: "pending", CreatedAt: createdAt, DeadlineAt: deadline, ExpiresAt: deadline.Add(diagnosticReceiptTTL)}, request, nil
}

func digestDatasourceConnectionTest(datasourceID string, version int64) [32]byte {
	encoded, _ := json.Marshal(struct {
		Version      int    `json:"version"`
		DatasourceID string `json:"datasource_id"`
		ETagVersion  int64  `json:"etag_version"`
	}{Version: 1, DatasourceID: datasourceID, ETagVersion: version})
	return sha256.Sum256(encoded)
}

func readDatasourceConnectionTestReceipt(ctx context.Context, queries *dbgen.Queries, tx pgx.Tx, actorID pgtype.UUID, key string, digest [32]byte, now time.Time) (DiagnosticRecord, bool, error) {
	receipt, err := queries.FindDatasourceDiagnosticReceipt(ctx, dbgen.FindDatasourceDiagnosticReceiptParams{ActorID: actorID, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return DiagnosticRecord{}, false, nil
	}
	if err != nil {
		return DiagnosticRecord{}, false, operationError(ctx, "storage: diagnostic receipt lookup failed", err)
	}
	if !now.Before(receipt.ExpiresAt.Time) {
		if _, err := queries.DeleteExpiredDatasourceDiagnosticReceipt(ctx, dbgen.DeleteExpiredDatasourceDiagnosticReceiptParams{ActorID: actorID, IdempotencyKey: key, Now: timestamp(now)}); err != nil {
			return DiagnosticRecord{}, false, operationError(ctx, "storage: diagnostic receipt cleanup failed", err)
		}
		return DiagnosticRecord{}, false, nil
	}
	if !bytes.Equal(receipt.Digest, digest[:]) {
		return DiagnosticRecord{}, false, ErrDiagnosticConflict
	}
	var saved struct {
		DiagnosticID string `json:"diagnostic_id"`
	}
	if json.Unmarshal(receipt.Result, &saved) != nil {
		return DiagnosticRecord{}, false, errors.New("storage: invalid diagnostic receipt")
	}
	id, valid := canonicalUUID(saved.DiagnosticID)
	if !valid {
		return DiagnosticRecord{}, false, errors.New("storage: invalid diagnostic receipt")
	}
	record, err := readDiagnostic(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DiagnosticRecord{}, false, errors.New("storage: missing diagnostic receipt target")
	}
	if err != nil {
		return DiagnosticRecord{}, false, operationError(ctx, "storage: diagnostic receipt target lookup failed", err)
	}
	return record, true, nil
}

func assignmentForConnectionTest(input BeginDiagnosticInput) (agentprotocol.DatasourceAssignment, error) {
	settings := input.ConnectionSettings
	if settings.Validate() != nil || settings.AgentID != input.AgentID || (settings.AuthenticationMode == "basic" && input.Password == "") || (settings.AuthenticationMode == "bearer" && input.Token == "") {
		return agentprotocol.DatasourceAssignment{}, ErrInvalidAgentInput
	}
	assignment := agentprotocol.DatasourceAssignment{DatasourceID: input.DiagnosticID, DatasourceVersion: "1", Name: "connection-test",
		Provider: settings.Provider, ConfigurationFormat: settings.ConfigurationFormat, Endpoint: settings.Endpoint,
		TLS:                agentprotocol.DatasourceTLS{InsecureSkipVerify: settings.TLS.InsecureSkipVerify, CAPEM: settings.TLS.CAPEM},
		AuthenticationMode: settings.AuthenticationMode, Username: settings.AuthenticationUser, Password: input.Password, BearerToken: input.Token,
		PollingIntervalSeconds: 60, SettlingDelaySeconds: datasource.PrometheusSettlingDelay}
	if err := assignment.Validate(); err != nil {
		return agentprotocol.DatasourceAssignment{}, ErrInvalidAgentInput
	}
	return assignment, nil
}

func GetDatasourceDiagnostic(ctx context.Context, databaseURL string, proof DatasourceProof, diagnosticID string, now func() time.Time) (DiagnosticRecord, error) {
	id, valid := canonicalUUID(diagnosticID)
	if !valid || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return DiagnosticRecord{}, ErrInvalidAgentInput
	}
	conn, _, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return DiagnosticRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	record, err := readDiagnostic(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && user.Role != "administrator" && record.ActorID != user.ID.String()) {
		return DiagnosticRecord{}, ErrDiagnosticNotFound
	}
	if err != nil {
		return DiagnosticRecord{}, operationError(ctx, "storage: diagnostic read failed", err)
	}
	currentTime := normalizedAgentNow(now)
	if !currentTime.Before(record.ExpiresAt) {
		return DiagnosticRecord{}, ErrDiagnosticNotFound
	}
	if (record.State == "pending" || record.State == "running") && !currentTime.Before(record.DeadlineAt) {
		record, err = transitionOpenDiagnostic(ctx, tx, record, "timed_out", currentTime)
		if err != nil {
			return DiagnosticRecord{}, operationError(ctx, "storage: diagnostic timeout update failed", err)
		}
		agentUUID, _ := canonicalUUID(record.AgentID)
		if err := pruneDatasourceDiagnosticReceipts(ctx, tx, agentUUID, currentTime); err != nil {
			return DiagnosticRecord{}, operationError(ctx, "storage: diagnostic receipt pruning failed", err)
		}
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return DiagnosticRecord{}, err
	}
	return record, nil
}

func CancelDatasourceDiagnostic(ctx context.Context, databaseURL string, proof DatasourceProof, diagnosticID string, now func() time.Time) (DiagnosticRecord, error) {
	id, valid := canonicalUUID(diagnosticID)
	if !valid || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return DiagnosticRecord{}, ErrInvalidAgentInput
	}
	conn, _, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor")
	if err != nil {
		return DiagnosticRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	record, err := readDiagnostic(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && user.Role != "administrator" && record.ActorID != user.ID.String()) {
		return DiagnosticRecord{}, ErrDiagnosticNotFound
	}
	if err != nil {
		return DiagnosticRecord{}, operationError(ctx, "storage: diagnostic cancellation lookup failed", err)
	}
	currentTime := normalizedAgentNow(now)
	if !currentTime.Before(record.ExpiresAt) {
		return DiagnosticRecord{}, ErrDiagnosticNotFound
	}
	if record.State == "pending" || record.State == "running" {
		newState := "canceled"
		if !currentTime.Before(record.DeadlineAt) {
			newState = "timed_out"
		}
		record, err = transitionOpenDiagnostic(ctx, tx, record, newState, currentTime)
		if err != nil {
			return DiagnosticRecord{}, operationError(ctx, "storage: diagnostic cancellation update failed", err)
		}
		agentUUID, _ := canonicalUUID(record.AgentID)
		if err := pruneDatasourceDiagnosticReceipts(ctx, tx, agentUUID, currentTime); err != nil {
			return DiagnosticRecord{}, operationError(ctx, "storage: diagnostic receipt pruning failed", err)
		}
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return DiagnosticRecord{}, err
	}
	return record, nil
}

func transitionOpenDiagnostic(ctx context.Context, tx pgx.Tx, record DiagnosticRecord, state string, now time.Time) (DiagnosticRecord, error) {
	err := tx.QueryRow(ctx, `UPDATE public.almanac_diagnostics SET state=$2, updated_at=$3
		WHERE id=$1 AND state IN ('pending','running') RETURNING state`, record.ID, state, timestamp(now)).Scan(&record.State)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DiagnosticRecord{}, err
	}
	id, valid := canonicalUUID(record.ID)
	if !valid {
		return DiagnosticRecord{}, ErrDiagnosticNotFound
	}
	return readDiagnostic(ctx, tx, id)
}

func CompleteDatasourceDiagnostic(ctx context.Context, databaseURL, credentialID string, credentialVerifier [32]byte, generation int64, result agentprotocol.DiagnosticResult, now time.Time, runtimes ...*Runtime) error {
	id, validID := canonicalUUID(result.DiagnosticID)
	credential, validCredential := canonicalUUID(credentialID)
	if !validID || !validCredential || generation <= 0 || result.ValidateHTTPS() != nil || !validAgentClock(now) {
		return ErrInvalidAgentInput
	}
	state, err := beginAgentOwnershipTransaction(ctx, databaseURL, credential, credentialVerifier, runtimes...)
	if err != nil {
		return err
	}
	defer closeAgentOwnershipTransaction(state)
	currentTime := now.UTC().Truncate(time.Microsecond)
	if !currentAgentCredentialValid(state.installation, state.agent.Enabled, state.credential, credentialVerifier, currentTime) {
		return ErrInvalidAgentCredential
	}
	encoded, err := result.CanonicalJSON()
	if err != nil || len(encoded) > agentprotocol.MaxHTTPSDiagnosticResultBytes {
		return ErrInvalidAgentInput
	}
	digest := sha256.Sum256(encoded)
	var stateName string
	var rowGeneration int64
	var deadline time.Time
	var priorDigest []byte
	err = state.tx.QueryRow(ctx, `SELECT state, session_generation, deadline_at, result_digest
		FROM public.almanac_diagnostics WHERE id=$1 AND agent_id=$2 FOR UPDATE`, id, state.agentID).
		Scan(&stateName, &rowGeneration, &deadline, &priorDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDiagnosticNotFound
	}
	if err != nil {
		return operationError(ctx, "storage: diagnostic result lookup failed", err)
	}
	owner, err := state.queries.LockLatestAgentOwnership(ctx, state.agentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (owner.Generation != generation || !currentTime.Before(owner.ExpiresAt.Time))) {
		return ErrDiagnosticConflict
	}
	if err != nil {
		return operationError(ctx, "storage: diagnostic result ownership lookup failed", err)
	}
	if rowGeneration != generation {
		return ErrDiagnosticConflict
	}
	if stateName != "pending" && stateName != "running" {
		if len(priorDigest) == sha256.Size && subtle.ConstantTimeCompare(priorDigest, digest[:]) == 1 {
			return state.tx.Commit(ctx)
		}
		return ErrDiagnosticConflict
	}
	if !currentTime.Before(deadline) {
		return ErrDiagnosticConflict
	}
	_, err = state.tx.Exec(ctx, `UPDATE public.almanac_diagnostics SET state=$2, result=$3::jsonb, result_digest=$4,
		error_code=NULLIF($5,''), message=NULLIF($6,''), updated_at=$7 WHERE id=$1 AND state IN ('pending','running')`,
		id, result.State, encoded, digest[:], result.ErrorCode, result.Message, timestamp(currentTime))
	if err != nil {
		return operationError(ctx, "storage: diagnostic result update failed", err)
	}
	if err := pruneDatasourceDiagnosticReceipts(ctx, state.tx, state.agentID, currentTime); err != nil {
		return operationError(ctx, "storage: diagnostic receipt pruning failed", err)
	}
	return state.tx.Commit(ctx)
}

func MarkDatasourceDiagnosticRunning(ctx context.Context, databaseURL, diagnosticID, agentID string, generation int64, now time.Time) error {
	return updateDatasourceDiagnosticState(ctx, databaseURL, diagnosticID, agentID, generation, "running", now, "", "")
}

func FailDatasourceDiagnosticDispatch(ctx context.Context, databaseURL, diagnosticID, agentID string, generation int64, now time.Time) error {
	return updateDatasourceDiagnosticState(ctx, databaseURL, diagnosticID, agentID, generation, "unknown", now, "network", "The assigned agent session became unavailable")
}

func updateDatasourceDiagnosticState(ctx context.Context, databaseURL, diagnosticID, agentID string, generation int64, state string, now time.Time, errorCode, message string) error {
	diagnostic, validDiagnostic := canonicalUUID(diagnosticID)
	agent, validAgent := canonicalUUID(agentID)
	if !validDiagnostic || !validAgent || generation <= 0 || (state != "running" && state != "unknown") || !validAgentClock(now) {
		return ErrInvalidAgentInput
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return operationError(ctx, "storage: diagnostic state transaction failed", err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return operationError(ctx, "storage: diagnostic state setup failed", err)
	}
	_, err = tx.Exec(ctx, `UPDATE public.almanac_diagnostics SET state=$4, error_code=NULLIF($5,''), message=NULLIF($6,''), updated_at=$7
		WHERE id=$1 AND agent_id=$2 AND session_generation=$3 AND state='pending'`, diagnostic, agent, generation, state, errorCode, message, timestamp(now))
	if err != nil {
		return operationError(ctx, "storage: diagnostic state update failed", err)
	}
	if state == "unknown" {
		if err := pruneDatasourceDiagnosticReceipts(ctx, tx, agent, now.UTC().Truncate(time.Microsecond)); err != nil {
			return operationError(ctx, "storage: diagnostic receipt pruning failed", err)
		}
	}
	return tx.Commit(ctx)
}

func pruneDatasourceDiagnosticReceipts(ctx context.Context, tx pgx.Tx, agentID pgtype.UUID, now time.Time) error {
	_, err := tx.Exec(ctx, `DELETE FROM public.almanac_diagnostics WHERE agent_id=$1 AND expires_at <= $2`, agentID, timestamp(now))
	return err
}

func readDiagnostic(ctx context.Context, tx pgx.Tx, id pgtype.UUID) (DiagnosticRecord, error) {
	var record DiagnosticRecord
	var datasourceID pgtype.UUID
	var result []byte
	var errorCode, message pgtype.Text
	err := tx.QueryRow(ctx, `SELECT id, actor_id, agent_id, datasource_id, session_generation, operation, state, result,
		error_code, message, created_at, deadline_at, expires_at FROM public.almanac_diagnostics WHERE id=$1`, id).
		Scan(&record.ID, &record.ActorID, &record.AgentID, &datasourceID, &record.SessionGeneration, &record.Operation, &record.State, &result,
			&errorCode, &message, &record.CreatedAt, &record.DeadlineAt, &record.ExpiresAt)
	if err != nil {
		return DiagnosticRecord{}, err
	}
	if datasourceID.Valid {
		record.DatasourceID = datasourceID.String()
	}
	record.Result = bytes.Clone(result)
	if errorCode.Valid {
		record.ErrorCode = errorCode.String
	}
	if message.Valid {
		record.Message = message.String
	}
	return record, nil
}
