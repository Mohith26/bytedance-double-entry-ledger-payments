// Package ledger defines the immutable double-entry domain types and the
// balancing invariant shared across the store and ops layers.
package ledger

import (
	"errors"
	"fmt"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
)

// AccountKind classifies accounts for reporting and negative-balance policy.
type AccountKind string

const (
	KindCustomer AccountKind = "customer" // end-user funds; must not go negative
	KindMerchant AccountKind = "merchant" // merchant receivable; must not go negative
	KindExternal AccountKind = "external" // the outside world / clearing; may go negative
	KindFX       AccountKind = "fx"       // FX position account; may go negative
	KindPayout   AccountKind = "payout"   // merchant payout rail; may go negative
)

// Account is a single-currency account with a cached balance.
type Account struct {
	ID            int64          `json:"id"`
	Name          string         `json:"name"`
	Currency      money.Currency `json:"currency"`
	Kind          AccountKind    `json:"kind"`
	AllowNegative bool           `json:"allow_negative"`
	Balance       money.Minor    `json:"balance_minor"`
}

// TxnKind classifies a journal entry.
type TxnKind string

const (
	TxnTransfer   TxnKind = "transfer"
	TxnPayment    TxnKind = "payment"
	TxnRefund     TxnKind = "refund"
	TxnFX         TxnKind = "fx"
	TxnSettlement TxnKind = "settlement"
)

// Leg is a single posting request (before it is persisted): move `Amount`
// (signed minor units) into AccountID. Currency is validated against the
// account's currency by the store.
type Leg struct {
	AccountID int64
	Currency  money.Currency
	Amount    money.Minor
}

// Entry is a proposed balanced journal entry: a set of legs plus metadata.
type Entry struct {
	Kind     TxnKind
	Memo     string
	RefTxnID *int64
	Legs     []Leg
	// PendingSettle marks a payment as awaiting a settlement batch payout.
	PendingSettle bool
}

var (
	// ErrUnbalanced means the legs do not net to zero within some currency.
	ErrUnbalanced = errors.New("ledger: transaction is not balanced (debits != credits)")
	// ErrTooFewLegs means fewer than two legs were supplied.
	ErrTooFewLegs = errors.New("ledger: a transaction needs at least two legs")
	// ErrZeroLeg means a leg had a zero amount.
	ErrZeroLeg = errors.New("ledger: leg amount must be non-zero")
)

// Validate checks the balancing invariant WITHOUT touching the database:
// there must be >= 2 legs, no zero-amount leg, and for every currency present
// the signed amounts must sum to exactly zero. This is the pure, in-memory
// heart of double-entry: money is never created or destroyed.
func (e Entry) Validate() error {
	if len(e.Legs) < 2 {
		return ErrTooFewLegs
	}
	perCurrency := make(map[money.Currency]int64)
	for _, l := range e.Legs {
		if l.Amount == 0 {
			return ErrZeroLeg
		}
		if !money.IsSupported(l.Currency) {
			return fmt.Errorf("%w: %q", money.ErrUnknownCurrency, l.Currency)
		}
		perCurrency[l.Currency] += int64(l.Amount)
	}
	for cur, sum := range perCurrency {
		if sum != 0 {
			return fmt.Errorf("%w: currency %s nets to %d", ErrUnbalanced, cur, sum)
		}
	}
	return nil
}
