package agentmanagement

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/authentication"
)

func TestNewRequiresValidTrustedDependencies(t *testing.T) {
	if _, err := New("bad", nil, time.Now); !errors.Is(err, authentication.ErrInvalidInput) {
		t.Fatal("constructor accepted invalid database URL")
	}
}
