// Package money models monetary amounts as signed integer minor units
// (e.g. cents). It never uses floating point for money: every value is an
// int64 count of the smallest indivisible unit of a currency, so arithmetic
// is exact and no cents are ever created or destroyed by rounding drift.
package money

import (
	"errors"
	"fmt"
	"strings"
)

// Minor is a signed amount in a currency's smallest unit (minor units).
// Positive values are debits-to-the-account convention is applied by the
// ledger layer; here Minor is just an exact integer quantity of money.
type Minor int64

// Currency is an ISO-4217-style alphabetic code (e.g. "USD"). For this v1 the
// supported currencies all use 2 decimal places (100 minor units per major
// unit); JPY-style zero-decimal currencies are intentionally out of scope and
// documented as such.
type Currency string

// Supported currencies and their number of decimal places (minor units per
// major unit is 10^decimals). Kept to 2-decimal currencies for v1.
var currencyDecimals = map[Currency]int32{
	"USD": 2,
	"EUR": 2,
	"GBP": 2,
	"CAD": 2,
	"AUD": 2,
}

var (
	// ErrUnknownCurrency is returned for a currency not in the supported set.
	ErrUnknownCurrency = errors.New("money: unknown currency")
	// ErrBadAmount is returned when a major-unit string cannot be parsed.
	ErrBadAmount = errors.New("money: invalid amount")
)

// IsSupported reports whether c is a known currency.
func IsSupported(c Currency) bool {
	_, ok := currencyDecimals[c]
	return ok
}

// Decimals returns the number of decimal places for c, or an error if unknown.
func Decimals(c Currency) (int32, error) {
	d, ok := currencyDecimals[c]
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownCurrency, c)
	}
	return d, nil
}

// scale returns 10^decimals for a supported currency (e.g. 100 for USD).
func scale(c Currency) (int64, error) {
	d, err := Decimals(c)
	if err != nil {
		return 0, err
	}
	s := int64(1)
	for i := int32(0); i < d; i++ {
		s *= 10
	}
	return s, nil
}

// ParseMajor parses a human major-unit string like "12.34" or "-0.05" into
// exact minor units for currency c, WITHOUT ever using a float. It rejects
// amounts with more fractional digits than the currency supports.
func ParseMajor(c Currency, s string) (Minor, error) {
	if _, err := Decimals(c); err != nil {
		return 0, err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: empty", ErrBadAmount)
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if intPart == "" && fracPart == "" {
		return 0, fmt.Errorf("%w: %q", ErrBadAmount, s)
	}
	decimals, _ := Decimals(c)
	if hasFrac && int32(len(fracPart)) > decimals {
		return 0, fmt.Errorf("%w: %q has more than %d decimal places", ErrBadAmount, s, decimals)
	}
	// Right-pad the fractional part to exactly `decimals` digits.
	for int32(len(fracPart)) < decimals {
		fracPart += "0"
	}
	var whole int64
	for _, r := range intPart {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%w: %q", ErrBadAmount, s)
		}
		whole = whole*10 + int64(r-'0')
	}
	var frac int64
	for _, r := range fracPart {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%w: %q", ErrBadAmount, s)
		}
		frac = frac*10 + int64(r-'0')
	}
	sc, _ := scale(c)
	total := whole*sc + frac
	if neg {
		total = -total
	}
	return Minor(total), nil
}

// FormatMajor renders m as a major-unit decimal string for currency c using
// only integer math (no float formatting).
func FormatMajor(c Currency, m Minor) (string, error) {
	sc, err := scale(c)
	if err != nil {
		return "", err
	}
	decimals, _ := Decimals(c)
	v := int64(m)
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	whole := v / sc
	frac := v % sc
	if decimals == 0 {
		return fmt.Sprintf("%s%d", sign, whole), nil
	}
	return fmt.Sprintf("%s%d.%0*d", sign, whole, decimals, frac), nil
}
