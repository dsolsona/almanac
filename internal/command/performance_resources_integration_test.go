//go:build integration

package command

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

type performanceDiskAdmission struct {
	Available int64 `json:"available_bytes"`
	Required  int64 `json:"required_bytes"`
}

func performanceFixtureDisk(t *testing.T, parent context.Context, fleet performanceFleet, required int64) performanceDiskAdmission {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	deployment := fleet.fixture.backend.deployment
	if deployment == nil {
		t.Fatal("capacity disk admission requires the owned normal deployment")
	}
	deployment.evidence(t)
	command := exec.CommandContext(ctx, "docker", "exec", deployment.postgresID, "df", "-Pk", "/var/lib/postgresql")
	output, err := command.Output()
	if err != nil || len(output) > 4096 {
		t.Fatal("read isolated performance fixture free disk failed")
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 {
		t.Fatal("isolated performance fixture disk output has unexpected shape")
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 6 {
		t.Fatal("isolated performance fixture disk output has unexpected columns")
	}
	kilobytes, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || kilobytes < 0 || kilobytes > (1<<63-1)/1024 {
		t.Fatal("isolated performance fixture disk availability is invalid")
	}
	result := performanceDiskAdmission{Available: kilobytes * 1024, Required: required}
	if required < 0 || result.Available < required {
		t.Fatalf("isolated performance fixture has insufficient disk: available=%d required=%d", result.Available, required)
	}
	return result
}
