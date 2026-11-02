//go:build integration

package command

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only the separately approved long-workload entry can reach this deployment.
// It uses the normal Compose initializer, migrations, images and private volumes.
type performanceDeploymentEvidence struct {
	Project           string `json:"project"`
	AppContainer      string `json:"app_container"`
	PostgresContainer string `json:"postgres_container"`
	Image             string `json:"app_image_id"`
	BackendSHA        string `json:"backend_sha256"`
	User              string `json:"app_user"`
	ReadOnly          bool   `json:"app_read_only"`
}

type performanceSamplingTransition struct {
	Before, After     performanceDeploymentEvidence
	Shutdown, Startup time.Duration
	RootSampling      float64   `json:"root_sampling"`
	ReadyAt           time.Time `json:"ready_at"`
}

func enablePerformanceFullTracing(t *testing.T, f objectiveJourneyFixture) performanceSamplingTransition {
	t.Helper()
	r := f.backend.deployment
	if r == nil {
		t.Fatal("full capture requires owned normal deployment")
	}
	report := performanceSamplingTransition{Before: r.evidence(t)}
	body, err := os.ReadFile(r.override)
	var override map[string]any
	if err != nil || json.Unmarshal(body, &override) != nil {
		t.Fatal("read owned sampling override failed")
	}
	services, ok := override["services"].(map[string]any)
	if !ok {
		t.Fatal("owned sampling override has no services")
	}
	app, ok := services["app"].(map[string]any)
	if !ok {
		t.Fatal("owned sampling override has no backend")
	}
	environment, ok := app["environment"].(map[string]any)
	if !ok || environment["OTEL_TRACES_SAMPLER_ARG"] != "0.01" {
		t.Fatal("owned backend sampling did not match bulk setup")
	}
	environment["OTEL_TRACES_SAMPLER_ARG"] = "1"
	body, err = json.Marshal(override)
	if err != nil {
		t.Fatal("encode owned full capture override failed")
	}
	report.Shutdown = r.stop(t)
	if err := os.WriteFile(r.override, body, 0o600); err != nil {
		t.Fatal("write owned full capture override failed")
	}
	r.appID = "" // Compose replaces only this stopped, owned backend for changed env.
	report.Startup = r.start(t)
	report.After = r.evidence(t)
	if report.After.AppContainer == report.Before.AppContainer || report.After.PostgresContainer != report.Before.PostgresContainer || report.After.Image != report.Before.Image || report.After.BackendSHA != report.Before.BackendSHA {
		t.Fatal("sampling transition changed data/image ownership or did not replace backend")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{range .Config.Env}}{{if eq . "OTEL_TRACES_SAMPLER_ARG=1"}}full{{end}}{{end}}`, r.appID).Output()
	if err != nil || strings.TrimSpace(string(output)) != "full" {
		t.Fatal("normal acceptance backend is not fully sampled")
	}
	response, err := f.apiClient.Get(f.baseURL + "/readyz")
	if err != nil {
		t.Fatal("full capture backend host HTTPS unavailable")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("full capture backend host HTTPS not ready")
	}
	report.RootSampling = 1
	report.ReadyAt = time.Now().UTC()
	return report
}

func performancePorts(t *testing.T) ([]string, func()) {
	t.Helper()
	var listeners []net.Listener
	release := func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}
	t.Cleanup(release)
	var ports []string
	for range 5 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal("reserve owned performance deployment port failed")
		}
		listeners = append(listeners, listener)
		_, port, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			t.Fatal("read owned performance deployment port failed")
		}
		ports = append(ports, port)
	}
	return ports, release
}

func (r *performanceDeployment) run(t *testing.T, parent context.Context, budget time.Duration, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	command, err := r.command(ctx, args...)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		t.Fatal("owned performance Compose operation failed")
	}
}

func (r *performanceDeployment) output(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command, err := r.command(ctx, args...)
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.Output()
	if err != nil || len(output) > 64*1024 {
		t.Fatal("read bounded owned deployment metadata failed")
	}
	return output
}

func (r *performanceDeployment) evidence(t *testing.T) performanceDeploymentEvidence {
	t.Helper()
	app := strings.TrimSpace(string(r.output(t, "ps", "-aq", "app")))
	postgres := strings.TrimSpace(string(r.output(t, "ps", "-q", "postgres")))
	if app == "" || postgres == "" || r.appID != "" && app != r.appID || r.postgresID != "" && postgres != r.postgresID {
		t.Fatal("owned performance deployment container identity changed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	format := `{{.Image}}|{{.Config.User}}|{{.HostConfig.ReadonlyRootfs}}|{{index .Config.Labels "com.docker.compose.project"}}|{{.State.Running}}`
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", format, app).Output()
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	if err != nil || len(fields) != 5 || !strings.HasPrefix(fields[0], "sha256:") || fields[1] != "10001:10001" || fields[2] != "true" || fields[3] != r.project || fields[4] != fmt.Sprint(r.running) || r.imageID != "" && fields[0] != r.imageID {
		t.Fatal("performance backend differs from normal owned container deployment")
	}
	r.appID, r.postgresID, r.imageID = app, postgres, fields[0]
	return performanceDeploymentEvidence{Project: r.project, AppContainer: app, PostgresContainer: postgres, Image: r.imageID, BackendSHA: r.digest, User: fields[1], ReadOnly: true}
}

func (r *performanceDeployment) requireStoppedDatabase(t *testing.T, databaseURL string) {
	t.Helper()
	if r.running || databaseURL != r.databaseURL || !strings.HasPrefix(r.project, "almanac-perf-") {
		t.Fatal("offline performance mutation requires its stopped owned deployment")
	}
	r.evidence(t)
}

func (r *performanceDeployment) start(t *testing.T) time.Duration {
	t.Helper()
	if r.running {
		t.Fatal("owned performance backend already running")
	}
	started := time.Now()
	r.run(t, t.Context(), 2*time.Minute, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "90", "app")
	r.running = true
	r.evidence(t)
	return time.Since(started)
}

func (r *performanceDeployment) stop(t *testing.T) time.Duration {
	t.Helper()
	if !r.running {
		return 0
	}
	started := time.Now()
	r.run(t, t.Context(), 45*time.Second, "stop", "app")
	r.running = false
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{.State.Running}}|{{.State.ExitCode}}|{{.State.OOMKilled}}`, r.appID).Output()
	if err != nil || strings.TrimSpace(string(output)) != "false|0|false" {
		t.Fatal("owned performance backend did not stop cleanly")
	}
	return time.Since(started)
}

func startPerformanceComposeFixture(t *testing.T) objectiveJourneyFixture {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("resolve normal performance deployment source failed")
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".local"), "backend-performance-runtime-")
	if err != nil {
		t.Fatal("create protected performance runtime directory failed")
	}
	ports, releasePorts := performancePorts(t)
	r := &performanceDeployment{root: root, project: "almanac-perf-" + strings.ToLower(rand.Text()), override: filepath.Join(dir, "compose-performance.json"),
		appPort: ports[0], postgresPort: ports[1], metricsPort: ports[2], providerPort: ports[3], tracesPort: ports[4]}
	override := map[string]any{"services": map[string]any{
		"postgres": map[string]any{"ports": []string{"127.0.0.1:" + r.postgresPort + ":5432"}},
		"app": map[string]any{"ports": []string{"127.0.0.1:" + r.metricsPort + ":9090"}, "environment": map[string]string{
			"ALMANAC_INITIAL_ADMIN_LOGIN": "sli-owner", "ALMANAC_METRICS_LISTEN": "0.0.0.0:9090", "ALMANAC_ENVIRONMENT": "acceptance",
			"OTEL_SDK_DISABLED": "false", "OTEL_TRACES_SAMPLER": "parentbased_traceidratio", "OTEL_TRACES_SAMPLER_ARG": "0.01",
		}},
	}}
	body, err := json.Marshal(override)
	if err != nil || os.WriteFile(r.override, body, 0o600) != nil {
		t.Fatal("write owned performance deployment overrides failed")
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved failed performance deployment project=%s protected_runtime=%s", r.project, dir)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command, err := r.command(ctx, "down", "--volumes", "--remove-orphans")
		if err != nil {
			t.Error(err)
			return
		}
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Run(); err != nil {
			t.Error("cleanup owned performance deployment failed")
		}
		remove := exec.CommandContext(ctx, "docker", "image", "rm", r.project+":dev")
		remove.Stdout, remove.Stderr = io.Discard, io.Discard
		if err := remove.Run(); err != nil {
			t.Error("cleanup owned performance image failed")
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Error("cleanup owned performance runtime directory failed")
		}
	})
	r.run(t, t.Context(), 5*time.Minute, "build")
	releasePorts()
	r.run(t, t.Context(), 2*time.Minute, "up", "-d", "--wait", "--wait-timeout", "90")
	r.running = true
	r.evidence(t)
	certificate := r.output(t, "exec", "-T", "app", "cat", "/run/almanac-secrets/tls.crt")
	block, rest := pem.Decode(certificate)
	if block == nil || len(rest) != 0 {
		t.Fatal("normal performance deployment omitted its certificate")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("normal performance deployment certificate is invalid")
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	baseURL := "https://localhost:" + r.appPort
	response, err := client.Get(baseURL + "/readyz")
	if err != nil {
		t.Fatal("normal performance deployment host HTTPS unavailable")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("normal performance deployment host HTTPS not ready")
	}
	certFile := filepath.Join(dir, "backend-cert.pem")
	if err := os.WriteFile(certFile, certificate, 0o600); err != nil {
		t.Fatal("save owned deployment public certificate failed")
	}
	internalURL := r.output(t, "exec", "-T", "app", "cat", "/run/almanac-secrets/database-url")
	database, err := url.Parse(strings.TrimSpace(string(internalURL)))
	clear(internalURL)
	if err != nil || database.Host != "postgres:5432" || database.Path != "/almanac" || database.User == nil || database.User.Username() != "almanac" {
		t.Fatal("normal performance database secret has unexpected identity")
	}
	database.Host = "127.0.0.1:" + r.postgresPort
	r.databaseURL = database.String()
	binary := filepath.Join(dir, "almanac")
	for _, name := range []string{"almanac", "almanac-agent"} {
		r.run(t, t.Context(), 30*time.Second, "cp", "app:/usr/local/bin/"+name, filepath.Join(dir, name))
	}
	candidate, err := os.Open(binary)
	if err != nil {
		t.Fatal("identify normal deployed executable failed")
	}
	digest := sha256.New()
	_, hashErr := io.Copy(digest, candidate)
	closeErr := candidate.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatal("hash normal deployed executable failed")
	}
	r.digest = hex.EncodeToString(digest.Sum(nil))
	deployed := strings.Fields(string(r.output(t, "exec", "-T", "app", "sha256sum", "/usr/local/bin/almanac")))
	if len(deployed) != 2 || deployed[0] != r.digest {
		t.Fatal("copied executable differs from normal deployed backend")
	}
	initial := r.output(t, "exec", "-T", "app", "cat", "/run/almanac-secrets/initial-admin-password")
	initialFile, permanentFile := filepath.Join(dir, "initial-password"), filepath.Join(dir, "permanent-password")
	password := "SLI journey permanent phrase 825!"
	if os.WriteFile(initialFile, initial, 0o600) != nil || os.WriteFile(permanentFile, []byte(password), 0o600) != nil {
		t.Fatal("write protected deployment onboarding input failed")
	}
	clear(initial)
	setup := exec.CommandContext(t.Context(), binary, "setup", "--backend-url="+baseURL, "--initial-admin-login=sli-owner", "--initial-admin-password-file="+initialFile,
		"--new-password-file="+permanentFile, "--ca-file="+certFile, "--output=json")
	setup.Stdout, setup.Stderr = io.Discard, io.Discard
	if err := setup.Run(); err != nil {
		t.Fatal("normal deployment administrator onboarding failed")
	}
	sourceCA, sourceKey, err := datasourceTestCertificate()
	if err != nil {
		t.Fatal("create independent performance source TLS identity failed")
	}
	sourceCertFile, sourceKeyFile := filepath.Join(dir, "source-cert.pem"), filepath.Join(dir, "source-key.pem")
	if os.WriteFile(sourceCertFile, sourceCA, 0o644) != nil || os.WriteFile(sourceKeyFile, sourceKey, 0o600) != nil {
		t.Fatal("write independent performance source TLS identity failed")
	}
	sourceEndpoint := startVictoriaMetrics(t, dir, sourceCertFile, sourceKeyFile, sourceCA)
	logs := newRuntimeLog()
	backend := &objectiveJourneyBackend{binary: binary, client: client, baseURL: baseURL, logs: logs, deployment: r}
	return objectiveJourneyFixture{databaseURL: r.databaseURL, binary: binary, dir: dir, baseURL: baseURL, certFile: certFile, password: password,
		certificate: certificate, sourceCA: sourceCA, sourceEndpoint: sourceEndpoint, apiClient: client, logs: logs, backend: backend,
		metricsURL: fmt.Sprintf("http://127.0.0.1:%s/metrics", r.metricsPort)}
}
