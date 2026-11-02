//go:build integration

package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/testpostgres"
	"github.com/jackc/pgx/v5/pgconn"
)

type performanceEnvironment struct {
	GoVersion     string                        `json:"go_version"`
	OS            string                        `json:"os"`
	Architecture  string                        `json:"architecture"`
	LogicalCPUs   int                           `json:"host_logical_cpus"`
	HostMemory    int64                         `json:"host_memory_bytes"`
	CPUQuota      string                        `json:"host_cgroup_cpu_max"`
	MemoryLimit   string                        `json:"host_cgroup_memory_max"`
	BackendSHA256 string                        `json:"backend_sha256"`
	AgentSHA256   string                        `json:"agent_sha256"`
	PostgreSQL    string                        `json:"postgresql"`
	Settings      map[string]string             `json:"postgresql_settings"`
	Deployment    performanceDeploymentEvidence `json:"deployment"`
}

func performanceEnvironmentEvidence(t *testing.T, fleet performanceFleet) performanceEnvironment {
	t.Helper()
	report := performanceEnvironment{GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH, LogicalCPUs: runtime.NumCPU(), Settings: make(map[string]string)}
	if fleet.fixture.backend.deployment == nil {
		t.Fatal("capacity evidence requires normal container deployment")
	}
	report.Deployment = fleet.fixture.backend.deployment.evidence(t)
	for path, destination := range map[string]*string{fleet.fixture.binary: &report.BackendSHA256, filepath.Join(filepath.Dir(fleet.fixture.binary), "almanac-agent"): &report.AgentSHA256} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal("open candidate executable for environment evidence failed")
		}
		digest := sha256.New()
		_, hashErr := io.Copy(digest, file)
		closeErr := file.Close()
		if hashErr != nil || closeErr != nil {
			t.Fatal("identify candidate executable for environment evidence failed")
		}
		*destination = hex.EncodeToString(digest.Sum(nil))
	}
	if body, err := os.ReadFile("/proc/meminfo"); err == nil {
		for line := range strings.SplitSeq(string(body), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) == 3 && fields[2] == "kB" {
					kilobytes, err := strconv.ParseInt(fields[1], 10, 64)
					if err == nil {
						report.HostMemory = kilobytes * 1024
					}
				}
			}
		}
	}
	for path, destination := range map[string]*string{"/sys/fs/cgroup/cpu.max": &report.CPUQuota, "/sys/fs/cgroup/memory.max": &report.MemoryLimit} {
		*destination = "unavailable"
		if body, err := os.ReadFile(path); err == nil && len(body) < 128 {
			*destination = strings.TrimSpace(string(body))
		}
	}
	db := testpostgres.Connect(t, fleet.fixture.databaseURL)
	defer closePerformanceObserver(db)
	if err := db.QueryRow(t.Context(), `SELECT version()`).Scan(&report.PostgreSQL); err != nil {
		t.Fatal("read performance PostgreSQL version failed")
	}
	rows, err := db.Query(t.Context(), `SELECT name,setting||coalesce(' '||unit,'') FROM pg_settings
WHERE name=ANY($1::text[]) ORDER BY name`, []string{"max_connections", "shared_buffers", "work_mem", "maintenance_work_mem", "effective_cache_size", "jit", "synchronous_commit", "fsync", "full_page_writes", "max_parallel_workers_per_gather", "max_worker_processes", "max_wal_size", "checkpoint_timeout"})
	if err != nil {
		t.Fatal("read fixed performance PostgreSQL settings failed")
	}
	defer rows.Close()
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			t.Fatal("decode fixed performance PostgreSQL settings failed")
		}
		report.Settings[name] = value
	}
	if rows.Err() != nil || len(report.Settings) != 13 || report.HostMemory <= 0 {
		t.Fatal("performance environment evidence is incomplete")
	}
	return report
}

// Extract the exact generated SQL used by the candidate, rather than maintaining
// a separately optimized approximation just for EXPLAIN evidence.
func performanceGeneratedSQL(file, name string) (string, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "storage", "dbgen", file), nil, 0)
	if err != nil {
		return "", errors.New("read generated candidate query failed")
	}
	var result string
	ast.Inspect(parsed, func(node ast.Node) bool {
		value, ok := node.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || value.Names[0].Name != name || len(value.Values) != 1 {
			return true
		}
		literal, ok := value.Values[0].(*ast.BasicLit)
		if ok && literal.Kind == token.STRING {
			result, _ = strconv.Unquote(literal.Value)
		}
		return false
	})
	if result == "" {
		return "", errors.New("candidate generated query was not found")
	}
	return result, nil
}

func performanceQueryPlans(t *testing.T, fleet performanceFleet, analyze bool) map[string]jsontext.Value {
	t.Helper()
	db := testpostgres.Connect(t, fleet.fixture.databaseURL)
	defer closePerformanceObserver(db)
	ctx, cancel := context.WithTimeout(fleet.ctx, 20*time.Second)
	defer cancel()
	now := time.Now().UTC()
	// Bind-only placeholders are not populated-data or query-performance evidence.
	generation, calc, kind := "00000000-0000-0000-0000-000000000000", "binding-preflight", "retained_detail"
	definition, measurement, source := int64(1), int64(1), int64(0)
	through := now.Truncate(time.Minute)
	start, end, retention := through.Add(-7*24*time.Hour), through, through.Add(-365*24*time.Hour)
	burn := int32(3600)
	if analyze {
		if err := db.QueryRow(ctx, `SELECT g.id::text,g.calculation_version,g.definition_revision,g.measurement_revision,g.source_generation,
g.evaluated_through,b.range_start,b.range_end,g.retention_start,b.burn_window_seconds,b.kind
FROM public.almanac_slo_derived_generations g JOIN public.almanac_slo_history_blocks b ON b.generation_id=g.id
WHERE g.slo_id=$1 AND b.kind='retained_detail' ORDER BY g.published_at DESC LIMIT 1`, fleet.objectives[0]).Scan(&generation, &calc, &definition, &measurement, &source, &through, &start, &end, &retention, &burn, &kind); err != nil {
			t.Fatal("query-plan evidence requires a real prepared history generation")
		}
	}
	var tileStarts, tileEnds []time.Time
	var resolutions []int32
	firstDay := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	if firstDay.Before(start) {
		firstDay = firstDay.Add(24 * time.Hour)
	}
	for day := firstDay; !day.Add(24 * time.Hour).After(end); day = day.Add(24 * time.Hour) {
		tileStarts, tileEnds = append(tileStarts, day), append(tileEnds, day.Add(24*time.Hour))
		resolutions = append(resolutions, 86400)
	}
	const sliFetchLimit = 21
	queries := []struct {
		file, name string
		args       []any
		canonical  bool
	}{
		{"slis.sql.go", "listSlis", []any{now.Add(-365 * 24 * time.Hour), now, "", nil, time.Unix(0, 0).UTC(), "00000000-0000-0000-0000-000000000000", int32(sliFetchLimit)}, true},
		{"derived_overview.sql.go", "operationalScopeFingerprint", []any{calc, now, nil, nil}, false},
		{"derived_projections.sql.go", "getPublishedCurrentProjection", []any{calc, now, fleet.objectives[0]}, false},
		{"derived_history.sql.go", "findPreparedSloHistory", []any{fleet.objectives[0], definition, measurement, source, through, start, end, burn, calc, retention, now, kind}, false},
		{"derived_history.sql.go", "readPreparedSloValuePage", []any{[]string{"budget_series", "samples"}, int32(0), int32(100), fleet.objectives[0], generation, now, calc}, false},
		{"retained_read.sql.go", "readRetainedParts", []any{fleet.objectives[0], definition, tileStarts, tileEnds, resolutions, false, measurement, calc}, false},
		{"retained_read.sql.go", "readRetainedEdgePoints", []any{fleet.objectives[0], definition, measurement, calc, end.Add(-time.Minute), end}, false},
	}
	report := make(map[string]jsontext.Value)
	for _, query := range queries {
		sql, err := performanceGeneratedSQL(query.file, query.name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal("begin candidate query-plan capture failed")
		}
		for _, setting := range []string{`SET LOCAL statement_timeout='2s'`, `SET LOCAL lock_timeout='250ms'`, `SET LOCAL jit=off`} {
			if _, err := tx.Exec(ctx, setting); err != nil {
				t.Fatal("apply ordinary interactive query-plan budgets failed")
			}
		}
		if strings.HasPrefix(query.name, "readRetained") {
			if _, err := tx.Exec(ctx, `SET LOCAL work_mem='32MB'`); err != nil {
				t.Fatal("apply retained detail query workspace failed")
			}
		}
		var body []byte
		explain := "EXPLAIN (SETTINGS,FORMAT JSON) "
		if analyze {
			explain = "EXPLAIN (ANALYZE,BUFFERS,SETTINGS,FORMAT JSON) "
		}
		err = tx.QueryRow(ctx, explain+sql, query.args...).Scan(&body)
		rollbackErr := tx.Rollback(ctx)
		if err != nil || rollbackErr != nil {
			code := "client"
			if failure, ok := errors.AsType[*pgconn.PgError](err); ok {
				code = failure.Code
			}
			t.Fatalf("query-plan %s failed: error_type=%T sqlstate=%s rollback_failed=%t", query.name, err, code, rollbackErr != nil)
		}
		if !query.canonical && (strings.Contains(string(body), `"Relation Name": "almanac_sli_coverage`) || strings.Contains(string(body), `"Relation Name": "almanac_sli_observation_values`)) {
			t.Fatal("ready derived query plan scanned canonical retention")
		}
		var plan []struct {
			Plan map[string]any `json:"Plan"`
		}
		if json.Unmarshal(body, &plan) != nil || len(plan) != 1 {
			t.Fatal("candidate query-plan capture omitted real result rows")
		}
		if analyze {
			actualRows, valid := plan[0].Plan["Actual Rows"].(float64)
			if !valid || actualRows <= 0 || (query.name == "listSlis" && actualRows != float64(min(sliFetchLimit, len(fleet.slis)))) {
				t.Fatal("candidate query-plan capture omitted the required populated result")
			}
		}
		report[query.name] = jsontext.Value(body)
	}
	return report
}
