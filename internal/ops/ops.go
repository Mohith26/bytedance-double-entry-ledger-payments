// Package ops implements the payment operations (transfer, payment, refund, FX)
// as balanced double-entry journal entries posted through the store. Every
// operation is idempotent (keyed) and concurrency-safe (row-locked in store).
package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/jackc/pgx/v5"
)

// Service exposes the payment operations over a store.
type Service struct {
	st *store.Store
}

// New returns an ops Service.
func New(st *store.Store) *Service { return &Service{st: st} }

// Store exposes the underlying store (for reconcile/read helpers).
func (s *Service) Store() *store.Store { return s.st }

var (
	// ErrNonPositive means a caller-supplied amount was <= 0.
	ErrNonPositive = errors.New("ops: amount must be positive")
	// ErrRefundExceedsCaptured means a refund would exceed the captured amount.
	ErrRefundExceedsCaptured = errors.New("ops: refund exceeds captured amount")
)

// Result is the normalized result of a mutating operation.
type Result struct {
	TxnID    int64 `json:"txn_id"`
	Replayed bool  `json:"replayed"`
}

// Transfer moves `amount` (positive minor units) of `currency` from -> to as a
// balanced entry: source is debited, destination credited. Idempotent by key.
func (s *Service) Transfer(ctx context.Context, key string, from, to int64, amount money.Minor, currency money.Currency) (Result, error) {
	if amount <= 0 {
		return Result{}, ErrNonPositive
	}
	if from == to {
		return Result{}, errors.New("ops: transfer source and destination must differ")
	}
	e := ledger.Entry{
		Kind: ledger.TxnTransfer,
		Memo: "transfer",
		Legs: []ledger.Leg{
			{AccountID: from, Currency: currency, Amount: -amount},
			{AccountID: to, Currency: currency, Amount: amount},
		},
	}
	return s.postIdem(ctx, key, e, transferReq{"transfer", from, to, int64(amount), currency})
}

// Payment credits a merchant from an external source account (the outside
// world). It is flagged pending settlement until a settlement batch pays out.
func (s *Service) Payment(ctx context.Context, key string, source, merchant int64, amount money.Minor, currency money.Currency) (Result, error) {
	if amount <= 0 {
		return Result{}, ErrNonPositive
	}
	e := ledger.Entry{
		Kind:          ledger.TxnPayment,
		Memo:          "payment",
		PendingSettle: true,
		Legs: []ledger.Leg{
			{AccountID: source, Currency: currency, Amount: -amount},
			{AccountID: merchant, Currency: currency, Amount: amount},
		},
	}
	return s.postIdem(ctx, key, e, transferReq{"payment", source, merchant, int64(amount), currency})
}

// Refund reverses (up to) `amount` of a prior payment: the merchant is debited
// and the original source credited. The reversal is bounded to the still-
// refundable portion of the captured amount, checked atomically under locks.
func (s *Service) Refund(ctx context.Context, key string, paymentTxnID int64, amount money.Minor) (Result, error) {
	if amount <= 0 {
		return Result{}, ErrNonPositive
	}
	legs, err := s.st.GetPaymentLegs(ctx, paymentTxnID)
	if err != nil {
		return Result{}, err
	}
	e := ledger.Entry{
		Kind:     ledger.TxnRefund,
		Memo:     "refund",
		RefTxnID: &paymentTxnID,
		Legs: []ledger.Leg{
			{AccountID: legs.MerchantAccountID, Currency: legs.Currency, Amount: -amount},
			{AccountID: legs.SourceAccountID, Currency: legs.Currency, Amount: amount},
		},
	}
	// Guard: (already refunded + this) must not exceed captured. Evaluated
	// while the merchant/source rows are locked, so concurrent refunds on the
	// same payment cannot jointly over-refund.
	guard := s.refundGuard(paymentTxnID, legs, amount)
	req := refundReq{"refund", paymentTxnID, int64(amount)}
	res, err := s.st.PostEntryIdempotentGuarded(ctx, key, store.HashRequest(req), e, Result{}, guard)
	if err != nil {
		return Result{}, err
	}
	return Result{TxnID: res.TxnID, Replayed: res.Replayed}, nil
}

// refundGuard returns a store.Guard enforcing the refund bound.
func (s *Service) refundGuard(paymentTxnID int64, legs store.PaymentLegs, amount money.Minor) store.Guard {
	return func(ctx context.Context, tx pgx.Tx) error {
		prior, err := store.RefundedSoFar(ctx, tx, paymentTxnID, legs.MerchantAccountID)
		if err != nil {
			return err
		}
		if prior+amount > legs.Captured {
			return fmt.Errorf("%w: captured=%d already_refunded=%d requested=%d",
				ErrRefundExceedsCaptured, legs.Captured, prior, amount)
		}
		return nil
	}
}

// postIdem posts a keyed entry (or a non-idempotent one if key == "").
func (s *Service) postIdem(ctx context.Context, key string, e ledger.Entry, req any) (Result, error) {
	if key == "" {
		r, err := s.st.PostEntry(ctx, e)
		if err != nil {
			return Result{}, err
		}
		return Result{TxnID: r.TxnID}, nil
	}
	res, err := s.st.PostEntryIdempotent(ctx, key, store.HashRequest(req), e, Result{})
	if err != nil {
		return Result{}, err
	}
	return Result{TxnID: res.TxnID, Replayed: res.Replayed}, nil
}

// request payloads used only for idempotency hashing (detect key reuse).
type transferReq struct {
	Kind     string
	From, To int64
	Amount   int64
	Currency money.Currency
}

type refundReq struct {
	Kind      string
	PaymentID int64
	Amount    int64
}
