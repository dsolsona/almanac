package datasource

import "testing"

func TestResolveSecretActionsRequiresExplicitModeTransitions(t *testing.T) {
	tests := []struct {
		name             string
		current, desired string
		passwordPresent  bool
		tokenPresent     bool
		password         SecretAction
		token            SecretAction
		wantErr          bool
	}{
		{name: "create basic replaces password", desired: "basic", password: SecretAction{Action: "replace", Value: "secret"}, token: SecretAction{Action: "clear"}},
		{name: "retain keeps configured password", current: "basic", desired: "basic", passwordPresent: true, password: SecretAction{Action: "retain"}, token: SecretAction{Action: "clear"}},
		{name: "cannot change mode without clearing old field", current: "basic", desired: "bearer", passwordPresent: true, password: SecretAction{Action: "retain"}, token: SecretAction{Action: "replace", Value: "token"}, wantErr: true},
		{name: "new bearer mode requires token", desired: "bearer", password: SecretAction{Action: "clear"}, token: SecretAction{Action: "clear"}, wantErr: true},
		{name: "none clears both", desired: "none", password: SecretAction{Action: "clear"}, token: SecretAction{Action: "clear"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveSecretActions(tt.current, tt.desired, tt.passwordPresent, tt.tokenPresent, tt.password, tt.token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveSecretActions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
