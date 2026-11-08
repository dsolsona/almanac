package setupclient

import (
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/identity"
)

func TestUserClientProtectsOneTimeLinkOriginAndUnknownOutcomes(t *testing.T) {
	secret, _, err := identity.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "foreign-origin", "missing-link", "replayed-link", "invalid-json", "definitive-conflict"} {
		t.Run(mode, func(t *testing.T) {
			var origin string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Origin") != origin {
					t.Error("missing bound origin")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/context" {
					fmt.Fprint(w, `{"csrf_token":"csrf","session":{"user_id":"owner","login":"owner","role":"administrator","restricted":false}}`)
					return
				}
				if r.URL.Path != "/api/v1/users" || r.Header.Get("X-CSRF-Token") != "csrf" || r.Header.Get("Idempotency-Key") != "create-one" {
					t.Error("mutation did not preserve its authority and key")
				}
				if mode == "definitive-conflict" {
					w.WriteHeader(409)
					fmt.Fprint(w, `{"code":"email_in_use"}`)
					return
				}
				w.Header().Set("ETag", `"user-00000000-0000-4000-8000-000000000001-1"`)
				w.WriteHeader(201)
				if mode == "invalid-json" {
					fmt.Fprint(w, `{`)
					return
				}
				link := origin + "/activate#alu_setup_" + secret
				if mode == "foreign-origin" {
					link = "https://foreign.test/activate#alu_setup_" + secret
				}
				var user api.ManagedUser
				if err := json.Unmarshal([]byte(`{"id":"00000000-0000-4000-8000-000000000001","full_name":"Sam Rivera","email":"sam@example.test","role":"viewer","status":"pending","version":"1","last_sign_in":null,"sign_in_history_known":true,"is_last_admin":false}`), &user); err != nil {
					t.Error(err)
				}
				value := api.UserCommandResult{User: user, Purpose: "setup", Link: &link, Replayed: mode == "replayed-link"}
				expires := time.Unix(1_800_000_000, 0).UTC()
				value.ExpiresAt = &expires
				if mode == "missing-link" {
					value.Link = nil
				}
				_ = json.MarshalWrite(w, value)
			}))
			defer server.Close()
			origin = server.URL
			client, err := NewSession(SessionOptions{BackendURL: origin, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), Session: testCookie})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			result, err := client.ChangeUser(t.Context(), "create", "", "", "create-one", api.UserWriteRequest{FullName: "Sam Rivera", Email: "sam@example.test", Role: "viewer"})
			if mode == "valid" {
				if err != nil || result.Link == nil || !strings.HasPrefix(*result.Link, origin+"/activate#") {
					t.Fatal("valid issuance rejected", err)
				}
				return
			}
			failure, ok := errors.AsType[*Failure](err)
			if !ok || failure.OutcomeUnknown != (mode != "definitive-conflict") {
				t.Fatalf("unsafe response must preserve outcome certainty: %v", err)
			}
			if strings.Contains(failure.Error(), secret) {
				t.Fatal("secret included in diagnostic")
			}
		})
	}
}
