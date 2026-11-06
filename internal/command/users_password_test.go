//go:build linux

package command

import (
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsolsona/almanac/internal/cliprofile"
)

func TestAccountCommandsPreserveExactProtectedPasswordBytes(t *testing.T) {
	const password = "  a deliberate password ending in LF\n"
	for _, operation := range []string{"password", "redeem"} {
		t.Run(operation, func(t *testing.T) {
			seen := false
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				const identity = `{"csrf_token":"csrf","session":{"user_id":"owner","login":"owner","role":"administrator","restricted":false}}`
				if r.URL.Path == "/api/v1/auth/context" {
					fmt.Fprint(w, identity)
					return
				}
				var body struct {
					NewPassword string `json:"new_password"`
					Password    string `json:"password"`
					Token       string `json:"token"`
				}
				if json.UnmarshalRead(r.Body, &body) != nil {
					t.Error("invalid password request")
				}
				if r.Header.Get("X-CSRF-Token") != "csrf" {
					t.Error("missing current CSRF token")
				}
				if operation == "password" && r.URL.Path == "/api/v1/auth/password" {
					seen = true
					if body.NewPassword != password {
						t.Error("password file bytes were altered")
					}
					w.Header().Set("Set-Cookie", "__Host-almanac-session="+setupTestCookie+setupCookieAttributes)
					fmt.Fprint(w, identity)
				} else if operation == "redeem" && r.URL.Path == "/api/v1/account/activation" {
					seen = true
					if body.Password != password {
						t.Error("redemption file bytes were altered")
					}
					w.WriteHeader(http.StatusNoContent)
				} else {
					t.Error("unexpected account operation")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			directory := t.TempDir()
			profile := filepath.Join(directory, "profile.json")
			passwordFile := filepath.Join(directory, "password")
			linkFile := filepath.Join(directory, "link")
			if err := os.WriteFile(passwordFile, []byte(password), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(linkFile, []byte(server.URL+"/activate#alu_setup_test\n"), 0600); err != nil {
				t.Fatal(err)
			}
			locked, err := cliprofile.Open(t.Context(), profile, true)
			if err != nil {
				t.Fatal(err)
			}
			record := cliprofile.Record{SchemaVersion: 1, BackendURL: server.URL, CAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})), Session: setupTestCookie}
			if err := locked.Save(record); err != nil {
				t.Fatal(err)
			}
			if err := locked.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := performUser(t.Context(), userOptions{group: "account", operation: operation, profile: profile, passwordFile: passwordFile, linkFile: linkFile}); err != nil {
				t.Fatal(err)
			}
			if !seen {
				t.Fatal("password command did not reach its API")
			}
		})
	}
}
