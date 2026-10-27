package agentprotocol

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
	"uuid"
)

func positiveDecimal(value string, allowZero bool) (int64, error) {
	if value == "" || len(value) > 19 || (len(value) > 1 && value[0] == '0') {
		return 0, fmt.Errorf("must be a canonical decimal int64 string")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || (!allowZero && parsed == 0) || strconv.FormatInt(parsed, 10) != value {
		return 0, fmt.Errorf("must be %sint64 decimal", map[bool]string{true: "non-negative ", false: "positive "}[allowZero])
	}
	return parsed, nil
}

func validateNonce(value string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return fmt.Errorf("must be canonical base64url for exactly 32 bytes")
	}
	var nonzero byte
	for _, b := range decoded {
		nonzero |= b
	}
	if nonzero == 0 {
		return fmt.Errorf("must not be all zero")
	}
	return nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value && value != "00000000-0000-0000-0000-000000000000"
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0
}

func validOperation(value string) bool {
	switch value {
	case "collect", "connection_test", "query_preview", "backfill":
		return true
	default:
		return false
	}
}

func hasDuplicates(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
