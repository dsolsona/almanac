package alerthttp

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsolsona/almanac/internal/alert"
)

var decimalThreshold = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

func validName(value string) bool {
	if !utf8.ValidString(value) || len(value) > 1024 || utf8.RuneCountInString(value) > 256 || strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateConditions(conditions []alert.Condition) string {
	if len(conditions) < 1 || len(conditions) > 8 {
		return "conditions"
	}
	seen := make(map[string]bool, len(conditions))
	for index, condition := range conditions {
		field := "conditions." + strconv.Itoa(index)
		if len(condition.ID) < 1 || len(condition.ID) > 64 || strings.TrimSpace(condition.ID) == "" || seen[condition.ID] {
			return field + ".id"
		}
		for _, character := range condition.ID {
			if character < 32 || character > 126 {
				return field + ".id"
			}
		}
		seen[condition.ID] = true
		if condition.Short <= 0 || condition.Short%time.Minute != 0 || condition.Short > 30*24*time.Hour {
			return field + ".short_window_seconds"
		}
		if condition.Long < condition.Short || condition.Long%time.Minute != 0 || condition.Long > 30*24*time.Hour {
			return field + ".long_window_seconds"
		}
		if len(condition.Threshold) > 32 || !decimalThreshold.MatchString(condition.Threshold) {
			return field + ".threshold"
		}
		threshold, valid := new(big.Rat).SetString(condition.Threshold)
		if !valid || threshold.Sign() <= 0 {
			return field + ".threshold"
		}
	}
	return ""
}
