package money

import (
	"errors"
	"fmt"
	"math/big"
)

// ErrBadRate is returned for a non-positive FX rate numerator/denominator.
var ErrBadRate = errors.New("money: invalid fx rate")

// Rate expresses an exchange rate as an exact integer fraction Num/Den so that
// conversion uses no floating point. To convert `from` minor units at this
// rate: converted = round(from * Num / Den). Both fields must be > 0.
type Rate struct {
	Num int64
	Den int64
}

// Validate reports whether the rate is usable.
func (r Rate) Validate() error {
	if r.Num <= 0 || r.Den <= 0 {
		return fmt.Errorf("%w: %d/%d", ErrBadRate, r.Num, r.Den)
	}
	return nil
}

// Convert returns `from` minor units converted at rate r, using round-half-up
// (away from zero on a .5 tie) on the exact rational result. It uses exact
// big-integer arithmetic so there is never any floating-point imprecision.
//
// Whatever integer this returns, the FX operation posts it as balanced legs in
// each currency (see ops.ConvertFX), so rounding here can never create or
// destroy money within a currency — the residual lands in the FX position
// account, which is exactly the intended semantics.
func (r Rate) Convert(from Minor) (Minor, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	neg := from < 0
	f := int64(from)
	if neg {
		f = -f
	}
	// q = round(f * Num / Den) = floor((f*Num + Den/2) / Den) for f >= 0.
	num := new(big.Int).Mul(big.NewInt(f), big.NewInt(r.Num))
	den := big.NewInt(r.Den)
	half := new(big.Int).Rsh(den, 1) // Den/2 (integer)
	num.Add(num, half)
	q := new(big.Int).Quo(num, den)
	res := q.Int64()
	if neg {
		res = -res
	}
	return Minor(res), nil
}
