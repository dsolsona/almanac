// Package slo implements exact occurrence-objective calculations.
package slo

import (
	"errors"
	"math/big"
	"strings"
)

var (
	ErrInvalidCalculation = errors.New("slo: invalid occurrence calculation")
	ErrNoTraffic          = errors.New("slo: no traffic")
)

type OccurrenceResult struct {
	State, ObservedState                                                          string
	Provisional                                                                   bool
	Attainment, Bad, Allowed, Remaining, Consumed, RemainingFraction, Burn        string
	ExactBad, ExactAllowed, ExactRemaining, ExactConsumed, ExactRemainingFraction *big.Rat
}

// CalculateOccurrences calculates on exact decimal quantities, rounding only for output.
func CalculateOccurrences(goodText, totalText, targetText string, provisional bool) (OccurrenceResult, error) {
	good, ok := decimal(goodText, false)
	if !ok {
		return OccurrenceResult{}, ErrInvalidCalculation
	}
	total, ok := decimal(totalText, false)
	if !ok || good.Cmp(total) > 0 {
		return OccurrenceResult{}, ErrInvalidCalculation
	}
	target, ok := decimal(targetText, true)
	if !ok || target.Cmp(big.NewRat(1, 1)) >= 0 {
		return OccurrenceResult{}, ErrInvalidCalculation
	}
	if total.Sign() == 0 {
		return OccurrenceResult{}, ErrNoTraffic
	}
	return calculateOccurrences(good, total, target, provisional), nil
}

// Inputs have already been validated. Window sweeps can use their exact totals
// directly instead of formatting and reparsing them for every history point.
func calculateOccurrences(good, total, target *big.Rat, provisional bool) OccurrenceResult {
	one := big.NewRat(1, 1)
	bad := new(big.Rat).Sub(total, good)
	allowed := new(big.Rat).Mul(new(big.Rat).Sub(one, target), total)
	remaining := new(big.Rat).Sub(allowed, bad)
	consumed := new(big.Rat).Quo(bad, allowed)
	attainment := new(big.Rat).Quo(good, total)
	state := "violated"
	if attainment.Cmp(target) >= 0 {
		state = "met"
	}
	remainingFraction := new(big.Rat).Sub(one, consumed)
	result := OccurrenceResult{State: state, ObservedState: state, Provisional: provisional,
		Attainment: rounded(attainment), Bad: rounded(bad), Allowed: rounded(allowed),
		Remaining: rounded(remaining), Consumed: rounded(consumed),
		RemainingFraction: rounded(remainingFraction), Burn: rounded(consumed),
		ExactBad: new(big.Rat).Set(bad), ExactAllowed: new(big.Rat).Set(allowed), ExactRemaining: new(big.Rat).Set(remaining),
		ExactConsumed: new(big.Rat).Set(consumed), ExactRemainingFraction: new(big.Rat).Set(remainingFraction)}
	if provisional {
		result.State = "unknown"
	}
	return result
}

func decimal(text string, target bool) (*big.Rat, bool) {
	whole, fraction, hasFraction := strings.Cut(text, ".")
	if whole == "" || (len(whole) > 1 && whole[0] == '0') || (hasFraction && (fraction == "" || len(fraction) > 12)) {
		return nil, false
	}
	if target && len(whole) > 26 {
		return nil, false
	}
	for _, r := range whole + fraction {
		if r < '0' || r > '9' {
			return nil, false
		}
	}
	if hasFraction {
		fraction = strings.TrimRight(fraction, "0")
		text = whole
		if fraction != "" {
			text += "." + fraction
		}
	}
	r, ok := new(big.Rat).SetString(text)
	return r, ok
}

func rounded(value *big.Rat) string {
	scaled := new(big.Int).Mul(value.Num(), big.NewInt(1_000_000_000_000))
	negative := scaled.Sign() < 0
	scaled.Abs(scaled)
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(scaled, value.Denom(), r)
	cmp := new(big.Int).Lsh(r, 1).Cmp(value.Denom())
	if cmp > 0 || cmp == 0 && q.Bit(0) == 1 {
		q.Add(q, big.NewInt(1))
	}
	digits := q.String()
	if len(digits) <= 12 {
		digits = strings.Repeat("0", 13-len(digits)) + digits
	}
	result := digits[:len(digits)-12] + "." + digits[len(digits)-12:]
	if negative && q.Sign() != 0 {
		result = "-" + result
	}
	return result
}

// FormatFraction formats an exact ratio using the contract's half-even rounding.
func FormatFraction(numerator, denominator int64) string {
	return rounded(big.NewRat(numerator, denominator))
}
