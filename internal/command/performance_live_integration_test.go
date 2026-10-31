//go:build integration

package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/testpostgres"
)

type performanceMetricsSample struct {
	At        time.Time                     `json:"at"`
	Ingestion performanceIngestionHistogram `json:"ingestion"`
	Scalars   map[string]float64            `json:"scalars"`
}

type performanceCollectionReport struct {
	Start           time.Time               `json:"start"`
	End             time.Time               `json:"end"`
	Expected        int64                   `json:"expected_intervals"`
	Observed        int64                   `json:"observed_intervals"`
	Missing         int64                   `json:"missing_source_intervals"`
	AttemptFailures int64                   `json:"failed_or_incomplete_attempts"`
	CollectionDelay performanceDistribution `json:"collection_delay_after_settling"`
	DeliveryAge     performanceDistribution `json:"delivery_age_before_commit"`
}

type performanceLiveReport struct {
	Name       string                      `json:"name"`
	Started    time.Time                   `json:"started"`
	Ended      time.Time                   `json:"ended"`
	Wall       time.Duration               `json:"wall_ns"`
	Metrics    []performanceMetricsSample  `json:"metrics"`
	Ingestion  performancePercentileBounds `json:"ingestion_p95_bounds"`
	Collection performanceCollectionReport `json:"collection"`
	Failures   []string                    `json:"failures"`
}

func readPerformanceMetrics(ctx context.Context, f objectiveJourneyFixture) (performanceMetricsSample, error) {
	sample := performanceMetricsSample{At: time.Now().UTC(), Scalars: make(map[string]float64)}
	endpoint, err := url.Parse(f.metricsURL)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path != "/metrics" || endpoint.RawQuery != "" || endpoint.User != nil {
		return sample, errors.New("metrics observation requires the separate test-owned loopback listener")
	}
	requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return sample, errors.New("construct performance metrics observation failed")
	}
	response, err := f.apiClient.Do(request)
	if err != nil {
		return sample, errors.New("read performance metrics observation failed")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 256<<10 || response.StatusCode != http.StatusOK {
		return sample, errors.New("performance metrics observation is unavailable or oversized")
	}
	sample.Ingestion, err = parsePerformanceIngestionMetrics(string(body))
	if err != nil {
		return sample, err
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "_bucket{") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(sample.Scalars) >= 256 {
			return sample, errors.New("metrics observation exceeded its fixed scalar budget")
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return sample, errors.New("invalid metrics scalar observation")
		}
		sample.Scalars[fields[0]] = value
	}
	return sample, nil
}

// Both phases use an actual fifteen-minute window with the same live fleet.
// The saturated phase supplies bounded historical/read traffic through load;
// a nil load selects the ingestion-only baseline. A cancelled or prematurely
// finished load cannot produce a passing full-window report.
func measurePerformanceLiveWindow(t *testing.T, fleet performanceFleet, name string, load func(context.Context) error) (performanceLiveReport, error) {
	t.Helper()
	started := time.Now()
	report := performanceLiveReport{Name: name, Started: started.UTC()}
	initial, err := readPerformanceMetrics(fleet.ctx, fleet.fixture)
	if err != nil {
		return report, err
	}
	report.Metrics = append(report.Metrics, initial)
	ctx, cancel := context.WithTimeout(fleet.ctx, 15*time.Minute)
	defer cancel()
	var loadDone chan error
	if load != nil {
		loadDone = make(chan error, 1)
		go func() { loadDone <- load(ctx) }()
	}
	ticks := time.NewTicker(5 * time.Second)
	defer ticks.Stop()
	observing := true
	for observing {
		select {
		case <-ctx.Done():
			observing = false
		case err := <-loadDone:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				report.Failures = append(report.Failures, "historical/read load failed")
			}
			if ctx.Err() == nil {
				report.Failures = append(report.Failures, "historical/read load ended before its fifteen-minute window")
			}
			loadDone = nil
			cancel()
			observing = false
		case <-ticks.C:
			sample, err := readPerformanceMetrics(ctx, fleet.fixture)
			if err != nil {
				if ctx.Err() == nil {
					report.Failures = append(report.Failures, err.Error())
					cancel()
					observing = false
				}
			} else {
				report.Metrics = append(report.Metrics, sample)
			}
		}
	}
	cancel()
	if loadDone != nil {
		select {
		case err := <-loadDone:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				report.Failures = append(report.Failures, "historical/read load failed")
			}
		case <-time.After(5 * time.Second):
			report.Failures = append(report.Failures, "historical/read load exceeded its cancellation bound")
		}
	}
	report.Ended, report.Wall = time.Now().UTC(), time.Since(started)
	if fleet.ctx.Err() != nil || report.Wall < 15*time.Minute {
		report.Failures = append(report.Failures, "live observation did not complete fifteen minutes")
	}
	final, err := readPerformanceMetrics(fleet.ctx, fleet.fixture)
	if err != nil {
		report.Failures = append(report.Failures, err.Error())
	} else {
		report.Metrics = append(report.Metrics, final)
		report.Ingestion, err = performanceIngestionDelta(initial.Ingestion, final.Ingestion)
		if err != nil {
			report.Failures = append(report.Failures, err.Error())
		}
	}
	report.Collection = collectPerformanceCollectionTiming(t, fleet, report.Started, report.Ended)
	if report.Collection.Missing < 0 {
		report.Failures = append(report.Failures, "collection interval count exceeded the fixed fleet cadence")
	}
	if len(report.Failures) > 0 {
		return report, fmt.Errorf("live phase %s has %d failures", name, len(report.Failures))
	}
	return report, nil
}

func checkPerformanceLiveIsolation(baseline, loaded performanceLiveReport) error {
	if len(baseline.Failures) != 0 || len(loaded.Failures) != 0 || baseline.Wall < 15*time.Minute || loaded.Wall < 15*time.Minute {
		return errors.New("live isolation requires two complete, failure-free fifteen-minute phases")
	}
	if err := checkPerformanceIngestionIsolation(baseline.Ingestion, loaded.Ingestion); err != nil {
		return err
	}
	if baseline.Collection.CollectionDelay.Count < 100 || loaded.Collection.CollectionDelay.Count < 100 ||
		loaded.Collection.CollectionDelay.P99 > baseline.Collection.CollectionDelay.P99+time.Second {
		return errors.New("collection lateness does not establish the one-second p99 isolation budget")
	}
	for _, phase := range []performanceLiveReport{baseline, loaded} {
		for _, sample := range phase.Metrics {
			for class, capacity := range map[string]float64{"live": 4, "interactive": 8, "derived": 2} {
				capacityKey := fmt.Sprintf(`almanac_db_pool_capacity{class=%q}`, class)
				acquiredKey := fmt.Sprintf(`almanac_db_pool_acquired{class=%q}`, class)
				actual, exists := sample.Scalars[capacityKey]
				acquired, acquiredExists := sample.Scalars[acquiredKey]
				if !exists || !acquiredExists || actual != capacity || acquired > capacity {
					return errors.New("live isolation changed or exceeded the reserved pool budgets")
				}
			}
		}
	}
	if len(loaded.Metrics) < 2 {
		return errors.New("historical saturation has no observed runtime evidence")
	}
	historicalObserved := false
	for _, sample := range loaded.Metrics {
		if sample.Scalars["almanac_derived_dirty_keys"] > 100 || sample.Scalars["almanac_derived_history_jobs"] > 0 {
			historicalObserved = true
		}
	}
	if !historicalObserved {
		return errors.New("historical saturation was not observed; idle phases cannot prove isolation")
	}
	return nil
}

func collectPerformanceCollectionTiming(t *testing.T, fleet performanceFleet, start, end time.Time) performanceCollectionReport {
	t.Helper()
	// Omit boundary intervals still settling or in flight; disclose the exact
	// complete comparison range rather than counting them as missing samples.
	first := start.Truncate(time.Minute).Add(time.Minute)
	last := end.Truncate(time.Minute).Add(-time.Minute)
	report := performanceCollectionReport{Start: first, End: last}
	if !last.After(first) {
		return report
	}
	report.Expected = int64(last.Sub(first)/time.Minute) * int64(len(fleet.slis))
	db := testpostgres.Connect(t, fleet.fixture.databaseURL)
	defer closePerformanceObserver(db)
	rows, err := db.Query(t.Context(), `SELECT
 extract(epoch FROM (c.agent_created_at-c.interval_end-make_interval(secs=>d.settling_delay_seconds)))::float8,
 extract(epoch FROM (c.recorded_at-c.agent_created_at))::float8
FROM public.almanac_sli_coverage c
JOIN public.almanac_sli_measurements m ON m.sli_id=c.sli_id AND m.revision=c.measurement_revision
JOIN public.almanac_datasources d ON d.id=m.datasource_id
WHERE c.sli_id=ANY($1::uuid[]) AND c.interval_end>$2 AND c.interval_end<=$3 AND c.outcome='observed'
ORDER BY c.sli_id,c.interval_end`, fleet.slis, first, last)
	if err != nil {
		t.Fatal("read real performance fleet collection timings failed")
	}
	defer rows.Close()
	var collection, delivery []time.Duration
	for rows.Next() {
		var collectionSeconds, deliverySeconds float64
		if err := rows.Scan(&collectionSeconds, &deliverySeconds); err != nil {
			t.Fatal("decode real collection timings failed")
		}
		collection = append(collection, time.Duration(collectionSeconds*float64(time.Second)))
		delivery = append(delivery, time.Duration(deliverySeconds*float64(time.Second)))
	}
	if err := rows.Err(); err != nil {
		t.Fatal("iterate real collection timings failed")
	}
	report.Observed = int64(len(collection))
	report.Missing = report.Expected - report.Observed
	report.CollectionDelay = performancePercentiles(collection)
	// recorded_at precedes commit: this is delivery age, explicitly not the
	// accepted-HTTP latency measured by the independent server histogram.
	report.DeliveryAge = performancePercentiles(delivery)
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_sli_execution_attempts
WHERE sli_id=ANY($1::uuid[]) AND interval_end>$2 AND interval_end<=$3`, fleet.slis, first, last).Scan(&report.AttemptFailures); err != nil {
		t.Fatal("read failed/incomplete source-attempt count failed")
	}
	return report
}
