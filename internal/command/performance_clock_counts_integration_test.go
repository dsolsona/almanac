//go:build integration

package command

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type performanceClockWindow struct {
	SliID      string
	Class      int
	Start, End time.Time
	Corrected  bool
	Source     int64
}

// Read only the short live tail. The much larger imported historical prefix is
// independently counted analytically, without using application derived tables.
func performanceClockCounts(ctx context.Context, db *pgx.Conn, windows []performanceClockWindow, analyticalEnd, late time.Time) ([]performanceOracleWindowView, error) {
	slis := make([]string, len(windows))
	starts := make([]time.Time, len(windows))
	ends := make([]time.Time, len(windows))
	generations := make([]int64, len(windows))
	views := make([]performanceOracleWindowView, len(windows))
	for index, window := range windows {
		if window.Source <= 0 {
			return nil, errors.New("independent current oracle requires a committed source watermark")
		}
		generations[index] = window.Source
		slis[index], starts[index], ends[index] = window.SliID, window.Start, window.End
		if starts[index].Before(analyticalEnd) {
			starts[index] = analyticalEnd
		}
		if starts[index].After(window.End) {
			starts[index] = window.End
		}
		counts := performanceOracleCounts{Good: new(big.Rat), Total: new(big.Rat)}
		boundary := window.End
		if analyticalEnd.Before(boundary) {
			boundary = analyticalEnd
		}
		if window.Start.Before(boundary) {
			var err error
			counts, err = performanceOracleWindow(window.Class, window.Start, boundary)
			if err != nil {
				return nil, err
			}
			if window.Corrected && !late.Before(window.Start) && late.Before(boundary) {
				counts.Good.Add(counts.Good, big.NewRat(99999, 1000))
				counts.Total.Add(counts.Total, big.NewRat(100125, 1000))
				counts.Observed++
				counts.Missing--
			}
			for attempt := range 2 {
				end := boundary.Add(-time.Duration(attempt) * time.Minute)
				if !end.After(window.Start) {
					break
				}
				point, err := performanceOracleWindow(window.Class, end.Add(-time.Minute), end)
				if err != nil {
					return nil, err
				}
				if point.Observed > 0 {
					views[index].Last = end
					break
				}
			}
		}
		phaseHash := sha256.Sum256([]byte(window.SliID)) // SPEC-0002 phase v1.
		phase := time.Duration(binary.BigEndian.Uint64(phaseHash[:8])%60_000_000) * time.Microsecond
		views[index].Counts, views[index].End, views[index].Grace = counts, window.End, 120*time.Second+phase
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := db.Query(bounded, `WITH requested AS (
 SELECT * FROM unnest($1::uuid[],$2::timestamptz[],$3::timestamptz[],$4::bigint[]) WITH ORDINALITY AS w(sli_id,start_at,end_at,source_generation,ordinal)
)
SELECT r.ordinal,
 coalesce(sum(v.good_count) FILTER(WHERE c.outcome='observed'),0)::text,
 coalesce(sum(v.total_count) FILTER(WHERE c.outcome='observed'),0)::text,
 count(*) FILTER(WHERE c.outcome='observed'),count(*) FILTER(WHERE c.outcome='complete_empty'),
 count(*) FILTER(WHERE c.outcome='observed' AND v.total_count=0),
 max(c.interval_end) FILTER(WHERE c.outcome='observed'),
 count(*) FILTER(WHERE c.outcome='observed' AND (v.good_count IS NULL OR v.total_count IS NULL)),
 coalesce(max(source.generation)>=min(r.source_generation),false)
FROM requested r LEFT JOIN public.almanac_sli_source_generations source ON source.sli_id=r.sli_id AND source.measurement_revision=1
LEFT JOIN public.almanac_sli_coverage c ON c.sli_id=r.sli_id AND c.measurement_revision=1
 AND c.interval_start>=r.start_at AND c.interval_start<r.end_at AND c.interval_end<=r.end_at
 AND c.ingestion_generation<=r.source_generation
LEFT JOIN public.almanac_sli_observation_values v ON v.sli_id=c.sli_id AND v.measurement_revision=c.measurement_revision AND v.interval_start=c.interval_start
GROUP BY r.ordinal ORDER BY r.ordinal`, slis, starts, ends, generations)
	if err != nil {
		return nil, errors.New("read independent live-tail counts failed")
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var ordinal, observed, empty, zero, invalid int64
		var good, total string
		var last pgtype.Timestamptz
		var committed bool
		if err := rows.Scan(&ordinal, &good, &total, &observed, &empty, &zero, &last, &invalid, &committed); err != nil || ordinal != int64(seen+1) || seen >= len(windows) || invalid != 0 || !committed {
			return nil, errors.New("independent live-tail counts incomplete or invalid")
		}
		g, gok := new(big.Rat).SetString(good)
		n, nok := new(big.Rat).SetString(total)
		if !gok || !nok || g.Sign() < 0 || n.Cmp(g) < 0 {
			return nil, errors.New("independent live-tail counts malformed")
		}
		view := &views[seen]
		view.Counts.Good.Add(view.Counts.Good, g)
		view.Counts.Total.Add(view.Counts.Total, n)
		view.Counts.Observed += observed
		view.Counts.Empty += empty
		view.Counts.Zero += zero
		view.Counts.Missing = int64(windows[seen].End.Sub(windows[seen].Start)/time.Minute) - view.Counts.Observed - view.Counts.Empty
		if view.Counts.Missing < 0 {
			return nil, errors.New("independent live-tail intervals overlap")
		}
		if last.Valid && last.Time.After(view.Last) {
			view.Last = last.Time.UTC()
		}
		seen++
	}
	if rows.Err() != nil || seen != len(windows) {
		return nil, errors.New("independent live-tail oracle omitted requested windows")
	}
	return views, nil
}
