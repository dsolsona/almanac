//go:build integration

package agentmanagement

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

func (c *safeClock) SetNow(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = now
}

func TestIntegrationManagementRotationAndThinLifecycleWrappers(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	clock := &safeClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	auth, err := authentication.New(t.Context(), databaseURL, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(databaseURL, auth, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	password := "Rotation service initial password 492!"
	path := filepath.Join(t.TempDir(), "initial-password")
	if err := os.WriteFile(path, []byte(password), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.EnsureInitialOwner(t.Context(), "rotation-admin", path, "rotation-bootstrap"); err != nil {
		t.Fatal(err)
	}
	source := netip.MustParseAddr("192.0.2.151")
	restricted, err := auth.Login(t.Context(), authentication.LoginInput{Login: "rotation-admin", Password: password, Source: source, RequestID: "rotation-login"})
	if err != nil {
		t.Fatal(err)
	}
	restrictedTarget := "abcdefab-cdef-4abc-8def-abcdefabcdee"
	for _, denied := range []struct {
		name string
		run  func() error
		want error
	}{
		{"get", func() error {
			got, err := manager.Get(t.Context(), GetInput{SessionSecret: restricted.Secret, AgentID: restrictedTarget})
			if got != (storage.AgentRecord{}) {
				return errors.New("restricted Get returned data")
			}
			return err
		}, storage.ErrAgentForbidden},
		{"rotate", func() error {
			got, err := manager.Rotate(t.Context(), RotateInput{SessionSecret: restricted.Secret, Source: netip.MustParseAddr("192.0.2.173"), RequestID: "restricted-rotate", IdempotencyKey: "restricted-rotate", AgentID: restrictedTarget, ExpectedVersion: 1})
			if got != (RotationIssuance{}) {
				return errors.New("restricted Rotate returned data")
			}
			return err
		}, authentication.ErrForbidden},
		{"list", func() error {
			got, err := manager.List(t.Context(), ListInput{SessionSecret: restricted.Secret})
			if len(got.Items) != 0 || got.NextCursor != "" {
				return errors.New("restricted List returned data")
			}
			return err
		}, storage.ErrAgentForbidden},
		{"credentials", func() error {
			got, err := manager.Credentials(t.Context(), CredentialsInput{SessionSecret: restricted.Secret, AgentID: restrictedTarget})
			if len(got.Items) != 0 || got.NextCursor != "" {
				return errors.New("restricted Credentials returned data")
			}
			return err
		}, storage.ErrAgentForbidden},
		{"revoke", func() error {
			got, err := manager.Revoke(t.Context(), RevokeInput{SessionSecret: restricted.Secret, Source: netip.MustParseAddr("192.0.2.161"), RequestID: "restricted-revoke", IdempotencyKey: "restricted-revoke", AgentID: restrictedTarget, CredentialID: "abcdefab-cdef-4abc-8def-abcdefabcdea", ExpectedVersion: 1})
			if got != (storage.AgentLifecycleResult{}) {
				return errors.New("restricted Revoke returned data")
			}
			return err
		}, authentication.ErrForbidden},
		{"disable", func() error {
			got, err := manager.Disable(t.Context(), DisableInput{SessionSecret: restricted.Secret, Source: netip.MustParseAddr("192.0.2.162"), RequestID: "restricted-disable", IdempotencyKey: "restricted-disable", AgentID: restrictedTarget, ExpectedVersion: 1})
			if got != (storage.AgentLifecycleResult{}) {
				return errors.New("restricted Disable returned data")
			}
			return err
		}, authentication.ErrForbidden},
	} {
		t.Run("restricted "+denied.name, func(t *testing.T) {
			if err := denied.run(); !errors.Is(err, denied.want) {
				t.Fatalf("restricted operation error=%v", err)
			}
		})
	}
	permanent := "Rotation service permanent password 830!"
	normal, err := auth.ChangePassword(t.Context(), authentication.ChangePasswordInput{SessionSecret: restricted.Secret, NewPassword: permanent, Source: source, RequestID: "rotation-onboarding"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(t.Context(), CreateInput{SessionSecret: normal.Secret, RequestID: "rotation-create", IdempotencyKey: "rotation-create", Name: "rotation-target", DisplayName: "Rotation Target"})
	if err != nil {
		t.Fatal(err)
	}
	invalidatedRestricted := RotateInput{SessionSecret: restricted.Secret, Source: netip.MustParseAddr("192.0.2.172"), RequestID: "rotation-invalidated-restricted", IdempotencyKey: "rotation-invalidated-restricted", AgentID: created.Creation.Agent.ID, ExpectedVersion: 1}
	if got, err := manager.Rotate(t.Context(), invalidatedRestricted); !errors.Is(err, authentication.ErrUnauthenticated) || got != (RotationIssuance{}) {
		t.Fatalf("onboarding-invalidated restricted session returned token=%t metadata=%+v err=%v", got.Token != "", got.Rotation, err)
	}
	conn := testpostgres.Connect(t, databaseURL)
	sessionVerifier, err := identity.SessionSecretVerifier(normal.Secret)
	if err != nil {
		t.Fatal(err)
	}
	var lastSeenBefore time.Time
	if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", sessionVerifier[:]).Scan(&lastSeenBefore); err != nil {
		t.Fatal(err)
	}
	var proofRowsBefore int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_login_attempts").Scan(&proofRowsBefore); err != nil {
		t.Fatal(err)
	}
	clock.SetNow(lastSeenBefore.Add(time.Second))
	get, err := manager.Get(t.Context(), GetInput{SessionSecret: normal.Secret, AgentID: created.Creation.Agent.ID})
	if err != nil || get != created.Creation.Agent {
		t.Fatalf("Get = %+v, %v", get, err)
	}
	list, err := manager.List(t.Context(), ListInput{SessionSecret: normal.Secret})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != get.ID {
		t.Fatalf("List = %+v, %v", list, err)
	}
	history, err := manager.Credentials(t.Context(), CredentialsInput{SessionSecret: normal.Secret, AgentID: get.ID})
	if err != nil || len(history.Items) != 1 {
		t.Fatalf("Credentials = %+v, %v", history, err)
	}
	var lastSeenAfter time.Time
	if err := conn.QueryRow(t.Context(), "SELECT last_seen_at FROM public.almanac_sessions WHERE verifier=$1", sessionVerifier[:]).Scan(&lastSeenAfter); err != nil {
		t.Fatal(err)
	}
	if !lastSeenAfter.Equal(lastSeenBefore) {
		t.Fatalf("safe management reads touched session: %v -> %v", lastSeenBefore, lastSeenAfter)
	}
	var proofRowsAfter int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_login_attempts").Scan(&proofRowsAfter); err != nil {
		t.Fatal(err)
	}
	if proofRowsAfter != proofRowsBefore {
		t.Fatalf("safe reads performed password proof work: login attempt rows %d -> %d", proofRowsBefore, proofRowsAfter)
	}
	input := RotateInput{SessionSecret: normal.Secret, Source: netip.MustParseAddr("192.0.2.153"), RequestID: "rotation-service-first", IdempotencyKey: "rotation-service-key", AgentID: get.ID, ExpectedVersion: 1}
	issued, err := manager.Rotate(t.Context(), input)
	if err != nil || issued.Token == "" || issued.Rotation.Replayed {
		t.Fatalf("fresh rotation token-present=%t metadata=%+v err=%v", issued.Token != "", issued.Rotation, err)
	}
	id, verifier, err := identity.AgentTokenVerifier(issued.Token)
	if err != nil || id != issued.Rotation.Credential.ID {
		t.Fatal("rotation token did not match replacement ID")
	}
	var persisted []byte
	if err := conn.QueryRow(t.Context(), "SELECT verifier FROM public.almanac_agent_credentials WHERE id=$1", id).Scan(&persisted); err != nil || string(persisted) != string(verifier[:]) {
		t.Fatal("replacement verifier was not persisted")
	}
	badReplay := input
	badReplay.Source = netip.MustParseAddr("192.0.2.163")
	badReplay.SessionSecret = "invalid-session"
	badReplay.RequestID = "rotation-bad-replay-proof"
	if got, err := manager.Rotate(t.Context(), badReplay); !errors.Is(err, authentication.ErrUnauthenticated) || got != (RotationIssuance{}) {
		t.Fatalf("invalid-session replay token-present=%t metadata=%+v err=%v", got.Token != "", got.Rotation, err)
	}
	input.Source = netip.MustParseAddr("192.0.2.154")
	replay, err := manager.Rotate(t.Context(), input)
	if err != nil || !replay.Rotation.Replayed || replay.Token != "" || replay.Rotation.Credential.ID != id {
		t.Fatalf("rotation replay token-present=%t metadata=%+v err=%v", replay.Token != "", replay.Rotation, err)
	}
	wrong := input
	wrong.Source = netip.MustParseAddr("192.0.2.155")
	wrong.SessionSecret = "invalid-session"
	wrong.IdempotencyKey = "rotation-wrong-password"
	wrong.RequestID = "rotation-wrong-password"
	if got, err := manager.Rotate(t.Context(), wrong); !errors.Is(err, authentication.ErrUnauthenticated) || got != (RotationIssuance{}) {
		t.Fatalf("invalid-session rotation token-present=%t metadata=%+v err=%v", got.Token != "", got.Rotation, err)
	}
	revokeInput := RevokeInput{SessionSecret: normal.Secret, Source: netip.MustParseAddr("192.0.2.156"), RequestID: "rotation-revoke", IdempotencyKey: "rotation-revoke", AgentID: get.ID, CredentialID: id, ExpectedVersion: 2}
	badRevoke := revokeInput
	badRevoke.Source = netip.MustParseAddr("192.0.2.164")
	badRevoke.SessionSecret = "invalid-session"
	badRevoke.RequestID = "rotation-revoke-wrong-proof"
	if got, err := manager.Revoke(t.Context(), badRevoke); !errors.Is(err, authentication.ErrUnauthenticated) || got != (storage.AgentLifecycleResult{}) {
		t.Fatalf("invalid-session revoke = %+v, %v", got, err)
	}
	if got, err := manager.Revoke(t.Context(), revokeInput); err != nil || !got.Changed || got.Credential == nil {
		t.Fatalf("service revoke = %+v, %v", got, err)
	}
	badRevokeReplay := revokeInput
	badRevokeReplay.Source = netip.MustParseAddr("192.0.2.165")
	badRevokeReplay.SessionSecret = "invalid-session"
	badRevokeReplay.RequestID = "rotation-revoke-bad-replay-proof"
	if got, err := manager.Revoke(t.Context(), badRevokeReplay); !errors.Is(err, authentication.ErrUnauthenticated) || got != (storage.AgentLifecycleResult{}) {
		t.Fatalf("invalid-session revoke replay = %+v, %v", got, err)
	}
	disableInput := DisableInput{SessionSecret: normal.Secret, Source: netip.MustParseAddr("192.0.2.157"), RequestID: "rotation-disable", IdempotencyKey: "rotation-disable", AgentID: get.ID, ExpectedVersion: 3}
	badDisable := disableInput
	badDisable.Source = netip.MustParseAddr("192.0.2.166")
	badDisable.SessionSecret = "invalid-session"
	badDisable.RequestID = "rotation-disable-wrong-proof"
	if got, err := manager.Disable(t.Context(), badDisable); !errors.Is(err, authentication.ErrUnauthenticated) || got != (storage.AgentLifecycleResult{}) {
		t.Fatalf("invalid-session disable = %+v, %v", got, err)
	}
	if got, err := manager.Disable(t.Context(), disableInput); err != nil || !got.Changed || got.Agent.Enabled {
		t.Fatalf("service disable = %+v, %v", got, err)
	}
	if got, err := manager.Revoke(t.Context(), revokeInput); err != nil || !got.Replayed || !got.Changed || got.Agent.Version != 3 || got.Credential == nil || got.Credential.ID != id {
		t.Fatalf("authorized historical revoke replay = %+v, %v", got, err)
	}
	if got, err := manager.Disable(t.Context(), disableInput); err != nil || !got.Replayed || !got.Changed || got.Agent.Enabled || got.Agent.Version != 4 {
		t.Fatalf("authorized disable replay = %+v, %v", got, err)
	}
	badDisableReplay := disableInput
	badDisableReplay.Source = netip.MustParseAddr("192.0.2.167")
	badDisableReplay.SessionSecret = "invalid-session"
	badDisableReplay.RequestID = "rotation-disable-bad-replay-proof"
	if got, err := manager.Disable(t.Context(), badDisableReplay); !errors.Is(err, authentication.ErrUnauthenticated) || got != (storage.AgentLifecycleResult{}) {
		t.Fatalf("invalid-session disable replay = %+v, %v", got, err)
	}
	if _, err := conn.Exec(t.Context(), "UPDATE public.almanac_users SET role='viewer' WHERE id=(SELECT id FROM public.almanac_users WHERE login='rotation-admin')"); err != nil {
		t.Fatal(err)
	}
	denied := input
	denied.Source = netip.MustParseAddr("192.0.2.158")
	denied.RequestID = "rotation-viewer-denial"
	denied.IdempotencyKey = "rotation-viewer-denial"
	if got, err := manager.Rotate(t.Context(), denied); !errors.Is(err, authentication.ErrForbidden) || got != (RotationIssuance{}) {
		t.Fatalf("viewer rotation token-present=%t metadata=%+v err=%v", got.Token != "", got.Rotation, err)
	}
	if got, err := manager.Get(t.Context(), GetInput{SessionSecret: normal.Secret, AgentID: get.ID}); !errors.Is(err, storage.ErrAgentForbidden) || got != (storage.AgentRecord{}) {
		t.Fatalf("viewer read = %+v, %v", got, err)
	}
	if got, err := manager.List(t.Context(), ListInput{SessionSecret: normal.Secret}); !errors.Is(err, storage.ErrAgentForbidden) || len(got.Items) != 0 || got.NextCursor != "" {
		t.Fatalf("viewer list = %+v, %v", got, err)
	}
	if got, err := manager.Credentials(t.Context(), CredentialsInput{SessionSecret: normal.Secret, AgentID: get.ID}); !errors.Is(err, storage.ErrAgentForbidden) || len(got.Items) != 0 || got.NextCursor != "" {
		t.Fatalf("viewer credentials = %+v, %v", got, err)
	}
	viewerRevoke := revokeInput
	viewerRevoke.Source = netip.MustParseAddr("192.0.2.168")
	viewerRevoke.RequestID = "rotation-viewer-revoke"
	if got, err := manager.Revoke(t.Context(), viewerRevoke); !errors.Is(err, authentication.ErrForbidden) || got != (storage.AgentLifecycleResult{}) {
		t.Fatalf("viewer revoke = %+v, %v", got, err)
	}
	viewerRevokeReplay := revokeInput
	viewerRevokeReplay.Source = netip.MustParseAddr("192.0.2.169")
	viewerRevokeReplay.RequestID = "rotation-viewer-revoke-replay"
	if got, err := manager.Revoke(t.Context(), viewerRevokeReplay); !errors.Is(err, authentication.ErrForbidden) || got != (storage.AgentLifecycleResult{}) {
		t.Fatalf("viewer revoke replay = %+v, %v", got, err)
	}
	viewerDisable := disableInput
	viewerDisable.Source = netip.MustParseAddr("192.0.2.170")
	viewerDisable.RequestID = "rotation-viewer-disable"
	if got, err := manager.Disable(t.Context(), viewerDisable); !errors.Is(err, authentication.ErrForbidden) || got != (storage.AgentLifecycleResult{}) {
		t.Fatalf("viewer disable = %+v, %v", got, err)
	}
	viewerDisableReplay := disableInput
	viewerDisableReplay.Source = netip.MustParseAddr("192.0.2.171")
	viewerDisableReplay.RequestID = "rotation-viewer-disable-replay"
	if got, err := manager.Disable(t.Context(), viewerDisableReplay); !errors.Is(err, authentication.ErrForbidden) || got != (storage.AgentLifecycleResult{}) {
		t.Fatalf("viewer disable replay = %+v, %v", got, err)
	}
}
