package command

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

type performanceIngestionHistogram struct {
	Bounds       []float64 `json:"bounds_seconds"`
	Cumulative   []uint64  `json:"cumulative"`
	Count        uint64    `json:"accepted_batches"`
	Unsuccessful uint64    `json:"unsuccessful_batches"`
}

type performancePercentileBounds struct {
	Count uint64  `json:"count"`
	Lower float64 `json:"p95_lower_seconds"`
	Upper float64 `json:"p95_upper_seconds"`
}

func parsePerformanceIngestionMetrics(body string) (performanceIngestionHistogram, error) {
	var result performanceIngestionHistogram
	counts := make(map[float64]uint64)
	countPresent, failurePresent := false, false
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return result, errors.New("invalid internal metrics line")
		}
		name, raw := fields[0], fields[1]
		bucket, isBucket := strings.CutPrefix(name, `almanac_ingestion_batch_duration_seconds_bucket{result="accepted",le="`)
		if !isBucket && name != `almanac_ingestion_batch_duration_seconds_count{result="accepted"}` && name != `almanac_ingestion_batch_duration_seconds_count{result="unsuccessful"}` {
			continue
		}
		count, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return result, errors.New("invalid ingestion metric count")
		}
		if isBucket {
			boundText, valid := strings.CutSuffix(bucket, `"}`)
			bound, err := strconv.ParseFloat(boundText, 64)
			if !valid || err != nil || math.IsNaN(bound) || bound <= 0 {
				return result, errors.New("invalid ingestion bucket bound")
			}
			if _, exists := counts[bound]; exists {
				return result, errors.New("duplicate ingestion bucket")
			}
			counts[bound] = count
		} else if strings.Contains(name, `result="accepted"`) {
			if countPresent {
				return result, errors.New("duplicate ingestion total")
			}
			countPresent, result.Count = true, count
		} else {
			if failurePresent {
				return result, errors.New("duplicate ingestion failure total")
			}
			failurePresent, result.Unsuccessful = true, count
		}
	}
	_, infinityPresent := counts[math.Inf(1)]
	if !countPresent || !failurePresent || !infinityPresent || len(counts) < 2 || counts[math.Inf(1)] != result.Count {
		return result, errors.New("missing or inconsistent ingestion histogram")
	}
	for bound := range counts {
		if !math.IsInf(bound, 1) {
			result.Bounds = append(result.Bounds, bound)
		}
	}
	slices.Sort(result.Bounds)
	for _, bound := range result.Bounds {
		count := counts[bound]
		if count > result.Count || (len(result.Cumulative) > 0 && count < result.Cumulative[len(result.Cumulative)-1]) {
			return result, errors.New("non-monotonic ingestion histogram")
		}
		result.Cumulative = append(result.Cumulative, count)
	}
	return result, nil
}

func performanceIngestionDelta(before, after performanceIngestionHistogram) (performancePercentileBounds, error) {
	var result performancePercentileBounds
	if len(before.Bounds) == 0 || !slices.Equal(before.Bounds, after.Bounds) || len(before.Cumulative) != len(before.Bounds) || len(after.Cumulative) != len(after.Bounds) ||
		after.Count < before.Count || after.Unsuccessful < before.Unsuccessful {
		return result, errors.New("ingestion histogram reset or changed schema during the phase")
	}
	result.Count = after.Count - before.Count
	if result.Count < 100 {
		return result, fmt.Errorf("ingestion phase has only %d accepted batches; need at least 100", result.Count)
	}
	if after.Unsuccessful != before.Unsuccessful {
		return result, errors.New("ingestion phase contains unsuccessful batches")
	}
	// Integer nearest rank avoids overflow/rounding in ceil(0.95*n).
	if result.Count > 1<<53 {
		return result, errors.New("ingestion sample count exceeds controlled fixture bounds")
	}
	rank := (95*result.Count + 99) / 100
	previous := uint64(0)
	found := false
	for index, bound := range after.Bounds {
		if after.Cumulative[index] < before.Cumulative[index] {
			return result, errors.New("ingestion bucket counter reset during the phase")
		}
		count := after.Cumulative[index] - before.Cumulative[index]
		if count < previous || count > result.Count {
			return result, errors.New("inconsistent ingestion phase bucket counts")
		}
		if !found && count >= rank {
			result.Upper = bound
			if index > 0 {
				result.Lower = after.Bounds[index-1]
			}
			found = true
		}
		previous = count
	}
	if !found {
		return result, errors.New("ingestion p95 exceeds the largest finite histogram bound")
	}
	return result, nil
}

func checkPerformanceIngestionIsolation(baseline, loaded performancePercentileBounds) error {
	// Compare the loaded upper bound to the baseline lower bound. Approximate
	// histogram interpolation must never manufacture a passing 1.25× ratio.
	if baseline.Count < 100 || loaded.Count < 100 || baseline.Lower <= 0 || loaded.Upper <= 0 {
		return errors.New("insufficient ingestion percentile evidence")
	}
	if loaded.Upper > .250 || loaded.Upper > 1.25*baseline.Lower {
		return errors.New("ingestion percentile bounds do not establish the 250 ms and 1.25x isolation budgets")
	}
	return nil
}
