package agentmanagement

import (
	"encoding/json/v2"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
)

func TestManagementInputsExcludeSessionAndPasswordAndMalformedReadsFailAuthentication(t *testing.T) {
	const databaseURL = "postgres://almanac:local_test@127.0.0.1:15487/almanac_test?sslmode=disable"
	auth, err := authentication.New(t.Context(), databaseURL, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(databaseURL, auth, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []struct {
		name string
		call func() error
	}{
		{"get", func() error { _, err := service.Get(t.Context(), GetInput{SessionSecret: "malformed"}); return err }},
		{"list", func() error { _, err := service.List(t.Context(), ListInput{SessionSecret: "malformed"}); return err }},
		{"credentials", func() error {
			_, err := service.Credentials(t.Context(), CredentialsInput{SessionSecret: "malformed"})
			return err
		}},
	} {
		t.Run(run.name, func(t *testing.T) {
			if err := run.call(); !errors.Is(err, authentication.ErrUnauthenticated) {
				t.Fatalf("malformed session error=%v", err)
			}
		})
	}
	inputs := []any{
		GetInput{SessionSecret: "session-secret", AgentID: "agent"}, ListInput{SessionSecret: "session-secret", Limit: 10}, CredentialsInput{SessionSecret: "session-secret", AgentID: "agent"},
		RevokeInput{SessionSecret: "session-secret", CurrentPassword: "password", Source: netip.MustParseAddr("192.0.2.1")},
		DisableInput{SessionSecret: "session-secret", CurrentPassword: "password", Source: netip.MustParseAddr("192.0.2.1")},
	}
	for _, input := range inputs {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "session-secret") || strings.Contains(string(encoded), "password") {
			t.Fatalf("sensitive input JSON=%s", encoded)
		}
	}
	encoded, err := json.Marshal(RotationIssuance{Token: "secret-token"})
	if err != nil || strings.Contains(string(encoded), "secret-token") {
		t.Fatalf("rotation issuance JSON=%s err=%v", encoded, err)
	}
}
