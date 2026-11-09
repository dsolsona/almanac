//go:build integration

package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationRetainedTileWaitsForHourClose(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "tile-due", true)
	id := mustPGUUID(s.createSLO(t, "tile-due"))
	row, err := dbgen.New(s.conn).GetSlo(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	hour := s.at.Truncate(time.Hour)
	published := hour.Add(7 * time.Minute)
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "repair"}[batch], func(t *testing.T) {
			tx, err := s.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTransaction(tx)
			q := dbgen.New(tx)
			if batch {
				_, err = q.PublishRetainedSloPointBatch(t.Context(), dbgen.PublishRetainedSloPointBatchParams{
					SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
					CalculationVersion: operationalCalculationVersion, SourceGeneration: 1, PublishedAt: timestamp(published),
					EvaluationAts: []pgtype.Timestamptz{timestamp(published)}, Payloads: []string{`{}`},
				})
			} else {
				_, err = q.PublishRetainedSloPoint(t.Context(), dbgen.PublishRetainedSloPointParams{
					SloID: id, DefinitionRevision: row.DefinitionRevision, MeasurementRevision: row.MeasurementRevision,
					CalculationVersion: operationalCalculationVersion, SourceGeneration: 1, PublishedAt: timestamp(published),
					EvaluationAt: timestamp(published), Payload: []byte(`{}`),
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := q.ClaimRetainedTileWork(t.Context(), dbgen.ClaimRetainedTileWorkParams{
				Now: timestamp(published), LeaseUntil: timestamp(published.Add(time.Minute)),
			}); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("open-hour tile claimed before its hour closed: %v", err)
			}
			for _, expected := range []struct {
				resolution int32
				due        time.Time
			}{{3600, hour.Add(time.Hour)}, {86400, utcDayStart(hour).Add(24 * time.Hour)}} {
				work, err := q.ClaimRetainedTileWork(t.Context(), dbgen.ClaimRetainedTileWorkParams{
					Now: timestamp(expected.due), LeaseUntil: timestamp(expected.due.Add(time.Minute)),
				})
				if err != nil || work.ResolutionSeconds != expected.resolution || !work.NextAttemptAt.Time.Equal(expected.due) {
					t.Fatalf("expected %ds tile due at %s: %+v, %v", expected.resolution, expected.due, work, err)
				}
				if _, err := q.AcknowledgeRetainedTileWork(t.Context(), dbgen.AcknowledgeRetainedTileWorkParams{
					ID: work.ID, Version: work.Version, LeaseEpoch: work.LeaseEpoch,
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestIntegrationOpenHourDetailKeepsExactPointsWithoutATile(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "open-hour", true)
	id := s.createSLO(t, "open-hour")
	s.now = s.at.Add(7 * time.Minute)
	s.drain(t, "open-hour source")
	if err := s.runtime.refreshCurrentProjections(t.Context(), s.clock); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		worked, err := s.runtime.repairRetainedSeries(t.Context(), s.clock)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	var verifier [32]byte
	verifier[0] = 98
	value, err := ReadSloOperationalDetail(t.Context(), s.f.databaseURL, OperationalDetailInput{
		Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, ID: id, Period: "7d", Now: s.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	var last *api.SloOperationalSample
	for i := range value.BudgetSeries.Samples {
		if value.BudgetSeries.Samples[i].EvaluationAt.Equal(s.now) {
			last = &value.BudgetSeries.Samples[i]
		}
	}
	// Seven missing minutes leave 53 observed intervals, each with 990 good of 1,000.
	if last == nil || last.Good == nil || *last.Good != "52470.000000000000" ||
		last.Total == nil || *last.Total != "53000.000000000000" ||
		last.Quality.State != "partial" || last.Quality.ObservedIntervals != 53 || last.Quality.MissingIntervals != 7 {
		t.Fatalf("open-hour graph lost exact points or hid missing observations: %+v", last)
	}
	var tiles int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM public.almanac_slo_series_tiles
WHERE slo_id=$1 AND resolution_seconds=3600 AND range_start=$2`, id, s.now.Truncate(time.Hour)).Scan(&tiles); err != nil || tiles != 0 {
		t.Fatalf("open-hour read required a prematurely published tile: %d, %v", tiles, err)
	}
}
