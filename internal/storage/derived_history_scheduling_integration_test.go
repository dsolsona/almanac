//go:build integration

package storage

import (
	"log/slog"
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestIntegrationHistoryPendingTurnWaitsAfterCompletion(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "history-pending-pause", true)
	id := s.createSLOWithHistory(t, "history-pending-pause", 24*time.Hour, s.historyStart)
	s.drain(t, "history pending-pause source")
	hour := s.historyStart.Add(time.Hour)
	row, err := dbgen.New(s.conn).GetSlo(t.Context(), mustPGUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98 // Reuse the authenticated session created by createSLOWithHistory.
	_, healthy, err := CreateObjective(t.Context(), s.f.databaseURL, CreateObjectiveInput{
		Proof: DatasourceProof{SessionVerifier: verifier}, Name: "history-ready-neighbor", DisplayName: "Ready neighbor",
		ServiceID: row.ServiceID.String(), SliID: s.sliID,
		Target: "0.99", RollingWindowSeconds: 3600, EvaluationValidFrom: new(hour.Add(time.Hour)),
		IdempotencyKey: "history-ready-neighbor", RequestID: "history-ready-neighbor", Now: s.clock,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	healthyID := healthy.ID
	if _, err := s.conn.Exec(t.Context(), `INSERT INTO public.almanac_derived_dirty_ranges
(sli_id,measurement_revision,generation,range_start,range_end,next_attempt_at,updated_at)
VALUES($1,1,1,$2,$2::timestamptz+interval '1 hour',$3,$3)`, s.sliID, hour, s.now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_extrema_cursor
SET built_through=$2,dirty_from=NULL,dirty_to=NULL,next_attempt_at=$3 WHERE slo_id=$1`, id, hour, s.now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	began := s.now
	finished := began.Add(3 * time.Second)
	calls := 0
	clock := func() time.Time {
		calls++
		if calls <= 2 {
			return began
		}
		return finished
	}
	next := s.runtime.runHistoryTurn(t.Context(), slog.Default(), clock)
	if next.Before(finished.Add(100 * time.Millisecond)) {
		t.Errorf("pending work consumed its own retry pause: finished=%s next=%s", finished, next)
	}
	if s.runtime.pools[DerivedTraffic].Stat().AcquiredConns() != 0 {
		t.Error("history turn held a connection while waiting")
	}
	var through, retry time.Time
	var attempts, hours int
	var leased bool
	if err := s.conn.QueryRow(t.Context(), `SELECT built_through,next_attempt_at,attempts,lease_until IS NOT NULL,
(SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1)
FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&through, &retry, &attempts, &leased, &hours); err != nil {
		t.Fatal(err)
	}
	if !through.Equal(hour) || !retry.Equal(finished.Add(time.Second)) || attempts != 0 || leased || hours != 0 {
		t.Fatalf("pending turn changed durable progress/retry: through=%s retry=%s attempts=%d lease=%v hours=%d", through, retry, attempts, leased, hours)
	}
	// The paused worker may service another ready SLO before the pending cursor's
	// durable retry deadline; one blocked source must not stop healthy history.
	s.now = finished.Add(100 * time.Millisecond)
	next = s.runtime.runHistoryTurn(t.Context(), slog.Default(), s.clock)
	if !next.Equal(s.now) {
		t.Fatalf("ready neighbor did not drain immediately: %s", next)
	}
	if err := s.conn.QueryRow(t.Context(), `SELECT built_through,
(SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600)
FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, healthyID).Scan(&through, &hours); err != nil {
		t.Fatal(err)
	}
	if !through.Equal(s.now.Truncate(time.Hour)) || hours != 1 {
		t.Fatalf("pending source blocked ready neighbor: %s, %d", through, hours)
	}
	s.drain(t, "ready after pending history")
	s.now = finished.Add(time.Second)
	next = s.runtime.runHistoryTurn(t.Context(), slog.Default(), s.clock)
	if !next.Equal(s.now) {
		t.Fatalf("successful history was unnecessarily delayed: %s", next)
	}
	if s.runtime.pools[DerivedTraffic].Stat().AcquiredConns() != 0 {
		t.Error("successful turn held a connection")
	}
	if err := s.conn.QueryRow(t.Context(), `SELECT built_through,
(SELECT count(*) FROM public.almanac_slo_extrema WHERE slo_id=$1 AND resolution_seconds=3600)
FROM public.almanac_slo_extrema_cursor WHERE slo_id=$1`, id).Scan(&through, &hours); err != nil {
		t.Fatal(err)
	}
	if !through.Equal(s.now.Truncate(time.Hour)) || hours != 2 {
		t.Fatalf("ready hours did not publish after deferral: %s, %d", through, hours)
	}
	var marks, wrong int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE
(mark->>'good')::numeric*100<>(mark->>'total')::numeric*99)
FROM public.almanac_slo_extrema e CROSS JOIN LATERAL jsonb_each(e.lanes) lane
CROSS JOIN LATERAL jsonb_array_elements(lane.value->'marks') mark
WHERE e.slo_id=$1 AND e.resolution_seconds=3600`, id).Scan(&marks, &wrong); err != nil {
		t.Fatal(err)
	}
	if marks == 0 || wrong != 0 {
		t.Fatalf("recovered history lost exact 990/1000 inputs: marks=%d wrong=%d", marks, wrong)
	}
}
