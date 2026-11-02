//go:build integration

package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/testpostgres"
)

type performancePreparationSample struct {
	Target  int           `json:"target"`
	Elapsed time.Duration `json:"elapsed_ns"`
	Pending int           `json:"pending_responses"`
	Failure string        `json:"failure,omitempty"`
	Body    []byte        `json:"-"`
}

// First visits are checked once, including their latency. Only explicit fixture
// import/recovery waits for automatic maintenance before normal reads begin.
func preparePerformanceOperation(ctx context.Context, readers []performanceReader, operation performanceOperation) ([]performancePreparationSample, error) {
	if len(readers) != performanceUsers || len(operation.Paths) == 0 || operation.Check == nil {
		return nil, errors.New("preparation requires ten readers and checked targets")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	samples := make([]performancePreparationSample, len(operation.Paths))
	var workers sync.WaitGroup
	for actor, reader := range readers {
		workers.Go(func() {
			for target := actor; target < len(operation.Paths); target += performanceUsers {
				samples[target] = preparePerformanceTarget(ctx, reader, operation, target)
			}
		})
	}
	workers.Wait()
	failures := 0
	for _, sample := range samples {
		if sample.Failure != "" {
			failures++
		}
	}
	if failures > 0 {
		return samples, fmt.Errorf("%s: %d/%d preparation targets failed", operation.Name, failures, len(samples))
	}
	return samples, nil
}

func preparePerformanceTarget(ctx context.Context, reader performanceReader, operation performanceOperation, target int) performancePreparationSample {
	started := time.Now()
	sample := performancePreparationSample{Target: target}
	requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := reader.Request(requestContext, operation.Paths[target])
	if err != nil || request == nil {
		sample.Failure = "request_construction"
		sample.Elapsed = time.Since(started)
		return sample
	}
	response, err := reader.Client.Do(request)
	if err != nil {
		sample.Failure = "transport"
		sample.Elapsed = time.Since(started)
		return sample
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (20<<20)+1))
	closeErr := response.Body.Close()
	sample.Elapsed = time.Since(started)
	switch {
	case readErr != nil || closeErr != nil || len(body) > 20<<20:
		sample.Failure = "response_read_or_size"
	case response.StatusCode != http.StatusOK:
		sample.Failure = "normal_navigation_unavailable"
		if response.StatusCode == http.StatusServiceUnavailable {
			sample.Pending = 1
		}
	case operation.Check(target, body) != nil:
		sample.Failure = "response_contract"
	case sample.Elapsed >= operation.Budget:
		sample.Failure = "first_request_budget"
	default:
		sample.Body = body
	}
	return sample
}

func awaitPerformanceHistory(t *testing.T, ctx context.Context, fleet performanceFleet) (time.Duration, error) {
	t.Helper()
	started := time.Now()
	db := testpostgres.Connect(t, fleet.fixture.databaseURL)
	defer closePerformanceObserver(db)
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()
	for {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		var pending, failed bool
		err := db.QueryRow(bounded, `SELECT
 EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges WHERE sli_id=ANY($1::uuid[]) AND range_start<$3) OR
 EXISTS(SELECT 1 FROM public.almanac_slo_series_repair WHERE slo_id=ANY($2::uuid[]) AND cursor_at<$3) OR
 EXISTS(SELECT 1 FROM public.almanac_slo_series_tile_work WHERE slo_id=ANY($2::uuid[]) AND range_end<=$3) OR
 EXISTS(SELECT 1 FROM public.almanac_slos s JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
   WHERE s.id=ANY($2::uuid[]) AND NOT EXISTS(SELECT 1 FROM public.almanac_slo_current_projections p
     JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
     WHERE p.slo_id=s.id AND g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision
       AND g.evaluated_through>=to_timestamp(floor(extract(epoch FROM CURRENT_TIMESTAMP)/d.cadence_seconds)*d.cadence_seconds))),
 EXISTS(SELECT 1 FROM public.almanac_derived_dirty_ranges WHERE sli_id=ANY($1::uuid[]) AND range_start<$3 AND attempts>=5) OR
 EXISTS(SELECT 1 FROM public.almanac_slo_series_repair WHERE slo_id=ANY($2::uuid[]) AND cursor_at<$3 AND attempts>=5) OR
 EXISTS(SELECT 1 FROM public.almanac_slo_series_tile_work WHERE slo_id=ANY($2::uuid[]) AND range_end<=$3 AND attempts>=5) OR
 EXISTS(SELECT 1 FROM public.almanac_slo_current_failures WHERE slo_id=ANY($2::uuid[]) AND attempts>=5)`, fleet.slis, fleet.objectives, fleet.historyEnd).Scan(&pending, &failed)
		cancel()
		if err != nil {
			return time.Since(started), errors.New("observe automatic historical maintenance failed")
		}
		if failed {
			var detail []byte
			bounded, stop := context.WithTimeout(ctx, 5*time.Second)
			err := db.QueryRow(bounded, `SELECT coalesce(jsonb_agg(evidence),'[]'::jsonb) FROM (
 SELECT jsonb_build_object('stage','source','attempts',attempts,'error',last_error,'start',range_start,'lease_until',lease_until) evidence
 FROM public.almanac_derived_dirty_ranges WHERE sli_id=ANY($1::uuid[]) AND range_start<$3 AND attempts>=5
 UNION ALL SELECT jsonb_build_object('stage','repair','attempts',attempts,'error',last_error,'cursor',cursor_at,'end',end_at,'lease_until',lease_until)
 FROM public.almanac_slo_series_repair WHERE slo_id=ANY($2::uuid[]) AND cursor_at<$3 AND attempts>=5
 UNION ALL SELECT jsonb_build_object('stage','tile','attempts',attempts,'error',last_error,'start',range_start,'lease_until',lease_until)
 FROM public.almanac_slo_series_tile_work WHERE slo_id=ANY($2::uuid[]) AND range_end<=$3 AND attempts>=5
 UNION ALL SELECT jsonb_build_object('stage','current','attempts',attempts,'error',last_error)
 FROM public.almanac_slo_current_failures WHERE slo_id=ANY($2::uuid[]) AND attempts>=5
 LIMIT 20) failures`, fleet.slis, fleet.objectives, fleet.historyEnd).Scan(&detail)
			stop()
			if err != nil {
				return time.Since(started), errors.New("automatic historical maintenance failed; queue diagnostics unavailable")
			}
			logs := fleet.fixture.backend.deployment.output(t, "logs", "--no-color", "--tail", "80", "postgres")
			categories, err := marshalPerformanceEvidence(performancePostgresFailureCounts(logs))
			clear(logs)
			if err != nil {
				return time.Since(started), errors.New("encode maintenance failure categories failed")
			}
			return time.Since(started), fmt.Errorf("automatic historical maintenance exhausted a work budget: queues=%s postgres_error_counts=%s", detail, categories)
		}
		if !pending {
			return time.Since(started), nil
		}
		select {
		case <-ctx.Done():
			return time.Since(started), ctx.Err()
		case <-ticks.C:
		}
	}
}
