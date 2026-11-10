package storage

import (
	"testing"
	"time"

	"github.com/dsolsona/almanac/internal/storage/dbgen"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestCanonicalObservationIdentityUsesTheWholeCanonicalKey(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	sliID := "8748d63e-c657-4e3f-98df-35d0acafff22"
	id := canonicalObservationID(sliID, 1, start)
	if id != canonicalObservationID(sliID, 1, start.In(time.FixedZone("same instant", 3600))) {
		t.Fatal("identity depends on presentation timezone")
	}
	for _, other := range []struct {
		sli      string
		revision int64
		start    time.Time
	}{
		{"8748d63e-c657-4e3f-98df-35d0acafff23", 1, start},
		{sliID, 2, start}, {sliID, 1, start.Add(time.Microsecond)},
	} {
		if id == canonicalObservationID(other.sli, other.revision, other.start) {
			t.Fatal("different canonical keys share an observation identity")
		}
	}
}

func TestObservationWithoutReplayReceiptRetainsAQueryableIdentity(t *testing.T) {
	id := mustPGUUID("8748d63e-c657-4e3f-98df-35d0acafff22")
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	row := dbgen.ListSliObservationsRow{MeasurementRevision: 1, IntervalStart: timestamp(start), IntervalEnd: timestamp(start.Add(time.Minute)), Outcome: "observed", GoodCount: "99", TotalCount: "100"}
	got := sliObservationFromListRow(id, row)
	if got.ObservationID != canonicalObservationID(id.String(), 1, start).String() {
		t.Fatal("receipt expiry erased canonical identity")
	}
	row.ObservationID = pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	if got := sliObservationFromListRow(id, row); got.ObservationID != row.ObservationID.String() {
		t.Fatal("retained replay identity changed")
	}
}
