//go:build integration

package command

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func TestIntegrationExecutableAlertPolicyJourney(t *testing.T) {
	t.Parallel()
	// Reuse the existing isolated TLS backend, protected profile, and real agent
	// setup. Alert management itself never connects the CLI to PostgreSQL.
	f := startObjectiveJourneyFixtureWithMetrics(t, false)
	journey := runConnectedSLIAgentJourney(t, f.databaseURL, f.binary, f.dir, f.baseURL, f.certFile, f.password)
	datasourceID := createSLIJourneyDatasource(t, f.binary, f.dir, journey, f.sourceEndpoint, f.sourceCA)
	run := func(want int, args ...string) objectiveJourneyDocument {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, f.binary, append(args, "--profile-file="+journey.profile, "--output=json")...)
		var out, diagnostic bytes.Buffer
		command.Stdout = &out
		command.Stderr = &diagnostic
		err := command.Run()
		code := 0
		if err != nil {
			if exit, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exit.ExitCode()
			} else {
				t.Fatalf("CLI launch: %v", err)
			}
		}
		var doc objectiveJourneyDocument
		if code != want || diagnostic.Len() != 0 || json.Unmarshal(out.Bytes(), &doc) != nil || doc.SchemaVersion != 1 {
			t.Fatalf("%s %s exit=%d want=%d result=%s stderr=%s", args[0], args[1], code, want, out.Bytes(), diagnostic.Bytes())
		}
		return doc
	}
	input := func(name string, value any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(f.dir, name+".json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	sliFile := input("alert-sli", map[string]any{"name": "alert-sli", "display_name": "Alert SLI", "datasource_id": datasourceID, "measurement_kind": "event_counts", "component_mode": "interval_count", "good_query": "vector(99)", "total_query": "vector(100)"})
	sliResult := run(0, "slis", "create", "--input-file="+sliFile, "--idempotency-key=alert-sli")
	var sli api.Sli
	if json.Unmarshal(sliResult.Result["sli"], &sli) != nil || !validCLIUUID(sli.Id.String()) {
		t.Fatal("SLI prerequisite")
	}
	serviceFile := input("alert-service", map[string]string{"name": "alert-service", "display_name": "Alert Service"})
	serviceResult := run(0, "services", "create", "--input-file="+serviceFile, "--idempotency-key=alert-service")
	var service api.Service
	if json.Unmarshal(serviceResult.Result["service"], &service) != nil {
		t.Fatal("service prerequisite")
	}
	sloFile := input("alert-slo", map[string]any{"name": "alert-slo", "display_name": "Alert SLO", "service_id": service.Id.String(), "sli_id": sli.Id.String(), "target": "0.999", "rolling_window_seconds": 2592000})
	sloResult := run(0, "slos", "create", "--input-file="+sloFile, "--idempotency-key=alert-slo")
	var slo api.Slo
	if json.Unmarshal(sloResult.Result["slo"], &slo) != nil {
		t.Fatal("SLO prerequisite")
	}
	conditions := []api.AlertCondition{{Id: "fast", ShortWindowSeconds: 300, LongWindowSeconds: 3600, Threshold: "14.4"}, {Id: "sustained", ShortWindowSeconds: 1800, LongWindowSeconds: 21600, Threshold: "6"}, {Id: "custom", ShortWindowSeconds: 600, LongWindowSeconds: 7200, Threshold: "3"}}
	createFile := input("alert-policy", api.AlertPolicyWrite{Name: "CLI Alert", SloId: slo.Id, Conditions: conditions})
	created := run(0, "alert-policies", "create", "--input-file="+createFile, "--idempotency-key=alert-create")
	decode := func(doc objectiveJourneyDocument) (api.AlertPolicy, string) {
		t.Helper()
		var p api.AlertPolicy
		var tag string
		if json.Unmarshal(doc.Result["policy"], &p) != nil || json.Unmarshal(doc.Result["etag"], &tag) != nil || !validCLIUUID(p.Id.String()) {
			t.Fatal("policy response/etag")
		}
		return p, tag
	}
	policy, tag := decode(created)
	if policy.Version != "1" || policy.EvaluationRevision != "1" || len(policy.Conditions) != 3 {
		t.Fatalf("create policy state: %+v", policy)
	}
	idFlag := "--policy-id=" + policy.Id.String()
	listed := run(0, "alert-policies", "list", "--slo-id="+slo.Id.String(), "--enabled=true", "--limit=20")
	var total string
	if json.Unmarshal(listed.Result["total"], &total) != nil || total != "1" {
		t.Fatal("list exact total")
	}
	fetched, _ := decode(run(0, "alert-policies", "get", idFlag))
	if fetched.Id != policy.Id {
		t.Fatal("read identity")
	}
	renameFile := input("alert-rename", map[string]string{"name": "Renamed CLI Alert"})
	updated := run(0, "alert-policies", "update", idFlag, "--if-match="+tag, "--input-file="+renameFile, "--idempotency-key=alert-update")
	policy, tag = decode(updated)
	if policy.Version != "2" || policy.EvaluationRevision != "1" {
		t.Fatal("metadata revised evaluation")
	}
	stale := run(5, "alert-policies", "update", idFlag, "--if-match=\"alert-policy:"+policy.Id.String()+":1\"", "--input-file="+renameFile, "--idempotency-key=stale-update")
	if stale.Error == nil || stale.Error.Code != "version_conflict" {
		t.Fatal("stale version not explicit")
	}
	disabled := run(0, "alert-policies", "disable", idFlag, "--if-match="+tag, "--idempotency-key=alert-disable")
	policy, tag = decode(disabled)
	if policy.Enabled {
		t.Fatal("disable intent")
	}
	enabled := run(0, "alert-policies", "enable", idFlag, "--if-match="+tag, "--idempotency-key=alert-enable")
	policy, tag = decode(enabled)
	if !policy.Enabled || policy.EvaluationRevision != "2" {
		t.Fatal("fresh enable revision")
	}
	replay := run(0, "alerts", "command-result", "--operation=alert-policy.create", "--idempotency-key=alert-create")
	var committed api.AlertCommandResult
	if json.Unmarshal(replay.Result["result"], &committed) != nil || committed.Policy == nil || committed.Policy.Name != "CLI Alert" || committed.Policy.Version != "1" {
		t.Fatal("receipt substituted current resource")
	}
	unknown := run(0, "alerts", "command-result", "--operation=alert-policy.create", "--idempotency-key=never-sent")
	var status string
	if json.Unmarshal(unknown.Result["status"], &status) != nil || status != "unknown" {
		t.Fatal("unknown command misrepresented")
	}
	run(0, "alert-policies", "ranges", idFlag, "--revision=1")
	run(0, "alerts", "processing", idFlag)
	alerts := run(0, "alerts", "list", "--scope=all", "--group-by=slo", "--state=firing,acknowledged", "--severity=high,medium")
	if json.Unmarshal(alerts.Result["total"], &total) != nil || total != "0" {
		t.Fatal("unwired engine fabricated alert")
	}
	missing := "--alert-id=11111111-1111-4111-8111-111111111111"
	for _, action := range []string{"get", "evidence", "transitions"} {
		run(4, "alerts", action, missing)
	}
	run(4, "alerts", "acknowledge", missing, "--if-match=\"alert-occurrence:11111111-1111-4111-8111-111111111111:1\"", "--idempotency-key=missing-ack")
	retry := run(5, "alert-policies", "retry", idFlag, "--if-match="+tag, "--idempotency-key=not-exhausted")
	if retry.Error == nil || retry.Error.Code != "version_conflict" {
		t.Fatal("retry invented exhausted work")
	}
	// Text mode is a real separate process and preserves names/version strings.
	cmd := exec.CommandContext(t.Context(), f.binary, "alert-policies", "list", "--profile-file="+journey.profile)
	text, err := cmd.Output()
	if err != nil || !bytes.Contains(text, []byte("Renamed CLI Alert")) {
		t.Fatal("text output omitted actual policy")
	}
}
