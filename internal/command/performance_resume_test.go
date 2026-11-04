package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type performanceRetainedFixture struct {
	Runtime string   `json:"runtime"`
	Project string   `json:"project"`
	Report  string   `json:"report"`
	Ports   []string `json:"ports"`
}

func (f performanceRetainedFixture) deployment(root string, size int) (*performanceDeployment, string, error) {
	if size != 1 && size != 10 && size != 100 && size != 1000 ||
		!performanceArtifactName(f.Runtime, "backend-performance-runtime-") ||
		!performanceArtifactName(f.Report, "backend-performance-controlled-") || len(f.Ports) != 5 {
		return nil, "", errors.New("retained capacity fixture has an invalid size or artifact identity")
	}
	seen := make(map[string]bool)
	for _, port := range f.Ports {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value < 1024 || strconv.FormatUint(value, 10) != port || seen[port] {
			return nil, "", errors.New("retained capacity ports must be distinct explicit loopback bindings")
		}
		seen[port] = true
	}
	dir := filepath.Join(root, ".local", f.Runtime)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.New("retained capacity runtime must be its existing private directory")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return nil, "", errors.New("retained capacity runtime escaped the source workspace")
	}
	r := &performanceDeployment{root: root, override: filepath.Join(dir, "compose-performance.json"), project: f.Project,
		appPort: f.Ports[0], postgresPort: f.Ports[1], metricsPort: f.Ports[2], providerPort: f.Ports[3], tracesPort: f.Ports[4]}
	if _, err := r.command(context.Background(), "version"); err != nil {
		return nil, "", err
	}
	return r, dir, nil
}

func performanceArtifactName(value, prefix string) bool {
	suffix, found := strings.CutPrefix(value, prefix)
	return found && suffix != "" && strings.Trim(suffix, "0123456789") == ""
}

func TestPerformanceRetainedFixtureOwnership(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".local", "backend-performance-runtime-123")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	valid := performanceRetainedFixture{Runtime: filepath.Base(dir), Report: "backend-performance-controlled-456",
		Project: "almanac-perf-abcdefghijklmnopqrstuvwxyz", Ports: []string{"32001", "32002", "32003", "32004", "32005"}}
	if _, actual, err := valid.deployment(root, 10); err != nil || actual != dir {
		t.Fatal("valid owned fixture rejected", err)
	}
	for _, mutate := range []func(*performanceRetainedFixture){
		func(f *performanceRetainedFixture) { f.Project = "almanac-local" },
		func(f *performanceRetainedFixture) { f.Runtime = "../backend-performance-runtime-123" },
		func(f *performanceRetainedFixture) { f.Report = "/tmp/backend-performance-controlled-456" },
		func(f *performanceRetainedFixture) { f.Ports = []string{"0", "32002", "32003", "32004", "32005"} },
		func(f *performanceRetainedFixture) { f.Ports = []string{"32001", "32001", "32003", "32004", "32005"} },
	} {
		invalid := valid
		mutate(&invalid)
		if _, _, err := invalid.deployment(root, 10); err == nil {
			t.Fatal("unowned retained fixture accepted")
		}
	}
	if _, _, err := valid.deployment(root, 20); err == nil {
		t.Fatal("unapproved fleet size accepted")
	}
	if err := os.Symlink(dir, filepath.Join(root, ".local", "backend-performance-runtime-789")); err != nil {
		t.Fatal(err)
	}
	valid.Runtime = "backend-performance-runtime-789"
	if _, _, err := valid.deployment(root, 10); err == nil {
		t.Fatal("symlinked runtime accepted")
	}
}
