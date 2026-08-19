package ledger

import (
	"errors"
	"testing"
)

func TestEntryValidateBalanced(t *testing.T) {
	e := Entry{Kind: TxnTransfer, Legs: []Leg{
		{AccountID: 1, Currency: "USD", Amount: -500},
		{AccountID: 2, Currency: "USD", Amount: 500},
	}}
	if err := e.Validate(); err != nil {
		t.Fatalf("expected balanced, got %v", err)
	}
}

func TestEntryValidateUnbalanced(t *testing.T) {
	e := Entry{Kind: TxnTransfer, Legs: []Leg{
		{AccountID: 1, Currency: "USD", Amount: -500},
		{AccountID: 2, Currency: "USD", Amount: 499},
	}}
	if err := e.Validate(); !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("expected ErrUnbalanced, got %v", err)
	}
}

func TestEntryValidateMultiCurrencyBalanced(t *testing.T) {
	// FX: each currency nets to zero independently.
	e := Entry{Kind: TxnFX, Legs: []Leg{
		{AccountID: 1, Currency: "USD", Amount: -10000},
		{AccountID: 2, Currency: "USD", Amount: 10000},
		{AccountID: 3, Currency: "EUR", Amount: -9200},
		{AccountID: 4, Currency: "EUR", Amount: 9200},
	}}
	if err := e.Validate(); err != nil {
		t.Fatalf("expected multi-currency balanced, got %v", err)
	}
}

func TestEntryValidateMultiCurrencyImbalance(t *testing.T) {
	// USD balances, EUR does not -> rejected.
	e := Entry{Kind: TxnFX, Legs: []Leg{
		{AccountID: 1, Currency: "USD", Amount: -10000},
		{AccountID: 2, Currency: "USD", Amount: 10000},
		{AccountID: 3, Currency: "EUR", Amount: -9200},
		{AccountID: 4, Currency: "EUR", Amount: 9100},
	}}
	if err := e.Validate(); !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("expected ErrUnbalanced, got %v", err)
	}
}

func TestEntryValidateTooFewLegs(t *testing.T) {
	e := Entry{Kind: TxnTransfer, Legs: []Leg{{AccountID: 1, Currency: "USD", Amount: -500}}}
	if err := e.Validate(); !errors.Is(err, ErrTooFewLegs) {
		t.Fatalf("expected ErrTooFewLegs, got %v", err)
	}
}

func TestEntryValidateZeroLeg(t *testing.T) {
	e := Entry{Kind: TxnTransfer, Legs: []Leg{
		{AccountID: 1, Currency: "USD", Amount: 0},
		{AccountID: 2, Currency: "USD", Amount: 0},
	}}
	if err := e.Validate(); !errors.Is(err, ErrZeroLeg) {
		t.Fatalf("expected ErrZeroLeg, got %v", err)
	}
}
