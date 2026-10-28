package sli

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// LatestSettledEnd returns the latest Unix-aligned interval endpoint whose
// settling delay has elapsed. Cadence must be a positive whole number of seconds.
func LatestSettledEnd(now time.Time, cadenceSeconds, settlingSeconds int) time.Time {
	cadence := int64(cadenceSeconds) * 1_000_000
	us := now.UTC().UnixMicro() - int64(settlingSeconds)*1_000_000
	quotient := us / cadence
	if us < 0 && us%cadence != 0 {
		quotient--
	}
	return time.UnixMicro(quotient * cadence).UTC()
}

// DispatchPhase uses the first 64 bits of SHA-256 of the SLI ID, modulo the
// cadence in microseconds. This algorithm and precision are stable across runs.
func DispatchPhase(id string, cadenceSeconds int) time.Duration {
	digest := sha256.Sum256([]byte(id))
	return time.Duration(binary.BigEndian.Uint64(digest[:8])%(uint64(cadenceSeconds)*1_000_000)) * time.Microsecond
}

const (
	ObservationReplayWindow = 7 * 24 * time.Hour
	ObservationRetention    = 400 * 24 * time.Hour
)

var (
	ErrInvalidObservationItem = errors.New("sli: invalid observation item")
	ErrReplayExpired          = errors.New("sli: observation replay window expired")
)

type OutcomeState string

const (
	OutcomeObserved      OutcomeState = "observed"
	OutcomeCompleteEmpty OutcomeState = "complete_empty"
	OutcomeIncomplete    OutcomeState = "incomplete"
	OutcomeFailed        OutcomeState = "failed"
)

type ExecutionOutcome struct {
	State             OutcomeState
	GoodCount         string
	TotalCount        string
	MissingComponents []string
	ErrorCode         string
}

type ObservationItem struct {
	DeliveryID            string
	ExecutionID           string
	CreatedAt             time.Time
	LeaseID               string
	ConfigurationRevision string
	SessionGeneration     string
	SLIID                 string
	MeasurementRevision   string
	IntervalStart         time.Time
	IntervalEnd           time.Time
	Outcome               ExecutionOutcome
}

func (item ObservationItem) Validate(cadenceSeconds int, effectiveFrom time.Time, settlingDelay time.Duration, now time.Time) error {
	if cadenceSeconds < 15 || cadenceSeconds > 86400 || settlingDelay < 0 ||
		!canonicalUUID(item.DeliveryID) || !canonicalUUID(item.ExecutionID) ||
		!canonicalUUID(item.SLIID) || !positiveDecimal(item.ConfigurationRevision) ||
		!positiveDecimal(item.MeasurementRevision) || !positiveDecimal(item.SessionGeneration) ||
		!validLeaseID(item.LeaseID) || !canonicalMicrosecondUTC(item.CreatedAt) ||
		!canonicalMicrosecondUTC(item.IntervalStart) || !canonicalMicrosecondUTC(item.IntervalEnd) ||
		!canonicalMicrosecondUTC(effectiveFrom) || !canonicalMicrosecondUTC(now) ||
		!item.IntervalEnd.After(item.IntervalStart) ||
		item.IntervalEnd.Sub(item.IntervalStart) != time.Duration(cadenceSeconds)*time.Second ||
		item.IntervalStart.UnixMicro()%int64(cadenceSeconds*1_000_000) != 0 ||
		item.IntervalEnd.UnixMicro()%int64(cadenceSeconds*1_000_000) != 0 ||
		item.IntervalStart.Before(effectiveFrom) || item.CreatedAt.Before(item.IntervalEnd.Add(settlingDelay)) ||
		item.IntervalStart.Before(now.Add(-ObservationRetention)) ||
		item.CreatedAt.After(now.Add(5*time.Second)) {
		return ErrInvalidObservationItem
	}
	if now.Sub(item.CreatedAt) > ObservationReplayWindow {
		return ErrReplayExpired
	}
	return item.Outcome.Validate()
}

func (outcome ExecutionOutcome) Validate() error {
	noCounts := outcome.GoodCount == "" && outcome.TotalCount == ""
	noMissing := len(outcome.MissingComponents) == 0
	switch outcome.State {
	case OutcomeObserved:
		if noCounts || !noMissing || outcome.ErrorCode != "" {
			return ErrInvalidObservationItem
		}
		if _, _, err := ValidateIntervalCounts(outcome.GoodCount, outcome.TotalCount); err != nil {
			return ErrInvalidObservationItem
		}
	case OutcomeCompleteEmpty:
		if !noCounts || !noMissing || outcome.ErrorCode != "" {
			return ErrInvalidObservationItem
		}
	case OutcomeIncomplete:
		if !noCounts || outcome.ErrorCode != "" || len(outcome.MissingComponents) == 0 || len(outcome.MissingComponents) > 2 {
			return ErrInvalidObservationItem
		}
		seen := make(map[string]struct{}, len(outcome.MissingComponents))
		for _, component := range outcome.MissingComponents {
			if component != "good" && component != "total" {
				return ErrInvalidObservationItem
			}
			if _, exists := seen[component]; exists {
				return ErrInvalidObservationItem
			}
			seen[component] = struct{}{}
		}
	case OutcomeFailed:
		if !noCounts || !noMissing || !safeErrorCodes[outcome.ErrorCode] {
			return ErrInvalidObservationItem
		}
	default:
		return ErrInvalidObservationItem
	}
	return nil
}

var safeErrorCodes = map[string]bool{
	"authentication": true, "incomplete": true, "network": true, "query": true,
	"rate_limited": true, "tls": true, "timeout": true, "unsupported": true,
	"assignment_revoked": true, "deadline": true,
}

var positiveDecimalPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

func positiveDecimal(value string) bool {
	if !positiveDecimalPattern.MatchString(value) {
		return false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func validLeaseID(value string) bool {
	if len(value) < 32 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func canonicalMicrosecondUTC(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0 && value.Nanosecond()%1_000 == 0
}

func CanonicalOutcome(outcome ExecutionOutcome) ExecutionOutcome {
	copy := outcome
	if copy.State == OutcomeObserved {
		copy.GoodCount = NormalizeDecimal(copy.GoodCount)
		copy.TotalCount = NormalizeDecimal(copy.TotalCount)
	}
	copy.MissingComponents = slices.Clone(outcome.MissingComponents)
	slices.Sort(copy.MissingComponents)
	return copy
}

func NormalizeDecimal(value string) string {
	whole, fraction, found := strings.Cut(value, ".")
	if !found {
		return value
	}
	fraction = strings.TrimRight(fraction, "0")
	if fraction == "" {
		return whole
	}
	return whole + "." + fraction
}
