package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/jackc/pgx/v5"
)

// ErrNotAPayment means the referenced transaction is not a payment.
var ErrNotAPayment = errors.New("store: referenced transaction is not a payment")

// PaymentLegs describes the two accounts and captured amount of a payment,
// derived from its postings: the merchant is the credited (positive) leg and
// the source is the debited (negative) leg.
type PaymentLegs struct {
	MerchantAccountID int64
	SourceAccountID   int64
	Currency          money.Currency
	Captured          money.Minor // the positive amount credited to the merchant
}

// GetPaymentLegs loads the leg structure of a payment transaction.
func (s *Store) GetPaymentLegs(ctx context.Context, paymentTxnID int64) (PaymentLegs, error) {
	var kind string
	if err := s.pool.QueryRow(ctx, `SELECT kind FROM transactions WHERE id=$1`, paymentTxnID).Scan(&kind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PaymentLegs{}, fmt.Errorf("payment %d: %w", paymentTxnID, ErrNotAPayment)
		}
		return PaymentLegs{}, err
	}
	if kind != "payment" {
		return PaymentLegs{}, fmt.Errorf("txn %d kind=%s: %w", paymentTxnID, kind, ErrNotAPayment)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT account_id, currency, amount FROM postings WHERE txn_id=$1`, paymentTxnID)
	if err != nil {
		return PaymentLegs{}, err
	}
	defer rows.Close()
	var pl PaymentLegs
	found := 0
	for rows.Next() {
		var acc int64
		var cur money.Currency
		var amt int64
		if err := rows.Scan(&acc, &cur, &amt); err != nil {
			return PaymentLegs{}, err
		}
		pl.Currency = cur
		found++
		if amt > 0 {
			pl.MerchantAccountID = acc
			pl.Captured = money.Minor(amt)
		} else {
			pl.SourceAccountID = acc
		}
	}
	if err := rows.Err(); err != nil {
		return PaymentLegs{}, err
	}
	if found != 2 || pl.MerchantAccountID == 0 || pl.SourceAccountID == 0 {
		return PaymentLegs{}, fmt.Errorf("payment %d: %w (unexpected leg shape)", paymentTxnID, ErrNotAPayment)
	}
	return pl, nil
}

// RefundedSoFar returns the total already refunded for a payment (absolute
// minor units), read inside tx so it reflects the row-locked snapshot.
func RefundedSoFar(ctx context.Context, tx pgx.Tx, paymentTxnID, merchantAccountID int64) (money.Minor, error) {
	var sum int64
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(-SUM(p.amount),0)
		   FROM postings p JOIN transactions t ON t.id = p.txn_id
		  WHERE t.kind='refund' AND t.ref_txn_id=$1
		    AND p.account_id=$2 AND p.amount < 0`,
		paymentTxnID, merchantAccountID).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("refunded so far: %w", err)
	}
	return money.Minor(sum), nil
}
