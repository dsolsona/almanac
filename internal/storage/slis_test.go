package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/sli"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestSliStatusSeparatesActivationAndPhaseAwareFreshness(t *testing.T) {
	base := time.Date(2026, 10, 28, 13, 45, 20, 0, time.UTC)
	id := uuidToPG(uuid.MustParse("00000000-0000-4000-8000-000000000011"))
	validFrom := base.Add(-2 * time.Minute)
	phase := sli.DispatchPhase(id.String(), 60)
	end := time.Date(2026, 10, 28, 13, 44, 0, 0, time.UTC)
	latest := &SliObservationRecord{IntervalEnd: end, Outcome: string(sli.OutcomeObserved), RecordedAt: base}
	revisions := pgtype.Int8{Int64: 1, Valid: true}
	build := func(activation pgtype.Timestamptz, now time.Time, observation *SliObservationRecord) SliRecord {
		return sliRecordFromFields(id, "name", "Name", 1, 1, id, 1, id, "event_counts", "interval_count", "good", "total", 60, 60,
			timestamp(validFrom), activation, timestamp(base), timestamp(base), true, timestamp(base), timestamp(base), pgtype.Text{}, revisions, revisions,
			pgtype.Text{}, pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Timestamptz{}, observation, nil, now)
	}
	pending := build(pgtype.Timestamptz{}, base, nil)
	if pending.ActivationState != "preparing" || pending.ActivatedAt != nil || pending.NextExpectedExecutionAt != nil {
		t.Fatalf("pending status %+v", pending)
	}
	due := end.Add(2*time.Minute + phase)
	active := build(timestamp(base), due, latest)
	if active.ActivatedAt == nil || !active.ActivatedAt.Equal(base) || active.ActivationState != "active" || active.Freshness != "fresh" || active.NextExpectedExecutionAt == nil || !active.NextExpectedExecutionAt.Equal(due) {
		t.Fatalf("active status %+v, due %v", active, due)
	}
	if late := build(timestamp(base), due.Add(time.Microsecond), latest); late.Freshness != "stale" {
		t.Fatalf("late freshness %+v", late)
	}
}

func TestSLIActivationBoundaryIsFutureCanonicalBoundary(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"on boundary", time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 27, 12, 0, 15, 0, time.UTC)},
		{"between boundaries", time.Date(2026, 10, 27, 12, 0, 8, 400_000_000, time.UTC), time.Date(2026, 10, 27, 12, 0, 30, 0, time.UTC)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := nextSliActivationBoundary(test.now, 15)
			if !got.Equal(test.want) || !got.After(test.now) || got.Unix()%15 != 0 {
				t.Fatalf("activation boundary = %s, want future canonical %s", got, test.want)
			}
		})
	}
}

func TestMigrationSQLStateDiagnosticRejectsUntrustedText(t *testing.T) {
	for _, value := range []string{"42P01", "23505"} {
		if !safeSQLState(value) {
			t.Errorf("valid SQLSTATE %q rejected", value)
		}
	}
	for _, value := range []string{"", "secret", "42p01", "42P01\nsecret", "abcde", "1234"} {
		if safeSQLState(value) {
			t.Errorf("unsafe SQLSTATE %q accepted", value)
		}
	}
}
