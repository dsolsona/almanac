package httpgzip

import (
	"strconv"
	"strings"
)

// Accepted honors an explicit gzip quality before a wildcard preference.
func Accepted(header string) bool {
	wildcard := false
	for item := range strings.SplitSeq(header, ",") {
		parts := strings.Split(item, ";")
		encoding := strings.TrimSpace(parts[0])
		if !strings.EqualFold(encoding, "gzip") && encoding != "*" {
			continue
		}
		quality := 1.0
		for _, param := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if strings.EqualFold(encoding, "gzip") {
			return quality > 0
		}
		wildcard = quality > 0
	}
	return wildcard
}
