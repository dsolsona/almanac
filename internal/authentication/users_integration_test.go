//go:build integration

package authentication

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
)

func TestIntegrationUserAdministrationLifecycle(t *testing.T) {
	t.Parallel()
	databaseURL, service, clock := newIntegrationService(t)
	integrationOwner(t, service, "initial owner password for users")
	source := netip.MustParseAddr("192.0.2.41")
	login, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: "initial owner password for users", Source: source, RequestID: "users-login"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: login.Secret, NewPassword: "permanent owner password for users", Source: source, RequestID: "users-onboard"})
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := identity.SessionSecretVerifier(owner.Secret)
	proof := storage.DatasourceProof{SessionVerifier: verifier}
	created, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, Operation: "create", Name: "Sam Rivera", Email: "sam@example.test", Role: "editor", Key: "users-create", RequestID: "users-create", Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if created.User.Status != "pending" || created.Secret == "" {
		t.Fatal("new user must be pending with a one-time setup credential")
	}
	first, err := storage.ListUsers(t.Context(), databaseURL, storage.UserListInput{Proof: proof, Limit: 1, Now: clock.Now})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal("bounded first page missing cursor", err)
	}
	if _, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, Operation: "create", Name: "Aardvark Teammate", Email: "aa@example.test", Role: "viewer", Key: "before-cursor", RequestID: "before-cursor", Now: clock.Now}); err != nil {
		t.Fatal(err)
	}
	next, err := storage.ListUsers(t.Context(), databaseURL, storage.UserListInput{Proof: proof, Limit: 1, Cursor: first.NextCursor, Now: clock.Now})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != created.User.ID || next.Total != 3 {
		t.Fatal("insertion before the boundary shifted the next page", err)
	}
	if _, err := storage.ListUsers(t.Context(), databaseURL, storage.UserListInput{Proof: proof, Limit: 1, Cursor: first.NextCursor, Search: "Sam", Now: clock.Now}); !errors.Is(err, storage.ErrUserInvalid) {
		t.Fatal("cursor accepted different filters", err)
	}
	replay, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, Operation: "create", Name: "Sam Rivera", Email: "sam@example.test", Role: "editor", Key: "users-create", RequestID: "users-replay", Now: clock.Now})
	if err != nil || replay.Secret != "" || !replay.Replayed {
		t.Fatal("replay must not return the setup secret", err)
	}
	if err := service.RedeemUserChallenge(t.Context(), created.Secret, "Sam's first permanent password", source, "users-activate"); err != nil {
		t.Fatal(err)
	}
	if err := service.RedeemUserChallenge(t.Context(), created.Secret, "Sam's second permanent password", source, "users-reuse"); !errors.Is(err, storage.ErrUserChallenge) {
		t.Fatal("used setup proof accepted", err)
	}
	sam, err := service.Login(t.Context(), LoginInput{Login: "SAM@example.test", Password: "Sam's first permanent password", Source: source, RequestID: "sam-login"})
	if err != nil {
		t.Fatal("email login failed", err)
	}
	samVerifier, _ := identity.SessionSecretVerifier(sam.Secret)
	if _, err := storage.ListUsers(t.Context(), databaseURL, storage.UserListInput{Proof: storage.DatasourceProof{SessionVerifier: samVerifier}, Limit: 10, Now: clock.Now}); !errors.Is(err, storage.ErrAgentForbidden) {
		t.Fatal("editor enumerated users", err)
	}
	record, err := storage.GetUser(t.Context(), databaseURL, proof, created.User.ID, clock.Now)
	if err != nil || record.LastSignIn == nil || !record.SignInHistoryKnown {
		t.Fatal("successful login not recorded", err)
	}
	disabled, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, UserID: record.ID, Operation: "disable", Version: record.Version, Key: "users-disable", RequestID: "users-disable", Now: clock.Now})
	if err != nil || disabled.User.Status != "disabled" {
		t.Fatal("disable failed", err)
	}
	if _, err := service.Authenticate(t.Context(), sam.Secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("disabled user's session survived", err)
	}
	if _, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, UserID: record.ID, Operation: "enable", Version: record.Version, Key: "stale-enable", RequestID: "stale-enable", Now: clock.Now}); !errors.Is(err, storage.ErrUserConflict) {
		t.Fatal("stale mutation accepted", err)
	}
	enabled, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, UserID: record.ID, Operation: "enable", Version: disabled.User.Version, Key: "users-enable", RequestID: "users-enable", Now: clock.Now})
	if err != nil || enabled.User.Status != "active" {
		t.Fatal("enable failed", err)
	}
	reset, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, UserID: record.ID, Operation: "reset-password", Version: enabled.User.Version, Key: "users-reset", RequestID: "users-reset", Now: clock.Now})
	if err != nil || reset.User.Status != "resetting" || reset.Secret == "" {
		t.Fatal("reset failed", err)
	}
	if _, err := service.Login(t.Context(), LoginInput{Login: "sam@example.test", Password: "Sam's first permanent password", Source: source, RequestID: "sam-suspended"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("suspended password accepted", err)
	}
	if err := service.RedeemUserChallenge(t.Context(), reset.Secret, "Sam's reset permanent password", source, "sam-reset"); err != nil {
		t.Fatal(err)
	}
	record, err = storage.GetUser(t.Context(), databaseURL, proof, record.ID, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, UserID: record.ID, Operation: "delete", Version: record.Version, Key: "users-delete", RequestID: "users-delete", Now: clock.Now})
	if err != nil || archived.User.Status != "deleted" {
		t.Fatal("archive failed", err)
	}
	if _, err := storage.GetUser(t.Context(), databaseURL, proof, record.ID, clock.Now); !errors.Is(err, storage.ErrUserNotFound) {
		t.Fatal("archived user remains in management", err)
	}
	own, err := storage.GetAccount(t.Context(), databaseURL, proof, clock.Now)
	if err != nil || !own.IsLastAdmin {
		t.Fatal("last admin metadata missing", err)
	}
	for _, op := range []string{"disable", "delete", "reset-password"} {
		if _, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, UserID: own.ID, Operation: op, Version: own.Version, Key: "owner-" + op, RequestID: "owner-" + op, Now: clock.Now}); !errors.Is(err, storage.ErrLastAdmin) {
			t.Fatalf("last admin %s accepted: %v", op, err)
		}
	}
	second, err := storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, Operation: "create", Name: "Second Admin", Email: "second-admin@example.test", Role: "administrator", Key: "second-admin", RequestID: "second-admin", Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RedeemUserChallenge(t.Context(), second.Secret, "a second administrator's permanent password", source, "second-activate"); err != nil {
		t.Fatal(err)
	}
	secondLogin, err := service.Login(t.Context(), LoginInput{Login: "second-admin@example.test", Password: "a second administrator's permanent password", Source: source, RequestID: "second-login"})
	if err != nil {
		t.Fatal(err)
	}
	secondVerifier, _ := identity.SessionSecretVerifier(secondLogin.Secret)
	secondRecord, err := storage.GetUser(t.Context(), databaseURL, proof, second.User.ID, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, in := range []storage.UserCommandInput{
		{Proof: proof, UserID: own.ID, Version: own.Version, Operation: "disable", Key: "concurrent-owner", RequestID: "concurrent-owner", Now: clock.Now},
		{Proof: storage.DatasourceProof{SessionVerifier: secondVerifier}, UserID: secondRecord.ID, Version: secondRecord.Version, Operation: "disable", Key: "concurrent-second", RequestID: "concurrent-second", Now: clock.Now},
	} {
		go func() { <-start; _, err := storage.ChangeUser(t.Context(), databaseURL, in); results <- err }()
	}
	close(start)
	succeeded, protected := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, storage.ErrLastAdmin) {
			protected++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || protected != 1 {
		t.Fatalf("concurrent disables removed last-admin protection: succeeded=%d protected=%d", succeeded, protected)
	}
}

func TestIntegrationAccountLinksExpireAndReplacementRevokesOldProof(t *testing.T) {
	t.Parallel()
	databaseURL, service, clock := newIntegrationService(t)
	integrationOwner(t, service, "initial owner password for link checks")
	source := netip.MustParseAddr("192.0.2.51")
	login, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: "initial owner password for link checks", Source: source, RequestID: "link-login"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: login.Secret, NewPassword: "permanent owner password for link checks", Source: source, RequestID: "link-rotation"})
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := identity.SessionSecretVerifier(owner.Secret)
	proof := storage.DatasourceProof{SessionVerifier: verifier}
	command := func(op string, user storage.UserRecord, key string) (storage.UserCommandResult, error) {
		return storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, Operation: op, UserID: user.ID, Version: user.Version, Name: "Link Tester", Email: "links@example.test", Role: "viewer", Key: key, RequestID: key, Now: clock.Now})
	}
	created, err := command("create", storage.UserRecord{}, "link-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.InspectUserChallenge(t.Context(), databaseURL, created.Secret, clock.Now); err != nil {
		t.Fatal(err)
	}
	replaced, err := command("replace-setup-link", created.User, "link-replace")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RedeemUserChallenge(t.Context(), created.Secret, "a safe replacement password for links", source, "link-old"); !errors.Is(err, storage.ErrUserChallenge) {
		t.Fatal("old setup proof accepted", err)
	}
	if err := service.RedeemUserChallenge(t.Context(), replaced.Secret, "a safe replacement password for links", source, "link-new"); err != nil {
		t.Fatal(err)
	}
	current, err := storage.GetUser(t.Context(), databaseURL, proof, created.User.ID, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	reset, err := command("reset-password", current, "link-reset")
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := command("disable", reset.User, "link-disable")
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := command("enable", disabled.User, "link-enable")
	if err != nil || enabled.User.Status != "resetting" {
		t.Fatal("enable cancelled a suspended password without explicit consent", err)
	}
	cancelled, err := command("cancel-password-reset", enabled.User, "link-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RedeemUserChallenge(t.Context(), reset.Secret, "a rejected password after cancel", source, "link-cancelled"); !errors.Is(err, storage.ErrUserChallenge) {
		t.Fatal("cancelled reset accepted", err)
	}
	if _, err := service.Login(t.Context(), LoginInput{Login: "links@example.test", Password: "a safe replacement password for links", Source: source, RequestID: "link-restored"}); err != nil {
		t.Fatal("cancel did not restore previous password", err)
	}
	reset, err = command("reset-password", cancelled.User, "link-expiry")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(15 * time.Minute)
	if _, err := storage.InspectUserChallenge(t.Context(), databaseURL, reset.Secret, clock.Now); !errors.Is(err, storage.ErrUserChallenge) {
		t.Fatal("expired reset context accepted", err)
	}
	if err := service.RedeemUserChallenge(t.Context(), reset.Secret, "a rejected password exactly at expiry", source, "link-expired"); !errors.Is(err, storage.ErrUserChallenge) {
		t.Fatal("reset accepted at expiry", err)
	}
}

func TestIntegrationEmailAndLegacyUsernameShareLoginRateLimit(t *testing.T) {
	t.Parallel()
	databaseURL, service, clock := newIntegrationService(t)
	integrationOwner(t, service, "legacy owner initial password for aliases")
	source := netip.MustParseAddr("192.0.2.61")
	login, err := service.Login(t.Context(), LoginInput{Login: "owner", Password: "legacy owner initial password for aliases", Source: source, RequestID: "alias-login"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := service.ChangePassword(t.Context(), ChangePasswordInput{SessionSecret: login.Secret, NewPassword: "permanent legacy owner password for aliases", Source: source, RequestID: "alias-rotation"})
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := identity.SessionSecretVerifier(owner.Secret)
	proof := storage.DatasourceProof{SessionVerifier: verifier}
	account, err := storage.GetAccount(t.Context(), databaseURL, proof, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = storage.ChangeUser(t.Context(), databaseURL, storage.UserCommandInput{Proof: proof, Operation: "update", UserID: account.ID, Version: account.Version, Name: "Legacy Owner", Email: "owner@example.test", Role: "administrator", Key: "alias-email", RequestID: "alias-email", Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 5 {
		identifier := "owner"
		if attempt%2 == 0 {
			identifier = "OWNER@example.test"
		}
		_, err := service.Login(t.Context(), LoginInput{Login: identifier, Password: "incorrect password candidate", Source: netip.AddrFrom4([4]byte{192, 0, 2, byte(70 + attempt)}), RequestID: fmt.Sprintf("alias-failure-%d", attempt)})
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatal(err)
		}
	}
	for _, identifier := range []string{"owner", "owner@example.test"} {
		_, err := service.Login(t.Context(), LoginInput{Login: identifier, Password: "permanent legacy owner password for aliases", Source: netip.MustParseAddr("192.0.2.89"), RequestID: "alias-blocked"})
		if _, ok := errors.AsType[*RateLimitError](err); !ok {
			t.Fatalf("%s bypassed shared account admission: %v", identifier, err)
		}
	}
}
