//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"math/big"
	"net/url"
	"os/exec"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/testpostgres"
	"github.com/jackc/pgx/v5"
)

type performanceClockReport struct {
	AgentsDisabled int                     `json:"agents_disabled"`
	DisabledAt     time.Time               `json:"disabled_at"`
	Phases         []performanceClockPhase `json:"phases"`
}

type performanceClockPhase struct {
	NotBefore         time.Time `json:"clock_not_before"`
	Rows, Windows     int
	Counts            api.SloUrgencyCounts           `json:"independent_group_counts"`
	API               []performancePreparationSample `json:"list_reads"`
	Oracle            time.Duration                  `json:"independent_oracle_ns"`
	GenerationsStable bool                           `json:"canonical_generations_unchanged"`
	Cases             []performanceClockCase         `json:"representative_exact_cases"`
}

type performanceClockCase struct {
	SloID                                  string `json:"slo_id"`
	ExpectedGood, ExpectedTotal            string
	Actual                                 api.SloOperationalValue `json:"actual_current"`
	ExpectedUrgency, ExpectedLeadingReason string
}

type performanceClockObjective struct {
	sli, target string
	class       int
	window      time.Duration
}

func performanceClockOnly(t *testing.T, parent context.Context, fleet performanceFleet, shared performanceSharedReport, late performanceLateReport) (performanceClockReport, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 7*time.Minute)
	defer cancel()
	report := performanceClockReport{}
	for _, agent := range fleet.agents {
		get := exec.CommandContext(ctx, fleet.fixture.binary, "agents", "get", "--profile-file="+agent.profile, "--agent-id="+agent.id, "--output=json")
		get.Stderr = io.Discard
		body, err := get.Output()
		var detail struct {
			Result struct {
				ETag string `json:"etag"`
			} `json:"result"`
		}
		if err != nil || json.Unmarshal(body, &detail) != nil || !validETag(detail.Result.ETag, agent.id) {
			return report, errors.New("read owned fixture agent version before disable failed")
		}
		disable := exec.CommandContext(ctx, fleet.fixture.binary, "agents", "disable", "--profile-file="+agent.profile, "--agent-id="+agent.id, "--if-match="+detail.Result.ETag, "--idempotency-key=clock-disable-"+agent.id, "--confirm", "--output=json")
		disable.Stdout, disable.Stderr = io.Discard, io.Discard
		if err := disable.Run(); err != nil {
			return report, errors.New("disable owned fixture collection through normal CLI failed")
		}
		report.AgentsDisabled++
	}
	report.DisabledAt = time.Now().UTC()
	objectives := make(map[string]performanceClockObjective, len(fleet.objectives)+len(shared.Added))
	for index, id := range fleet.objectives {
		class := (index + 1) % 10
		objectives[id] = performanceClockObjective{sli: fleet.slis[index], target: "0.995", class: class, window: performanceOracleRollingWindow(class)}
	}
	for _, objective := range shared.Added {
		objectives[objective.Id.String()] = performanceClockObjective{sli: shared.SharedSLI, target: objective.Target, class: 0, window: time.Duration(objective.RollingWindowSeconds) * time.Second}
	}
	db := testpostgres.Connect(t, fleet.fixture.databaseURL)
	defer closePerformanceObserver(db)
	readSources := func() (map[string]int64, error) {
		bounded, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		rows, err := db.Query(bounded, `SELECT sli_id::text,generation FROM public.almanac_sli_source_generations WHERE sli_id=ANY($1::uuid[]) AND measurement_revision=1`, fleet.slis)
		if err != nil {
			return nil, errors.New("read quiescent source generations failed")
		}
		defer rows.Close()
		result := make(map[string]int64, len(fleet.slis))
		for rows.Next() {
			var id string
			var generation int64
			if rows.Scan(&id, &generation) != nil || generation <= 0 {
				return nil, errors.New("invalid quiescent source generation")
			}
			result[id] = generation
		}
		if rows.Err() != nil || len(result) != len(fleet.slis) {
			return nil, errors.New("quiescent source generations omitted measurements")
		}
		return result, nil
	}
	sources, err := readSources()
	if err != nil {
		return report, err
	}
	// Allow the existing ten-second publication contract after the administrative
	// collection change; no normal page request waits or polls for prepared data.
	for _, due := range []time.Time{report.DisabledAt.Add(10 * time.Second), report.DisabledAt.Truncate(time.Minute).Add(4*time.Minute + 10*time.Second)} {
		timer := time.NewTimer(time.Until(due))
		select {
		case <-ctx.Done():
			timer.Stop()
			return report, ctx.Err()
		case <-timer.C:
		}
		notBefore := due.Add(-10 * time.Second).Truncate(time.Minute)
		phase, err := performanceClockSnapshot(ctx, db, fleet, objectives, sources, late.IntervalStart, notBefore)
		report.Phases = append(report.Phases, phase)
		if err != nil {
			return report, err
		}
		current, err := readSources()
		if err != nil {
			return report, err
		}
		for id, generation := range sources {
			if current[id] != generation {
				return report, errors.New("canonical ingestion advanced after fixture agents were disabled")
			}
		}
		report.Phases[len(report.Phases)-1].GenerationsStable = true
	}
	if report.Phases[1].Counts.NeedsAttention != int64(len(objectives)) {
		return report, errors.New("clock-only aging did not expose every stale objective")
	}
	return report, nil
}

func performanceClockSnapshot(ctx context.Context, db *pgx.Conn, fleet performanceFleet, objectives map[string]performanceClockObjective, sources map[string]int64, late, notBefore time.Time) (performanceClockPhase, error) {
	report := performanceClockPhase{NotBefore: notBefore}
	var items []api.SloOperationalOverviewItem
	var first api.SloOperationalOverviewPage
	cursor := ""
	seen := make(map[string]bool, len(objectives))
	for pageIndex := 0; pageIndex < (len(objectives)+19)/20; pageIndex++ {
		query := url.Values{"limit": {"20"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var page api.SloOperationalOverviewPage
		op := performanceOperation{Name: "clock-only-overview", Paths: []string{"/api/v1/slos/overview?" + query.Encode()}, Budget: 200 * time.Millisecond}
		op.Check = func(_ int, body []byte) error {
			if json.Unmarshal(body, &page) != nil || len(page.Items) != min(20, len(objectives)-len(items)) {
				return errors.New("clock-only overview omitted expected page rows")
			}
			if pageIndex == 0 {
				first = page
			} else if page.SnapshotId != first.SnapshotId || page.GroupCounts != first.GroupCounts {
				return errors.New("clock-only overview changed pinned snapshot/counts")
			}
			for _, item := range page.Items {
				id := item.Slo.Id.String()
				expected, known := objectives[id]
				if !known || seen[id] || item.Slo.SliId.String() != expected.sli || item.Slo.Target != expected.target || time.Duration(item.Slo.RollingWindowSeconds)*time.Second != expected.window || item.Generation == nil || checkPerformanceGeneration(*item.Generation) != nil {
					return errors.New("clock-only overview changed objective identity/definition")
				}
				source, err := strconv.ParseInt(item.Generation.SourceGeneration, 10, 64)
				if err != nil || source != sources[expected.sli] || item.Generation.EvaluatedThrough.Before(notBefore) || !item.Current.EvaluationAt.Equal(item.Generation.EvaluatedThrough) {
					return errors.New("clock-only overview did not publish the required clock/source generation")
				}
				seen[id] = true
			}
			return nil
		}
		samples, err := preparePerformanceOperation(ctx, fleet.readers, op)
		report.API = append(report.API, samples...)
		if err != nil {
			return report, err
		}
		items = append(items, page.Items...)
		cursor = page.NextCursor
		if (cursor != "") != (len(items) < len(objectives)) {
			return report, errors.New("clock-only cursor ended early or beyond the installation")
		}
	}
	windows := make([]performanceClockWindow, 0, len(items)*3)
	for _, item := range items {
		expected := objectives[item.Slo.Id.String()]
		for _, duration := range []time.Duration{expected.window, time.Hour, 6 * time.Hour} {
			windows = append(windows, performanceClockWindow{SliID: expected.sli, Class: expected.class, Start: item.Current.EvaluationAt.Add(-duration), End: item.Current.EvaluationAt, Corrected: item.Slo.Id.String() == fleet.objectives[0]})
		}
	}
	started := time.Now()
	views, err := performanceClockCounts(ctx, db, windows, fleet.historyEnd, late)
	report.Oracle = time.Since(started)
	if err != nil {
		return report, err
	}
	for index, item := range items {
		expected := objectives[item.Slo.Id.String()]
		for lane, value := range []api.SloOperationalValue{item.Current, {EvaluationAt: item.Burn1h.End, Performance: item.Burn1h.Performance, Quality: item.Burn1h.Quality}, {EvaluationAt: item.Burn6h.End, Performance: item.Burn6h.Performance, Quality: item.Burn6h.Quality}} {
			view := views[index*3+lane]
			if !value.EvaluationAt.Equal(view.End) {
				return report, errors.New("clock-only summary/burn moved evaluation boundaries")
			}
			p, q := value.Performance, value.Quality
			if err := checkPerformanceOracleTargetMetrics(p.Good, p.Total, p.Attainment, p.AllowedBudget, p.RemainingBudget, p.RemainingFraction, p.Burn, view.Counts, "budget", expected.target); err != nil {
				return report, err
			}
			if err := checkPerformanceOracleQuality(q, view.Counts, windows[index*3+lane].End.Sub(windows[index*3+lane].Start)); err != nil {
				return report, err
			}
			freshness := "unknown"
			if !view.Last.IsZero() {
				freshness = "fresh"
				if view.End.After(view.Last.Add(view.Grace)) {
					freshness = "stale"
				}
			}
			if string(q.Freshness) != freshness || (q.LastObservationAt == nil) != view.Last.IsZero() || q.LastObservationAt != nil && !q.LastObservationAt.Equal(view.Last) {
				return report, errors.New("clock-only freshness differs from canonical observation time")
			}
			provisional := view.Counts.Missing > 0 || view.Counts.Empty > 0
			state := api.SloEvaluationPerformanceState("unknown")
			if !provisional && view.Counts.Observed > 0 && view.Counts.Total.Sign() > 0 {
				target, _ := new(big.Rat).SetString(expected.target)
				attainment := new(big.Rat).Quo(view.Counts.Good, view.Counts.Total)
				state = "met"
				if attainment.Cmp(target) < 0 {
					state = "violated"
				}
			}
			if p.Provisional != provisional || p.State != state {
				return report, errors.New("clock-only observed arithmetic changed qualified performance")
			}
		}
		group, reason, err := performanceOracleUrgency(views[index*3], views[index*3+1], expected.target)
		if err != nil {
			return report, err
		}
		if string(item.Urgency) != group || string(item.LeadingReason.Code) != reason {
			return report, errors.New("clock-only urgency differs from independent exact triage")
		}
		if slices.Contains(fleet.objectives[:10], item.Slo.Id.String()) || !slices.Contains(fleet.objectives, item.Slo.Id.String()) {
			report.Cases = append(report.Cases, performanceClockCase{SloID: item.Slo.Id.String(), ExpectedGood: views[index*3].Counts.Good.RatString(), ExpectedTotal: views[index*3].Counts.Total.RatString(), Actual: item.Current, ExpectedUrgency: group, ExpectedLeadingReason: reason})
		}
		switch group {
		case "needs_attention":
			report.Counts.NeedsAttention++
		case "watch":
			report.Counts.Watch++
		case "healthy":
			report.Counts.Healthy++
		}
	}
	report.Rows, report.Windows = len(items), len(views)
	if report.Rows != len(objectives) || report.Counts != first.GroupCounts {
		return report, errors.New("global urgency counts differ from the full independently evaluated installation")
	}
	return report, nil
}
