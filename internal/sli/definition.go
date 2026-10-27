// Package sli defines the native event-count measurement supported by the
// first collected SLI journey.
package sli

import (
	"errors"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type MeasurementKind string

const MeasurementKindEventCounts MeasurementKind = "event_counts"

type ComponentMode string

const ComponentModeIntervalCount ComponentMode = "interval_count"

var (
	ErrInvalidDefinition          = errors.New("sli: invalid definition")
	ErrUnsupportedMeasurementKind = errors.New("sli: unsupported measurement kind")
	ErrUnsupportedComponentMode   = errors.New("sli: unsupported component mode")
	ErrInvalidCount               = errors.New("sli: invalid decimal count")
	ErrGoodExceedsTotal           = errors.New("sli: good count exceeds total count")
)

type Definition struct {
	ID                  string
	Name                string
	DisplayName         string
	DatasourceID        string
	DatasourceVersion   int64
	MeasurementRevision int64
	MeasurementKind     MeasurementKind
	ComponentMode       ComponentMode
	GoodQuery           string
	TotalQuery          string
}

func (d Definition) Validate() error {
	if d.MeasurementKind != MeasurementKindEventCounts {
		return ErrUnsupportedMeasurementKind
	}
	if d.ComponentMode != ComponentModeIntervalCount {
		return ErrUnsupportedComponentMode
	}
	if d.ID != "" && !canonicalUUID(d.ID) || !ValidName(d.Name) || !validDisplayName(d.DisplayName) ||
		!canonicalUUID(d.DatasourceID) || d.DatasourceVersion < 0 || d.MeasurementRevision < 0 ||
		!validQuery(d.GoodQuery) || !validQuery(d.TotalQuery) {
		return ErrInvalidDefinition
	}
	return nil
}

func ValidateCounts(goodText, totalText string) (string, string, error) {
	good, err := scaledCount(goodText)
	if err != nil {
		return "", "", err
	}
	total, err := scaledCount(totalText)
	if err != nil {
		return "", "", err
	}
	if good.Cmp(total) > 0 {
		return "", "", ErrGoodExceedsTotal
	}
	return goodText, totalText, nil
}

// ValidateIntervalCounts applies the exact event-count mode used by the first
// collected SLI. Fractional estimates require a separately declared mode and
// provenance, neither of which this measurement contract exposes.
func ValidateIntervalCounts(goodText, totalText string) (string, string, error) {
	good, total, err := ValidateCounts(goodText, totalText)
	if err != nil {
		return "", "", err
	}
	if !isIntegralCount(good) || !isIntegralCount(total) {
		return "", "", ErrInvalidCount
	}
	return good, total, nil
}

func isIntegralCount(value string) bool {
	_, fraction, hasFraction := strings.Cut(value, ".")
	if !hasFraction {
		return true
	}
	for _, digit := range fraction {
		if digit != '0' {
			return false
		}
	}
	return true
}

func scaledCount(value string) (*big.Int, error) {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") ||
		strings.ContainsAny(value, "eE") || strings.ContainsAny(value, " \t\r\n") {
		return nil, ErrInvalidCount
	}
	whole, fraction, hasFraction := strings.Cut(value, ".")
	if hasFraction && (fraction == "" || len(fraction) > 12) || !digits(whole) || (len(whole) > 1 && whole[0] == '0') || !digitsOrEmpty(fraction) {
		return nil, ErrInvalidCount
	}
	if len(whole) > 26 || len(whole)+len(fraction) > 38 {
		return nil, ErrInvalidCount
	}
	scaled := whole + fraction + strings.Repeat("0", 12-len(fraction))
	result, ok := new(big.Int).SetString(scaled, 10)
	if !ok {
		return nil, ErrInvalidCount
	}
	return result, nil
}

func digits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func digitsOrEmpty(value string) bool { return value == "" || digits(value) }

// ValidName reports whether value is an RFC1123 label suitable for a stable SLI name.
func ValidName(value string) bool {
	if len(value) == 0 || len(value) > 63 || !asciiAlphaNumeric(value[0]) || !asciiAlphaNumeric(value[len(value)-1]) {
		return false
	}
	for i := range len(value) {
		character := value[i]
		if !asciiAlphaNumeric(character) && character != '-' {
			return false
		}
		if character >= 'A' && character <= 'Z' {
			return false
		}
	}
	return true
}

func asciiAlphaNumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character >= 'A' && character <= 'Z'
}

func validDisplayName(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len([]rune(value)) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validQuery(value string) bool {
	return utf8.ValidString(value) && len(value) > 0 && len(value) <= 4096 && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, '\x00')
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value && value != uuid.Nil.String()
}
