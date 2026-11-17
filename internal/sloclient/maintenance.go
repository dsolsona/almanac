package sloclient

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math/big"
	"strconv"
	"time"

	"github.com/dsolsona/almanac/internal/api"
)

func maintenanceRevision(value string) (int64, bool) {
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil && n >= 0 && strconv.FormatInt(n, 10) == value
}

func validCalculationProvenance(value *api.SloCalculationProvenance, quality api.SloOperationalQuality) bool {
	if value == nil {
		return true
	}
	c, cok := maintenanceRevision(value.ComputedRevision)
	r, rok := maintenanceRevision(value.RequestedRevision)
	_, gok := maintenanceRevision(value.SourceGeneration)
	if !cok || !rok || !gok || c > r || quality.ExcludedIntervals == nil || (value.BuildId == nil && c != 0) || (value.BuildId != nil && *value.BuildId == [16]byte{}) {
		return false
	}
	p := value.Compatibility
	if p == nil {
		return c == r
	}
	indexed, ok := maintenanceRevision(p.IndexedThrough)
	if !ok || p.ComputedRevision != value.ComputedRevision || p.RequestedRevision != value.RequestedRevision || !maintenanceInstant(p.Start) || !maintenanceInstant(p.End) || !p.Start.Before(p.End) {
		return false
	}
	if p.Kind == "same_revision" {
		return c == r && p.ComparisonId == nil
	}
	if c >= r || indexed < r {
		return false
	}
	switch p.Kind {
	case "indexed_absence":
		return p.ComparisonId == nil
	case "equal_union":
		return p.ComparisonId != nil && *p.ComparisonId != [16]byte{}
	default:
		return false
	}
}

func maintenanceInstant(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0 && value.Nanosecond()%1000 == 0
}

// Explicit calculation bounds are distinct from graph buckets, whose width does
// not identify the complete rolling input window.
func validWindowCalculationProvenance(value *api.SloCalculationProvenance, quality api.SloOperationalQuality, start, end time.Time) bool {
	if !validCalculationProvenance(value, quality) {
		return false
	}
	if value == nil || value.Compatibility == nil {
		return true
	}
	proof := value.Compatibility
	return !start.IsZero() && start.Before(end) && !proof.Start.After(start) && !proof.End.Before(end)
}

func validExcludedPopulation(value api.SloOperationalQuality) bool {
	g, total := value.ExcludedGood, value.ExcludedTotal
	if value.ExcludedIntervals == nil {
		return len(g) == 0 && len(total) == 0
	}
	if *value.ExcludedIntervals < 0 || len(g) == 0 || len(total) == 0 {
		return false
	}
	gn, tn := bytes.Equal(bytes.TrimSpace(g), []byte("null")), bytes.Equal(bytes.TrimSpace(total), []byte("null"))
	if gn || tn {
		return gn && tn
	}
	good, ok := excludedDecimal(g)
	all, tok := excludedDecimal(total)
	return ok && tok && good.Cmp(all) <= 0
}

func excludedDecimal(raw jsontext.Value) (*big.Rat, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil || len(value) == 0 || len(value) > 128 || value[0] == '-' || !decimalPattern.MatchString(value) {
		return nil, false
	}
	return new(big.Rat).SetString(value)
}

func validMaintenanceDisplay(value *api.SloDisplayCoverage) bool {
	if value == nil {
		return true
	}
	states := make(map[api.SloDisplayCoverageStates]bool)
	for _, state := range value.States {
		switch state {
		case "observed", "zero_traffic", "known_empty", "missing", "expired", "not_applicable", "partial", "stale", "excluded":
		default:
			return false
		}
		if states[state] {
			return false
		}
		states[state] = true
	}
	qualities := make(map[api.SloDisplayCoverageQualityStates]bool)
	for _, state := range value.QualityStates {
		switch state {
		case "complete", "partial", "no_data", "zero_traffic", "expired_history", "not_yet_eligible", "not_applicable", "unavailable", "excluded":
		default:
			return false
		}
		if qualities[state] {
			return false
		}
		qualities[state] = true
	}
	freshness := make(map[api.SloDisplayCoverageFreshness]bool)
	for _, state := range value.Freshness {
		if state != "unknown" && state != "fresh" && state != "stale" || freshness[state] {
			return false
		}
		freshness[state] = true
	}
	return len(states) > 0 && len(qualities) > 0 && len(freshness) > 0
}

// Generated optional pointers cannot distinguish omission from explicit null.
// Check only the operational envelope paths, using the already decoded response.
func validMaintenanceEnvelope(value any, depth int) bool {
	if depth > 12 {
		return false
	}
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if !validMaintenanceEnvelope(item, depth+1) {
				return false
			}
		}
	case map[string]any:
		if raw, present := v["calculation_provenance"]; present {
			provenance, ok := raw.(map[string]any)
			if !ok {
				return false
			}
			if id, present := provenance["build_id"]; present && id == nil {
				return false
			}
			if raw, present := provenance["compatibility"]; present {
				proof, ok := raw.(map[string]any)
				if !ok {
					return false
				}
				if id, present := proof["comparison_id"]; present && id == nil {
					return false
				}
			}
		}
		if quality, ok := v["quality"].(map[string]any); ok {
			if intervals, present := quality["excluded_intervals"]; present && intervals == nil {
				return false
			}
		}
		for _, key := range []string{"items", "current", "burn_1h", "burn_6h", "budget_sparkline", "attainment_series", "budget_series", "burn_series", "compliance_series", "samples", "days"} {
			if child, present := v[key]; present && !validMaintenanceEnvelope(child, depth+1) {
				return false
			}
		}
	}
	return true
}
