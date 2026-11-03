//go:build integration

package command

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/testpostgres"
	"github.com/jackc/pgx/v5"
)

// The fixed capacity entry may reuse only explicitly named, owned year fixtures.
// It reruns acceptance on the candidate; a previous passing receipt is not reused.
func retainedPerformanceFixtures(t *testing.T) map[string]performanceRetainedFixture {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", ".local", "performance-resume.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var fixtures map[string]performanceRetainedFixture
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, &fixtures, json.RejectUnknownMembers(true)) != nil {
		t.Fatal("retained capacity manifest is invalid")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for size, fixture := range fixtures {
		n, err := strconv.Atoi(size)
		if err != nil || strconv.Itoa(n) != size || n != 10 {
			t.Fatal("this phase permits only the ten-SLO retained fixture")
		}
		if _, _, err := fixture.deployment(root, n); err != nil {
			t.Fatal(err)
		}
	}
	return fixtures
}

func resumePerformanceFleet(t *testing.T, ctx context.Context, size int, saved performanceRetainedFixture) performanceFleet {
	t.Helper()
	started := time.Now()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	r, dir, err := saved.deployment(root, size)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".local", saved.Report, "performance.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		t.Fatal("retained capacity report is not a bounded regular artifact")
	}
	if resolved, err := filepath.EvalSymlinks(path); err != nil || resolved != path {
		t.Fatal("retained capacity report escaped its workspace")
	}
	if info, err := os.Lstat(r.override); err != nil || !info.Mode().IsRegular() {
		t.Fatal("retained capacity override is not its original regular file")
	}
	body, err := os.ReadFile(path)
	var previous performanceControlledReport
	if err != nil || json.Unmarshal(body, &previous) != nil || previous.Result != "failed" {
		t.Fatal("retained capacity fixture has no original failed report")
	}
	var environment struct {
		Deployment performanceDeploymentEvidence `json:"deployment"`
	}
	var imported struct {
		Days int       `json:"days"`
		End  time.Time `json:"end"`
		Rows int64     `json:"canonical_rows"`
	}
	seed := "seed-365-days"
	if size == 1 {
		seed = "one-slo-365-days-import"
	}
	if json.Unmarshal(previous.Stages["environment"], &environment) != nil || environment.Deployment.Project != r.project ||
		json.Unmarshal(previous.Stages[seed], &imported) != nil || imported.Days != 365 || imported.Rows < int64(size)*365*24*60*97/100 || imported.End.IsZero() {
		t.Fatal("retained capacity report does not establish this independent year dataset")
	}
	if _, visited := previous.Stages["production-browser-365-days-0ms"]; visited {
		t.Fatal("retained capacity browser target was already visited")
	}
	app := strings.TrimSpace(string(r.output(t, "ps", "-aq", "app")))
	files, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "com.docker.compose.project.config_files"}}`, app).Output()
	if err != nil || strings.TrimSpace(string(files)) != filepath.Join(root, "compose.yaml")+","+r.override {
		t.Fatal("retained container configuration does not name this runtime directory")
	}
	state, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", app).Output()
	if err != nil {
		t.Fatal("read retained backend state failed")
	}
	r.running = strings.TrimSpace(string(state)) == "true"
	r.evidence(t)
	for _, binding := range []struct{ service, port, want string }{
		{"app", "8080/tcp", r.appPort}, {"app", "9090/tcp", r.metricsPort}, {"postgres", "5432/tcp", r.postgresPort},
		{"victoriametrics", "8428/tcp", r.providerPort}, {"victoriatraces", "10428/tcp", r.tracesPort},
	} {
		id := strings.TrimSpace(string(r.output(t, "ps", "-aq", binding.service)))
		out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .NetworkSettings.Ports}}", id).Output()
		var ports map[string][]struct{ HostIp, HostPort string }
		if err != nil || json.Unmarshal(out, &ports) != nil || len(ports[binding.port]) != 1 ||
			ports[binding.port][0].HostIp != "127.0.0.1" || ports[binding.port][0].HostPort != binding.want {
			t.Fatal("retained capacity port differs from its owned container")
		}
	}
	registerPerformanceDeploymentCleanup(t, r, dir)
	r.stop(t)
	// The normal full-capture transition remains measured after fixture setup.
	config, err := os.ReadFile(r.override)
	var override struct {
		Services map[string]map[string]jsontext.Value `json:"services"`
	}
	if err != nil || json.Unmarshal(config, &override) != nil {
		t.Fatal("read retained capacity overrides failed")
	}
	var appEnvironment map[string]string
	if json.Unmarshal(override.Services["app"]["environment"], &appEnvironment) != nil {
		t.Fatal("retained backend has no normal tracing configuration")
	}
	appEnvironment["OTEL_TRACES_SAMPLER_ARG"] = "0.01"
	encoded, err := json.Marshal(appEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	override.Services["app"]["environment"] = encoded
	config, err = json.Marshal(override)
	if err != nil || os.WriteFile(r.override, config, 0o600) != nil {
		t.Fatal("restore retained setup sampling failed")
	}
	r.run(t, ctx, 5*time.Minute, "build", "init")
	r.run(t, ctx, 2*time.Minute, "run", "--rm", "--no-deps", "migrate")
	r.appID, r.imageID = "", ""
	r.start(t)
	f := openPerformanceComposeFixture(t, r, dir, false)
	fleet := performanceFleet{ctx: ctx, fixture: f, historyEnd: imported.End, traceURL: "http://127.0.0.1:" + r.tracesPort, reserveDetail: size != 1, resumed: true}
	configurePerformanceTracing(t, fleet.traceURL)
	db := testpostgres.Connect(t, f.databaseURL)
	defer closePerformanceObserver(db)
	fleet.slis = retainedPerformanceIDs(t, ctx, db, "SELECT id::text FROM public.almanac_slis WHERE name LIKE 'performance-%' ORDER BY name")
	fleet.objectives = retainedPerformanceIDs(t, ctx, db, "SELECT id::text FROM public.almanac_slos WHERE name LIKE 'performance-%' ORDER BY name")
	fleet.datasources = retainedPerformanceIDs(t, ctx, db, "SELECT id::text FROM public.almanac_datasources WHERE name LIKE 'performance-metrics-%' ORDER BY name")
	services := retainedPerformanceIDs(t, ctx, db, "SELECT id::text FROM public.almanac_services WHERE name='performance-service'")
	agents := retainedPerformanceIDs(t, ctx, db, "SELECT id::text FROM public.almanac_agents WHERE name LIKE 'performance-agent-%' ORDER BY name")
	readers := retainedPerformanceIDs(t, ctx, db, "SELECT id::text FROM public.almanac_users WHERE login LIKE 'performance-reader-%' ORDER BY login")
	if len(fleet.slis) != size || len(fleet.objectives) != size || len(fleet.datasources) != min(size, 10) || len(agents) != min(size, 10) || len(services) != 1 || len(readers) != 10 {
		t.Fatal("retained capacity identities differ from their independent fleet")
	}
	fleet.serviceID = services[0]
	refreshRetainedBrowserIdentity(t, &fleet, db)
	for index, id := range agents {
		agentDir := filepath.Join(dir, fmt.Sprintf("agent-%02d", index))
		fleet.agents = append(fleet.agents, connectedAgentJourney{id: id, profile: filepath.Join(agentDir, "sli-agent-profile", "profile.json"), passwordPath: filepath.Join(agentDir, "sli-agent-password")})
	}
	for index := range fleet.agents {
		reauthenticatePerformanceManager(t, ctx, fleet, index)
		renewPerformanceDatasource(t, fleet, index)
		agentDir := filepath.Dir(fleet.agents[index].passwordPath)
		fleet.agents[index].done, fleet.agents[index].result = startSLIJourneyAgent(t, filepath.Join(dir, "almanac-agent"), filepath.Join(agentDir, "sli-agent-token"), f.baseURL, f.certFile)
	}
	for index, identity := range readers {
		profile := filepath.Join(dir, fmt.Sprintf("reader-%02d.json", index))
		logout := exec.CommandContext(ctx, f.binary, "auth", "logout", "--profile-file="+profile)
		logout.Stdout, logout.Stderr = io.Discard, io.Discard
		if err := logout.Run(); err != nil {
			t.Fatal("sign out prior retained fixture reader failed")
		}
		fleet.readers = append(fleet.readers, loginPerformanceReader(t, fleet, index, identity))
	}
	fleet.setup = time.Since(started)
	t.Logf("reused retained year dataset project=%s original_report=%s; acceptance reruns on this candidate", r.project, saved.Report)
	return fleet
}

func refreshRetainedBrowserIdentity(t *testing.T, fleet *performanceFleet, db *pgx.Conn) {
	t.Helper()
	index := len(fleet.objectives) - 1
	previous := fleet.objectives[index]
	f := fleet.fixture
	f.backend.stop(t)
	f.backend.deployment.requireStoppedDatabase(t, f.databaseURL)
	tx, err := db.Begin(fleet.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var replacement string
	if err := tx.QueryRow(fleet.ctx, `SELECT gen_random_uuid()::text`).Scan(&replacement); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("performance-%03d", index)
	// Expired responses cannot prove an objective was never visited. Every reuse
	// provisions a fresh identity with its exact definition and retained series.
	tag, err := tx.Exec(fleet.ctx, `UPDATE public.almanac_slos SET name='retired-'||id::text WHERE id=$1 AND name=$2`, previous, name)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatal("retained browser identity differs from its fixed fleet")
	}
	if _, err := tx.Exec(fleet.ctx, `INSERT INTO public.almanac_slos(id,name,display_name,version,definition_revision,created_at,updated_at)
SELECT $2::uuid,$3,display_name,version,definition_revision,created_at,updated_at FROM public.almanac_slos WHERE id=$1`, previous, replacement, name); err != nil {
		t.Fatal("provision unvisited retained fixture identity failed", err)
	}
	if _, err := tx.Exec(fleet.ctx, `INSERT INTO public.almanac_slo_definitions(slo_id,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at)
SELECT $2::uuid,revision,service_id,sli_id,measurement_revision,target,rolling_window_seconds,cadence_seconds,activated_at,evaluation_valid_from,historical_requested_by,historical_requested_at FROM public.almanac_slo_definitions WHERE slo_id=$1`, previous, replacement); err != nil {
		t.Fatal("preserve retained fixture definition failed", err)
	}
	for _, table := range []string{"almanac_slo_series_points", "almanac_slo_series_tiles", "almanac_slo_series_tile_work", "almanac_slo_series_repair"} {
		if _, err := tx.Exec(fleet.ctx, "UPDATE public."+table+" SET slo_id=$2 WHERE slo_id=$1", previous, replacement); err != nil {
			t.Fatal("preserve retained fixture series failed", err)
		}
	}
	for _, table := range []string{"almanac_slo_current_projections", "almanac_slo_current_failures", "almanac_slo_history_jobs", "almanac_slo_display_blocks", "almanac_slo_derived_generations", "almanac_slo_definitions", "almanac_slos"} {
		column := "slo_id"
		if table == "almanac_slos" {
			column = "id"
		}
		if _, err := tx.Exec(fleet.ctx, "DELETE FROM public."+table+" WHERE "+column+"=$1", previous); err != nil {
			t.Fatal("remove previous retained fixture identity failed", err)
		}
	}
	if err := tx.Commit(fleet.ctx); err != nil {
		t.Fatal("commit unvisited retained fixture identity failed", err)
	}
	fleet.objectives[index] = replacement
	f.backend.start(t)
	t.Logf("reserved unvisited retained objective old=%s new=%s; canonical observations unchanged", previous, replacement)
}

func retainedPerformanceIDs(t *testing.T, ctx context.Context, db *pgx.Conn, statement string) []string {
	t.Helper()
	rows, err := db.Query(ctx, statement)
	if err != nil {
		t.Fatal("read retained capacity identities failed", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil || !validCLIUUID(id) {
			t.Fatal("retained capacity identity is invalid")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func renewPerformanceDatasource(t *testing.T, fleet performanceFleet, index int) {
	t.Helper()
	f, agent := fleet.fixture, fleet.agents[index]
	get := exec.CommandContext(fleet.ctx, f.binary, "datasources", "get", "--profile-file="+agent.profile, "--datasource-id="+fleet.datasources[index], "--output=json")
	get.Stderr = io.Discard
	body, err := get.Output()
	var existing struct {
		Result struct {
			ETag string `json:"etag"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(body, &existing) != nil || !validDatasourceETag(existing.Result.ETag, fleet.datasources[index]) {
		t.Fatal("read retained datasource version failed")
	}
	body, err = os.ReadFile(filepath.Join(filepath.Dir(agent.passwordPath), "sli-journey-datasource.json"))
	var input map[string]any
	if err != nil || json.Unmarshal(body, &input) != nil || input["agent_id"] != agent.id {
		t.Fatal("retained datasource input differs from its assigned agent")
	}
	input["endpoint"], input["tls"] = f.sourceEndpoint, map[string]any{"insecure_skip_verify": false, "ca_pem": string(f.sourceCA)}
	body, err = json.Marshal(input)
	path := filepath.Join(filepath.Dir(agent.passwordPath), "resumed-datasource.json")
	if err != nil || os.WriteFile(path, body, 0o600) != nil {
		t.Fatal("encode renewed capacity source trust failed")
	}
	update := exec.CommandContext(fleet.ctx, f.binary, "datasources", "update", "--profile-file="+agent.profile, "--datasource-id="+fleet.datasources[index], "--if-match="+existing.Result.ETag, "--input-file="+path, "--output=json")
	update.Stdout, update.Stderr = io.Discard, io.Discard
	if err := update.Run(); err != nil {
		t.Fatal("renew capacity source through authenticated management API failed")
	}
}
