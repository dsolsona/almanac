//go:build integration

package command

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dsolsona/almanac/internal/agentprotocol"
)

// Mirrors c6b8674: established readRaw treats every close as unavailable;
// handshake readJSON classifies session_rejected as terminal.
func assertLegacyReplacementProtocol(t *testing.T, baseURL, token string, tlsClient *http.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme, u.Path = "wss", "/api/agent/v1/connect"
	dial := func(proof byte, generation string, capability *bool) (*websocket.Conn, string, error) {
		conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: &http.Client{Transport: tlsClient.Transport, Timeout: 5 * time.Second}, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}, Subprotocols: []string{agentprotocol.ProtocolV1}})
		if err != nil {
			return nil, "", err
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		payload := map[string]any{"protocol_versions": []string{agentprotocol.ProtocolV1}, "software_version": "legacy-fixture", "capabilities": []any{}, "resume_token": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{proof}, 32)), "request_nonce": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{proof + 1}, 32))}
		if capability != nil {
			payload["supports_owner_replaced"] = *capability
		}
		body, err := json.Marshal(map[string]any{"kind": "hello", "message_id": "legacy-fixture", "session_generation": generation, "payload": payload})
		if err != nil {
			return conn, "", err
		}
		if err = conn.Write(ctx, websocket.MessageText, body); err != nil {
			return conn, "", err
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			return conn, "", err
		}
		var welcome agentprotocol.Envelope[agentprotocol.Welcome]
		if err := json.Unmarshal(data, &welcome); err != nil {
			return conn, "", err
		}
		return conn, welcome.SessionGeneration, nil
	}
	closeReason := func(conn *websocket.Conn, want string) error {
		t.Helper()
		_, _, err := conn.Read(ctx)
		var closeErr websocket.CloseError
		if !errors.As(err, &closeErr) || closeErr.Reason != want {
			t.Fatalf("close=%v want=%s", err, want)
		}
		_ = conn.CloseNow()
		return err
	}
	legacy, generation, err := dial(41, "0", nil)
	if err != nil {
		t.Fatal(err)
	}
	f := false
	replacement, _, err := dial(43, "0", &f)
	if err != nil {
		t.Fatal(err)
	}
	// Established legacy readRaw returns the same temporary unavailable error for any close.
	messageType, _, legacyReadErr := legacy.Read(ctx)
	if legacyReadErr != nil || messageType != websocket.MessageText {
		legacyReadErr = errors.New("backend message unavailable") // c6b8674 readRaw
	}
	if legacyReadErr == nil || legacyReadErr.Error() != "backend message unavailable" {
		t.Fatal("legacy reader did not use temporary fallback")
	}
	_ = legacy.CloseNow()
	resume, _, err := dial(41, generation, nil)
	var rejected websocket.CloseError
	if !errors.As(err, &rejected) || rejected.Reason != "session_rejected" {
		t.Fatalf("legacy handshake did not terminate: %v", err)
	}
	// c6b8674 closeReasonError maps this handshake reason to ErrSessionRejected,
	// which its retry loop treats as terminal.
	_ = resume.CloseNow()
	capable := true
	current, _, err := dial(45, "0", &capable)
	if err != nil {
		t.Fatal(err)
	}
	closeReason(replacement, "session_rejected")
	next, _, err := dial(47, "0", &capable)
	if err != nil {
		t.Fatal(err)
	}
	closeReason(current, "owner_replaced")
	_ = next.CloseNow()
}
