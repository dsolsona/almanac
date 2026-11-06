package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"math"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrUserInvalid   = errors.New("storage: invalid user input")
	ErrUserNotFound  = errors.New("storage: user not found")
	ErrUserConflict  = errors.New("storage: stale user or command conflict")
	ErrUserEmail     = errors.New("storage: email is already reserved")
	ErrLastAdmin     = errors.New("storage: last usable local administrator")
	ErrUserChallenge = errors.New("storage: account link unavailable")
)

type UserRecord struct {
	ID                 string     `json:"id"`
	FullName           string     `json:"full_name"`
	Email              *string    `json:"email"`
	Role               string     `json:"role"`
	Status             string     `json:"status"`
	Version            string     `json:"version"`
	LastSignIn         *time.Time `json:"last_sign_in"`
	SignInHistoryKnown bool       `json:"sign_in_history_known"`
	IsLastAdmin        bool       `json:"is_last_admin"`
}

type UserPage struct {
	Items      []UserRecord `json:"items"`
	Total      int64        `json:"total"`
	NextCursor string       `json:"next_cursor"`
}

type UserListInput struct {
	Proof                DatasourceProof
	Limit                int
	Cursor               string
	Search, Status, Role string
	Now                  func() time.Time
}

type UserCommandInput struct {
	Proof                           DatasourceProof
	Operation, UserID, Version, Key string
	Name, Email, Role, RequestID    string
	Now                             func() time.Time
}

type UserCommandResult struct {
	User      UserRecord `json:"user"`
	Purpose   string     `json:"purpose"`
	ExpiresAt *time.Time `json:"expires_at"`
	Replayed  bool       `json:"replayed"`
	Secret    string     `json:"-"`
}

func ListUsers(ctx context.Context, databaseURL string, in UserListInput) (UserPage, error) {
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Now == nil || in.Limit < 1 || in.Limit > 200 || !utf8.ValidString(in.Search) || utf8.RuneCountInString(in.Search) > 256 || strings.ContainsFunc(in.Search, unicode.IsControl) ||
		!slices.Contains([]string{"", "active", "pending", "disabled", "resetting"}, in.Status) || !slices.Contains([]string{"", "viewer", "editor", "administrator"}, in.Role) {
		return UserPage{}, ErrUserInvalid
	}
	cursor, err := decodeUserCursor(in)
	if err != nil {
		return UserPage{}, err
	}
	conn, q, tx, user, installation, session, started, err := beginAgentRead(ctx, databaseURL, in.Proof.SessionVerifier, in.Now)
	if err != nil {
		return UserPage{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	total, err := q.CountManagedUsers(ctx, dbgen.CountManagedUsersParams{Search: in.Search, RoleFilter: in.Role, StatusFilter: in.Status})
	if err != nil {
		return UserPage{}, userOperationError(ctx, err)
	}
	rows, err := q.ListManagedUsers(ctx, dbgen.ListManagedUsersParams{Search: in.Search, RoleFilter: in.Role, StatusFilter: in.Status, RowLimit: int32(in.Limit + 1), AfterName: cursor.Name, AfterID: uuidOrZero(cursor.ID)})
	if err != nil {
		return UserPage{}, userOperationError(ctx, err)
	}
	admins, err := q.CountUsableAdmins(ctx)
	if err != nil {
		return UserPage{}, userOperationError(ctx, err)
	}
	result := UserPage{Items: make([]UserRecord, 0, len(rows)), Total: total}
	if len(rows) > in.Limit {
		rows = rows[:in.Limit]
		last := rows[len(rows)-1]
		cursor.ID = last.ID.String()
		cursor.Name = last.FullName
		if cursor.Name == "" {
			cursor.Name = last.Login
		}
		value, err := json.Marshal(cursor)
		if err != nil {
			return UserPage{}, userOperationError(ctx, err)
		}
		result.NextCursor = base64.RawURLEncoding.EncodeToString(value)
	}
	for _, row := range rows {
		result.Items = append(result.Items, userRecord(row, admins))
	}
	if err := finishAgentRead(ctx, tx, in.Now, started, user, installation, session); err != nil {
		return UserPage{}, err
	}
	return result, nil
}

type userCursor struct {
	Version int    `json:"v"`
	Filter  string `json:"filter"`
	Name    string `json:"name"`
	ID      string `json:"id"`
}

func decodeUserCursor(in UserListInput) (userCursor, error) {
	filters, _ := json.Marshal([]string{in.Search, in.Status, in.Role})
	digest := sha256.Sum256(filters)
	cursor := userCursor{Version: 1, Filter: hex.EncodeToString(digest[:])}
	if in.Cursor == "" {
		return cursor, nil
	}
	if len(in.Cursor) > 2048 {
		return cursor, ErrUserInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(in.Cursor)
	var value userCursor
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != in.Cursor || json.Unmarshal(data, &value, json.RejectUnknownMembers(true)) != nil || value.Version != 1 || value.Filter != cursor.Filter || value.Name == "" || len(value.Name) > 640 || !utf8.ValidString(value.Name) {
		return cursor, ErrUserInvalid
	}
	if _, ok := canonicalUUID(value.ID); !ok {
		return cursor, ErrUserInvalid
	}
	return value, nil
}

func GetUser(ctx context.Context, databaseURL string, proof DatasourceProof, id string, now func() time.Time) (UserRecord, error) {
	return getUser(ctx, databaseURL, proof, id, now, false)
}

func GetAccount(ctx context.Context, databaseURL string, proof DatasourceProof, now func() time.Time) (UserRecord, error) {
	return getUser(ctx, databaseURL, proof, "", now, true)
}

func getUser(ctx context.Context, databaseURL string, proof DatasourceProof, id string, clock func() time.Time, own bool) (UserRecord, error) {
	if clock == nil {
		return UserRecord{}, ErrUserInvalid
	}
	roles := []string{"administrator"}
	if own {
		roles = append(roles, "editor", "viewer")
	}
	conn, q, tx, actor, installation, session, started, err := beginAgentReadWithRoles(ctx, databaseURL, proof.SessionVerifier, clock, roles...)
	if err != nil {
		return UserRecord{}, err
	}
	defer closeInitialOwnerConnection(conn)
	defer rollbackTransaction(tx)
	target := actor.ID
	if !own {
		var ok bool
		target, ok = canonicalUUID(id)
		if !ok {
			return UserRecord{}, ErrUserNotFound
		}
	}
	row, err := q.GetManagedUser(ctx, target)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, ErrUserNotFound
	}
	if err != nil {
		return UserRecord{}, userOperationError(ctx, err)
	}
	admins, err := q.CountUsableAdmins(ctx)
	if err != nil {
		return UserRecord{}, userOperationError(ctx, err)
	}
	if err := finishAgentRead(ctx, tx, clock, started, actor, installation, session); err != nil {
		return UserRecord{}, err
	}
	return userRecord(row, admins), nil
}

func ChangeUser(ctx context.Context, databaseURL string, in UserCommandInput) (UserCommandResult, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if err := validateUserCommand(in); err != nil {
		return UserCommandResult{}, err
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return UserCommandResult{}, err
	}
	defer closeInitialOwnerConnection(conn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	q := dbgen.New(tx)
	// The installation lock precedes every identity lock, including password rotation.
	install, err := q.LockPasswordRotationInstallation(ctx)
	if err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	actorID, err := q.FindSessionUserID(ctx, in.Proof.SessionVerifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return UserCommandResult{}, ErrInvalidSession
	}
	if err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	actor, err := q.LockSessionUser(ctx, actorID)
	if err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	session, err := q.LockSession(ctx, in.Proof.SessionVerifier[:])
	if err != nil {
		return UserCommandResult{}, ErrInvalidSession
	}
	started := normalizedAgentNow(in.Now)
	installation := dbgen.LockSessionInstallationRow(install)
	if started.IsZero() || invalidSessionState(installation, actor, session, started) {
		return UserCommandResult{}, ErrInvalidSession
	}
	if session.Restricted || actor.MustChangePassword || !install.OnboardingCompletedAt.Valid || (actor.Role != "administrator" && in.Operation != "profile") {
		return UserCommandResult{}, ErrAgentForbidden
	}
	raw, err := json.Marshal(struct{ Operation, Target, Version, Name, Email, Role string }{in.Operation, in.UserID, in.Version, in.Name, in.Email, in.Role})
	if err != nil {
		return UserCommandResult{}, ErrUserInvalid
	}
	digest := sha256.Sum256(raw)
	receipt, err := q.FindUserReceipt(ctx, dbgen.FindUserReceiptParams{ActorID: actorID, CommandKey: in.Key, Now: timestamp(started)})
	if err == nil {
		if !bytes.Equal(receipt.Digest, digest[:]) {
			return UserCommandResult{}, ErrUserConflict
		}
		var saved UserCommandResult
		if json.Unmarshal(receipt.Result, &saved) != nil {
			return UserCommandResult{}, ErrUserConflict
		}
		saved.Replayed = true
		ended := normalizedAgentNow(in.Now)
		if ended.Before(started) || invalidSessionState(installation, actor, session, ended) {
			return UserCommandResult{}, ErrInvalidSession
		}
		if err := tx.Commit(ctx); err != nil {
			return UserCommandResult{}, userOperationError(ctx, err)
		}
		return saved, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	var target dbgen.AlmanacUser
	if in.Operation == "create" {
		used, err := q.UserEmailInUse(ctx, dbgen.UserEmailInUseParams{ExceptID: pgtype.UUID{Valid: true}, Email: in.Email})
		if err != nil {
			return UserCommandResult{}, userOperationError(ctx, err)
		}
		if used {
			return UserCommandResult{}, ErrUserEmail
		}
		target, err = q.InsertManagedUser(ctx, dbgen.InsertManagedUserParams{Email: in.Email, FullName: in.Name, Role: in.Role, Now: timestamp(started)})
		if err != nil {
			return UserCommandResult{}, userOperationError(ctx, err)
		}
	} else {
		targetID := actorID
		if in.Operation != "profile" {
			targetID, _ = canonicalUUID(in.UserID)
		}
		target, err = q.LockManagedUser(ctx, targetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return UserCommandResult{}, ErrUserNotFound
		}
		if err != nil {
			return UserCommandResult{}, userOperationError(ctx, err)
		}
		if strconv.FormatInt(target.Version, 10) != in.Version {
			return UserCommandResult{}, ErrUserConflict
		}
		if target.Version == math.MaxInt64 || target.CredentialRevision == math.MaxInt64 {
			return UserCommandResult{}, ErrUserConflict
		}
		admins, err := q.CountUsableAdmins(ctx)
		if err != nil {
			return UserCommandResult{}, userOperationError(ctx, err)
		}
		if usableAdmin(target) && admins <= 1 && (slices.Contains([]string{"disable", "delete", "reset-password"}, in.Operation) || in.Operation == "update" && in.Role != "administrator") {
			return UserCommandResult{}, ErrLastAdmin
		}
		updated := target
		invalidate := int64(0)
		switch in.Operation {
		case "profile":
			updated.FullName = in.Name
		case "update":
			used, err := q.UserEmailInUse(ctx, dbgen.UserEmailInUseParams{ExceptID: target.ID, Email: in.Email})
			if err != nil {
				return UserCommandResult{}, userOperationError(ctx, err)
			}
			if used {
				return UserCommandResult{}, ErrUserEmail
			}
			updated.FullName = in.Name
			updated.Email = pgtype.Text{String: in.Email, Valid: true}
			updated.Role = in.Role
			if !target.LoginIsAlias {
				updated.Login = in.Email
			}
			if target.Email.String != in.Email || target.Role != in.Role {
				invalidate = 1
			}
		case "disable":
			updated.Enabled = false
			invalidate = 1
		case "enable":
			updated.Enabled = true
			invalidate = 1
		case "delete":
			updated.Enabled = false
			updated.DeletedAt = timestamp(started)
			invalidate = 1
		case "reset-password":
			if !target.Enabled || !target.PasswordVerifier.Valid {
				return UserCommandResult{}, ErrUserConflict
			}
			updated.PasswordLoginSuspended = true
			invalidate = 1
		case "cancel-password-reset":
			if !target.PasswordLoginSuspended {
				return UserCommandResult{}, ErrUserConflict
			}
			updated.PasswordLoginSuspended = false
			invalidate = 1
		case "replace-setup-link":
			if !target.Enabled || target.PasswordVerifier.Valid {
				return UserCommandResult{}, ErrUserConflict
			}
			invalidate = 1
		}
		if invalidate != 0 {
			if err := q.DeleteUserSessions(ctx, target.ID); err != nil {
				return UserCommandResult{}, userOperationError(ctx, err)
			}
			if err := q.DeleteUserChallenges(ctx, target.ID); err != nil {
				return UserCommandResult{}, userOperationError(ctx, err)
			}
		}
		target, err = q.UpdateManagedUser(ctx, dbgen.UpdateManagedUserParams{ID: target.ID, FullName: updated.FullName, Email: updated.Email, Login: updated.Login, Role: updated.Role, Enabled: updated.Enabled, Suspended: updated.PasswordLoginSuspended, DeletedAt: updated.DeletedAt, Invalidate: invalidate, Now: timestamp(started)})
		if err != nil {
			return UserCommandResult{}, userOperationError(ctx, err)
		}
	}
	result := UserCommandResult{}
	if slices.Contains([]string{"create", "replace-setup-link", "reset-password"}, in.Operation) {
		purpose := "setup"
		lifetime := 24 * time.Hour
		if in.Operation == "reset-password" {
			purpose = "reset"
			lifetime = 15 * time.Minute
		}
		secret, verifier, err := newUserChallenge(purpose)
		if err != nil {
			return UserCommandResult{}, err
		}
		expires := started.Add(lifetime)
		if err := q.InsertUserChallenge(ctx, dbgen.InsertUserChallengeParams{Verifier: verifier[:], UserID: target.ID, Purpose: purpose, CredentialRevision: target.CredentialRevision, CredentialEpoch: install.CredentialEpoch, ExpiresAt: timestamp(expires)}); err != nil {
			return UserCommandResult{}, userOperationError(ctx, err)
		}
		result.Secret = secret
		result.ExpiresAt = &expires
		result.Purpose = purpose
	}
	admins, err := q.CountUsableAdmins(ctx)
	if err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	result.User = userRecord(target, admins)
	if err := q.InsertSessionAudit(ctx, dbgen.InsertSessionAuditParams{Actor: actor.ID.String(), SubjectID: target.ID, Action: "identity.user." + in.Operation, OccurredAt: timestamp(started), RequestID: in.RequestID}); err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	metadata, err := json.Marshal(result)
	if err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	if err := q.DeleteExpiredUserReceipts(ctx, timestamp(started)); err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	if err := q.InsertUserReceipt(ctx, dbgen.InsertUserReceiptParams{ActorID: actorID, CommandKey: in.Key, Digest: digest[:], Result: metadata, ExpiresAt: timestamp(started.Add(24 * time.Hour))}); err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	ended := normalizedAgentNow(in.Now)
	if ended.Before(started) || invalidSessionState(installation, actor, session, ended) {
		return UserCommandResult{}, ErrInvalidSession
	}
	if err := tx.Commit(ctx); err != nil {
		return UserCommandResult{}, userOperationError(ctx, err)
	}
	return result, nil
}

func validateUserCommand(in UserCommandInput) error {
	if in.Now == nil || !validRequestID(in.RequestID) || len(in.Key) < 1 || len(in.Key) > 128 || strings.ContainsFunc(in.Key, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		return ErrUserInvalid
	}
	if !slices.Contains([]string{"create", "profile", "update", "disable", "enable", "delete", "reset-password", "cancel-password-reset", "replace-setup-link"}, in.Operation) {
		return ErrUserInvalid
	}
	if in.Operation != "create" {
		version, err := strconv.ParseInt(in.Version, 10, 64)
		if err != nil || version <= 0 || strconv.FormatInt(version, 10) != in.Version {
			return ErrUserInvalid
		}
		if in.Operation != "profile" {
			if _, ok := canonicalUUID(in.UserID); !ok {
				return ErrUserInvalid
			}
		}
	}
	if slices.Contains([]string{"create", "profile", "update"}, in.Operation) {
		if in.Name == "" || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 160 || strings.ContainsFunc(in.Name, unicode.IsControl) {
			return ErrUserInvalid
		}
	}
	if in.Operation == "create" || in.Operation == "update" {
		address, err := mail.ParseAddress(in.Email)
		if err != nil || address.Name != "" || address.Address != in.Email || len(in.Email) > 254 || !strings.Contains(in.Email, "@") || !slices.Contains([]string{"viewer", "editor", "administrator"}, in.Role) {
			return ErrUserInvalid
		}
	}
	return nil
}

func userRecord(row dbgen.AlmanacUser, admins int64) UserRecord {
	name := row.FullName
	if name == "" {
		name = string([]rune(row.Login)[:min(160, utf8.RuneCountInString(row.Login))])
	}
	r := UserRecord{ID: row.ID.String(), FullName: name, Role: row.Role, Version: strconv.FormatInt(row.Version, 10), SignInHistoryKnown: row.SignInHistoryKnown, IsLastAdmin: usableAdmin(row) && admins == 1, Status: "active"}
	if row.Email.Valid {
		r.Email = new(row.Email.String)
	}
	if row.LastSignIn.Valid {
		r.LastSignIn = new(row.LastSignIn.Time.UTC())
	}
	switch {
	case row.DeletedAt.Valid:
		r.Status = "deleted"
	case !row.Enabled:
		r.Status = "disabled"
	case row.PasswordLoginSuspended:
		r.Status = "resetting"
	case !row.PasswordVerifier.Valid:
		r.Status = "pending"
	}
	return r
}

func usableAdmin(row dbgen.AlmanacUser) bool {
	return row.Enabled && row.Role == "administrator" && row.PasswordVerifier.Valid && !row.PasswordLoginSuspended && !row.DeletedAt.Valid
}
func userOperationError(ctx context.Context, err error) error {
	return operationError(ctx, "storage: user operation failed", err)
}

func newUserChallenge(purpose string) (string, [32]byte, error) {
	secret, _, err := identity.NewSessionSecret()
	if err != nil {
		return "", [32]byte{}, err
	}
	token := "alu_" + purpose + "_" + secret
	return token, sha256.Sum256([]byte(token)), nil
}

func userChallengeVerifier(token string) ([32]byte, string, error) {
	rest, ok := strings.CutPrefix(token, "alu_")
	if !ok {
		return [32]byte{}, "", ErrUserChallenge
	}
	purpose, secret, ok := strings.Cut(rest, "_")
	if !ok || !slices.Contains([]string{"setup", "reset"}, purpose) {
		return [32]byte{}, "", ErrUserChallenge
	}
	if _, err := identity.SessionSecretVerifier(secret); err != nil {
		return [32]byte{}, "", ErrUserChallenge
	}
	return sha256.Sum256([]byte(token)), purpose, nil
}

type UserChallengeContext struct {
	FullName string  `json:"full_name"`
	Email    *string `json:"email"`
	Purpose  string  `json:"purpose"`
}

func InspectUserChallenge(ctx context.Context, databaseURL, token string, clock func() time.Time) (UserChallengeContext, error) {
	if clock == nil {
		return UserChallengeContext{}, ErrUserInvalid
	}
	verifier, purpose, err := userChallengeVerifier(token)
	if err != nil {
		return UserChallengeContext{}, err
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return UserChallengeContext{}, err
	}
	defer closeInitialOwnerConnection(conn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return UserChallengeContext{}, userOperationError(ctx, err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return UserChallengeContext{}, userOperationError(ctx, err)
	}
	q := dbgen.New(tx)
	installation, err := q.LockSessionInstallation(ctx)
	if err != nil {
		return UserChallengeContext{}, userOperationError(ctx, err)
	}
	proof, err := q.FindUserChallenge(ctx, verifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return UserChallengeContext{}, ErrUserChallenge
	}
	if err != nil {
		return UserChallengeContext{}, userOperationError(ctx, err)
	}
	user, err := q.GetManagedUser(ctx, proof.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserChallengeContext{}, ErrUserChallenge
	}
	if err != nil {
		return UserChallengeContext{}, userOperationError(ctx, err)
	}
	if !installation.OnboardingCompletedAt.Valid || !user.Enabled || proof.Purpose != purpose || proof.CredentialEpoch != installation.CredentialEpoch || proof.CredentialRevision != user.CredentialRevision || !clock().Before(proof.ExpiresAt.Time) || purpose == "setup" && user.PasswordVerifier.Valid || purpose == "reset" && !user.PasswordLoginSuspended {
		return UserChallengeContext{}, ErrUserChallenge
	}
	r := userRecord(user, 0)
	if err := tx.Commit(ctx); err != nil {
		return UserChallengeContext{}, userOperationError(ctx, err)
	}
	return UserChallengeContext{FullName: r.FullName, Email: r.Email, Purpose: purpose}, nil
}

// RedeemUserChallenge commits a previously hashed password only with current proof.
func RedeemUserChallenge(ctx context.Context, databaseURL, token, passwordVerifier, requestID string, clock func() time.Time) error {
	verifier, purpose, err := userChallengeVerifier(token)
	if err != nil {
		return err
	}
	if clock == nil || !validRequestID(requestID) || identity.ValidatePasswordVerifier(passwordVerifier) != nil {
		return ErrUserInvalid
	}
	conn, err := connectCurrentSchema(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer closeInitialOwnerConnection(conn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return userOperationError(ctx, err)
	}
	defer rollbackTransaction(tx)
	if err := setTransactionBounds(ctx, tx); err != nil {
		return userOperationError(ctx, err)
	}
	q := dbgen.New(tx)
	install, err := q.LockPasswordRotationInstallation(ctx)
	if err != nil {
		return userOperationError(ctx, err)
	}
	proof, err := q.FindUserChallenge(ctx, verifier[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserChallenge
	}
	if err != nil {
		return userOperationError(ctx, err)
	}
	user, err := q.LockManagedUser(ctx, proof.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserChallenge
	}
	if err != nil {
		return userOperationError(ctx, err)
	}
	now := normalizedAgentNow(clock)
	if now.IsZero() || !now.Before(proof.ExpiresAt.Time) || !user.Enabled || proof.Purpose != purpose || proof.CredentialEpoch != install.CredentialEpoch || proof.CredentialRevision != user.CredentialRevision || !install.OnboardingCompletedAt.Valid || user.CredentialRevision == math.MaxInt64 || user.Version == math.MaxInt64 {
		return ErrUserChallenge
	}
	if (purpose == "setup" && user.PasswordVerifier.Valid) || (purpose == "reset" && !user.PasswordLoginSuspended) {
		return ErrUserChallenge
	}
	if _, err := q.RedeemUserPassword(ctx, dbgen.RedeemUserPasswordParams{ID: user.ID, PasswordVerifier: pgtype.Text{String: passwordVerifier, Valid: true}, Now: timestamp(now)}); err != nil {
		return userOperationError(ctx, err)
	}
	if err := q.DeleteUserSessions(ctx, user.ID); err != nil {
		return userOperationError(ctx, err)
	}
	if err := q.DeleteUserChallenges(ctx, user.ID); err != nil {
		return userOperationError(ctx, err)
	}
	if err := q.InsertSessionAudit(ctx, dbgen.InsertSessionAuditParams{Actor: user.ID.String(), SubjectID: user.ID, Action: "identity.user." + purpose + ".redeemed", OccurredAt: timestamp(now), RequestID: requestID}); err != nil {
		return userOperationError(ctx, err)
	}
	ended := normalizedAgentNow(clock)
	if ended.Before(now) || !ended.Before(proof.ExpiresAt.Time) {
		return ErrUserChallenge
	}
	if err := tx.Commit(ctx); err != nil {
		return userOperationError(ctx, err)
	}
	return nil
}
