package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/storage/dbgen"
)

func TestCurrentCacheExpiresAtFreshnessDeadlineWithoutRepublishingStaleForever(t *testing.T) {
	id, _ := canonicalUUID("00000000-0000-4000-8000-000000000010")
	row := dbgen.GetSloRow{SliID: id, CadenceSeconds: 60}
	source := dbgen.GetSloOperationalContextRow{SettlingDelaySeconds: 60}
	last := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	at := last.Add(2*time.Minute + sli.DispatchPhase(id.String(), 60))
	quality := api.SloOperationalQuality{State: "complete", Freshness: "fresh", LastObservationAt: &last}
	if got := currentCacheDeadline(row, source, at, quality); !got.Equal(at.Add(time.Microsecond)) {
		t.Fatalf("fresh cache deadline=%s want=%s", got, at.Add(time.Microsecond))
	}
	quality.Freshness = "stale"
	at = at.Add(time.Microsecond)
	if got := currentCacheDeadline(row, source, at, quality); !got.Equal(at.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatalf("stale cache must not remain continuously due: %s", got)
	}
}
