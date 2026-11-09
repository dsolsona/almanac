//go:build integration

package storage

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/dsolsona/almanac/internal/api"
)

func TestIntegrationOverviewKeysetPagesLiveRows(t *testing.T) {
	t.Parallel()
	s := newSelfHealFixture(t, "keyset", true)
	template := s.createSLO(t, "keyset-template")
	ids, cleanup := cloneReviewObjectives(t, s.conn, template, "keyset-row", 25)
	defer cleanup()
	s.drain(t, "keyset source")
	for _, id := range ids {
		if err := s.runtime.refreshCurrentProjection(t.Context(), id, s.clock); err != nil {
			t.Fatal(err)
		}
	}
	// Ordering fixtures exercise all three urgency ranks independently of arithmetic.
	if _, err := s.conn.Exec(t.Context(), `WITH statuses AS (
 SELECT p.slo_id,CASE substring(s.name FROM '[0-9]+$')::integer%3
 WHEN 0 THEN 'needs_attention' WHEN 1 THEN 'watch' ELSE 'healthy' END AS urgency
 FROM public.almanac_slo_current_projections p JOIN public.almanac_slos s ON s.id=p.slo_id WHERE s.name LIKE 'keyset-row-%'
), changed AS (
 SELECT p.slo_id,v.urgency,jsonb_set(p.payload,'{urgency}',to_jsonb(v.urgency)) AS payload
	 FROM statuses v JOIN public.almanac_slo_current_projections p ON p.slo_id=v.slo_id
)
UPDATE public.almanac_slo_current_projections p SET urgency=c.urgency,payload=c.payload FROM changed c WHERE p.slo_id=c.slo_id`); err != nil {
		t.Fatal(err)
	}
	var verifier [32]byte
	verifier[0] = 98
	input := OperationalOverviewInput{Runtime: s.runtime, Proof: DatasourceProof{SessionVerifier: verifier}, Query: "keyset-row-", Now: s.clock}
	want := make([]string, 0, 25)
	for rank := range 3 {
		for index := range 25 {
			if index%3 == rank {
				want = append(want, fmt.Sprintf("keyset-row-%03d", index))
			}
		}
	}
	for _, size := range []int{10, 20} {
		input.Limit, input.Cursor = size, ""
		got := []string{}
		var preceding []api.SloOperationalOverviewItem
		for pageIndex := range 3 {
			page, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
			if err != nil || page.GroupCounts != (api.SloUrgencyCounts{NeedsAttention: 9, Watch: 8, Healthy: 8}) {
				t.Fatalf("size %d page %d: counts=%+v err=%v", size, pageIndex, page.GroupCounts, err)
			}
			for _, item := range page.Items {
				got = append(got, item.Slo.Name)
			}
			if pageIndex > 0 {
				back := input
				back.Cursor = page.PreviousCursor
				previous, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, back)
				if err != nil || len(previous.Items) != len(preceding) {
					t.Fatalf("reverse page size=%d: %v", size, err)
				}
				for i := range preceding {
					if preceding[i].Slo.Id != previous.Items[i].Slo.Id {
						t.Fatal("reverse scan changed ordering")
					}
				}
			}
			if page.NextCursor == "" {
				break
			}
			preceding, input.Cursor = page.Items, page.NextCursor
		}
		if !slices.Equal(got, want) {
			t.Fatalf("size %d order=%v want=%v", size, got, want)
		}
	}
	input.Limit, input.Cursor = 10, ""
	first, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*OperationalOverviewInput){
		func(in *OperationalOverviewInput) { in.Query = "another" },
		func(in *OperationalOverviewInput) { in.ServiceID = first.Items[0].Slo.ServiceId.String() },
		func(in *OperationalOverviewInput) { in.Urgency = "healthy" },
		func(in *OperationalOverviewInput) { in.Limit = 20 },
	} {
		other := input
		other.Cursor = first.NextCursor
		change(&other)
		if _, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, other); !errors.Is(err, ErrOperationalSnapshot) {
			t.Fatalf("cursor accepted a different scope: %v", err)
		}
	}
	// A row moving past the cursor appears again at its new position, as ADR-0042 permits.
	moved := first.Items[0].Slo.Id
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_slo_current_projections SET urgency='healthy',
 payload=jsonb_set(payload,'{urgency}','"healthy"') WHERE slo_id=$1`, moved); err != nil {
		t.Fatal(err)
	}
	input.Cursor = first.NextCursor
	seen := false
	for range 3 {
		page, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input)
		if err != nil || page.GroupCounts.NeedsAttention != 8 || page.GroupCounts.Healthy != 9 {
			t.Fatalf("moving row did not update live counts: %+v %v", page.GroupCounts, err)
		}
		seen = seen || slices.ContainsFunc(page.Items, func(item api.SloOperationalOverviewItem) bool { return item.Slo.Id == moved })
		input.Cursor = page.NextCursor
		if input.Cursor == "" {
			break
		}
	}
	if !seen {
		t.Fatal("cursor retained the old urgency snapshot")
	}
	if _, err := s.conn.Exec(t.Context(), `UPDATE public.almanac_users SET enabled=false WHERE id=$1`, s.f.userID); err != nil {
		t.Fatal(err)
	}
	input.Cursor = first.NextCursor
	if _, err := ReadSloOperationalOverview(t.Context(), s.f.databaseURL, input); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked user traversal returned %v", err)
	}
	var obsolete int
	if err := s.conn.QueryRow(t.Context(), `SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace
 AND relname IN ('almanac_slo_overview_snapshots','almanac_slo_overview_snapshot_items','almanac_slo_projection_payloads','almanac_slo_history_admission')`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatalf("obsolete overview stores=%d err=%v", obsolete, err)
	}
}
