package command

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type performanceDeployment struct {
	root, override, project, databaseURL string
	appID, postgresID, imageID, digest   string
	appPort, postgresPort, metricsPort   string
	providerPort, tracesPort             string
	running                              bool
}

func (r *performanceDeployment) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	suffix := strings.TrimPrefix(r.project, "almanac-perf-")
	if suffix == r.project || len(suffix) != 26 || strings.Trim(suffix, "abcdefghijklmnopqrstuvwxyz0123456789") != "" || !filepath.IsAbs(r.root) || !filepath.IsAbs(r.override) {
		return nil, errors.New("performance Compose command requires its owned project and absolute source paths")
	}
	command := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", "compose.yaml", "-f", r.override, "-p", r.project}, args...)...)
	command.Dir = r.root
	command.Env = append(os.Environ(), "COMPOSE_PROJECT_NAME="+r.project, "ALMANAC_PORT="+r.appPort,
		"ALMANAC_METRICS_PORT="+r.providerPort, "ALMANAC_TRACES_PORT="+r.tracesPort)
	return command, nil
}

func TestPerformanceComposeCommandPinsOwnedProjectAndPorts(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_NAME", "almanac-local")
	t.Setenv("ALMANAC_PORT", "18088")
	r := performanceDeployment{root: t.TempDir(), override: filepath.Join(t.TempDir(), "compose.json"), project: "almanac-perf-" + strings.ToLower(rand.Text()), appPort: "32101", providerPort: "32102", tracesPort: "32103"}
	command, err := r.command(t.Context(), "down", "--volumes")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "compose", "-f", "compose.yaml", "-f", r.override, "-p", r.project, "down", "--volumes"}
	if !reflect.DeepEqual(command.Args, want) || command.Dir != r.root {
		t.Fatal("performance command escaped its owned Compose source/project")
	}
	environment := make(map[string]string)
	for _, entry := range command.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		environment[key] = value
	}
	if environment["COMPOSE_PROJECT_NAME"] != r.project || environment["ALMANAC_PORT"] != r.appPort || environment["ALMANAC_METRICS_PORT"] != r.providerPort || environment["ALMANAC_TRACES_PORT"] != r.tracesPort {
		t.Fatal("performance command inherited operator project/ports")
	}
	for _, project := range []string{"almanac-local", "almanac-perf-", r.project + "\n"} {
		r.project = project
		if _, err := r.command(t.Context(), "down", "--volumes"); err == nil {
			t.Fatal("unowned project accepted for destructive deployment command")
		}
	}
	r.project, r.root = "almanac-perf-"+strings.ToLower(rand.Text()), "."
	if _, err := r.command(t.Context(), "down", "--volumes"); err == nil {
		t.Fatal("relative Compose source accepted for destructive deployment command")
	}
}
