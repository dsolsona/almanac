//go:build integration

package command

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dsolsona/almanac/internal/agentprotocol"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/testpostgres"
)

func assertIncompatibleAgentAttempt(t *testing.T, baseURL, token, databaseURL, agentID string, tlsClient *http.Client, generation string, proofByte byte, wantReason string, wantReachability api.AgentRuntimeStatusReachability) {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal("parse incompatible-agent backend URL failed")
	}
	u.Scheme, u.Path = "wss", "/api/agent/v1/connect"
	proof := bytes.Repeat([]byte{proofByte}, 32)
	nonce := bytes.Repeat([]byte{proofByte + 1}, 32)
	hello := agentprotocol.Envelope[agentprotocol.Hello]{
		Kind: agentprotocol.KindHello, MessageID: "incompatible-test", SessionGeneration: generation,
		Payload: agentprotocol.Hello{
			ProtocolVersions: []string{"almanac.agent.future"}, SoftwareVersion: "future-test",
			Capabilities: []agentprotocol.Capability{}, ResumeToken: base64.RawURLEncoding.EncodeToString(proof),
			RequestNonce: base64.RawURLEncoding.EncodeToString(nonce),
		},
	}
	if err := hello.Validate(); err != nil {
		t.Fatalf("unsupported hello fixture is structurally invalid: %v", err)
	}
	body, err := json.Marshal(hello)
	if err != nil {
		t.Fatal("encode unsupported hello failed")
	}
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	httpClient := &http.Client{Transport: tlsClient.Transport, Timeout: 5 * time.Second}
	conn, _, err := websocket.Dial(t.Context(), u.String(), &websocket.DialOptions{
		HTTPClient: httpClient, HTTPHeader: header, Subprotocols: []string{agentprotocol.ProtocolV1},
	})
	if err != nil {
		t.Fatal("open incompatible hello websocket failed")
	}
	writeCtx, cancelWrite := context.WithTimeout(t.Context(), time.Second)
	err = conn.Write(writeCtx, websocket.MessageText, body)
	cancelWrite()
	if err != nil {
		_ = conn.CloseNow()
		t.Fatal("send incompatible hello failed")
	}
	readCtx, cancelRead := context.WithTimeout(t.Context(), 2*time.Second)
	_, _, err = conn.Read(readCtx)
	cancelRead()
	_ = conn.CloseNow()
	var closeError websocket.CloseError
	if !errors.As(err, &closeError) || closeError.Reason != wantReason {
		t.Fatalf("incompatible hello close=%v, want safe reason %q", err, wantReason)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/agents/"+agentID, nil)
	if err != nil {
		t.Fatal("create incompatible-agent detail request failed")
	}
	response, err := tlsClient.Do(request)
	if err != nil {
		t.Fatal("read incompatible-agent detail failed")
	}
	var detail api.Agent
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	decodeErr := readErr
	if decodeErr == nil {
		decodeErr = json.Unmarshal(body, &detail)
	}
	if response.StatusCode != http.StatusOK || decodeErr != nil || detail.Runtime == nil || detail.Runtime.Reachability != wantReachability {
		t.Fatalf("incompatible-agent detail status=%d reachability=%v decode=%v", response.StatusCode, func() any {
			if detail.Runtime == nil {
				return nil
			}
			return detail.Runtime.Reachability
		}(), decodeErr)
	}
	if wantReachability == api.AgentRuntimeStatusReachabilityIncompatible && (detail.Runtime.Protocol != "incompatible" || detail.Runtime.SoftwareVersion != "unknown" ||
		detail.Runtime.DesiredConfigurationRevision != "1" || detail.Runtime.AppliedConfigurationRevision != "0" ||
		detail.Runtime.Capabilities == nil || len(detail.Runtime.Capabilities) != 0 || detail.Runtime.LastHeartbeatAt != nil) {
		t.Fatalf("incompatible-agent status contains fabricated details: %+v", detail.Runtime)
	}
	db := testpostgres.Connect(t, databaseURL)
	var owners, grants, attempts int
	if err := db.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM public.almanac_agent_ownership WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_execution_grants WHERE agent_id=$1),
		(SELECT count(*) FROM public.almanac_agent_incompatible_attempts WHERE agent_id=$1)`, agentID).Scan(&owners, &grants, &attempts); err != nil {
		t.Fatal("read incompatible-agent ownership boundary failed")
	}
	if wantReachability == api.AgentRuntimeStatusReachabilityIncompatible {
		if owners != 0 || grants != 0 || attempts != 1 {
			t.Fatalf("unowned incompatible owner/grant/attempt counts=%d/%d/%d", owners, grants, attempts)
		}
	} else if owners != 1 || grants == 0 || attempts != 0 {
		t.Fatalf("active-owner incompatible owner/grant/attempt counts=%d/%d/%d", owners, grants, attempts)
	}
}

func assertAgentWebSocketBoundaries(t *testing.T, baseURL, token string, tlsClient *http.Client) {
	t.Helper()
	dial := func(protocol string) (*websocket.Conn, error) {
		u, err := url.Parse(baseURL)
		if err != nil {
			t.Fatal("parse agent backend URL failed")
		}
		u.Scheme, u.Path = "wss", "/api/agent/v1/connect"
		header := http.Header{"Authorization": []string{"Bearer " + token}}
		httpClient := &http.Client{Transport: tlsClient.Transport, Timeout: 5 * time.Second}
		conn, _, err := websocket.Dial(t.Context(), u.String(), &websocket.DialOptions{HTTPClient: httpClient,
			HTTPHeader: header, Subprotocols: []string{protocol}})
		return conn, err
	}
	wrongProtocol, err := dial("almanac.agent.future")
	if err == nil {
		if wrongProtocol.Subprotocol() != "" {
			_ = wrongProtocol.CloseNow()
			t.Fatal("backend selected a subprotocol the client did not offer")
		}
		readCtx, cancel := context.WithTimeout(t.Context(), time.Second)
		_, _, readErr := wrongProtocol.Read(readCtx)
		cancel()
		_ = wrongProtocol.CloseNow()
		if readErr == nil {
			t.Fatal("backend kept a socket without the required subprotocol")
		}
	}
	for _, body := range [][]byte{
		[]byte(`{"kind":"unknown-operation","message_id":"frame-test","session_generation":"0","payload":{}}`),
		bytes.Repeat([]byte("x"), agentprotocol.MaxMessageBytes+1),
	} {
		conn, err := dial(agentprotocol.ProtocolV1)
		if err != nil {
			t.Fatal("open malformed-frame boundary socket failed")
		}
		writeCtx, cancel := context.WithTimeout(t.Context(), time.Second)
		writeErr := conn.Write(writeCtx, websocket.MessageText, body)
		cancel()
		if writeErr == nil {
			readCtx, readCancel := context.WithTimeout(t.Context(), time.Second)
			_, _, readErr := conn.Read(readCtx)
			readCancel()
			if readErr == nil {
				_ = conn.CloseNow()
				t.Fatal("backend accepted an unknown-kind or oversized frame")
			}
		}
		_ = conn.CloseNow()
	}
}

func assertCopiedAgentCannotReplaceOwner(t *testing.T, agentBinary, baseURL, token, caFile, databaseURL, agentID string, client *http.Client) {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "copied-agent-token")
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal("write copied-token fixture failed")
	}
	assertIncompatibleAgentAttempt(t, baseURL, token, databaseURL, agentID, client, "0", 87, "active_owner", api.AgentRuntimeStatusReachabilityConnected)
	copyAgent := exec.Command(agentBinary, "run", "--backend-url="+baseURL, "--token-file="+tokenPath, "--ca-file="+caFile)
	copyAgent.Stdout, copyAgent.Stderr = io.Discard, io.Discard
	if err := copyAgent.Start(); err != nil {
		t.Fatal("start copied-token agent process failed")
	}
	copyDone := make(chan error, 1)
	go func(process *exec.Cmd, done chan<- error) { done <- process.Wait() }(copyAgent, copyDone)
	select {
	case err := <-copyDone:
		if err == nil || copyAgent.ProcessState == nil || copyAgent.ProcessState.ExitCode() != 1 {
			t.Fatal("copied-token process did not report active-owner conflict")
		}
	case <-time.After(5 * time.Second):
		_ = copyAgent.Process.Kill()
		<-copyDone
		t.Fatal("copied-token process did not fail promptly")
	}
	db := testpostgres.Connect(t, databaseURL)
	var generation int64
	if err := db.QueryRow(t.Context(), "SELECT generation FROM public.almanac_agent_ownership WHERE agent_id=$1", agentID).Scan(&generation); err != nil || generation != 1 {
		t.Fatal("copied token displaced active ownership")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/agents/"+agentID, nil)
	if err != nil {
		t.Fatal("create active-owner status check failed")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("read active-owner status after copied-token attempt failed")
	}
	var detail api.Agent
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || json.Unmarshal(body, &detail) != nil || detail.Runtime == nil || detail.Runtime.Reachability != api.AgentRuntimeStatusReachabilityConnected {
		t.Fatal("copied-token attempt disrupted the active agent")
	}
}

func revokeConnectedAgentCredential(t *testing.T, backendBinary string, journey connectedAgentJourney, client *http.Client, baseURL string) {
	t.Helper()
	revoke := exec.CommandContext(t.Context(), backendBinary, "agents", "credentials", "revoke", "--profile-file="+journey.profile,
		"--agent-id="+journey.id, "--credential-id="+journey.credentialID, "--if-match=\"agent:"+journey.id+":1\"",
		"--current-password-file="+journey.passwordPath, "--idempotency-key=connected-agent-revoke", "--confirm", "--output=json")
	revoke.Stdout, revoke.Stderr = io.Discard, io.Discard
	if err := revoke.Run(); err != nil {
		t.Fatal("revoke connected-agent credential failed")
	}
	select {
	case <-journey.done:
		err := <-journey.result
		if err == nil || journeyProcessExitCode(err) != 1 {
			t.Fatal("revoked agent did not exit with its actionable revocation outcome")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("revoked agent session did not close within ten seconds")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/agent/v1/configuration", nil)
	if err != nil {
		t.Fatal("create revoked configuration request failed")
	}
	request.Header.Set("Authorization", "Bearer "+journey.token)
	noCookieClient := &http.Client{Transport: client.Transport, Timeout: 5 * time.Second}
	response, err := noCookieClient.Do(request)
	if err != nil {
		t.Fatal("send revoked configuration request failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked configuration status=%d", response.StatusCode)
	}
}

func journeyProcessExitCode(err error) int {
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return 0
}
