//go:build integration

package command

import (
	"encoding/hex"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsolsona/almanac/internal/testpostgres"
)

func TestIntegrationExecutableBrowserAgentRegistration(t *testing.T) {
	for _, scenario := range []string{"agents", "agents-close", "agents-lost"} {
		t.Run(scenario, func(t *testing.T) { testBrowserAuthentication(t, scenario) })
	}
}

func TestIntegrationExecutableBrowserAgentLifecycle(t *testing.T) {
	testBrowserAuthentication(t, "agents-lifecycle")
}

func seedBrowserAgents(t *testing.T, databaseURL string) {
	t.Helper()
	conn := testpostgres.Connect(t, databaseURL)
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agents
(id,name,display_name,created_at,updated_at)
SELECT ('10000000-0000-4000-8000-' || lpad(n::text,12,'0'))::uuid,
 'fixture-' || lpad(n::text,3,'0'), 'Fixture agent ' || lpad(n::text,3,'0'),
 '2025-01-01T00:00:00Z'::timestamptz, '2025-01-01T00:00:00Z'::timestamptz
FROM generate_series(1,51) AS n`); err != nil {
		t.Fatal("seed browser agent pages failed")
	}
	// Expired history exercises pagination without violating the two-current-token
	// lifecycle invariant or implying that any fixture agent is connected.
	if _, err := conn.Exec(t.Context(), `INSERT INTO public.almanac_agent_credentials
(id,agent_id,verifier,credential_epoch,issued_at,expires_at)
SELECT ('20000000-0000-4000-8000-' || lpad(n::text,12,'0'))::uuid,
 '10000000-0000-4000-8000-000000000001'::uuid, decode(repeat('01',32),'hex'),
 i.credential_epoch, '2025-01-02T00:00:00Z'::timestamptz,
 '2025-01-03T00:00:00Z'::timestamptz
FROM generate_series(1,51) AS n CROSS JOIN public.almanac_installation i WHERE i.singleton`); err != nil {
		t.Fatal("seed browser credential pages failed")
	}
}

func assertBrowserAgentRegistration(t *testing.T, databaseURL, artifacts, scenario string) {
	t.Helper()
	// The browser emits only a one-way digest and public IDs, never a bearer token.
	raw, err := os.ReadFile(filepath.Join(artifacts, "agent-registration.json"))
	if err != nil || len(raw) > 4096 {
		t.Fatal("read bounded browser registration evidence failed")
	}
	var evidence struct {
		AgentID        string `json:"agent_id"`
		CredentialID   string `json:"credential_id"`
		VerifierSHA256 string `json:"verifier_sha256"`
		Replayed       bool   `json:"replayed"`
		CreateRequests int    `json:"create_requests"`
	}
	if err := json.Unmarshal(raw, &evidence, json.RejectUnknownMembers(true)); err != nil {
		t.Fatal("decode browser registration evidence failed")
	}
	verifier, err := hex.DecodeString(evidence.VerifierSHA256)
	if err != nil || len(verifier) != 32 {
		t.Fatal("browser verifier evidence is invalid")
	}
	wantRequests := 1
	wantName := "browser-normal"
	if scenario == "agents-lost" {
		wantRequests = 2
		wantName = "browser-lost"
	} else if scenario == "agents-close" {
		wantRequests = 2 // A rejected password proof precedes the fresh issuance.
	}
	if evidence.Replayed != (scenario == "agents-lost") || evidence.CreateRequests != wantRequests {
		t.Fatal("browser registration dispatch/replay evidence differs")
	}
	conn := testpostgres.Connect(t, databaseURL)
	var matching int
	wantEnabled, wantVersion, wantCredentials := true, 1, 52
	if scenario == "agents-lifecycle" {
		wantEnabled, wantVersion, wantCredentials = false, 4, 53
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_agents a
JOIN public.almanac_agent_credentials c ON c.agent_id=a.id
WHERE a.id=$1 AND c.id=$2 AND a.name=$3 AND a.enabled=$5 AND a.version=$6
AND c.verifier=$4 AND (c.revoked_at IS NULL)=$5 AND (c.retire_at IS NULL)=$5`, evidence.AgentID,
		evidence.CredentialID, wantName, verifier, wantEnabled, wantVersion).Scan(&matching); err != nil || matching != 1 {
		t.Fatal("displayed or withheld issuance does not match persisted credential")
	}
	var agentCount, credentialCount, receiptCount int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_agents`).Scan(&agentCount); err != nil || agentCount != 52 {
		t.Fatal("browser registration duplicated or lost an agent")
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_agent_credentials`).Scan(&credentialCount); err != nil || credentialCount != wantCredentials {
		t.Fatal("browser registration duplicated or lost a credential")
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_management_receipts
WHERE operation='agent.create' AND NOT result ? 'token'`).Scan(&receiptCount); err != nil || receiptCount != 1 {
		t.Fatal("browser registration receipt differs or exposes token")
	}
	if scenario == "agents-lifecycle" {
		assertBrowserAgentLifecycle(t, databaseURL, artifacts, evidence.AgentID, evidence.CredentialID)
	}
}

func assertBrowserAgentLifecycle(t *testing.T, databaseURL, artifacts, agentID, originalCredentialID string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(artifacts, "agent-lifecycle.json"))
	if err != nil || len(raw) > 4096 {
		t.Fatal("read bounded browser lifecycle evidence failed")
	}
	var evidence struct {
		AgentID        string `json:"agent_id"`
		CredentialID   string `json:"credential_id"`
		VerifierSHA256 string `json:"verifier_sha256"`
		MutationCount  int    `json:"mutation_count"`
	}
	if err := json.Unmarshal(raw, &evidence, json.RejectUnknownMembers(true)); err != nil || evidence.AgentID != agentID || evidence.MutationCount != 3 {
		t.Fatal("browser lifecycle dispatch evidence differs")
	}
	verifier, err := hex.DecodeString(evidence.VerifierSHA256)
	if err != nil || len(verifier) != 32 {
		t.Fatal("browser replacement verifier evidence is invalid")
	}
	conn := testpostgres.Connect(t, databaseURL)
	var matching int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_agent_credentials
WHERE agent_id=$1 AND id=$2 AND verifier=$3 AND revoked_at IS NOT NULL`, agentID, evidence.CredentialID, verifier).Scan(&matching); err != nil || matching != 1 {
		t.Fatal("replacement token verifier or persisted revocation differs")
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*)
FROM public.almanac_agent_credentials original
JOIN public.almanac_agent_credentials replacement ON replacement.agent_id=original.agent_id
WHERE original.agent_id=$1 AND original.id=$2 AND replacement.id=$3
AND original.retire_at=replacement.issued_at`, agentID, originalCredentialID, evidence.CredentialID).Scan(&matching); err != nil || matching != 1 {
		t.Fatal("zero-overlap rotation did not immediately retire the original credential")
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_agent_credentials
WHERE agent_id=$1 AND revoked_at IS NULL`, agentID).Scan(&matching); err != nil || matching != 0 {
		t.Fatal("disabled agent retains an unrevoked credential")
	}
	var receipts, operations int
	if err := conn.QueryRow(t.Context(), `SELECT count(*), count(DISTINCT operation)
FROM public.almanac_management_receipts
WHERE operation <> 'agent.create' AND NOT result ? 'token'`).Scan(&receipts, &operations); err != nil || receipts != 3 || operations != 3 {
		t.Fatal("browser lifecycle receipts differ or contain a token")
	}
}
