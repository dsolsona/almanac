//go:build integration

package command

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

type performancePreparationSample struct {
	Target  int           `json:"target"`
	Elapsed time.Duration `json:"elapsed_ns"`
	Pending int           `json:"pending_responses"`
	Failure string        `json:"failure,omitempty"`
	Body    []byte        `json:"-"`
}

// Preparation is an explicitly separate phase with at most ten active readers.
// Its pending responses and duration never enter the ready-response percentiles.
// The caller retains the complete report before applying an acceptance failure.
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
	for {
		requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
		request, err := reader.Request(requestContext, operation.Paths[target])
		if err != nil || request == nil {
			cancel()
			sample.Failure = "request_construction"
			break
		}
		response, err := reader.Client.Do(request)
		if err != nil {
			cancel()
			sample.Failure = "transport"
			break
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (20<<20)+1))
		closeErr := response.Body.Close()
		cancel()
		if readErr != nil || closeErr != nil || len(body) > 20<<20 {
			sample.Failure = "response_read_or_size"
			break
		}
		if response.StatusCode == http.StatusOK {
			if operation.Check(target, body) != nil {
				sample.Failure = "response_contract"
			} else {
				sample.Body = body
			}
			break
		}
		var problem api.Problem
		if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "2" ||
			json.Unmarshal(body, &problem) != nil || problem.Code != "derived_data_pending" || problem.Preparation == nil ||
			problem.Preparation.State != api.DerivedPreparationStatePending || problem.Preparation.RetryAfterSeconds != 2 {
			sample.Failure = "unexpected_preparation_response"
			break
		}
		sample.Pending++
		select {
		case <-ctx.Done():
			sample.Elapsed = time.Since(started)
			sample.Failure = "preparation_deadline"
			return sample
		case <-time.After(2 * time.Second):
		}
	}
	sample.Elapsed = time.Since(started)
	return sample
}
