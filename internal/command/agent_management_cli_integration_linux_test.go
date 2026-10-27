//go:build integration && linux

package command

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/identity"
	"github.com/jackc/pgx/v5"
)

func runRestrictedAgentCLI(t *testing.T, binary, dir, baseURL, passwordFile, certFile string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	run := func(args ...string) (int, string, string) {
		cmd := exec.CommandContext(ctx, binary, args...)
		var out, errOut strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return exit.ExitCode(), out.String(), errOut.String()
			}
			return 1, out.String(), errOut.String()
		}
		return 0, out.String(), errOut.String()
	}
	restrictedDir := filepath.Join(dir, "restricted-profile")
	if err := os.Mkdir(restrictedDir, 0700); err != nil {
		t.Fatal("create restricted profile directory failed")
	}
	profile := filepath.Join(restrictedDir, "profile.json")
	if code, _, _ := run("auth", "login", "--profile-file="+profile, "--backend-url="+baseURL, "--login=agent-owner", "--password-file="+passwordFile, "--ca-file="+certFile); code != 0 {
		t.Fatalf("restricted CLI login exit=%d", code)
	}
	code, out, errOut := run("agents", "list", "--profile-file="+profile, "--output=json")
	var cliError struct {
		SchemaVersion int    `json:"schema_version"`
		Operation     string `json:"operation"`
		Error         struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if code != 3 || errOut != "" || json.Unmarshal([]byte(out), &cliError) != nil || cliError.SchemaVersion != 1 || cliError.Operation != "agents list" || cliError.Error.Code != "forbidden" {
		t.Fatal("restricted CLI response did not use the versioned safe error envelope")
	}
}

func runAgentManagementCLIAcceptance(t *testing.T, databaseURL, binary, dir, baseURL, certFile, permanentPassword string) bool {
	t.Helper()
	run := func(args ...string) (int, string, string) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		var out, errOut strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return 124, out.String(), errOut.String()
			}
			if exit, ok := err.(*exec.ExitError); ok {
				return exit.ExitCode(), out.String(), errOut.String()
			}
			return 1, out.String(), errOut.String()
		}
		return 0, out.String(), errOut.String()
	}
	requireExit := func(want, got int) {
		t.Helper()
		if got != want {
			t.Fatalf("agent CLI exit=%d, want %d", got, want)
		}
	}
	passwordPath := filepath.Join(dir, "cli-password")
	if err := os.WriteFile(passwordPath, []byte(permanentPassword), 0600); err != nil {
		t.Fatal("write protected CLI password failed")
	}
	profileDir := filepath.Join(dir, "admin-profile")
	if err := os.Mkdir(profileDir, 0700); err != nil {
		t.Fatal("create administrator profile directory failed")
	}
	profile := filepath.Join(profileDir, "profile.json")
	code, _, _ := run("auth", "login", "--profile-file="+profile, "--backend-url="+baseURL, "--login=agent-owner", "--password-file="+passwordPath, "--ca-file="+certFile)
	requireExit(0, code)
	db, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatal("connect to isolated database failed")
	}
	defer db.Close(t.Context())
	createTokenPath := filepath.Join(dir, "agent-create.token")
	createArgs := []string{"agents", "create", "--profile-file=" + profile, "--name=cli-agent", "--display-name=CLI Agent", "--idempotency-key=cli-create-agent-1", "--token-file=" + createTokenPath, "--output=json"}
	code, out, errOut := run(createArgs...)
	requireExit(0, code)
	if errOut != "" {
		t.Fatal("successful create wrote diagnostics")
	}
	var create agentCLICreateDocument
	if json.Unmarshal([]byte(out), &create) != nil || create.Result.Agent.ID == "" || create.Result.Agent.Version != "1" {
		t.Fatal("create result metadata was invalid")
	}
	token, err := os.ReadFile(createTokenPath)
	if err != nil || len(token) == 0 {
		t.Fatal("fresh create token file was not completed")
	}
	lookup, verifier, err := identity.AgentTokenVerifier(string(token))
	if err != nil {
		t.Fatal("create token did not parse")
	}
	var stored []byte
	if err := db.QueryRow(t.Context(), "SELECT verifier FROM public.almanac_agent_credentials WHERE id=$1", lookup).Scan(&stored); err != nil || !bytes.Equal(stored, verifier[:]) {
		t.Fatal("database did not store the matching token verifier")
	}
	if info, err := os.Stat(createTokenPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("create token sink was not private")
	}

	code, out, errOut = run("agents", "create", "--profile-file="+profile, "--name=cli-agent", "--display-name=CLI Agent", "--idempotency-key=cli-create-agent-1", "--show-token", "--output=json")
	var replay map[string]jsontext.Value
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &replay) != nil || strings.Contains(out, string(token)) {
		t.Fatal("create replay did not return safe metadata")
	}
	if _, exists := replay["token"]; exists {
		t.Fatal("create replay included token field")
	}
	if saved, err := os.ReadFile(profile); err != nil || strings.Contains(string(saved), string(token)) {
		t.Fatal("profile retained the agent token")
	}
	code, out, errOut = run("agents", "list", "--profile-file="+profile, "--output=json")
	if code != 0 || errOut != "" || !strings.Contains(out, create.Result.Agent.ID) {
		t.Fatal("agent list did not include the new agent")
	}
	code, out, errOut = run("agents", "get", "--profile-file="+profile, "--agent-id="+create.Result.Agent.ID, "--output=json")
	if code != 0 || errOut != "" {
		t.Fatal("agent get failed")
	}
	var detail struct {
		Result struct {
			ETag string `json:"etag"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(out), &detail) != nil || detail.Result.ETag != "\"agent:"+create.Result.Agent.ID+":1\"" {
		t.Fatal("agent get did not return the exact strong ETag")
	}
	code, out, errOut = run("agents", "credentials", "list", "--profile-file="+profile, "--agent-id="+create.Result.Agent.ID, "--output=json")
	if code != 0 || errOut != "" || !strings.Contains(out, create.Result.Credential.ID) {
		t.Fatal("credential history did not include the created credential")
	}

	var cliError struct {
		Operation string `json:"operation"`
		Error     struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	code, out, errOut = run("agents", "credentials", "rotate", "--profile-file="+profile, "--agent-id="+create.Result.Agent.ID, "--if-match=\"agent:"+create.Result.Agent.ID+":99\"", "--current-password-file="+passwordPath, "--idempotency-key=cli-stale-version", "--confirm", "--show-token", "--output=json")
	if code != 5 || errOut != "" || json.Unmarshal([]byte(out), &cliError) != nil || cliError.Operation != "agents credentials rotate" || cliError.Error.Code != "conflict" {
		t.Fatal("stale ETag did not return the versioned conflict envelope")
	}

	rotateTokenPath := filepath.Join(dir, "agent-rotate.token")
	rotateArgs := []string{"agents", "credentials", "rotate", "--profile-file=" + profile, "--agent-id=" + create.Result.Agent.ID, "--if-match=" + detail.Result.ETag, "--current-password-file=" + passwordPath, "--idempotency-key=cli-rotate-agent-1", "--confirm", "--lifetime-seconds=0", "--overlap-seconds=0", "--token-file=" + rotateTokenPath, "--output=json"}
	code, out, errOut = run(rotateArgs...)
	requireExit(0, code)
	if errOut != "" {
		t.Fatal("successful rotation wrote diagnostics")
	}
	var rotated struct {
		Result struct {
			Agent struct {
				Version string `json:"version"`
			} `json:"agent"`
			Credential struct {
				ID string `json:"id"`
			} `json:"credential"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(out), &rotated) != nil || rotated.Result.Agent.Version != "2" {
		t.Fatal("rotation result metadata or version was invalid")
	}
	rotateToken, err := os.ReadFile(rotateTokenPath)
	if err != nil || len(rotateToken) == 0 {
		t.Fatal("rotation token was not completed")
	}
	rotateLookup, rotateVerifier, err := identity.AgentTokenVerifier(string(rotateToken))
	if err != nil {
		t.Fatal("rotation token did not parse")
	}
	if err := db.QueryRow(t.Context(), "SELECT verifier FROM public.almanac_agent_credentials WHERE id=$1", rotateLookup).Scan(&stored); err != nil || !bytes.Equal(stored, rotateVerifier[:]) {
		t.Fatal("database did not store the matching rotation verifier")
	}
	code, out, errOut = run("agents", "credentials", "rotate", "--profile-file="+profile, "--agent-id="+create.Result.Agent.ID, "--if-match="+detail.Result.ETag, "--current-password-file="+passwordPath, "--idempotency-key=cli-rotate-agent-1", "--confirm", "--lifetime-seconds=0", "--overlap-seconds=0", "--show-token", "--output=json")
	if code != 0 || errOut != "" || strings.Contains(out, string(rotateToken)) || json.Unmarshal([]byte(out), &replay) != nil {
		t.Fatal("rotation replay did not return safe metadata")
	}
	if _, exists := replay["token"]; exists {
		t.Fatal("rotation replay included token field")
	}

	rotatedETag := "\"agent:" + create.Result.Agent.ID + ":" + rotated.Result.Agent.Version + "\""
	code, out, errOut = run("agents", "credentials", "revoke", "--profile-file="+profile, "--agent-id="+create.Result.Agent.ID, "--credential-id="+rotated.Result.Credential.ID, "--if-match="+rotatedETag, "--current-password-file="+passwordPath, "--idempotency-key=cli-revoke-agent-1", "--confirm", "--output=json")
	if code != 0 || errOut != "" {
		t.Fatal("credential revocation failed")
	}
	var revokedAt *string
	if err := db.QueryRow(t.Context(), "SELECT revoked_at::text FROM public.almanac_agent_credentials WHERE id=$1", rotated.Result.Credential.ID).Scan(&revokedAt); err != nil || revokedAt == nil {
		t.Fatal("rotated credential was not revoked in PostgreSQL")
	}
	code, out, errOut = run("agents", "get", "--profile-file="+profile, "--agent-id="+create.Result.Agent.ID, "--output=json")
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &detail) != nil {
		t.Fatal("agent get after revoke failed")
	}
	code, _, errOut = run("agents", "disable", "--profile-file="+profile, "--agent-id="+create.Result.Agent.ID, "--if-match="+detail.Result.ETag, "--current-password-file="+passwordPath, "--idempotency-key=cli-disable-agent-1", "--confirm", "--output=json")
	if code != 0 || errOut != "" {
		t.Fatal("agent disable failed")
	}
	var enabled bool
	var version int64
	if err := db.QueryRow(t.Context(), "SELECT enabled,version FROM public.almanac_agents WHERE id=$1", create.Result.Agent.ID).Scan(&enabled, &version); err != nil || enabled || version != 4 {
		t.Fatal("disabled agent final database state/version was wrong")
	}
	var receiptCount int
	if err := db.QueryRow(t.Context(), "SELECT count(*) FROM public.almanac_management_receipts WHERE idempotency_key IN ('cli-create-agent-1','cli-rotate-agent-1','cli-revoke-agent-1','cli-disable-agent-1')").Scan(&receiptCount); err != nil || receiptCount != 4 {
		t.Fatal("management mutation receipts were not persisted once each")
	}
	return true
}
