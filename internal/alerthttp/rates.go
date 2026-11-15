package alerthttp

import (
	"github.com/dsolsona/almanac/internal/alert"
	"github.com/dsolsona/almanac/internal/api"
	"github.com/dsolsona/almanac/internal/sli"
	"github.com/dsolsona/almanac/internal/slo"
)

// Enrich immutable evidence for display through the same SLO mathematics used
// elsewhere. These rounded fields never replace stored outcome/quality or read
// current source facts. Missing numerical populations remain absent.
func windowRatesDTO(value alert.WindowEvidence, target string) api.AlertWindowEvidence {
	result := windowDTO(value)
	target = sli.NormalizeDecimal(target)
	allowance, err := slo.CalculateOccurrences("1", "1", target, true)
	if err != nil {
		return result
	}
	result.AllowedErrorRate = new(sli.NormalizeDecimal(allowance.Allowed))
	if value.Good == "" || value.Total == "" {
		return result
	}
	good, total := sli.NormalizeDecimal(value.Good), sli.NormalizeDecimal(value.Total)
	if good == "0" && total == "0" {
		result.Bad = new("0")
		return result
	}
	calculation, err := slo.CalculateOccurrences(good, total, target, value.Quality != alert.Complete)
	if err != nil {
		return result
	}
	observed, err := slo.CalculateOccurrences(good, total, "0", true)
	if err != nil {
		return result
	}
	result.Bad = new(sli.NormalizeDecimal(calculation.Bad))
	result.Burn = new(sli.NormalizeDecimal(calculation.Burn))
	result.ObservedErrorRate = new(sli.NormalizeDecimal(observed.Burn))
	return result
}
