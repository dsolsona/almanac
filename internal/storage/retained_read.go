package storage

import "time"

type retainedReadPart struct {
	start, end time.Time
	resolution int32
}

// A year uses at most 365 complete UTC days, 46 complete edge hours and two
// partial hours. Storage work depends on the requested range, never fleet size.
func retainedReadParts(start, end time.Time) ([]retainedReadPart, error) {
	if !start.Before(end) || end.Sub(start) > 365*24*time.Hour {
		return nil, ErrOperationalRange
	}
	var parts []retainedReadPart
	for start.Before(end) {
		part := retainedReadPart{start: start, end: end}
		if start.Equal(utcDayStart(start)) && !start.Add(24*time.Hour).After(end) {
			part.end, part.resolution = start.Add(24*time.Hour), 86400
		} else if start.Equal(start.Truncate(time.Hour)) && !start.Add(time.Hour).After(end) {
			part.end, part.resolution = start.Add(time.Hour), 3600
		} else if nextHour := start.Truncate(time.Hour).Add(time.Hour); nextHour.Before(end) {
			part.end = nextHour
		}
		parts = append(parts, part)
		start = part.end
	}
	return parts, nil
}
