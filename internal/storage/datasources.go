package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/datasource"
	"github.com/dsolsona/almanac/internal/sourcesecret"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrDatasourceNotFound              = errors.New("storage: datasource not found")
	ErrDatasourceConflict              = errors.New("storage: datasource conflict")
	ErrDatasourceConfigurationTooLarge = errors.New("storage: datasource configuration exceeds protocol limits")
	ErrSourceSecretUnavailable         = errors.New("storage: source-secret key is unavailable")
)

const datasourceCreateReceiptTTL = 24 * time.Hour

type DatasourceProof struct {
	SessionVerifier [32]byte
}

type CreateDatasourceInput struct {
	Proof          DatasourceProof
	Settings       datasource.Settings
	Password       datasource.SecretAction
	Token          datasource.SecretAction
	IdempotencyKey string
	RequestID      string
	Now            func() time.Time
}

type UpdateDatasourceInput struct {
	Proof           DatasourceProof
	DatasourceID    string
	ExpectedVersion int64
	Settings        datasource.Settings
	Password        datasource.SecretAction
	Token           datasource.SecretAction
	RequestID       string
	Now             func() time.Time
}

type DatasourcePage struct {
	Items      []DatasourceRecord `json:"items"`
	NextCursor string             `json:"next_cursor"`
}

type DatasourceRecord struct {
	ID                            string              `json:"id"`
	Settings                      datasource.Settings `json:"-"`
	Version                       int64               `json:"-"`
	PasswordConfigured            bool                `json:"-"`
	TokenConfigured               bool                `json:"-"`
	ConfigurationRevision         int64               `json:"-"`
	PreviousConfigurationRevision int64               `json:"-"`
	PreviousAgentID               string              `json:"-"`
	CreatedAt                     time.Time           `json:"created_at"`
	UpdatedAt                     time.Time           `json:"updated_at"`
}

type DatasourceAgentCapability struct {
	Enabled                      bool
	Reachability                 string
	Reported                     bool
	Capabilities                 []agentprotocol.Capability
	DesiredConfigurationRevision *int64
	AppliedConfigurationRevision *int64
	ConfigurationFailureCode     *string
	ConfigurationFailureRevision *int64
}

func GetDatasourceAgentCapability(ctx context.Context, databaseURL string, proof DatasourceProof, agentID string, now func() time.Time) (DatasourceAgentCapability, error) {
	id, valid := canonicalUUID(agentID)
	if !valid || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return DatasourceAgentCapability{}, ErrInvalidAgentInput
	}
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return DatasourceAgentCapability{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	agent, err := queries.GetAgent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceAgentCapability{}, ErrAgentNotFound
	}
	if err != nil {
		return DatasourceAgentCapability{}, operationError(ctx, "storage: datasource agent lookup failed", err)
	}
	capability := DatasourceAgentCapability{Enabled: agent.Enabled, Reachability: "unreachable", Capabilities: []agentprotocol.Capability{}}
	incompatibleAt, incompatibleErr := queries.GetAgentIncompatibleAttempt(ctx, id)
	hasIncompatibleAttempt := incompatibleErr == nil
	if incompatibleErr != nil && !errors.Is(incompatibleErr, pgx.ErrNoRows) {
		return DatasourceAgentCapability{}, operationError(ctx, "storage: datasource incompatible state read failed", incompatibleErr)
	}
	runtime, err := queries.GetAgentRuntimeStatus(ctx, id)
	if err == nil {
		status, statusErr := runtimeStatus(runtime, normalizedAgentNow(now))
		if statusErr != nil {
			return DatasourceAgentCapability{}, operationError(ctx, "storage: datasource agent capability state invalid", statusErr)
		}
		capability.Reachability, capability.Capabilities = status.Reachability, status.Capabilities
		capability.Reported = true
		capability.DesiredConfigurationRevision = new(status.DesiredConfigurationRevision)
		capability.AppliedConfigurationRevision = new(status.AppliedConfigurationRevision)
		capability.ConfigurationFailureCode = status.ConfigurationFailureCode
		capability.ConfigurationFailureRevision = status.ConfigurationFailureRevision
		if hasIncompatibleAttempt {
			activityAt := runtime.UpdatedAt.Time
			if status.LastHeartbeatAt != nil {
				activityAt = *status.LastHeartbeatAt
			}
			if !incompatibleAt.Time.Before(activityAt) {
				capability.Reachability = "incompatible"
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return DatasourceAgentCapability{}, operationError(ctx, "storage: datasource agent capability lookup failed", err)
	} else {
		configuration, configurationErr := queries.GetAgentConfiguration(ctx, id)
		if configurationErr != nil {
			return DatasourceAgentCapability{}, operationError(ctx, "storage: datasource agent configuration read failed", configurationErr)
		}
		capability.DesiredConfigurationRevision = new(configuration.Revision)
		capability.AppliedConfigurationRevision = new(int64(0))
		if hasIncompatibleAttempt {
			capability.Reachability = "incompatible"
		}
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return DatasourceAgentCapability{}, err
	}
	return capability, nil
}

func CreateDatasource(ctx context.Context, databaseURL string, secrets *sourcesecret.Adapter, input CreateDatasourceInput) (DatasourceRecord, error) {
	if input.Now == nil || input.Settings.Validate() != nil || input.RequestID == "" || !validPrintableASCII(input.IdempotencyKey, 1, 128) {
		return DatasourceRecord{}, datasource.ErrInvalidSettings
	}
	digest, err := digestDatasourceCreate(input)
	if err != nil {
		return DatasourceRecord{}, ErrInvalidAgentInput
	}
	actions, err := datasource.ResolveSecretActions("", input.Settings.AuthenticationMode, false, false, input.Password, input.Token)
	if err != nil {
		return DatasourceRecord{}, ErrInvalidAgentInput
	}
	if (actions.Password.Action == "replace" || actions.Token.Action == "replace") && secrets == nil {
		return DatasourceRecord{}, ErrSourceSecretUnavailable
	}
	if input.Proof.SessionVerifier == ([32]byte{}) {
		return DatasourceRecord{}, ErrInvalidSession
	}
	conn, queries, tx, user, installation, session, now, err := beginDatasourceAdminWrite(ctx, databaseURL, input.Proof.SessionVerifier, input.Now)
	if err != nil {
		return DatasourceRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	receipt, err := queries.FindDatasourceReceipt(ctx, dbgen.FindDatasourceReceiptParams{ActorID: user.ID, IdempotencyKey: input.IdempotencyKey})
	if err == nil {
		if !now.Before(receipt.ExpiresAt.Time) {
			if _, err := queries.DeleteExpiredDatasourceReceipt(ctx, dbgen.DeleteExpiredDatasourceReceiptParams{ActorID: user.ID, IdempotencyKey: input.IdempotencyKey, Now: timestamp(now)}); err != nil {
				return DatasourceRecord{}, operationError(ctx, "storage: datasource receipt cleanup failed", err)
			}
		} else {
			if !bytes.Equal(receipt.Digest, digest[:]) {
				return DatasourceRecord{}, ErrDatasourceConflict
			}
			var saved datasourceCreateReceipt
			if json.Unmarshal(receipt.Result, &saved) != nil || saved.Version != 1 || saved.ResourceVersion <= 0 || saved.ConfigurationRevision <= 0 || saved.Settings.Validate() != nil {
				return DatasourceRecord{}, errors.New("storage: invalid datasource receipt")
			}
			savedID, valid := canonicalUUID(saved.DatasourceID)
			if !valid {
				return DatasourceRecord{}, errors.New("storage: invalid datasource receipt")
			}
			if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
				return DatasourceRecord{}, err
			}
			return DatasourceRecord{ID: savedID.String(), Settings: saved.Settings, Version: saved.ResourceVersion,
				PasswordConfigured: saved.PasswordConfigured, TokenConfigured: saved.TokenConfigured,
				ConfigurationRevision: saved.ConfigurationRevision, CreatedAt: saved.CreatedAt, UpdatedAt: saved.UpdatedAt}, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource receipt lookup failed", err)
	}
	installationID, err := loadInstallationID(ctx, tx)
	if err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource installation lookup failed", err)
	}
	agentID, _ := canonicalUUID(input.Settings.AgentID)
	if _, err := queries.LockAgentAccessAgent(ctx, agentID); errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, ErrAgentNotFound
	} else if err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource agent lock failed", err)
	}
	agent, err := queries.GetDatasourceAgentCapabilities(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, ErrAgentNotFound
	}
	if err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource agent lookup failed", err)
	}
	if !agent.Enabled || !datasource.SupportsOperation(decodeCapabilities(agent.Capabilities), "connection_test") || !datasource.SupportsOperation(decodeCapabilities(agent.Capabilities), "query_preview") {
		return DatasourceRecord{}, ErrAgentConflict
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return DatasourceRecord{}, errors.New("storage: datasource identifier unavailable")
	}
	passwordRevision, tokenRevision := int64(0), int64(0)
	var passwordEnvelope, tokenEnvelope []byte
	if actions.Password.Action == "replace" {
		binding := sourcesecret.Binding{InstallationID: installationID, ResourceKind: "datasource", ResourceID: id.String(), Field: "auth.password", Revision: 1}
		plaintext := []byte(actions.Password.Value)
		envelope, err := secrets.Encrypt(binding, plaintext)
		clear(plaintext)
		if err != nil {
			return DatasourceRecord{}, ErrSourceSecretUnavailable
		}
		passwordEnvelope, err = json.Marshal(envelope)
		if err != nil {
			return DatasourceRecord{}, ErrSourceSecretUnavailable
		}
		passwordRevision = 1
	}
	if actions.Token.Action == "replace" {
		binding := sourcesecret.Binding{InstallationID: installationID, ResourceKind: "datasource", ResourceID: id.String(), Field: "auth.token", Revision: 1}
		plaintext := []byte(actions.Token.Value)
		envelope, err := secrets.Encrypt(binding, plaintext)
		clear(plaintext)
		if err != nil {
			return DatasourceRecord{}, ErrSourceSecretUnavailable
		}
		tokenEnvelope, err = json.Marshal(envelope)
		if err != nil {
			return DatasourceRecord{}, ErrSourceSecretUnavailable
		}
		tokenRevision = 1
	}
	tls, err := json.Marshal(input.Settings.TLS)
	if err != nil {
		return DatasourceRecord{}, ErrInvalidAgentInput
	}
	var username pgtype.Text
	if input.Settings.AuthenticationUser != "" {
		username = pgtype.Text{String: input.Settings.AuthenticationUser, Valid: true}
	}
	created, err := queries.InsertDatasource(ctx, dbgen.InsertDatasourceParams{
		ID: uuidToPG(id), Name: input.Settings.Name, DisplayName: input.Settings.DisplayName,
		Provider: input.Settings.Provider, ConfigurationFormat: input.Settings.ConfigurationFormat,
		Endpoint: input.Settings.Endpoint, Tls: tls, AuthenticationMode: input.Settings.AuthenticationMode,
		AuthenticationUsername: username, PasswordEnvelope: passwordEnvelope, PasswordRevision: passwordRevision,
		TokenEnvelope: tokenEnvelope, TokenRevision: tokenRevision,
		PollingIntervalSeconds: int32(input.Settings.PollingIntervalSeconds), SettlingDelaySeconds: int32(input.Settings.SettlingDelaySeconds),
		AgentID: agentID, CreatedAt: timestamp(now), UpdatedAt: timestamp(now),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return DatasourceRecord{}, ErrDatasourceConflict
		}
		return DatasourceRecord{}, operationError(ctx, "storage: datasource create failed", err)
	}
	configurationRevision, err := advanceAgentConfiguration(ctx, queries, agentID, now)
	if err != nil {
		return DatasourceRecord{}, err
	}
	if _, err := buildDatasourceConfigurationSnapshot(ctx, queries, installationID, agentID, configurationRevision, secrets); err != nil {
		return DatasourceRecord{}, err
	}
	if err := queries.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: created.ID, Action: "datasource.created", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource audit failed", err)
	}
	record := datasourceRecord(created)
	record.ConfigurationRevision = configurationRevision
	receiptResult, err := json.Marshal(datasourceCreateReceipt{Version: 1, DatasourceID: created.ID.String(), Settings: record.Settings,
		ResourceVersion: record.Version, PasswordConfigured: record.PasswordConfigured, TokenConfigured: record.TokenConfigured,
		ConfigurationRevision: record.ConfigurationRevision, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt})
	if err != nil {
		return DatasourceRecord{}, errors.New("storage: datasource receipt encoding failed")
	}
	if err := queries.InsertDatasourceReceipt(ctx, dbgen.InsertDatasourceReceiptParams{ActorID: user.ID, IdempotencyKey: input.IdempotencyKey, Digest: digest[:], Result: receiptResult, CreatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(datasourceCreateReceiptTTL))}); err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource receipt insert failed", err)
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return DatasourceRecord{}, err
	}
	return record, nil
}

func GetDatasource(ctx context.Context, databaseURL, datasourceID string, proof DatasourceProof, now func() time.Time) (DatasourceRecord, error) {
	id, valid := canonicalUUID(datasourceID)
	if !valid || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return DatasourceRecord{}, ErrInvalidAgentInput
	}
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return DatasourceRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	row, err := queries.GetDatasource(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, ErrDatasourceNotFound
	}
	if err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource read failed", err)
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return DatasourceRecord{}, err
	}
	return datasourceRecord(row), nil
}

type datasourceCreateReceipt struct {
	Version               int                 `json:"version"`
	DatasourceID          string              `json:"datasource_id"`
	Settings              datasource.Settings `json:"settings"`
	ResourceVersion       int64               `json:"resource_version"`
	PasswordConfigured    bool                `json:"password_configured"`
	TokenConfigured       bool                `json:"token_configured"`
	ConfigurationRevision int64               `json:"configuration_revision"`
	CreatedAt             time.Time           `json:"created_at"`
	UpdatedAt             time.Time           `json:"updated_at"`
}

func digestDatasourceCreate(input CreateDatasourceInput) ([32]byte, error) {
	canonical, err := json.Marshal(struct {
		Version  int                     `json:"version"`
		Settings datasource.Settings     `json:"settings"`
		Password datasource.SecretAction `json:"password"`
		Token    datasource.SecretAction `json:"token"`
	}{Version: 1, Settings: input.Settings, Password: input.Password, Token: input.Token})
	if err != nil {
		return [32]byte{}, err
	}
	defer clear(canonical)
	return sha256.Sum256(canonical), nil
}

func ListDatasources(ctx context.Context, databaseURL string, proof DatasourceProof, limit int, cursor string, now func() time.Time) (DatasourcePage, error) {
	bounded, position, err := validateAgentPage("datasources", "", limit, cursor)
	if err != nil || proof.SessionVerifier == ([32]byte{}) || now == nil {
		return DatasourcePage{}, ErrInvalidAgentInput
	}
	conn, queries, tx, user, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, now, "administrator", "editor", "viewer")
	if err != nil {
		return DatasourcePage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	rows, err := queries.ListDatasources(ctx, dbgen.ListDatasourcesParams{AfterCreatedAt: timestamp(position.Time), AfterID: uuidOrZero(position.ID), RowLimit: int32(bounded + 1)})
	if err != nil {
		return DatasourcePage{}, operationError(ctx, "storage: datasource list failed", err)
	}
	page := DatasourcePage{Items: []DatasourceRecord{}}
	for i, row := range rows {
		if i == bounded {
			break
		}
		page.Items = append(page.Items, datasourceRecord(row))
	}
	if len(rows) > bounded {
		last := page.Items[len(page.Items)-1]
		page.NextCursor, err = encodeAgentCursor(agentCursor{Version: 1, Kind: "datasources", Time: last.CreatedAt, ID: last.ID})
		if err != nil {
			return DatasourcePage{}, err
		}
	}
	if err := finishAgentRead(ctx, tx, now, started, user, installation, session); err != nil {
		return DatasourcePage{}, err
	}
	return page, nil
}

func UpdateDatasource(ctx context.Context, databaseURL string, secrets *sourcesecret.Adapter, input UpdateDatasourceInput) (DatasourceRecord, error) {
	id, valid := canonicalUUID(input.DatasourceID)
	if !valid || input.ExpectedVersion <= 0 || input.Now == nil || input.Settings.Validate() != nil || input.RequestID == "" || input.Proof.SessionVerifier == ([32]byte{}) {
		return DatasourceRecord{}, ErrInvalidAgentInput
	}
	conn, queries, tx, user, installation, session, now, err := beginDatasourceAdminWrite(ctx, databaseURL, input.Proof.SessionVerifier, input.Now)
	if err != nil {
		return DatasourceRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	observed, err := queries.GetDatasource(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, ErrDatasourceNotFound
	}
	if err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource update read failed", err)
	}
	if observed.Version != input.ExpectedVersion {
		return DatasourceRecord{}, ErrDatasourceConflict
	}
	newAgentID, _ := canonicalUUID(input.Settings.AgentID)
	if err := lockDatasourceAgents(ctx, queries, observed.AgentID, newAgentID); err != nil {
		return DatasourceRecord{}, err
	}
	current, err := queries.LockDatasource(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, ErrDatasourceNotFound
	}
	if err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource update lock failed", err)
	}
	if current.Version != input.ExpectedVersion || current.AgentID != observed.AgentID {
		return DatasourceRecord{}, ErrDatasourceConflict
	}
	if newAgentID != current.AgentID || int32(input.Settings.PollingIntervalSeconds) != current.PollingIntervalSeconds {
		hasSlis, err := queries.HasSLIsForDatasource(ctx, current.ID)
		if err != nil {
			return DatasourceRecord{}, operationError(ctx, "storage: datasource SLI guard failed", err)
		}
		if hasSlis {
			return DatasourceRecord{}, ErrDatasourceMeasurementConflict
		}
	}
	installationID, err := loadInstallationID(ctx, tx)
	if err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource installation lookup failed", err)
	}
	passwordPresent, tokenPresent := len(current.PasswordEnvelope) > 0, len(current.TokenEnvelope) > 0
	actions, err := datasource.ResolveSecretActions(current.AuthenticationMode, input.Settings.AuthenticationMode, passwordPresent, tokenPresent, input.Password, input.Token)
	if err != nil {
		return DatasourceRecord{}, ErrInvalidAgentInput
	}
	if (actions.Password.Action == "replace" || actions.Token.Action == "replace") && secrets == nil {
		return DatasourceRecord{}, ErrSourceSecretUnavailable
	}
	passwordEnvelope, passwordRevision, err := updateEncryptedField(secrets, installationID, current, "auth.password", current.PasswordRevision, current.PasswordEnvelope, actions.Password)
	if err != nil {
		return DatasourceRecord{}, err
	}
	tokenEnvelope, tokenRevision, err := updateEncryptedField(secrets, installationID, current, "auth.token", current.TokenRevision, current.TokenEnvelope, actions.Token)
	if err != nil {
		return DatasourceRecord{}, err
	}
	agent, err := queries.GetDatasourceAgentCapabilities(ctx, newAgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, ErrAgentNotFound
	}
	if err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource agent lookup failed", err)
	}
	capabilities := decodeCapabilities(agent.Capabilities)
	if !agent.Enabled || !datasource.SupportsOperation(capabilities, "connection_test") || !datasource.SupportsOperation(capabilities, "query_preview") {
		return DatasourceRecord{}, ErrAgentConflict
	}
	tls, err := json.Marshal(input.Settings.TLS)
	if err != nil {
		return DatasourceRecord{}, ErrInvalidAgentInput
	}
	var username pgtype.Text
	if input.Settings.AuthenticationUser != "" {
		username = pgtype.Text{String: input.Settings.AuthenticationUser, Valid: true}
	}
	updated, err := queries.UpdateDatasource(ctx, dbgen.UpdateDatasourceParams{
		Name: input.Settings.Name, DisplayName: input.Settings.DisplayName, Provider: input.Settings.Provider,
		ConfigurationFormat: input.Settings.ConfigurationFormat, Endpoint: input.Settings.Endpoint, Tls: tls,
		AuthenticationMode: input.Settings.AuthenticationMode, AuthenticationUsername: username,
		PasswordEnvelope: passwordEnvelope, PasswordRevision: passwordRevision, TokenEnvelope: tokenEnvelope, TokenRevision: tokenRevision,
		PollingIntervalSeconds: int32(input.Settings.PollingIntervalSeconds), SettlingDelaySeconds: int32(input.Settings.SettlingDelaySeconds),
		AgentID: newAgentID, UpdatedAt: timestamp(now), ID: id, ExpectedVersion: input.ExpectedVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DatasourceRecord{}, ErrDatasourceConflict
	}
	if err != nil {
		if isUniqueViolation(err) {
			return DatasourceRecord{}, ErrDatasourceConflict
		}
		return DatasourceRecord{}, operationError(ctx, "storage: datasource update failed", err)
	}
	oldAgentID := current.AgentID
	oldConfigurationRevision, err := advanceAgentConfiguration(ctx, queries, oldAgentID, now)
	if err != nil {
		return DatasourceRecord{}, err
	}
	newConfigurationRevision := oldConfigurationRevision
	if oldAgentID != newAgentID {
		newConfigurationRevision, err = advanceAgentConfiguration(ctx, queries, newAgentID, now)
		if err != nil {
			return DatasourceRecord{}, err
		}
	}
	if oldAgentID == newAgentID {
		if _, err := buildDatasourceConfigurationSnapshot(ctx, queries, installationID, newAgentID, newConfigurationRevision, secrets); err != nil {
			return DatasourceRecord{}, err
		}
	} else {
		if _, err := buildDatasourceConfigurationSnapshot(ctx, queries, installationID, oldAgentID, oldConfigurationRevision, secrets); err != nil {
			return DatasourceRecord{}, err
		}
		if _, err := buildDatasourceConfigurationSnapshot(ctx, queries, installationID, newAgentID, newConfigurationRevision, secrets); err != nil {
			return DatasourceRecord{}, err
		}
	}
	if err := queries.InsertAgentAudit(ctx, dbgen.InsertAgentAuditParams{Actor: user.ID.String(), SubjectID: updated.ID, Action: "datasource.updated", OccurredAt: timestamp(now), RequestID: input.RequestID}); err != nil {
		return DatasourceRecord{}, operationError(ctx, "storage: datasource audit failed", err)
	}
	if err := finishAgentRead(ctx, tx, input.Now, now, user, installation, session); err != nil {
		return DatasourceRecord{}, err
	}
	record := datasourceRecord(updated)
	record.ConfigurationRevision = newConfigurationRevision
	if oldAgentID != newAgentID {
		record.PreviousConfigurationRevision = oldConfigurationRevision
		record.PreviousAgentID = uuidFromPG(oldAgentID)
	}
	return record, nil
}

func updateEncryptedField(adapter *sourcesecret.Adapter, installationID string, current dbgen.AlmanacDatasource, field string, revision int64, envelope []byte, action datasource.ResolvedSecretAction) ([]byte, int64, error) {
	switch action.Action {
	case "retain":
		return envelope, revision, nil
	case "clear":
		return nil, revision + int64(action.RevisionDelta), nil
	case "replace":
		if adapter == nil || revision >= int64(^uint64(0)>>1) {
			return nil, 0, ErrSourceSecretUnavailable
		}
		plaintext := []byte(action.Value)
		defer clear(plaintext)
		binding := sourcesecret.Binding{InstallationID: installationID, ResourceKind: "datasource", ResourceID: current.ID.String(), Field: field, Revision: uint64(revision + 1)}
		encrypted, err := adapter.Encrypt(binding, plaintext)
		if err != nil {
			return nil, 0, ErrSourceSecretUnavailable
		}
		encoded, err := json.Marshal(encrypted)
		if err != nil {
			return nil, 0, ErrSourceSecretUnavailable
		}
		return encoded, revision + 1, nil
	default:
		return nil, 0, ErrInvalidAgentInput
	}
}

func beginDatasourceAdminWrite(ctx context.Context, databaseURL string, verifier [32]byte, now func() time.Time) (*pgx.Conn, *dbgen.Queries, pgx.Tx, dbgen.LockSessionUserRow, dbgen.LockSessionInstallationRow, dbgen.AlmanacSession, time.Time, error) {
	conn, q, tx, user, installation, session, started, err := beginAgentRead(ctx, databaseURL, verifier, now)
	if err != nil {
		return nil, nil, nil, dbgen.LockSessionUserRow{}, dbgen.LockSessionInstallationRow{}, dbgen.AlmanacSession{}, time.Time{}, err
	}
	if user.Role != "administrator" {
		rollbackTransaction(tx)
		closeInitialOwnerConnection(conn)
		return nil, nil, nil, dbgen.LockSessionUserRow{}, dbgen.LockSessionInstallationRow{}, dbgen.AlmanacSession{}, time.Time{}, ErrAgentForbidden
	}
	return conn, q, tx, user, installation, session, started, nil
}

func lockDatasourceAgents(ctx context.Context, queries *dbgen.Queries, ids ...pgtype.UUID) error {
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		if id.Valid {
			ordered = append(ordered, uuidFromPG(id))
		}
	}
	slices.Sort(ordered)
	for i, id := range ordered {
		if i > 0 && id == ordered[i-1] {
			continue
		}
		agentID, err := uuid.Parse(id)
		if err != nil {
			return ErrAgentNotFound
		}
		_, err = queries.LockAgentAccessAgent(ctx, uuidToPG(agentID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAgentNotFound
		}
		if err != nil {
			return operationError(ctx, "storage: datasource agent lock failed", err)
		}
	}
	return nil
}

func loadInstallationID(ctx context.Context, tx pgx.Tx) (string, error) {
	var id pgtype.UUID
	// Installation identity is immutable after bootstrap. Callers already hold
	// the installation authority lock; upgrading it here can deadlock when
	// concurrent datasource admissions each hold it shared and contend for one
	// agent row.
	err := tx.QueryRow(ctx, "SELECT installation_id FROM public.almanac_installation WHERE singleton = true").Scan(&id)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func advanceAgentConfiguration(ctx context.Context, queries *dbgen.Queries, agentID pgtype.UUID, now time.Time) (int64, error) {
	latest, err := queries.GetLatestAgentConfigurationRevision(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = ensureAgentConfiguration(ctx, queries, agentID, now)
		if err != nil {
			return 0, operationError(ctx, "storage: datasource agent configuration initialization failed", err)
		}
		latest, err = queries.GetLatestAgentConfigurationRevision(ctx, agentID)
	}
	if err != nil {
		return 0, operationError(ctx, "storage: datasource agent configuration revision read failed", err)
	}
	if latest >= int64(^uint64(0)>>1) {
		return 0, ErrAgentConflict
	}
	revision := latest + 1
	snapshot, err := json.Marshal(agentprotocol.ConfigurationSnapshot{AgentID: uuidFromPG(agentID), ConfigurationRevision: strconv.FormatInt(revision, 10), Assignments: []agentprotocol.DatasourceAssignment{}, SLIs: []agentprotocol.SLICollection{}})
	if err != nil {
		return 0, errors.New("storage: datasource agent configuration encoding failed")
	}
	if err := queries.InsertAgentConfigurationRevision(ctx, dbgen.InsertAgentConfigurationRevisionParams{AgentID: agentID, Revision: revision, Snapshot: snapshot, CreatedAt: timestamp(now)}); err != nil {
		return 0, operationError(ctx, "storage: datasource agent configuration revision insert failed", err)
	}
	if _, err := queries.UpdateAgentDesiredConfigurationRevision(ctx, dbgen.UpdateAgentDesiredConfigurationRevisionParams{AgentID: agentID, Revision: revision, UpdatedAt: timestamp(now)}); err != nil {
		return 0, operationError(ctx, "storage: datasource desired revision update failed", err)
	}
	return revision, nil
}

func datasourceRecord(row dbgen.AlmanacDatasource) DatasourceRecord {
	settings := datasource.Settings{Name: row.Name, DisplayName: row.DisplayName, Provider: row.Provider,
		ConfigurationFormat: row.ConfigurationFormat, Endpoint: row.Endpoint,
		AuthenticationMode: row.AuthenticationMode, PollingIntervalSeconds: int(row.PollingIntervalSeconds),
		SettlingDelaySeconds: int(row.SettlingDelaySeconds), AgentID: uuidFromPG(row.AgentID)}
	_ = json.Unmarshal(row.Tls, &settings.TLS)
	if row.AuthenticationUsername.Valid {
		settings.AuthenticationUser = row.AuthenticationUsername.String
	}
	return DatasourceRecord{ID: uuidFromPG(row.ID), Settings: settings, Version: row.Version,
		PasswordConfigured: len(row.PasswordEnvelope) > 0, TokenConfigured: len(row.TokenEnvelope) > 0,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()}
}

func uuidToPG(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: value, Valid: true}
}

func uuidFromPG(value pgtype.UUID) string { return value.String() }

func decodeCapabilities(data []byte) []agentprotocol.Capability {
	var capabilities []agentprotocol.Capability
	if len(data) == 0 || json.Unmarshal(data, &capabilities) != nil || capabilities == nil {
		return nil
	}
	return capabilities
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}
