package storage

import (
	"math/big"
	"slices"
	"time"

	"github.com/dsolsona/almanac/internal/slo"
)

// A retained mark may outlive this batch or be selected independently of another
// window. Its replay facts must not alias lazy values, other windows or reducers.
func ownMaintenanceWindowFacts(totals slo.WindowTotals, coverage slo.WindowCoverage) maintenanceWindowFacts {
	cloneRat := func(v *big.Rat) *big.Rat {
		if v == nil {
			return nil
		}
		return new(big.Rat).Set(v)
	}
	cloneTime := func(v *time.Time) *time.Time {
		if v == nil {
			return nil
		}
		return new(*v)
	}
	totals.Good, totals.Total = cloneRat(totals.Good), cloneRat(totals.Total)
	totals.Excluded.Good, totals.Excluded.Total = cloneRat(totals.Excluded.Good), cloneRat(totals.Excluded.Total)
	totals.FirstObservation, totals.LastObservation, totals.LatestReceipt = cloneTime(totals.FirstObservation), cloneTime(totals.LastObservation), cloneTime(totals.LatestReceipt)
	coverage.Points = slices.Clone(coverage.Points)
	return maintenanceWindowFacts{Totals: totals, Coverage: new(coverage)}
}
