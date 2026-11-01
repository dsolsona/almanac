package command

import (
	"errors"
	"math"
)

const performanceDiskReserve int64 = 10 << 30

// Admission is deliberately conservative: allow twice the projected extra
// database footprint for WAL/rebuild work, plus a fixed free-space reserve.
func performanceDiskRequired(databaseBytes, canonicalRows, extraRows int64) (int64, error) {
	if databaseBytes <= 0 || canonicalRows <= 0 || extraRows < 0 || databaseBytes > math.MaxInt64-canonicalRows+1 {
		return 0, errors.New("invalid measured performance fixture size")
	}
	bytesPerRow := (databaseBytes + canonicalRows - 1) / canonicalRows
	if extraRows > (math.MaxInt64-performanceDiskReserve)/2/bytesPerRow {
		return 0, errors.New("performance fixture disk estimate overflow")
	}
	return performanceDiskReserve + 2*bytesPerRow*extraRows, nil
}
