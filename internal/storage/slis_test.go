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

func TestSliListCursorBindsNormalizedUnicodeFiltersAndPreservesLegacyCursor(t *testing.T) {
	when := time.Date(2026, 10, 29, 12, 30, 0, 0, time.UTC)
	itemID := "8ed93d7e-51e3-4a3e-9222-3e8a612de20a"
	datasourceID := "b13e0739-3c86-4a7f-b7e5-c15e9626e71c"
	limit, _, filter, query, datasource, err := validateSliListPage(50, "", "  Café  ", datasourceID)
	if err != nil || limit != 50 || query != "café" || !datasource.Valid {
		t.Fatalf("normalized SLI filters: limit=%d query=%q datasource=%+v err=%v", limit, query, datasource, err)
	}
	encoded, err := encodeSliListCursor(when, itemID, filter)
	if err != nil {
		t.Fatal(err)
	}
	limit, position, sameFilter, query, datasource, err := validateSliListPage(50, encoded, "Café", datasourceID)
	if err != nil || limit != 50 || sameFilter != filter || query != "café" || position.ID != itemID || !position.Time.Equal(when) || !datasource.Valid {
		t.Fatalf("Unicode filter cursor roundtrip: limit=%d position=%+v query=%q datasource=%+v err=%v", limit, position, query, datasource, err)
	}
	for _, change := range []struct{ query, datasource string }{{"cafe", datasourceID}, {"café", itemID}} {
		if _, _, _, _, _, err := validateSliListPage(50, encoded, change.query, change.datasource); err == nil {
			t.Fatalf("cursor accepted changed filters query=%q datasource=%q", change.query, change.datasource)
		}
	}
	legacy, err := encodeAgentCursor(agentCursor{Version: 1, Kind: "slis", Time: when, ID: itemID})
	if err != nil {
		t.Fatal(err)
	}
	if _, position, _, _, _, err := validateSliListPage(50, legacy, "", ""); err != nil || position.ID != itemID {
		t.Fatalf("legacy unfiltered cursor rejected: %+v, %v", position, err)
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
