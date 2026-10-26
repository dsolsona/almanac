package storage

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestDecodeAgentLifecycleReceiptKeepsRetirementOptionalForHistoricalReceipts(t *testing.T) {
	raw := []byte(`{"version":1,"agent":{"id":"abcdefab-cdef-4abc-8def-abcdefabcdee","name":"agent-one","display_name":"Agent One","enabled":true,"version":2,"created_at":"2026-10-26T10:00:00Z","updated_at":"2026-10-26T10:00:01Z"},"credential":{"id":"abcdefab-cdef-4abc-8def-abcdefabcdea","agent_id":"abcdefab-cdef-4abc-8def-abcdefabcdee","issued_at":"2026-10-26T10:00:00Z","expires_at":"2026-10-26T11:00:00Z","revoked_at":"2026-10-26T10:00:01Z"},"changed":true,"revoked_credential_count":1}`)
	saved, err := decodeAgentLifecycleReceipt(raw)
	if err != nil || saved.Credential == nil || saved.Credential.RetireAt != nil {
		t.Fatalf("historical receipt decoded as %+v, %v", saved, err)
	}
}

func TestAgentPageValidation(t *testing.T) {
	const agentID = "abcdefab-cdef-4abc-8def-abcdefabcdee"
	base := time.Date(2026, 10, 26, 12, 0, 0, 123456000, time.UTC)
	cursor, err := encodeAgentCursor(agentCursor{Version: 1, Kind: "credentials", Time: base, ID: agentID, AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, kind, scope, cursor string
		limit                     int
		wantErr                   bool
	}{
		{"default", "agents", "", "", 0, false},
		{"maximum", "agents", "", "", 200, false},
		{"too large", "agents", "", "", 201, true},
		{"negative", "agents", "", "", -1, true},
		{"credential cursor", "credentials", agentID, cursor, 50, false},
		{"wrong kind", "agents", "", cursor, 50, true},
		{"wrong scope", "credentials", "abcdefab-cdef-4abc-8def-abcdefabcde0", cursor, 50, true},
		{"padded", "credentials", agentID, cursor + "=", 50, true},
		{"oversized", "agents", "", string(make([]byte, 513)), 50, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := validateAgentPage(test.kind, test.scope, test.limit, test.cursor)
			if test.wantErr && !errors.Is(err, ErrInvalidAgentInput) {
				t.Fatalf("error=%v; want invalid input", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAgentCursorRejectsMalformedOrNoncanonicalPayload(t *testing.T) {
	const id = "abcdefab-cdef-4abc-8def-abcdefabcdee"
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	valid := agentCursor{Version: 1, Kind: "agents", Time: base, ID: id}
	for _, mutate := range []func(*agentCursor){func(c *agentCursor) { c.Version = 2 }, func(c *agentCursor) { c.Time = time.Time{} }, func(c *agentCursor) { c.ID = "ABCDEFAB-CDEF-4ABC-8DEF-ABCDEFABCDEE" }} {
		c := valid
		mutate(&c)
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		encoded := base64.RawURLEncoding.EncodeToString(raw)
		if _, _, err := validateAgentPage("agents", "", 50, encoded); !errors.Is(err, ErrInvalidAgentInput) {
			t.Fatalf("cursor %s error=%v", encoded, err)
		}
	}
	unknown := []byte(`{"version":1,"kind":"agents","time":"2026-10-26T12:00:00Z","id":"abcdefab-cdef-4abc-8def-abcdefabcdee","extra":true}`)
	encoded := base64.RawURLEncoding.EncodeToString(unknown)
	if _, _, err := validateAgentPage("agents", "", 50, encoded); !errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("unknown-field cursor error=%v", err)
	}
}

func TestAgentLifecycleInputValidation(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	proof := AgentAdminProof{UserID: "abcdefab-cdef-4abc-8def-abcdefabcdee", CredentialEpoch: "abcdefab-cdef-4abc-8def-abcdefabcdef", CredentialRevision: 1, VerifiedAt: now}
	valid := func() error {
		_, _, err := validateLifecycleInput(proof, "abcdefab-cdef-4abc-8def-abcdefabcdee", 1, "key", "request", func() time.Time { return now })
		return err
	}
	if err := valid(); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	if _, _, err := validateLifecycleInput(proof, "bad", 1, "key", "request", func() time.Time { return now }); !errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("bad ID error=%v", err)
	}
	if _, _, err := validateLifecycleInput(proof, "abcdefab-cdef-4abc-8def-abcdefabcdee", 0, "key", "request", func() time.Time { return now }); !errors.Is(err, ErrInvalidAgentInput) {
		t.Fatalf("bad version error=%v", err)
	}
}

func TestAgentLifecycleReceiptRequiresSafeCompleteMetadata(t *testing.T) {
	const agentID = "abcdefab-cdef-4abc-8def-abcdefabcdee"
	const credentialID = "abcdefab-cdef-4abc-8def-abcdefabcdea"
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	base := agentLifecycleReceipt{Version: 1, Agent: AgentRecord{ID: agentID, Name: "collector-1", DisplayName: "Collector 1", Enabled: true, Version: 2, CreatedAt: at, UpdatedAt: at}, Credential: &AgentCredentialStateRecord{ID: credentialID, AgentID: agentID, IssuedAt: at, ExpiresAt: at.Add(time.Hour), RevokedAt: &at}, Changed: true, RevokedCredentialCount: 1}
	if !validAgentLifecycleReceipt(base, "agent.credential.revoke", uuidForTest(t, agentID), uuidForTest(t, credentialID), 1) {
		t.Fatal("valid lifecycle receipt was rejected")
	}
	for _, test := range []struct {
		name string
		edit func(*agentLifecycleReceipt)
	}{
		{"missing agent ID", func(r *agentLifecycleReceipt) { r.Agent.ID = "" }},
		{"missing credential", func(r *agentLifecycleReceipt) { r.Credential = nil }},
		{"wrong credential owner", func(r *agentLifecycleReceipt) { r.Credential.AgentID = "abcdefab-cdef-4abc-8def-abcdefabcdeb" }},
		{"negative count", func(r *agentLifecycleReceipt) { r.RevokedCredentialCount = -1 }},
		{"changed count mismatch", func(r *agentLifecycleReceipt) { r.RevokedCredentialCount = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := base
			credential := *base.Credential
			r.Credential = &credential
			test.edit(&r)
			if validAgentLifecycleReceipt(r, "agent.credential.revoke", uuidForTest(t, agentID), uuidForTest(t, credentialID), 1) {
				t.Fatal("malformed receipt accepted")
			}
		})
	}
	if validAgentLifecycleReceipt(agentLifecycleReceipt{Version: 1}, "agent.credential.revoke", uuidForTest(t, agentID), uuidForTest(t, credentialID), 1) {
		t.Fatal("version-only receipt accepted")
	}
}

func TestAgentLifecycleReceiptDecoderRequiresZeroValuedFields(t *testing.T) {
	const agentID = "abcdefab-cdef-4abc-8def-abcdefabcdee"
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	valid, err := json.Marshal(agentLifecycleReceipt{Version: 1, Agent: AgentRecord{ID: agentID, Name: "collector-1", DisplayName: "Collector 1", Enabled: false, Version: 2, CreatedAt: at, UpdatedAt: at}, Changed: false, RevokedCredentialCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeAgentLifecycleReceipt(valid)
	if err != nil || !validAgentLifecycleReceipt(decoded, "agent.disable", uuidForTest(t, agentID), pgtype.UUID{}, 2) {
		t.Fatalf("valid no-op disable decode=%+v err=%v", decoded, err)
	}
	for _, field := range []struct{ name, needle string }{{"changed", `"changed":false,`}, {"revoked_credential_count", `,"revoked_credential_count":0`}, {"agent.enabled", `"enabled":false,`}} {
		corrupt := strings.Replace(string(valid), field.needle, "", 1)
		if corrupt == string(valid) {
			t.Fatalf("test fixture did not contain %s", field.name)
		}
		if _, err := decodeAgentLifecycleReceipt([]byte(corrupt)); err == nil {
			t.Fatalf("receipt missing %s decoded successfully: %s", field.name, corrupt)
		}
	}
}

func TestAgentCreationReceiptDecoderRequiresMetadataFields(t *testing.T) {
	const agentID = "abcdefab-cdef-4abc-8def-abcdefabcdee"
	const credentialID = "abcdefab-cdef-4abc-8def-abcdefabcdea"
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	valid, err := json.Marshal(agentReceiptResult{Version: 1, Agent: AgentRecord{ID: agentID, Name: "collector-1", DisplayName: "Collector 1", Enabled: true, Version: 1, CreatedAt: at, UpdatedAt: at}, Credential: AgentCredentialRecord{ID: credentialID, AgentID: agentID, IssuedAt: at, ExpiresAt: at.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeAgentCreationReceipt(valid)
	if err != nil || !validAgentCreationReceipt(decoded) {
		t.Fatalf("valid creation receipt decode=%+v err=%v", decoded, err)
	}
	for _, field := range []string{`"enabled":true,`, `"issued_at":"2026-10-26T12:00:00Z",`} {
		corrupt := strings.Replace(string(valid), field, "", 1)
		if corrupt == string(valid) {
			t.Fatalf("fixture omitted required test field %s", field)
		}
		if _, err := decodeAgentCreationReceipt([]byte(corrupt)); err == nil {
			t.Fatalf("creation receipt missing %s decoded successfully", field)
		}
	}
}

func uuidForTest(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	value, ok := canonicalUUID(raw)
	if !ok {
		t.Fatalf("invalid test UUID %s", raw)
	}
	return value
}
