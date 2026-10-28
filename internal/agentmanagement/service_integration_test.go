//go:build integration

package agentmanagement

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
	"github.com/dsolsona/almanac/internal/identity"
	"github.com/dsolsona/almanac/internal/storage"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

type safeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *safeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *safeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(duration)
}

func TestIntegrationCreateAgentUsesCurrentSessionForFreshIssueAndReplay(t *testing.T) {
	databaseURL := testpostgres.Database(t)
	if _, err := storage.Upgrade(t.Context(), databaseURL); err != nil {
		t.Fatal("upgrade test database failed")
	}
	clock := &safeClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	auth, err := authentication.New(t.Context(), databaseURL, clock.Now)
	if err != nil {
		t.Fatal("construct authentication service failed")
	}
	initialPassword := "Agent management initial password 492!"
	passwordPath := filepath.Join(t.TempDir(), "initial-password")
	if err := os.WriteFile(passwordPath, []byte(initialPassword), 0o600); err != nil {
		t.Fatal("write protected password fixture failed")
	}
	owner, err := auth.EnsureInitialOwner(t.Context(), "agent-admin", passwordPath, "agent-bootstrap")
	if err != nil || !owner.Created {
		t.Fatal("provision test administrator failed")
	}
	source := netip.MustParseAddr("192.0.2.111")
	restricted, err := auth.Login(t.Context(), authentication.LoginInput{Login: "agent-admin", Password: initialPassword, Source: source, RequestID: "agent-login-restricted"})
	if err != nil {
		t.Fatal("create restricted session failed")
	}
	manager, err := New(databaseURL, auth, clock.Now)
	if err != nil {
		t.Fatal("construct agent management service failed")
	}
	if result, err := manager.Create(t.Context(), CreateInput{
		SessionSecret: restricted.Secret,
		RequestID:     "agent-restricted", IdempotencyKey: "restricted-key", Name: "restricted-agent", DisplayName: "Restricted Agent",
	}); !errors.Is(err, authentication.ErrForbidden) || result != (Issuance{}) {
		t.Fatal("valid restricted onboarding session did not return forbidden with zero issuance")
	}
	if counts := registrationCounts(t, databaseURL); counts != (registrationCountsResult{}) {
		t.Fatal("restricted session changed protected registration rows")
	}
	permanentPassword := "Agent management permanent password 830!"
	normal, err := auth.ChangePassword(t.Context(), authentication.ChangePasswordInput{
		SessionSecret: restricted.Secret, NewPassword: permanentPassword,
		Source: source, RequestID: "agent-complete-onboarding",
	})
	if err != nil {
		t.Fatal("complete administrator onboarding failed")
	}
	clock.Advance(6 * time.Minute)
	createInput := CreateInput{
		SessionSecret: normal.Secret,
		RequestID:     "agent-create-1", IdempotencyKey: "agent-create-key-1", Name: "metrics-east",
		DisplayName: "Metrics East", Lifetime: 24 * time.Hour,
	}
	created, err := manager.Create(t.Context(), createInput)
	if err != nil || created.Token == "" || created.Creation.Replayed || created.Creation.Credential.ID == "" {
		t.Fatal("fresh agent create did not return one-time token")
	}
	id, verifier, err := identity.AgentTokenVerifier(created.Token)
	if err != nil || id != created.Creation.Credential.ID {
		t.Fatal("issued token did not match created credential ID")
	}
	query := testpostgres.Connect(t, databaseURL)
	var stored []byte
	if err := query.QueryRow(t.Context(), "SELECT verifier FROM public.almanac_agent_credentials WHERE id=$1", id).Scan(&stored); err != nil || !bytes.Equal(stored, verifier[:]) {
		t.Fatal("persisted agent verifier did not match issued token")
	}

	replayInput := createInput
	replayInput.RequestID = "agent-replay-current-session"
	replayed, err := manager.Create(t.Context(), replayInput)
	if err != nil || !replayed.Creation.Replayed || replayed.Token != "" || replayed.Creation.Credential != created.Creation.Credential {
		t.Fatal("authorized replay returned token or changed safe metadata")
	}
	conflict := createInput
	conflict.DisplayName = "Changed display name"
	conflict.RequestID = "agent-replay-conflict"
	if result, err := manager.Create(t.Context(), conflict); !errors.Is(err, storage.ErrAgentConflict) || result.Token != "" {
		t.Fatal("same key with different body did not conflict safely")
	}
	invalidSession := createInput
	invalidSession.SessionSecret = "invalid"
	invalidSession.IdempotencyKey = "agent-invalid-session"
	invalidSession.RequestID = "agent-invalid-session"
	if result, err := manager.Create(t.Context(), invalidSession); !errors.Is(err, authentication.ErrUnauthenticated) || result.Token != "" {
		t.Fatal("invalid session returned an issuance")
	}
	revokedSession := createInput
	revokedSession.SessionSecret = restricted.Secret
	revokedSession.IdempotencyKey = "agent-revoked-session"
	revokedSession.RequestID = "agent-revoked-session"
	if result, err := manager.Create(t.Context(), revokedSession); !errors.Is(err, authentication.ErrUnauthenticated) || result != (Issuance{}) {
		t.Fatal("revoked session returned an issuance")
	}
	for _, role := range []string{"viewer", "editor"} {
		if _, err := query.Exec(t.Context(), "UPDATE public.almanac_users SET role=$1 WHERE id=$2", role, owner.State.AdministratorID); err != nil {
			t.Fatal("set denial role fixture failed")
		}
		denied := createInput
		denied.IdempotencyKey = "agent-role-" + role
		denied.RequestID = "agent-role-" + role
		if result, err := manager.Create(t.Context(), denied); !errors.Is(err, authentication.ErrForbidden) || result.Token != "" {
			t.Fatal("non-administrator role returned an issuance")
		}
		if _, err := query.Exec(t.Context(), "UPDATE public.almanac_users SET role='administrator' WHERE id=$1", owner.State.AdministratorID); err != nil {
			t.Fatal("restore administrator fixture failed")
		}
	}
	for _, failure := range []string{"audit", "receipt"} {
		trigger, function := "fail_agent_service_"+failure, "fail_agent_service_"+failure
		statement := "CREATE FUNCTION public." + function + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test service failure'; END $$;"
		if failure == "audit" {
			statement += " CREATE TRIGGER " + trigger + " BEFORE INSERT ON public.almanac_audit_events FOR EACH ROW WHEN (NEW.action='agent.created') EXECUTE FUNCTION public." + function + "();"
		} else {
			statement += " CREATE TRIGGER " + trigger + " BEFORE INSERT ON public.almanac_management_receipts FOR EACH ROW EXECUTE FUNCTION public." + function + "();"
		}
		if _, err := query.Exec(t.Context(), statement); err != nil {
			t.Fatal("install transaction failure fixture failed")
		}
		failed := createInput
		failed.Name = "failure-" + failure
		failed.DisplayName = "Failure " + failure
		failed.IdempotencyKey = "agent-failure-" + failure
		failed.RequestID = "agent-failure-" + failure
		wantError := map[string]string{
			"audit":   "storage: agent audit insert failed",
			"receipt": "storage: agent receipt insert failed",
		}[failure]
		if result, err := manager.Create(t.Context(), failed); err == nil || err.Error() != wantError || errors.Is(err, storage.ErrAgentConflict) || result != (Issuance{}) {
			t.Fatal("injected transaction failure did not return a safe zero issuance and non-conflict error")
		}
		if counts := registrationCounts(t, databaseURL); counts != (registrationCountsResult{agents: 1, credentials: 1, receipts: 1, audits: 2}) {
			t.Fatal("injected transaction failure changed protected registration rows")
		}
		if _, err := query.Exec(t.Context(), "DROP TRIGGER "+trigger+" ON public."+map[string]string{"audit": "almanac_audit_events", "receipt": "almanac_management_receipts"}[failure]); err != nil {
			t.Fatal("remove transaction failure trigger failed")
		}
		if _, err := query.Exec(t.Context(), "DROP FUNCTION public."+function+"()"); err != nil {
			t.Fatal("remove transaction failure function failed")
		}
	}
	encoded, err := json.Marshal(created)
	if err != nil || strings.Contains(string(encoded), created.Token) {
		t.Fatal("issuance JSON exposed token")
	}
	var agents, credentials, receipts, audits int
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agents").Scan(&agents); err != nil {
		t.Fatal("count agent rows failed")
	}
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials").Scan(&credentials); err != nil {
		t.Fatal("count credential rows failed")
	}
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts").Scan(&receipts); err != nil {
		t.Fatal("count receipt rows failed")
	}
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action IN ('agent.created','agent.credential.issued')").Scan(&audits); err != nil {
		t.Fatal("count agent audit rows failed")
	}
	if agents != 1 || credentials != 1 || receipts != 1 || audits != 2 {
		t.Fatal("agent replay or denied request changed protected registration rows")
	}
	if strings.Contains(authentication.ErrUnauthenticated.Error(), permanentPassword) {
		t.Fatal("authentication error contained password input")
	}
}

type registrationCountsResult struct{ agents, credentials, receipts, audits int }

func registrationCounts(t *testing.T, databaseURL string) registrationCountsResult {
	t.Helper()
	query := testpostgres.Connect(t, databaseURL)
	var counts registrationCountsResult
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agents").Scan(&counts.agents); err != nil {
		t.Fatal("count agent rows failed")
	}
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_agent_credentials").Scan(&counts.credentials); err != nil {
		t.Fatal("count credential rows failed")
	}
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts").Scan(&counts.receipts); err != nil {
		t.Fatal("count receipt rows failed")
	}
	if err := query.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_audit_events WHERE action IN ('agent.created','agent.credential.issued')").Scan(&counts.audits); err != nil {
		t.Fatal("count agent audit rows failed")
	}
	return counts
}
