//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIntegrationEmptyDerivedFleetSleepsForOneSecond(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t)
	conn := integrationConnect(t, f.databaseURL)
	defer closeIntegrationConn(conn)
	q := dbgen.New(conn)
	now := f.base.Add(time.Second)
	later := timestamp(now.Add(time.Minute))

	current, err := q.ReadDerivedDue(t.Context(), dbgen.ReadDerivedDueParams{
		Now: timestamp(now), CalculationVersion: operationalCalculationVersion, MetricsAt: later,
	})
	if err != nil || current.AggregateDue || current.CurrentDue || current.MetricsDue || !current.NextDue.Time.Equal(now.Add(time.Second)) {
		t.Fatalf("empty aggregate/current fleet did not sleep: %+v, %v", current, err)
	}
	extrema, err := q.ReadExtremaDue(t.Context(), dbgen.ReadExtremaDueParams{
		Now: timestamp(now), CalculationVersion: operationalCalculationVersion,
	})
	if err != nil || extrema.EnsureCursorsDue || extrema.ExtremaDue || !extrema.NextDue.Time.Equal(now.Add(time.Second)) {
		t.Fatalf("empty extrema fleet did not sleep: %+v, %v", extrema, err)
	}
	maintenance, err := q.ReadDerivedMaintenanceDue(t.Context(), dbgen.ReadDerivedMaintenanceDueParams{
		Now: timestamp(now), DiscoveryAt: later, PartitionsAt: later, ConsistencyAt: later,
	})
	if err != nil || maintenance.CleanupDue || maintenance.RetentionDue || maintenance.DiscoveryDue ||
		maintenance.PartitionsDue || maintenance.ConsistencyDue || !maintenance.NextDue.Time.Equal(now.Add(time.Second)) {
		t.Fatalf("empty maintenance fleet did not sleep: %+v, %v", maintenance, err)
	}
}

func TestIntegrationExtremaDueHonorsBackoffAndActiveLease(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "extrema-due", true)
	id := s.createSLO(t, "extrema-due")
	q := dbgen.New(s.conn)
	for _, test := range []struct {
		name     string
		attempts int
		next     time.Time
		lease    pgtype.Timestamptz
		dueAt    time.Time
	}{
		{"backoff", 5, s.now.Add(5 * time.Minute), pgtype.Timestamptz{}, s.now.Add(5 * time.Minute)},
		{"active lease", 1, s.now.Add(-time.Second), timestamp(s.now.Add(30 * time.Second)), s.now.Add(30 * time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor
SET built_through=$2,next_attempt_at=$3,lease_until=$4,attempts=$5,failing_since=$6
WHERE slo_id=$1`, id, s.historyStart.Truncate(time.Hour), test.next, test.lease, test.attempts, s.now.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			waiting, err := q.ReadExtremaDue(t.Context(), dbgen.ReadExtremaDueParams{
				Now: timestamp(s.now), CalculationVersion: operationalCalculationVersion,
			})
			if err != nil || waiting.EnsureCursorsDue || waiting.ExtremaDue || !waiting.NextDue.Time.Equal(s.now.Add(time.Second)) {
				t.Fatalf("extrema work ignored its scheduled wait: %+v, %v", waiting, err)
			}
			due, err := q.ReadExtremaDue(t.Context(), dbgen.ReadExtremaDueParams{
				Now: timestamp(test.dueAt), CalculationVersion: operationalCalculationVersion,
			})
			if err != nil || due.EnsureCursorsDue || !due.ExtremaDue || !due.NextDue.Time.Equal(test.dueAt) {
				t.Fatalf("extrema work did not wake at its due time: %+v, %v", due, err)
			}
		})
	}
}
