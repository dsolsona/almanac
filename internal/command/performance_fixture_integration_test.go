//go:build integration

package command

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/cliprofile"
	"github.com/dsolsona/almanac/internal/testpostgres"
	"github.com/jackc/pgx/v5"
)

// No Test entry deliberately: this fleet belongs to the bounded long-workload
// gate, not the ordinary two-minute integration catalog. All external resources
// are owned by the existing isolated executable fixture and its cleanup stack.
type performanceFleet struct {
	ctx        context.Context
	fixture    objectiveJourneyFixture
	agents     []connectedAgentJourney
	readers    []performanceReader
	slis       []string
	objectives []string
	setup      time.Duration
	historyEnd time.Time
}

func newPerformanceFleet(t *testing.T, ctx context.Context) performanceFleet {
	t.Helper()
	started := time.Now()
	f := startObjectiveJourneyFixtureWithMetrics(t, true)
	fleet := performanceFleet{ctx: ctx, fixture: f, historyEnd: started.UTC().Truncate(time.Hour)}
	for index := range 10 {
		if ctx.Err() != nil {
			t.Fatal("controlled performance fleet setup cancelled")
		}
		dir := filepath.Join(f.dir, fmt.Sprintf("agent-%02d", index))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal("create isolated performance agent directory failed")
		}
		agent := runNamedConnectedSLIAgentJourney(t, f.databaseURL, f.binary, dir, f.baseURL, f.certFile, f.password, fmt.Sprintf("performance-agent-%02d", index))
		fleet.agents = append(fleet.agents, agent)
		datasourceID := createSLIJourneyDatasourceWithCadence(t, f.binary, dir, agent, f.sourceEndpoint, f.sourceCA, fmt.Sprintf("performance-metrics-%02d", index), 60)
		for offset := range 10 {
			name := fmt.Sprintf("performance-%03d", index*10+offset)
			var measurement api.Sli
			performanceCreateCLI(t, f, agent.profile, "slis", name, map[string]any{
				"name": name, "display_name": name, "datasource_id": datasourceID,
				"measurement_kind": "event_counts", "component_mode": "interval_count", "good_query": "vector(99)", "total_query": "vector(100)",
			}, &measurement, "sli")
			if !validCLIUUID(measurement.Id.String()) {
				t.Fatal("performance fleet SLI response has no identity")
			}
			fleet.slis = append(fleet.slis, measurement.Id.String())
		}
	}
	var service api.Service
	profile := fleet.agents[0].profile
	performanceCreateCLI(t, f, profile, "services", "performance-service", map[string]string{"name": "performance-service", "display_name": "Performance service"}, &service, "service")
	for index, sliID := range fleet.slis {
		// Ten objectives share measurements with the first ten. All 100 SLIs
		// continue collecting, so sharing cannot reduce the ingestion workload.
		if index >= 90 {
			sliID = fleet.slis[index-90]
		}
		name := fmt.Sprintf("performance-%03d", index)
		var objective api.Slo
		performanceCreateCLI(t, f, profile, "slos", name, map[string]any{
			"name": name, "display_name": name, "service_id": service.Id.String(), "sli_id": sliID,
			"target": "0.995", "rolling_window_seconds": 28 * 86400,
		}, &objective, "slo")
		if !validCLIUUID(objective.Id.String()) {
			t.Fatal("performance fleet SLO response has no identity")
		}
		fleet.objectives = append(fleet.objectives, objective.Id.String())
	}
	// Fixture provisioning copies the already established real password verifier;
	// each distinct user still authenticates normally over candidate HTTPS. It
	// creates neither sessions directly nor a bypass of permission enforcement.
	db := testpostgres.Connect(t, f.databaseURL)
	defer closePerformanceObserver(db)
	for index := range performanceUsers {
		login := fmt.Sprintf("performance-reader-%02d", index)
		var identity string
		if err := db.QueryRow(t.Context(), `INSERT INTO public.almanac_users
(login,password_verifier,role,enabled,must_change_password,created_at,updated_at)
SELECT $1,password_verifier,'viewer',true,false,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
FROM public.almanac_users WHERE login='sli-owner' RETURNING id::text`, login).Scan(&identity); err != nil {
			t.Fatal("provision distinct performance reader failed")
		}
		readerProfile := filepath.Join(f.dir, fmt.Sprintf("reader-%02d.json", index))
		command := exec.CommandContext(t.Context(), f.binary, "auth", "login", "--profile-file="+readerProfile, "--backend-url="+f.baseURL,
			"--login="+login, "--password-file="+fleet.agents[0].passwordPath, "--ca-file="+f.certFile)
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Run(); err != nil {
			t.Fatal("authenticate distinct performance reader failed")
		}
		locked, err := cliprofile.Open(t.Context(), readerProfile, false)
		if err != nil {
			t.Fatal("open protected performance reader profile failed")
		}
		record, exists, loadErr := locked.Load()
		closeErr := locked.Close()
		if loadErr != nil || closeErr != nil || !exists || record.Session == "" {
			t.Fatal("read protected performance reader session failed")
		}
		fleet.readers = append(fleet.readers, performanceReader{Identity: identity, Client: f.apiClient,
			Request: func(ctx context.Context, path string) (*http.Request, error) {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+path, nil)
				if err == nil {
					request.AddCookie(&http.Cookie{Name: "__Host-almanac-session", Value: record.Session})
				}
				return request, err
			}})
	}
	fleet.setup = time.Since(started)
	return fleet
}

type performanceHistorySetup struct {
	Days            int           `json:"days"`
	Start           time.Time     `json:"start"`
	End             time.Time     `json:"end"`
	NominalSlots    int64         `json:"nominal_slots"`
	CanonicalRows   int64         `json:"canonical_rows"`
	GapSlots        int64         `json:"gap_slots"`
	KnownEmptyRows  int64         `json:"known_empty_rows"`
	ZeroTrafficRows int64         `json:"zero_traffic_rows"`
	Setup           time.Duration `json:"setup_ns"`
	DatabaseBytes   int64         `json:"database_bytes"`
	WALBytes        string        `json:"wal_bytes"`
}

func seedPerformanceHistory(t *testing.T, fleet performanceFleet, days int) performanceHistorySetup {
	t.Helper()
	if (days != 56 && days != 400) || len(fleet.slis) != 100 || len(fleet.agents) != 10 || len(fleet.objectives) != 100 {
		t.Fatal("historical performance fixture must use the accepted fleet and 56/400-day bounds")
	}
	started := time.Now()
	f := fleet.fixture
	db := testpostgres.Connect(t, f.databaseURL)
	defer closePerformanceObserver(db)
	// Historical records carry real lease/agent/configuration provenance from
	// this candidate's live fleet, rather than fabricated foreign key bypasses.
	for {
		var count int
		if err := db.QueryRow(t.Context(), `SELECT count(DISTINCT sli_id) FROM public.almanac_sli_coverage WHERE sli_id=ANY($1::uuid[]) AND outcome='observed'`, fleet.slis).Scan(&count); err != nil {
			t.Fatal("read live performance fleet coverage failed")
		}
		if count == 100 {
			break
		}
		if time.Since(started) > 2*time.Minute {
			t.Fatalf("performance agents did not establish live coverage for all measurements: %d/100", count)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("performance fleet coverage wait cancelled")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if _, err := db.Exec(t.Context(), `CREATE TEMP TABLE performance_proofs AS
SELECT row_number() OVER(ORDER BY sli_id) AS ordinal, proof.* FROM (
 SELECT DISTINCT ON (sli_id) sli_id,measurement_revision,agent_id,session_generation,configuration_revision,lease_id
 FROM public.almanac_sli_coverage WHERE sli_id=ANY($1::uuid[]) AND outcome='observed'
 ORDER BY sli_id,interval_start DESC
) proof`, fleet.slis); err != nil {
		t.Fatal("capture real performance fleet collection proofs failed")
	}
	end := fleet.historyEnd
	start := end.Add(-time.Duration(days) * 24 * time.Hour)
	for month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC); !month.After(end); month = month.AddDate(0, 1, 0) {
		for _, parent := range []string{"almanac_sli_coverage", "almanac_sli_observation_values"} {
			statement := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
				pgx.Identifier{"public", parent + "_" + month.Format("200601")}.Sanitize(), pgx.Identifier{"public", parent}.Sanitize(),
				month.Format(time.RFC3339), month.AddDate(0, 1, 0).Format(time.RFC3339))
			if _, err := db.Exec(t.Context(), statement); err != nil {
				t.Fatal("prepare retained performance month partition failed")
			}
		}
	}
	// The test setup establishes retained eligibility explicitly. The interactive
	// workload still uses ordinary authenticated APIs and production derivation.
	if _, err := db.Exec(t.Context(), `UPDATE public.almanac_sli_measurements SET effective_from=$2 WHERE sli_id=ANY($1::uuid[])`, fleet.slis, start); err != nil {
		t.Fatal("establish historical measurement eligibility failed")
	}
	if _, err := db.Exec(t.Context(), `UPDATE public.almanac_slo_definitions SET evaluation_valid_from=$2,
 historical_requested_by=(SELECT id FROM public.almanac_users WHERE login='sli-owner'),historical_requested_at=CURRENT_TIMESTAMP
 WHERE slo_id=ANY($1::uuid[])`, fleet.objectives, start); err != nil {
		t.Fatal("establish historical objective eligibility failed")
	}
	var initialLSN string
	if err := db.QueryRow(t.Context(), `SELECT pg_current_wal_lsn()::text`).Scan(&initialLSN); err != nil {
		t.Fatal("read fixture WAL baseline failed")
	}
	for hour := start; hour.Before(end); hour = hour.Add(time.Hour) {
		if fleet.ctx.Err() != nil {
			t.Fatal("controlled retained-history setup cancelled")
		}
		if int(hour.Sub(start)/time.Hour)%24 == 0 {
			performanceFixtureDisk(t, fleet.ctx, 8<<30)
		}
		ctx, cancel := context.WithTimeout(fleet.ctx, 30*time.Second)
		tx, err := db.Begin(ctx)
		if err != nil {
			cancel()
			t.Fatal("begin performance seed hour failed")
		}
		// Keep canonical constraints and dirty-generation triggers enabled. The
		// bounded hour transaction prevents an entire retained fixture becoming
		// one long chain of updates on the same source-generation rows.
		_, err = tx.Exec(ctx, `INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,
 execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,sli_id,measurement_revision,point+interval '1 minute',
 CASE WHEN ordinal%10=2 AND extract(minute FROM point)=0 THEN 'complete_empty' ELSE 'observed' END,
 decode(repeat('ab',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),agent_id,
 session_generation,configuration_revision,lease_id,point,point+interval '1 minute'
FROM performance_proofs CROSS JOIN generate_series($1::timestamptz,$1::timestamptz+interval '59 minutes',interval '1 minute') point
WHERE NOT (ordinal%10=1 AND extract(minute FROM point)=17)
ORDER BY sli_id,point ON CONFLICT DO NOTHING`, hour)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO public.almanac_sli_observation_values
(interval_start,sli_id,measurement_revision,good_count,total_count)
SELECT c.interval_start,c.sli_id,c.measurement_revision,
 CASE WHEN p.ordinal%10=3 AND extract(minute FROM c.interval_start)=0 THEN 0
      WHEN p.ordinal%10=4 THEN 99999999999999999999999999.999999999998
      WHEN extract(hour FROM c.interval_start) IN (3,4) THEN 97.125 ELSE 99.999 END,
 CASE WHEN p.ordinal%10=3 AND extract(minute FROM c.interval_start)=0 THEN 0
      WHEN p.ordinal%10=4 THEN 99999999999999999999999999.999999999999 ELSE 100.125 END
FROM public.almanac_sli_coverage c JOIN performance_proofs p USING(sli_id,measurement_revision)
WHERE c.interval_start >= $1 AND c.interval_start < $1::timestamptz+interval '1 hour' AND c.outcome='observed'
ORDER BY c.sli_id,c.interval_start ON CONFLICT DO NOTHING`, hour)
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		cancel()
		if err != nil {
			t.Fatalf("seed bounded performance history hour failed: %v", err)
		}
	}
	report := performanceHistorySetup{Days: days, Start: start, End: end, NominalSlots: int64(days) * 24 * 60 * 100}
	if err := db.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE outcome='complete_empty')
FROM public.almanac_sli_coverage WHERE sli_id=ANY($1::uuid[]) AND interval_start >= $2 AND interval_start < $3`, fleet.slis, start, end).Scan(&report.CanonicalRows, &report.KnownEmptyRows); err != nil {
		t.Fatal("count actual retained performance coverage failed")
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_observation_values
WHERE sli_id=ANY($1::uuid[]) AND interval_start >= $2 AND interval_start < $3 AND total_count=0`, fleet.slis, start, end).Scan(&report.ZeroTrafficRows); err != nil {
		t.Fatal("count retained performance zero-traffic intervals failed")
	}
	report.GapSlots = report.NominalSlots - report.CanonicalRows
	if report.GapSlots != int64(days)*24*10 || report.KnownEmptyRows != int64(days)*24*10 || report.ZeroTrafficRows != int64(days)*24*10 {
		t.Fatal("retained performance fixture lost its deliberate gaps, empty or zero-traffic intervals")
	}
	if err := db.QueryRow(t.Context(), `SELECT pg_database_size(current_database()),pg_wal_lsn_diff(pg_current_wal_lsn(),$1::pg_lsn)::text`, initialLSN).Scan(&report.DatabaseBytes, &report.WALBytes); err != nil {
		t.Fatal("measure performance fixture database/WAL cost failed")
	}
	report.Setup = time.Since(started)
	return report
}

func closePerformanceObserver(db *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = db.Close(ctx)
}

func performanceCreateCLI(t *testing.T, f objectiveJourneyFixture, profile, resource, name string, input, result any, field string) {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal("encode performance management input failed")
	}
	path := filepath.Join(f.dir, resource+"-"+name+".json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal("write protected performance management input failed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, f.binary, resource, "create", "--profile-file="+profile, "--input-file="+path, "--idempotency-key="+resource+"-"+name, "--output=json")
	command.Stderr = io.Discard
	output, err := command.Output()
	var document struct {
		SchemaVersion int            `json:"schema_version"`
		Result        jsontext.Value `json:"result"`
	}
	if err != nil || json.Unmarshal(output, &document) != nil || document.SchemaVersion != 1 {
		t.Fatal("create performance management record failed")
	}
	if field != "" {
		var envelope map[string]jsontext.Value
		if json.Unmarshal(document.Result, &envelope) != nil || len(envelope[field]) == 0 {
			t.Fatal("performance management response omitted record")
		}
		document.Result = envelope[field]
	}
	if json.Unmarshal(document.Result, result) != nil {
		t.Fatal("decode performance management record failed")
	}
}
