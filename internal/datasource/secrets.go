// Package datasource owns validated, redacted datasource configuration contracts.
package datasource

import "errors"

var ErrInvalidSecretAction = errors.New("datasource: invalid secret action")

type SecretAction struct {
	Action string `json:"action"`
	Value  string `json:"value,omitempty"`
}

type ResolvedSecretAction struct {
	Action        string
	Value         string
	RevisionDelta uint64
}

type SecretActions struct {
	Password ResolvedSecretAction
	Token    ResolvedSecretAction
}

// ResolveSecretActions requires explicit intent for both fields. A secret cannot
// cross authentication modes; selecting a mode that needs a secret must leave it
// configured after the requested action.
func ResolveSecretActions(currentMode, desiredMode string, passwordPresent, tokenPresent bool, password, token SecretAction) (SecretActions, error) {
	if !validAuthMode(desiredMode) || (currentMode != "" && !validAuthMode(currentMode)) ||
		(!passwordPresent && currentMode == "basic") || (!tokenPresent && currentMode == "bearer") {
		return SecretActions{}, ErrInvalidSecretAction
	}
	resolvedPassword, err := resolveOne(currentMode == "basic", passwordPresent, desiredMode == "basic", password)
	if err != nil {
		return SecretActions{}, err
	}
	resolvedToken, err := resolveOne(currentMode == "bearer", tokenPresent, desiredMode == "bearer", token)
	if err != nil {
		return SecretActions{}, err
	}
	return SecretActions{Password: resolvedPassword, Token: resolvedToken}, nil
}

func resolveOne(currentSelected, present, desiredSelected bool, action SecretAction) (ResolvedSecretAction, error) {
	if action.Value != "" && action.Action != "replace" {
		return ResolvedSecretAction{}, ErrInvalidSecretAction
	}
	switch action.Action {
	case "replace":
		if !desiredSelected || action.Value == "" || len(action.Value) > 16<<10 {
			return ResolvedSecretAction{}, ErrInvalidSecretAction
		}
		return ResolvedSecretAction{Action: "replace", Value: action.Value, RevisionDelta: 1}, nil
	case "retain":
		if !desiredSelected || !currentSelected || !present {
			return ResolvedSecretAction{}, ErrInvalidSecretAction
		}
		return ResolvedSecretAction{Action: "retain"}, nil
	case "clear":
		if desiredSelected {
			return ResolvedSecretAction{}, ErrInvalidSecretAction
		}
		resolved := ResolvedSecretAction{Action: "clear"}
		if present {
			resolved.RevisionDelta = 1
		}
		return resolved, nil
	default:
		return ResolvedSecretAction{}, ErrInvalidSecretAction
	}
}

func validAuthMode(value string) bool {
	return value == "none" || value == "basic" || value == "bearer"
}
