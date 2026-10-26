package agentmanagement

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
)

func TestNewRequiresValidTrustedDependencies(t *testing.T) {
	const databaseURL = "postgres://almanac:local_test@127.0.0.1:15487/almanac_test?sslmode=disable"
	auth, err := authentication.New(t.Context(), databaseURL, time.Now)
	if err != nil {
		t.Fatal("construct authentication dependency failed")
	}
	for _, test := range []struct {
		name        string
		databaseURL string
		auth        *authentication.Service
		now         func() time.Time
	}{
		{name: "invalid URL", databaseURL: "bad", auth: auth, now: time.Now},
		{name: "nil authentication", databaseURL: databaseURL, now: time.Now},
		{name: "different database", databaseURL: "postgres://almanac:local_test@127.0.0.1:15487/other_database?sslmode=disable", auth: auth, now: time.Now},
		{name: "nil clock", databaseURL: databaseURL, auth: auth},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := New(test.databaseURL, test.auth, test.now)
			if service != nil || !errors.Is(err, authentication.ErrInvalidInput) {
				t.Fatal("constructor did not reject invalid trusted dependency")
			}
		})
	}
	t.Run("valid dependencies", func(t *testing.T) {
		service, err := New(databaseURL, auth, time.Now)
		if err != nil || service == nil {
			t.Fatal("constructor rejected matching trusted dependencies")
		}
	})
}
