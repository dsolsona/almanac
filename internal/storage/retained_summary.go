package storage

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
)

type retainedTileSummary struct {
	SourceGeneration int64             `json:"source_generation"`
	Transitions      [4]int32          `json:"transitions"`
	Wire             map[string]string `json:"wire,omitempty"`
}

func marshalRetainedTileSummary(tile retainedSloTile, start, end time.Time, target string, generation int64) ([]byte, error) {
	fine, err := compactRetainedTile(tile, start, end, target)
	if err != nil {
		return nil, err
	}
	summary := retainedTileSummary{SourceGeneration: generation}
	for index, lane := range [][]api.SloOperationalSample{fine.Attainment, fine.Budget, fine.Burn1h, fine.Burn6h} {
		for _, sample := range lane {
			if len(sample.RetainedExtrema) == 0 {
				summary.Transitions[index]++
			}
		}
	}
	if summary.Transitions != ([4]int32{}) {
		compact, err := compactRetainedTileLimit(fine, start, end, target, 4)
		if err != nil {
			return nil, err
		}
		summary.Wire = make(map[string]string, 5)
		for name, value := range map[string]any{"attainment": compact.Attainment, "budget": compact.Budget, "burn_1h": compact.Burn1h, "burn_6h": compact.Burn6h, "compliance": compact.Compliance} {
			body, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			summary.Wire[name] = string(body)
		}
	}
	return json.Marshal(summary)
}

func prepareStoredRetainedTileSummary(ctx context.Context, q *dbgen.Queries, work dbgen.AlmanacSloSeriesTileWork, target string) error {
	stored, err := q.ReadRetainedTileSummaryInput(ctx, work.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tile, err := decodeRetainedTile(stored.Payload, retainedReadPart{start: work.RangeStart.Time.UTC(), end: work.RangeEnd.Time.UTC(), resolution: work.ResolutionSeconds}, strconv.FormatInt(work.DefinitionRevision, 10))
	if err != nil {
		return err
	}
	payload, err := marshalRetainedTileSummary(tile, work.RangeStart.Time.UTC(), work.RangeEnd.Time.UTC(), target, stored.SourceGeneration)
	if err != nil {
		return err
	}
	_, err = q.PublishRetainedTileSummary(ctx, dbgen.PublishRetainedTileSummaryParams{ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch, SourceGeneration: stored.SourceGeneration, CompactPayload: payload})
	return err
}

func (r *Runtime) queueRetainedTileSummaries(ctx context.Context, now time.Time) (int64, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := r.Acquire(bounded, DerivedTraffic)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	return dbgen.New(conn).QueueRetainedTileSummaries(bounded, dbgen.QueueRetainedTileSummariesParams{CalculationVersion: operationalCalculationVersion, RetentionStart: timestamp(now.Add(-sli.ObservationRetention)), Now: timestamp(now)})
}
