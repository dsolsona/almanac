//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/testpostgres"
	"github.com/jackc/pgx/v5"
)

type performanceLoadReader struct {
	Actor          int                     `json:"actor"`
	Ready          int                     `json:"ready"`
	Pending        int                     `json:"pending"`
	AdmissionLimit int                     `json:"history_admission_limit"`
	Failed         int                     `json:"failed"`
	Latency        performanceDistribution `json:"all_response_latency"`
}

type performanceSaturationReport struct {
	EnqueuePasses int                                `json:"enqueue_passes"`
	EnqueuedHours int64                              `json:"enqueued_hours"`
	Readers       []performanceLoadReader            `json:"readers"`
	Currentness   []performanceCurrentnessSample     `json:"currentness"`
	Lag           map[string]performanceDistribution `json:"lag_upper_bounds"`
	Unfinished    int                                `json:"overdue_currentness"`
	Failures      []string                           `json:"failures"`
}

type performanceSaturation struct {
	fleet       performanceFleet
	db          *pgx.Conn
	currentness *performanceCurrentness
	mu          sync.Mutex
	report      performanceSaturationReport
	durations   [performanceUsers][]time.Duration
}

func newPerformanceSaturation(t *testing.T, fleet performanceFleet) *performanceSaturation {
	t.Helper()
	s := &performanceSaturation{fleet: fleet, db: testpostgres.Connect(t, fleet.fixture.databaseURL),
		report: performanceSaturationReport{Readers: make([]performanceLoadReader, performanceUsers)}}
	for index := range s.report.Readers {
		s.report.Readers[index].Actor = index
	}
	return s
}

func (s *performanceSaturation) fail(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.report.Failures) < 20 {
		s.report.Failures = append(s.report.Failures, message)
	}
}

func (s *performanceSaturation) run(parent context.Context) error {
	defer closePerformanceObserver(s.db)
	if len(s.fleet.readers) != performanceUsers || len(s.fleet.objectives) != 100 {
		return errors.New("saturation requires the complete controlled fleet")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	s.currentness = newPerformanceCurrentness(time.Now())
	end := time.Now().UTC().Truncate(time.Minute)
	query := url.Values{"at": {end.Format(time.RFC3339Nano)}, "start": {end.Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}, "burn_window_seconds": {"3600"}}
	var workers sync.WaitGroup
	for actor, reader := range s.fleet.readers {
		workers.Go(func() {
			if err := s.readLoop(ctx, actor, reader, query.Encode()); err != nil && ctx.Err() == nil {
				s.fail(err.Error())
				cancel()
			}
		})
	}
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	var nextEnqueue time.Time
	for ctx.Err() == nil {
		now := time.Now()
		if err := s.currentness.discover(ctx, s.db, s.fleet.objectives, now); err != nil {
			if ctx.Err() == nil {
				s.fail(err.Error())
			}
			cancel()
			break
		}
		if !now.Before(nextEnqueue) {
			if err := s.enqueueHistory(ctx, now); err != nil {
				if ctx.Err() == nil {
					s.fail(err.Error())
				}
				cancel()
				break
			}
			nextEnqueue = time.Now().Add(5 * time.Second)
		}
		select {
		case <-ctx.Done():
		case <-ticks.C:
		}
	}
	cancel()
	workers.Wait()
	completed, lag, unfinished, lagErr := s.currentness.evidence(time.Now())
	s.mu.Lock()
	s.report.Currentness, s.report.Lag, s.report.Unfinished = completed, lag, unfinished
	for actor := range s.report.Readers {
		s.report.Readers[actor].Latency = performancePercentiles(s.durations[actor])
	}
	failed := len(s.report.Failures) > 0
	enqueued := s.report.EnqueuedHours
	s.mu.Unlock()
	if lagErr != nil {
		s.fail(lagErr.Error())
		return lagErr
	}
	if failed || enqueued == 0 {
		return errors.New("historical saturation failed or enqueued no retained work")
	}
	return parent.Err()
}

func (s *performanceSaturation) enqueueHistory(ctx context.Context, now time.Time) error {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var alreadyBusy bool
	if err := s.db.QueryRow(bounded, `SELECT EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges OFFSET 999 LIMIT 1)`).Scan(&alreadyBusy); err != nil {
		return errors.New("observe historical saturation queue failed")
	}
	if alreadyBusy {
		return nil
	}
	// Fault/load injection requeues existing retained hours without fabricating
	// canonical changes or advancing source generations. Recent live hours remain
	// first in the production worker's newest-hour-first admission order.
	tag, err := s.db.Exec(bounded, `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
SELECT a.sli_id,a.measurement_revision,source.generation,a.hour_start,a.hour_start+interval '1 hour',$2,$2
FROM public.almanac_sli_hourly_aggregates a
JOIN public.almanac_sli_source_generations source USING(sli_id,measurement_revision)
WHERE a.sli_id=ANY($1::uuid[]) AND a.hour_start >= $2::timestamptz-interval '56 days'
AND a.hour_start < $2::timestamptz-interval '30 days'
ORDER BY a.sli_id,a.hour_start ON CONFLICT DO NOTHING`, s.fleet.slis, now.UTC())
	if err != nil {
		return errors.New("enqueue bounded historical saturation work failed")
	}
	s.mu.Lock()
	s.report.EnqueuePasses++
	s.report.EnqueuedHours += tag.RowsAffected()
	s.mu.Unlock()
	return nil
}

func (s *performanceSaturation) readLoop(ctx context.Context, actor int, reader performanceReader, historyQuery string) error {
	ticks := time.NewTicker(250 * time.Millisecond)
	defer ticks.Stop()
	for turn := 0; ctx.Err() == nil; turn++ {
		id := s.fleet.objectives[actor+performanceUsers*((turn/2)%10)]
		path := "/api/v1/slos/" + id + "/status"
		kind := "status"
		if turn%4 == 0 {
			kind, path = "list", "/api/v1/slos/overview?limit=50"
		} else if turn%4 == 2 {
			kind, path = "history", "/api/v1/slos/"+id+"/operational?"+historyQuery
		}
		started := time.Now()
		status, retry, body, err := performanceLoadRequest(ctx, reader, path)
		elapsed := time.Since(started)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		outcome := "ready"
		if err == nil && status == http.StatusOK {
			err = s.checkRead(kind, id, body)
		} else if err == nil && status == http.StatusServiceUnavailable {
			var problem api.Problem
			if json.Unmarshal(body, &problem) != nil {
				err = errors.New("saturation read returned an invalid problem")
			} else if problem.Code == "derived_data_pending" && retry == "2" && problem.Preparation != nil && problem.Preparation.State == api.DerivedPreparationStatePending && problem.Preparation.RetryAfterSeconds == 2 {
				outcome = "pending"
			} else if kind == "history" && problem.Code == "resource_limit" {
				outcome = "admission"
			} else {
				err = errors.New("saturation read returned an unexpected unavailable state")
			}
		} else if err == nil {
			err = fmt.Errorf("saturation %s read returned HTTP %d", kind, status)
		}
		s.mu.Lock()
		if len(s.durations[actor]) < 4000 {
			s.durations[actor] = append(s.durations[actor], elapsed)
		}
		counts := &s.report.Readers[actor]
		switch {
		case err != nil:
			counts.Failed++
		case outcome == "pending":
			counts.Pending++
		case outcome == "admission":
			counts.AdmissionLimit++
		default:
			counts.Ready++
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-ticks.C:
		}
	}
	return ctx.Err()
}

func (s *performanceSaturation) checkRead(kind, id string, body []byte) error {
	switch kind {
	case "status":
		var status api.SloCurrentStatus
		if json.Unmarshal(body, &status) != nil || status.Slo.Id.String() != id {
			return errors.New("saturation status response changed objective identity")
		}
		return s.currentness.observe(status, time.Now())
	case "list":
		var page api.SloOperationalOverviewPage
		if json.Unmarshal(body, &page) != nil || len(page.Items) != 50 || page.NextCursor == "" || page.GroupCounts.Healthy+page.GroupCounts.NeedsAttention+page.GroupCounts.Watch != 100 {
			return errors.New("saturation overview lost its bounded page or global counts")
		}
	case "history":
		var detail api.SloOperationalDetail
		if json.Unmarshal(body, &detail) != nil || detail.Objective.Id.String() != id || detail.Generation == nil || checkPerformanceGeneration(*detail.Generation) != nil ||
			len(detail.AttainmentSeries.Samples) > 1500 || len(detail.BudgetSeries.Samples) > 1500 || len(detail.BurnSeries.Samples) > 1500 {
			return errors.New("saturation history lost its identity, provenance or point bounds")
		}
	}
	return nil
}

func performanceLoadRequest(ctx context.Context, reader performanceReader, path string) (int, string, []byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := reader.Request(bounded, path)
	if err != nil || request == nil {
		return 0, "", nil, errors.New("construct saturation read failed")
	}
	response, err := reader.Client.Do(request)
	if err != nil {
		return 0, "", nil, errors.New("saturation read transport failed")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (20<<20)+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 20<<20 {
		return response.StatusCode, "", nil, errors.New("saturation response was unreadable or oversized")
	}
	return response.StatusCode, strings.TrimSpace(response.Header.Get("Retry-After")), body, nil
}

func (s *performanceSaturation) evidence() performanceSaturationReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	report := s.report
	report.Readers = slices.Clone(report.Readers)
	report.Currentness = slices.Clone(report.Currentness)
	report.Failures = slices.Clone(report.Failures)
	report.Lag = maps.Clone(report.Lag)
	return report
}
