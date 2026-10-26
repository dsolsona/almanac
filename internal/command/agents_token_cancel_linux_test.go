//go:build linux

package command

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsolsona/almanac/internal/secretfile"
	"github.com/dsolsona/almanac/internal/setupclient"
)

func TestCancelledTokenCompletionWarnsThatIssuedTokenWasLost(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	sink, err := secretfile.Reserve(ctx, filepath.Join(t.TempDir(), "agent.token"))
	if err != nil {
		t.Fatal("reserve token output failed")
	}
	defer sink.Close()
	cancel()
	err = sink.Complete([]byte("one-time-token"))
	if !errors.Is(err, secretfile.ErrTokenCancelled) {
		t.Fatalf("cancelled Complete returned %v", err)
	}
	failure := agentTokenCompleteFailure(err)
	if failure.Kind != setupclient.KindCancelled || !failure.Unknown || !strings.Contains(failure.Message, "credential was issued") || !strings.Contains(failure.Message, "token cannot be recovered") {
		t.Fatalf("cancelled token completion lost its uncertainty warning: %#v", failure)
	}
	var stderr bytes.Buffer
	if exit := writeAgentFailure(&bytes.Buffer{}, &stderr, "agents create", false, failure); exit != 6 {
		t.Fatalf("cancelled token completion exit=%d", exit)
	}
	if !strings.Contains(stderr.String(), "credential was issued") || !strings.Contains(stderr.String(), "token cannot be recovered") {
		t.Fatalf("text output omitted lost-token warning: %q", stderr.String())
	}
}
