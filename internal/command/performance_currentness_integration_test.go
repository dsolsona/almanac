//go:build integration

package command

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/jackc/pgx/v5"
)

type performanceCurrentnessSample struct {
	Kind       string        `json:"kind"`
	SloID      string        `json:"slo_id"`
	Through    time.Time     `json:"through"`
	Source     int64         `json:"source_generation"`
	EarliestAt time.Time     `json:"earliest_at"`
	VisibleAt  time.Time     `json:"visible_at"`
	UpperBound time.Duration `json:"lag_upper_bound_ns"`
}

type performanceCurrentness struct {
	mu        sync.Mutex
	started   time.Time
	nextClock time.Time
	seen      map[string]time.Time
	pending   map[string][]performanceCurrentnessSample
	completed []performanceCurrentnessSample
}

func newPerformanceCurrentness(now time.Time) *performanceCurrentness {
	return &performanceCurrentness{started: now.UTC(), nextClock: now.UTC().Truncate(time.Minute).Add(time.Minute),
		seen: make(map[string]time.Time), pending: make(map[string][]performanceCurrentnessSample)}
}

func (c *performanceCurrentness) discover(ctx context.Context, db *pgx.Conn, objectives []string, now time.Time) error {
	// Recorded-at is before canonical commit. Measuring through a successful
	// authenticated status response therefore gives a conservative upper bound
	// on commit-to-visible lag, rather than guessing a commit timestamp.
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := db.Query(bounded, `SELECT s.id::text,source.generation,latest.interval_end,latest.recorded_at
FROM public.almanac_slos s
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
JOIN public.almanac_sli_source_generations source ON source.sli_id=d.sli_id AND source.measurement_revision=d.measurement_revision
JOIN LATERAL (
 SELECT interval_end,recorded_at FROM public.almanac_sli_coverage
 WHERE sli_id=d.sli_id AND measurement_revision=d.measurement_revision
 AND interval_start >= $2 AND recorded_at >= $3
 ORDER BY interval_start DESC LIMIT 1
) latest ON true WHERE s.id=ANY($1::uuid[])`, objectives, c.started.Truncate(time.Minute), c.started)
	if err != nil {
		return errors.New("discover currentness source commits failed")
	}
	defer rows.Close()
	var discovered []performanceCurrentnessSample
	for rows.Next() {
		var sample performanceCurrentnessSample
		sample.Kind = "receipt"
		if err := rows.Scan(&sample.SloID, &sample.Source, &sample.Through, &sample.EarliestAt); err != nil {
			return errors.New("decode currentness source commits failed")
		}
		discovered = append(discovered, sample)
	}
	if err := rows.Err(); err != nil {
		return errors.New("iterate currentness source commits failed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sample := range discovered {
		if sample.Through.After(c.seen[sample.SloID]) {
			c.seen[sample.SloID] = sample.Through
			c.pending[sample.SloID] = append(c.pending[sample.SloID], sample)
		}
	}
	for !c.nextClock.After(now) {
		for _, id := range objectives {
			c.pending[id] = append(c.pending[id], performanceCurrentnessSample{Kind: "clock", SloID: id, Through: c.nextClock, EarliestAt: c.nextClock})
		}
		c.nextClock = c.nextClock.Add(time.Minute)
	}
	return nil
}

func (c *performanceCurrentness) observe(status api.SloCurrentStatus, now time.Time) error {
	if checkPerformanceGeneration(status.Generation) != nil || !status.Current.EvaluationAt.Equal(status.Generation.EvaluatedThrough) {
		return errors.New("currentness response has inconsistent provenance")
	}
	source, err := strconv.ParseInt(status.Generation.SourceGeneration, 10, 64)
	if err != nil {
		return errors.New("currentness response has invalid source generation")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id := status.Slo.Id.String()
	pending := c.pending[id]
	retained := pending[:0]
	for _, sample := range pending {
		if source >= sample.Source && !status.Generation.EvaluatedThrough.Before(sample.Through) {
			sample.VisibleAt, sample.UpperBound = now.UTC(), now.Sub(sample.EarliestAt)
			c.completed = append(c.completed, sample)
		} else {
			retained = append(retained, sample)
		}
	}
	c.pending[id] = retained
	return nil
}

func (c *performanceCurrentness) evidence(now time.Time) ([]performanceCurrentnessSample, map[string]performanceDistribution, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	completed := append([]performanceCurrentnessSample(nil), c.completed...)
	durations := map[string][]time.Duration{"receipt": {}, "clock": {}}
	for _, sample := range completed {
		durations[sample.Kind] = append(durations[sample.Kind], sample.UpperBound)
	}
	unfinished := 0
	for _, pending := range c.pending {
		for _, sample := range pending {
			if now.Sub(sample.EarliestAt) > 10*time.Second {
				unfinished++
			}
		}
	}
	distributions := make(map[string]performanceDistribution)
	var resultErr error
	for _, kind := range []string{"receipt", "clock"} {
		distribution := performancePercentiles(durations[kind])
		distributions[kind] = distribution
		if distribution.Count < 100 || distribution.P95 > 10*time.Second {
			resultErr = errors.New("currentness samples do not establish the ten-second p95 budget")
		}
	}
	if unfinished != 0 {
		resultErr = errors.New("currentness observations remained unavailable beyond ten seconds")
	}
	return completed, distributions, unfinished, resultErr
}
