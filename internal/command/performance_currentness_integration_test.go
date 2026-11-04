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
	Measurement   string        `json:"measurement"`
	Kind          string        `json:"kind"`
	SloID         string        `json:"slo_id"`
	Through       time.Time     `json:"through"`
	Source        int64         `json:"source_generation"`
	EarliestAt    time.Time     `json:"earliest_at"`
	VisibleAt     time.Time     `json:"visible_at"`
	UpperBound    time.Duration `json:"lag_upper_bound_ns"`
	LowerBound    time.Duration `json:"lag_lower_bound_ns"`
	PreviousProbe *time.Time    `json:"previous_probe_started_at,omitempty"`
}

type performanceCurrentness struct {
	mu          sync.Mutex
	started     time.Time
	measurement string
	nextClock   time.Time
	seen        map[string]int64
	probed      map[string]performanceCurrentnessProbe
	pending     map[string][]performanceCurrentnessSample
	completed   []performanceCurrentnessSample
}

type performanceCurrentnessProbe struct {
	started, through time.Time
	source           int64
}

func newPerformanceCurrentness(now time.Time, measurement string) *performanceCurrentness {
	return &performanceCurrentness{started: now.UTC(), nextClock: now.UTC().Truncate(time.Minute).Add(time.Minute),
		measurement: measurement, seen: make(map[string]int64), probed: make(map[string]performanceCurrentnessProbe), pending: make(map[string][]performanceCurrentnessSample)}
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
		if sample.Source > c.seen[sample.SloID] {
			c.seen[sample.SloID] = sample.Source
			sample.Measurement = c.measurement
			c.pending[sample.SloID] = append(c.pending[sample.SloID], sample)
		}
	}
	for !c.nextClock.After(now) {
		for _, id := range objectives {
			c.pending[id] = append(c.pending[id], performanceCurrentnessSample{Measurement: c.measurement, Kind: "clock", SloID: id, Through: c.nextClock, EarliestAt: c.nextClock})
		}
		c.nextClock = c.nextClock.Add(time.Minute)
	}
	return nil
}

func (c *performanceCurrentness) observe(status api.SloCurrentStatus, probeStarted, now time.Time) error {
	if checkPerformanceGeneration(status.Generation) != nil || !status.Current.EvaluationAt.Equal(status.Generation.EvaluatedThrough) {
		return errors.New("currentness response has inconsistent provenance")
	}
	source, err := strconv.ParseInt(status.Generation.SourceGeneration, 10, 64)
	if err != nil {
		return errors.New("currentness response has invalid source generation")
	}
	c.observeProgress(status.Slo.Id.String(), source, status.Generation.EvaluatedThrough, probeStarted, now)
	return nil
}

func (c *performanceCurrentness) observeProgress(id string, source int64, through, probeStarted, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending[id]
	retained := pending[:0]
	for _, sample := range pending {
		if source >= sample.Source && !through.Before(sample.Through) {
			previous := c.probed[id]
			sample.VisibleAt = now.UTC()
			sample.LowerBound, sample.UpperBound = performanceLagBounds(sample.EarliestAt, now, previous.started, previous.source >= sample.Source && !previous.through.Before(sample.Through))
			if !previous.started.IsZero() {
				sample.PreviousProbe = new(previous.started.UTC())
			}
			c.completed = append(c.completed, sample)
		} else {
			retained = append(retained, sample)
		}
	}
	c.pending[id] = retained
	c.probed[id] = performanceCurrentnessProbe{started: probeStarted, source: source, through: through}
}

func (c *performanceCurrentness) observePublished(ctx context.Context, db *pgx.Conn, objectives []string) error {
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	started := time.Now()
	rows, err := db.Query(bounded, `SELECT p.slo_id::text,g.source_generation,g.evaluated_through
FROM public.almanac_slo_current_projections p
JOIN public.almanac_slo_derived_generations g ON g.id=p.generation_id
JOIN public.almanac_slos s ON s.id=p.slo_id
JOIN public.almanac_slo_definitions d ON d.slo_id=s.id AND d.revision=s.definition_revision
WHERE p.slo_id=ANY($1::uuid[]) AND g.definition_revision=d.revision AND g.measurement_revision=d.measurement_revision`, objectives)
	if err != nil {
		return errors.New("observe published current generations failed")
	}
	defer rows.Close()
	type progress struct {
		id      string
		source  int64
		through time.Time
	}
	var published []progress
	for rows.Next() {
		var value progress
		if err := rows.Scan(&value.id, &value.source, &value.through); err != nil {
			return errors.New("decode published current generations failed")
		}
		if value.source <= 0 || value.through.IsZero() {
			return errors.New("publication observer found invalid generation provenance")
		}
		published = append(published, value)
	}
	if rows.Err() != nil || len(published) != len(objectives) {
		return errors.New("publication observer omitted maintained objectives")
	}
	observed := time.Now()
	for _, value := range published {
		c.observeProgress(value.id, value.source, value.through, started, observed)
	}
	return nil
}

func (c *performanceCurrentness) evidence(now time.Time) ([]performanceCurrentnessSample, map[string]performanceDistribution, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	completed := append([]performanceCurrentnessSample(nil), c.completed...)
	durations := map[string][]time.Duration{"receipt": {}, "clock": {}}
	var resultErr error
	for _, sample := range completed {
		if sample.UpperBound < 0 || sample.LowerBound < 0 || sample.LowerBound > sample.UpperBound {
			resultErr = errors.New("currentness clock/probe bounds are inconsistent")
		}
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
