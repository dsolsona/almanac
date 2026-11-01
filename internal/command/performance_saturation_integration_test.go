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
	EnqueuePasses         int                                `json:"enqueue_passes"`
	EnqueuedHours         int64                              `json:"enqueued_hours"`
	Readers               []performanceLoadReader            `json:"readers"`
	Currentness           []performanceCurrentnessSample     `json:"currentness"`
	Lag                   map[string]performanceDistribution `json:"lag_upper_bounds"`
	Unfinished            int                                `json:"overdue_currentness"`
	AuthenticatedCohort   []string                           `json:"authenticated_visibility_cohort"`
	Publication           []performanceCurrentnessSample     `json:"publication_currentness"`
	PublicationLag        map[string]performanceDistribution `json:"publication_lag_upper_bounds"`
	PublicationUnfinished int                                `json:"overdue_publication"`
	Failures              []string                           `json:"failures"`
}

type performanceSaturation struct {
	fleet       performanceFleet
	db          *pgx.Conn
	enqueueDB   *pgx.Conn
	currentness *performanceCurrentness
	publication *performanceCurrentness
	mu          sync.Mutex
	report      performanceSaturationReport
	durations   [performanceUsers][]time.Duration
}

func newPerformanceSaturation(t *testing.T, fleet performanceFleet) *performanceSaturation {
	t.Helper()
	s := &performanceSaturation{fleet: fleet, db: testpostgres.Connect(t, fleet.fixture.databaseURL), enqueueDB: testpostgres.Connect(t, fleet.fixture.databaseURL),
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
	defer closePerformanceObserver(s.enqueueDB)
	if len(s.fleet.readers) != performanceUsers || len(s.fleet.objectives) < performanceUsers {
		return errors.New("saturation requires the complete controlled fleet")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	now := time.Now()
	s.currentness = newPerformanceCurrentness(now, "authenticated_visibility")
	s.publication = newPerformanceCurrentness(now, "publication_observed")
	cohort := s.fleet.objectives[:performanceUsers]
	s.report.AuthenticatedCohort = slices.Clone(cohort)
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
	// Fault injection owns a separate connection and cannot delay the observer.
	workers.Go(func() {
		ticks := time.NewTicker(5 * time.Second)
		defer ticks.Stop()
		for ctx.Err() == nil {
			if err := s.enqueueHistory(ctx, time.Now()); err != nil {
				if ctx.Err() == nil {
					s.fail(err.Error())
					cancel()
				}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticks.C:
			}
		}
	})
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	for ctx.Err() == nil {
		now := time.Now()
		if err := s.currentness.discover(ctx, s.db, cohort, now); err != nil {
			if ctx.Err() == nil {
				s.fail(err.Error())
			}
			cancel()
			break
		}
		if err := s.publication.discover(ctx, s.db, s.fleet.objectives, now); err != nil {
			if ctx.Err() == nil {
				s.fail(err.Error())
			}
			cancel()
			break
		}
		if err := s.publication.observePublished(ctx, s.db, s.fleet.objectives); err != nil {
			if ctx.Err() == nil {
				s.fail(err.Error())
			}
			cancel()
			break
		}
		select {
		case <-ctx.Done():
		case <-ticks.C:
		}
	}
	cancel()
	workers.Wait()
	completed, lag, unfinished, lagErr := s.currentness.evidence(time.Now())
	publication, publicationLag, publicationUnfinished, publicationErr := s.publication.evidence(time.Now())
	s.mu.Lock()
	s.report.Currentness, s.report.Lag, s.report.Unfinished = completed, lag, unfinished
	s.report.Publication, s.report.PublicationLag, s.report.PublicationUnfinished = publication, publicationLag, publicationUnfinished
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
	if publicationErr != nil {
		s.fail(publicationErr.Error())
		return publicationErr
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
	if err := s.enqueueDB.QueryRow(bounded, `SELECT EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges OFFSET 999 LIMIT 1)`).Scan(&alreadyBusy); err != nil {
		return errors.New("observe historical saturation queue failed")
	}
	if alreadyBusy {
		return nil
	}
	// Fault/load injection requeues existing retained hours without fabricating
	// canonical changes or advancing source generations. Recent live hours remain
	// first in the production worker's newest-hour-first admission order.
	tag, err := s.enqueueDB.Exec(bounded, `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
SELECT a.sli_id,a.measurement_revision,source.generation,a.hour_start,a.hour_start+interval '1 hour',$2,$2
FROM public.almanac_sli_hourly_aggregates a
JOIN public.almanac_sli_source_generations source USING(sli_id,measurement_revision)
WHERE a.sli_id=ANY($1::uuid[]) AND a.hour_start >= $2::timestamptz-interval '56 days'
AND a.hour_start < $2::timestamptz-interval '30 days'
AND NOT EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges d
    WHERE d.sli_id=a.sli_id AND d.measurement_revision=a.measurement_revision AND d.range_start=a.hour_start)
ORDER BY a.sli_id,a.hour_start
LIMIT GREATEST(0,1000-(SELECT count(*) FROM (SELECT 1 FROM public.almanac_derived_dirty_ranges LIMIT 1000) busy))
ON CONFLICT DO NOTHING`, s.fleet.slis, now.UTC())
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
		target, kind := performanceSaturationTarget(actor, turn, len(s.fleet.objectives), s.fleet.reserveDetail)
		id := s.fleet.objectives[target]
		path := "/api/v1/slos/" + id + "/status"
		if kind == "list" {
			path = "/api/v1/slos/overview?limit=20"
		} else if kind == "history" {
			path = "/api/v1/slos/" + id + "/operational?" + historyQuery
		}
		started := time.Now()
		status, _, body, err := performanceLoadRequest(ctx, reader, path)
		elapsed := time.Since(started)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil && status == http.StatusOK {
			err = s.checkRead(kind, id, body, started)
		} else if err == nil {
			err = fmt.Errorf("saturation %s read returned HTTP %d", kind, status)
		}
		budget := 200 * time.Millisecond
		if kind == "history" {
			budget = 500 * time.Millisecond
		}
		if err == nil && elapsed >= budget {
			err = fmt.Errorf("saturation %s read exceeded its latency budget", kind)
		}
		s.mu.Lock()
		if len(s.durations[actor]) < 4000 {
			s.durations[actor] = append(s.durations[actor], elapsed)
		}
		counts := &s.report.Readers[actor]
		if err != nil {
			counts.Failed++
		} else {
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

func (s *performanceSaturation) checkRead(kind, id string, body []byte, started time.Time) error {
	switch kind {
	case "status":
		var status api.SloCurrentStatus
		if json.Unmarshal(body, &status) != nil || status.Slo.Id.String() != id {
			return errors.New("saturation status response changed objective identity")
		}
		return s.currentness.observe(status, started, time.Now())
	case "list":
		var page api.SloOperationalOverviewPage
		if json.Unmarshal(body, &page) != nil || len(page.Items) != min(20, len(s.fleet.objectives)) || (page.NextCursor != "") != (len(s.fleet.objectives) > 20) || page.GroupCounts.Healthy+page.GroupCounts.NeedsAttention+page.GroupCounts.Watch != int64(len(s.fleet.objectives)) {
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
	report.AuthenticatedCohort = slices.Clone(report.AuthenticatedCohort)
	report.Publication = slices.Clone(report.Publication)
	report.PublicationLag = maps.Clone(report.PublicationLag)
	return report
}
