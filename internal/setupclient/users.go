package setupclient

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/identity"
)

func (s *SessionClient) userContext(ctx context.Context, admin bool) (api.AuthResponse, error) {
	auth, err := s.context(ctx)
	if err != nil {
		return auth, err
	}
	if auth.Session == nil {
		return auth, userFailure("unauthorized", false)
	}
	if auth.Session.Restricted || admin && auth.Session.Role != "administrator" {
		return auth, userFailure("forbidden", false)
	}
	return auth, nil
}

func (s *SessionClient) User(ctx context.Context, id string) (api.ManagedUser, string, error) {
	auth, err := s.userContext(ctx, id != "")
	if err != nil {
		return api.ManagedUser{}, "", err
	}
	var r *http.Response
	if id == "" {
		r, err = s.client.api.GetAccount(ctx, s.client.headers(auth.CsrfToken))
	} else {
		var value api.UserId
		if json.Unmarshal([]byte(strconv.Quote(id)), &value) != nil || value.String() != id {
			return api.ManagedUser{}, "", userFailure("invalid_request", false)
		}
		r, err = s.client.api.GetUser(ctx, value, s.client.headers(auth.CsrfToken))
	}
	var value api.ManagedUser
	if err := s.readUserResponse(r, err, http.StatusOK, &value, false); err != nil {
		return value, "", err
	}
	if !validManagedUser(value) || id != "" && value.Id.String() != id || r.Header.Get("ETag") != userETag(value) {
		return value, "", userFailure("invalid_response", false)
	}
	return value, userETag(value), nil
}

func (s *SessionClient) Users(ctx context.Context, params api.ListUsersParams) (api.UserPage, error) {
	auth, err := s.userContext(ctx, true)
	if err != nil {
		return api.UserPage{}, err
	}
	r, err := s.client.api.ListUsers(ctx, &params, s.client.headers(auth.CsrfToken))
	var page api.UserPage
	if err := s.readUserResponse(r, err, http.StatusOK, &page, false); err != nil {
		return page, err
	}
	if page.Items == nil || len(page.Items) > 200 || page.Total < int64(len(page.Items)) || len(page.NextCursor) > 2048 {
		return page, userFailure("invalid_response", false)
	}
	for _, user := range page.Items {
		if !validManagedUser(user) {
			return api.UserPage{}, userFailure("invalid_response", false)
		}
	}
	return page, nil
}

func (s *SessionClient) ChangeUser(ctx context.Context, operation, id, etag, key string, input api.UserWriteRequest) (api.UserCommandResult, error) {
	auth, err := s.userContext(ctx, operation != "profile")
	if err != nil {
		return api.UserCommandResult{}, err
	}
	var r *http.Response
	editor := s.client.headers(auth.CsrfToken)
	var target api.UserId
	if id != "" && (json.Unmarshal([]byte(strconv.Quote(id)), &target) != nil || target.String() != id) {
		return api.UserCommandResult{}, userFailure("invalid_request", false)
	}
	switch operation {
	case "create":
		r, err = s.client.api.CreateUser(ctx, &api.CreateUserParams{IdempotencyKey: key}, input, editor)
	case "profile":
		r, err = s.client.api.UpdateAccount(ctx, &api.UpdateAccountParams{IfMatch: etag, IdempotencyKey: key}, api.AccountUpdateRequest{FullName: input.FullName}, editor)
	case "update":
		r, err = s.client.api.UpdateUser(ctx, target, &api.UpdateUserParams{IfMatch: etag, IdempotencyKey: key}, input, editor)
	case "disable", "enable", "delete", "reset-password", "cancel-password-reset", "replace-setup-link":
		r, err = s.client.api.UserCommand(ctx, target, operation, &api.UserCommandParams{IfMatch: etag, IdempotencyKey: key}, api.UserCommandJSONRequestBody{}, editor)
	default:
		return api.UserCommandResult{}, userFailure("invalid_request", false)
	}
	status := http.StatusOK
	if operation == "create" {
		status = http.StatusCreated
	}
	var result api.UserCommandResult
	if err := s.readUserResponse(r, err, status, &result, true); err != nil {
		return result, err
	}
	if !validManagedUser(result.User) || id != "" && result.User.Id.String() != id || r.Header.Get("ETag") != userETag(result.User) || !slices.Contains([]string{"", "setup", "reset"}, string(result.Purpose)) {
		return api.UserCommandResult{}, userFailure("invalid_response", true)
	}
	issuesLink := slices.Contains([]string{"create", "reset-password", "replace-setup-link"}, operation)
	if !result.Replayed && issuesLink && result.Link == nil || !issuesLink && result.Link != nil {
		return api.UserCommandResult{}, userFailure("invalid_response", true)
	}
	if result.Link != nil {
		prefix := s.client.origin.String() + "/activate#alu_" + string(result.Purpose) + "_"
		secret, ok := strings.CutPrefix(*result.Link, prefix)
		if !ok || result.Replayed || result.ExpiresAt == nil || result.Purpose == "" {
			return api.UserCommandResult{}, userFailure("invalid_response", true)
		}
		if _, err := identity.SessionSecretVerifier(secret); err != nil {
			return api.UserCommandResult{}, userFailure("invalid_response", true)
		}
	}
	return result, nil
}

func (s *SessionClient) RedeemUser(ctx context.Context, token, password string) error {
	auth, err := s.context(ctx)
	if err != nil {
		return err
	}
	r, err := s.client.api.RedeemUserChallenge(ctx, api.UserRedemptionRequest{Token: &token, Password: &password}, s.client.headers(auth.CsrfToken))
	return s.readUserResponse(r, err, http.StatusNoContent, nil, true)
}

func (s *SessionClient) ChangePassword(ctx context.Context, password string) error {
	auth, err := s.userContext(ctx, false)
	if err != nil {
		return err
	}
	r, err := s.client.api.PostAuthPassword(ctx, api.PasswordRequest{NewPassword: password}, s.client.headers(auth.CsrfToken))
	var cookie *http.Cookie
	var ok bool
	if err == nil && r.StatusCode == http.StatusOK {
		cookie, ok = findCookie(r, "__Host-almanac-session")
		if ok {
			s.restore(cookie.Value)
		}
	}
	var result api.AuthResponse
	if err := s.readUserResponse(r, err, http.StatusOK, &result, true); err != nil {
		return err
	}
	if !ok || result.Session == nil || result.Session.UserId != auth.Session.UserId || result.Session.Restricted {
		return userFailure("invalid_response", true)
	}
	s.restore(cookie.Value)
	return nil
}

func (s *SessionClient) readUserResponse(r *http.Response, transport error, status int, target any, mutation bool) error {
	if transport != nil {
		return userFailure("user_unavailable", mutation)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, (512<<10)+1))
	if err != nil || len(body) > 512<<10 {
		return userFailure("invalid_response", mutation)
	}
	if r.StatusCode != status {
		var problem api.Problem
		if json.Unmarshal(body, &problem) != nil {
			return userFailure("invalid_response", mutation)
		}
		failure := userFailure(problem.Code, mutation && r.StatusCode >= 500)
		var requestID api.UserId
		if requestIDPattern.MatchString(problem.RequestId) || json.Unmarshal([]byte(strconv.Quote(problem.RequestId)), &requestID) == nil && requestID.String() == problem.RequestId {
			failure.RequestID = problem.RequestId
		}
		return failure
	}
	if target == nil {
		if len(body) != 0 {
			return userFailure("invalid_response", mutation)
		}
	} else if json.Unmarshal(body, target, json.RejectUnknownMembers(true)) != nil {
		return userFailure("invalid_response", mutation)
	}
	return nil
}

func userETag(value api.ManagedUser) string {
	return `"user-` + value.Id.String() + `-` + value.Version + `"`
}
func validManagedUser(value api.ManagedUser) bool {
	version, err := strconv.ParseInt(value.Version, 10, 64)
	return err == nil && version > 0 && strconv.FormatInt(version, 10) == value.Version && value.Id != (api.UserId{}) && utf8.ValidString(value.FullName) && value.FullName != "" && utf8.RuneCountInString(value.FullName) <= 160 && !strings.ContainsFunc(value.FullName, unicode.IsControl) && (value.Email == nil || len(*value.Email) <= 254) && slices.Contains([]string{"viewer", "editor", "administrator"}, string(value.Role)) && slices.Contains([]string{"active", "pending", "disabled", "resetting", "deleted"}, string(value.Status))
}
func userFailure(code string, unknown bool) *Failure {
	messages := map[string]string{"unauthorized": "sign in before managing users", "forbidden": "the current role cannot perform this operation", "invalid_request": "user command input is invalid", "not_found": "the user is unavailable", "conflict": "the user changed; retrieve its current version", "email_in_use": "this email address is already reserved", "last_administrator": "keep at least one active administrator with a local password", "link_unavailable": "this account link is no longer valid", "password_policy": "use 15–128 characters and avoid common passwords", "rate_limited": "too many attempts; wait before trying again", "user_unavailable": "account management is temporarily unavailable", "invalid_response": "the backend returned an invalid account response"}
	message, ok := messages[code]
	if !ok {
		code, message = "user_unavailable", messages["user_unavailable"]
	}
	if unknown {
		code, message = "outcome_unknown", "the command may have completed; inspect current state before retrying; one-time links cannot be recovered"
	}
	kind := KindRemote
	if code == "invalid_request" || code == "password_policy" {
		kind = KindInvalid
	}
	if code == "unauthorized" || code == "forbidden" {
		kind = KindAuthentication
	}
	if code == "not_found" {
		kind = KindNotFound
	}
	if slices.Contains([]string{"conflict", "email_in_use", "last_administrator"}, code) {
		kind = KindConflict
	}
	return &Failure{Kind: kind, Code: code, Message: message, OutcomeUnknown: unknown, SessionState: "unchecked"}
}
