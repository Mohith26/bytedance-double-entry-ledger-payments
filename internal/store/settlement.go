package store

import (
	"context"
	"fmt"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/jackc/pgx/v5"
)

// SettlementResult summarizes a settlement batch run.
type SettlementResult struct {
	BatchID    int64   `json:"batch_id"`
	ItemCount  int     `json:"item_count"` // pending payments settled
	MerchantN  int     `json:"merchant_n"` // distinct merchants paid
	NetTotal   int64   `json:"net_total"`  // total minor units paid out (across currencies)
	PayoutTxns []int64 `json:"payout_txns"`
}

// payoutAccountName is the naming convention for a currency's payout rail.
func payoutAccountName(c money.Currency) string { return "payout:" + string(c) }

// merchantNet is one row of the per-merchant netting query.
type merchantNet struct {
	merchant int64
	currency money.Currency
	net      money.Minor
}

// RunSettlement nets all pending payments per merchant/currency (accounting for
// refunds) and pays out each merchant's still-owed net to its payout rail in a
// single balanced settlement transaction, then flips the payments to settled.
// The whole batch is atomic.
func (s *Store) RunSettlement(ctx context.Context) (SettlementResult, error) {
	var res SettlementResult
	err := s.runInTx(ctx, func(tx pgx.Tx) error {
		nets, itemCount, paymentIDs, err := computeNets(ctx, tx)
		if err != nil {
			return err
		}
		// Create the batch header first so we can stamp items with its id.
		var batchID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO settlement_batches (item_count, net_total) VALUES (0,0) RETURNING id`).
			Scan(&batchID); err != nil {
			return fmt.Errorf("create batch: %w", err)
		}

		var netTotal int64
		var payoutTxns []int64
		for _, mn := range nets {
			if mn.net <= 0 {
				continue // fully refunded: nothing to pay out
			}
			payout, err := payoutAccount(ctx, tx, mn.currency)
			if err != nil {
				return err
			}
			e := ledger.Entry{
				Kind: ledger.TxnSettlement,
				Memo: "settlement payout",
				Legs: []ledger.Leg{
					{AccountID: mn.merchant, Currency: mn.currency, Amount: -mn.net},
					{AccountID: payout, Currency: mn.currency, Amount: mn.net},
				},
			}
			r, err := postEntryTx(ctx, tx, e, nil)
			if err != nil {
				return err
			}
			payoutTxns = append(payoutTxns, r.TxnID)
			netTotal += int64(mn.net)
		}

		// Flip all pending payments to settled and link them to the batch.
		if len(paymentIDs) > 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE transactions SET settled=true, pending_settle=false, settle_batch_id=$1
				  WHERE id = ANY($2)`, batchID, paymentIDs); err != nil {
				return fmt.Errorf("mark settled: %w", err)
			}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE settlement_batches SET item_count=$1, net_total=$2 WHERE id=$3`,
			itemCount, netTotal, batchID); err != nil {
			return fmt.Errorf("finalize batch: %w", err)
		}

		res = SettlementResult{
			BatchID:    batchID,
			ItemCount:  itemCount,
			MerchantN:  len(payoutTxns),
			NetTotal:   netTotal,
			PayoutTxns: payoutTxns,
		}
		return nil
	})
	return res, err
}

// computeNets returns per-merchant net still-owed across pending payments, the
// count of pending payments, and their ids. It locks the involved merchant rows
// FOR UPDATE (via the payout posting later) — here it reads under the tx.
func computeNets(ctx context.Context, tx pgx.Tx) ([]merchantNet, int, []int64, error) {
	rows, err := tx.Query(ctx, `
		WITH pending AS (
		    SELECT t.id AS txn_id, p.account_id AS merchant, p.currency, p.amount AS captured
		      FROM transactions t
		      JOIN postings p ON p.txn_id = t.id AND p.amount > 0
		     WHERE t.kind='payment' AND t.pending_settle=true AND t.settled=false
		),
		refunds AS (
		    SELECT t.ref_txn_id AS payment_id, COALESCE(-SUM(pr.amount),0) AS refunded
		      FROM transactions t
		      JOIN postings pr ON pr.txn_id = t.id AND pr.amount < 0
		     WHERE t.kind='refund'
		     GROUP BY t.ref_txn_id
		)
		SELECT pending.merchant, pending.currency,
		       SUM(pending.captured - COALESCE(refunds.refunded,0)) AS net
		  FROM pending LEFT JOIN refunds ON refunds.payment_id = pending.txn_id
		 GROUP BY pending.merchant, pending.currency
		 ORDER BY pending.merchant`)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("compute nets: %w", err)
	}
	defer rows.Close()
	var nets []merchantNet
	for rows.Next() {
		var mn merchantNet
		var net int64
		if err := rows.Scan(&mn.merchant, &mn.currency, &net); err != nil {
			return nil, 0, nil, err
		}
		mn.net = money.Minor(net)
		nets = append(nets, mn)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}

	// The ids of the pending payments being settled.
	idRows, err := tx.Query(ctx,
		`SELECT id FROM transactions WHERE kind='payment' AND pending_settle=true AND settled=false ORDER BY id`)
	if err != nil {
		return nil, 0, nil, err
	}
	defer idRows.Close()
	var ids []int64
	for idRows.Next() {
		var id int64
		if err := idRows.Scan(&id); err != nil {
			return nil, 0, nil, err
		}
		ids = append(ids, id)
	}
	return nets, len(ids), ids, idRows.Err()
}

// payoutAccount resolves the payout rail account id for a currency.
func payoutAccount(ctx context.Context, tx pgx.Tx, cur money.Currency) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE name=$1`, payoutAccountName(cur)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("payout account for %s: %w", cur, err)
	}
	return id, nil
}
