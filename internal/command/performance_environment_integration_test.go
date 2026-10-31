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
)

type performanceEnvironment struct {
	GoVersion     string            `json:"go_version"`
	OS            string            `json:"os"`
	Architecture  string            `json:"architecture"`
	LogicalCPUs   int               `json:"host_logical_cpus"`
	HostMemory    int64             `json:"host_memory_bytes"`
	CPUQuota      string            `json:"host_cgroup_cpu_max"`
	MemoryLimit   string            `json:"host_cgroup_memory_max"`
	BackendSHA256 string            `json:"backend_sha256"`
	AgentSHA256   string            `json:"agent_sha256"`
	PostgreSQL    string            `json:"postgresql"`
	Settings      map[string]string `json:"postgresql_settings"`
}

func performanceEnvironmentEvidence(t *testing.T, fleet performanceFleet) performanceEnvironment {
	t.Helper()
	report := performanceEnvironment{GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH, LogicalCPUs: runtime.NumCPU(), Settings: make(map[string]string)}
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

func performanceQueryPlans(t *testing.T, fleet performanceFleet) map[string]jsontext.Value {
	t.Helper()
	db := testpostgres.Connect(t, fleet.fixture.databaseURL)
	defer closePerformanceObserver(db)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var generation, calc, kind string
	var definition, measurement, source int64
	var through, start, end, retention time.Time
	var burn int32
	if err := db.QueryRow(ctx, `SELECT g.id::text,g.calculation_version,g.definition_revision,g.measurement_revision,g.source_generation,
g.evaluated_through,b.range_start,b.range_end,g.retention_start,b.burn_window_seconds,b.kind
FROM public.almanac_slo_derived_generations g JOIN public.almanac_slo_history_blocks b ON b.generation_id=g.id
WHERE g.slo_id=$1 AND b.kind='detail' ORDER BY g.published_at DESC LIMIT 1`, fleet.objectives[0]).Scan(&generation, &calc, &definition, &measurement, &source, &through, &start, &end, &retention, &burn, &kind); err != nil {
		t.Fatal("query-plan evidence requires a real prepared history generation")
	}
	now := time.Now().UTC()
	queries := []struct {
		file, name string
		args       []any
		canonical  bool
	}{
		{"slis.sql.go", "listSlis", []any{now.Add(-400 * 24 * time.Hour), now, "", nil, time.Unix(0, 0).UTC(), "00000000-0000-0000-0000-000000000000", int32(51)}, true},
		{"derived_overview.sql.go", "operationalScopeFingerprint", []any{calc, now, nil, nil}, false},
		{"derived_projections.sql.go", "getPublishedCurrentProjection", []any{calc, now, fleet.objectives[0]}, false},
		{"derived_history.sql.go", "findPreparedSloHistory", []any{fleet.objectives[0], definition, measurement, source, through, start, end, burn, calc, retention, now, kind}, false},
		{"derived_history.sql.go", "readPreparedSloValuePage", []any{[]string{"budget_series", "samples"}, int32(0), int32(100), fleet.objectives[0], generation, now, calc}, false},
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
		var body []byte
		err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,SETTINGS,FORMAT JSON) "+sql, query.args...).Scan(&body)
		rollbackErr := tx.Rollback(ctx)
		if err != nil || rollbackErr != nil {
			t.Fatal("execute actual candidate query-plan capture failed")
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
		actualRows, valid := plan[0].Plan["Actual Rows"].(float64)
		if !valid || actualRows <= 0 || (query.name == "listSlis" && actualRows != 51) {
			t.Fatal("candidate query-plan capture omitted the required populated result")
		}
		report[query.name] = jsontext.Value(body)
	}
	return report
}
