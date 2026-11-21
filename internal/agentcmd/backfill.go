package agentcmd

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/prometheus"
	"github.com/dsolsona/almanac/internal/sli"
)

func (c *client) runBackfill(ctx context.Context, generation string) {
	delay := time.NewTimer(2 * time.Second)
	defer delay.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-delay.C:
		}
		next := 10 * time.Second
		var claim sli.BackfillClaim
		claimStatus := c.backfillRequest(ctx, generation, "claim", nil, &claim)
		if claimStatus == http.StatusNotFound {
			return
		}
		if claimStatus == http.StatusOK {
			if result, ok := c.executeBackfill(ctx, claim); ok {
				for range 3 {
					status := c.backfillRequest(ctx, generation, "result", &result, nil)
					if status == http.StatusNoContent || (status >= 400 && status < 500) ||
						!claim.LeaseUntil.After(c.currentServerTime()) || !waitBackfill(ctx, 200*time.Millisecond) {
						break
					}
				}
			}
			next = time.Second
		}
		delay.Reset(next)
	}
}

// Historical requests have their own bounded body and worker and never use the live upload queue.
func (c *client) backfillRequest(parent context.Context, generation, action string, result *sli.BackfillChunkResult, claim *sli.BackfillClaim) int {
	u, err := url.Parse(c.opts.backendURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return 0
	}
	u.Path = "/api/agent/v1/backfills/" + action
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var body io.Reader
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return 0
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Agent-Session-Generation", generation)
	req.Header.Set("X-Agent-Session-Proof", c.resume)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK && claim != nil {
		raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		if err != nil || json.Unmarshal(raw, claim) != nil {
			return 0
		}
	}
	return response.StatusCode
}

func (c *client) executeBackfill(parent context.Context, claim sli.BackfillClaim) (sli.BackfillChunkResult, bool) {
	result := sli.BackfillChunkResult{ChunkID: claim.ID, Generation: claim.Generation}
	if claim.ID == "" || claim.Cadence < 15 || claim.Cadence > 86400 || !claim.Start.Before(claim.End) {
		return result, false
	}
	cadence := time.Duration(claim.Cadence) * time.Second
	count := claim.End.Sub(claim.Start) / cadence
	if count < 1 || count > 256 || !claim.Start.Add(count*cadence).Equal(claim.End) {
		return result, false
	}
	duration := min(claim.LeaseUntil.Sub(c.currentServerTime()), 20*time.Second)
	if duration <= 0 {
		return result, false
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	definition, ok := c.collections.backfillDefinition(claim)
	if !ok {
		result.Failure = "assignment_revoked"
		return result, true
	}
	source, err := newCollectionSource(definition)
	if err != nil {
		result.Failure = "unsupported"
		return result, true
	}
	defer source.CloseIdleConnections()
	outcomes := make([]sli.ExecutionOutcome, 0, count)
	covered := make(map[int64]struct{}, len(claim.CoveredStarts))
	for _, start := range claim.CoveredStarts {
		covered[start.UnixMicro()] = struct{}{}
	}
	for end := claim.Start.Add(cadence); !end.After(claim.End); end = end.Add(cadence) {
		if _, exists := covered[end.Add(-cadence).UnixMicro()]; exists {
			// A zero outcome requests a server-side coverage check without a provider call.
			outcomes = append(outcomes, sli.ExecutionOutcome{})
			continue
		}
		outcome, retryAfter := c.backfillInterval(ctx, definition, source, end)
		result.RetryAfterSeconds = max(result.RetryAfterSeconds, retryAfter)
		outcomes = append(outcomes, outcome)
		if outcome.State == sli.OutcomeFailed && outcome.ErrorCode != "incomplete" {
			for len(outcomes) < int(count) {
				outcomes = append(outcomes, outcome)
			}
			break
		}
	}
	result.Outcomes = outcomes
	return result, parent.Err() == nil
}

func (r *collectionRunner) backfillDefinition(claim sli.BackfillClaim) (collectionDefinition, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	assignment, ok := r.assignments[claim.DatasourceID]
	if !ok || assignment.PollingIntervalSeconds != claim.Cadence || assignment.Provider != "prometheus" {
		return collectionDefinition{}, false
	}
	revision := strconv.FormatInt(claim.Revision, 10)
	for _, definition := range r.slis {
		if definition.SLIID == claim.SLIID && definition.MeasurementRevision == revision && definition.DatasourceID == claim.DatasourceID {
			return collectionDefinition{sli: definition, assignment: assignment, agentID: r.lease.AgentID}, true
		}
	}
	return collectionDefinition{}, false
}

func (c *client) backfillInterval(ctx context.Context, definition collectionDefinition, source *prometheus.Client, end time.Time) (sli.ExecutionOutcome, int) {
	failed := func(code string) sli.ExecutionOutcome {
		return sli.ExecutionOutcome{State: sli.OutcomeFailed, ErrorCode: code}
	}
	good, err := c.collections.queryBackfill(ctx, definition, source, definition.sli.GoodQuery, end)
	if err != nil {
		return failed(collectionErrorCode(err)), historicalRetryAfter(err)
	}
	total, err := c.collections.queryBackfill(ctx, definition, source, definition.sli.TotalQuery, end)
	if err != nil {
		return failed(collectionErrorCode(err)), historicalRetryAfter(err)
	}
	outcome := collectedOutcomeAt(good, true, good.Class == "incomplete", total, true, total.Class == "incomplete", end)
	if outcome == nil || (outcome.State != sli.OutcomeObserved && outcome.State != sli.OutcomeFailed) {
		return failed("incomplete"), 0
	}
	return *outcome, 0
}

func historicalRetryAfter(err error) int {
	if source, ok := errors.AsType[*prometheus.SourceError](err); ok {
		return source.RetryAfterSeconds
	}
	return 0
}

func (r *collectionRunner) queryBackfill(ctx context.Context, definition collectionDefinition, source *prometheus.Client, query string, at time.Time) (prometheus.QueryResult, error) {
	id := definition.assignment.DatasourceID
	for ctx.Err() == nil {
		r.mu.Lock()
		sameAssignment := r.assignments[id] == definition.assignment
		ready := r.clockSafe && !r.paused && r.activeSources[id] == 0
		if sameAssignment && ready {
			queryCtx, cancel := context.WithCancel(ctx)
			r.historicalSource, r.cancelHistorical = id, cancel
			r.mu.Unlock()
			result, err := source.QueryAt(queryCtx, query, at)
			cancel()
			r.mu.Lock()
			r.historicalSource, r.cancelHistorical = "", nil
			r.mu.Unlock()
			r.signalSchedule()
			return result, err
		}
		r.mu.Unlock()
		if !sameAssignment {
			return prometheus.QueryResult{}, context.Canceled
		}
		waitBackfill(ctx, 100*time.Millisecond)
	}
	return prometheus.QueryResult{}, ctx.Err()
}

func waitBackfill(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
