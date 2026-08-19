package store

import (
	"context"
	"fmt"
	"sort"

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
//
// Concurrency safety (this is the critical part): the merchant AND payout
// accounts are locked FOR UPDATE *before* the net is computed, and the net is
// then computed UNDER those locks. Because payments and refunds also take the
// merchant-account row lock, this guarantees:
//   - no refund can land between "compute net" and "pay out" (it would either be
//     reflected in the net, or blocked until after settlement commits), so a
//     merchant is never over-paid a stale (pre-refund) net;
//   - two overlapping settlement runs serialize on the merchant lock; the second
//     recomputes and finds the payments already settled, so it pays nothing.
//
// Repeated / concurrent /settle calls are therefore safe: each pending payment
// is ever paid out exactly once. The whole batch is atomic.
func (s *Store) RunSettlement(ctx context.Context) (SettlementResult, error) {
	var res SettlementResult
	err := s.runInTx(ctx, func(tx pgx.Tx) error {
		// 1. Which merchants (and currencies) currently have pending payments.
		merchants, currencies, err := candidateMerchants(ctx, tx)
		if err != nil {
			return err
		}

		// 2. Resolve the payout account for each currency involved.
		payouts := make(map[money.Currency]int64, len(currencies))
		for _, c := range currencies {
			id, err := payoutAccount(ctx, tx, c)
			if err != nil {
				return err
			}
			payouts[c] = id
		}

		// 3. Lock every account this batch will touch (merchants ∪ payouts) in a
		//    single ascending-id FOR UPDATE so the net computed next is a frozen,
		//    consistent snapshot and there is no lock-order inversion with the
		//    per-payout postEntryTx below.
		lockIDs := unionSortedIDs(merchants, payouts)
		if _, err := lockAccounts(ctx, tx, lockIDs); err != nil {
			return err
		}

		// 4. Compute nets + the exact pending payment ids UNDER the locks,
		//    restricted to the locked merchants.
		nets, itemCount, paymentIDs, err := computeNetsLocked(ctx, tx, merchants)
		if err != nil {
			return err
		}

		// 5. Create the batch, post each merchant's payout, mark settled.
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
			e := ledger.Entry{
				Kind: ledger.TxnSettlement,
				Memo: "settlement payout",
				Legs: []ledger.Leg{
					{AccountID: mn.merchant, Currency: mn.currency, Amount: -mn.net},
					{AccountID: payouts[mn.currency], Currency: mn.currency, Amount: mn.net},
				},
			}
			// postEntryTx re-locks these already-held rows (a no-op within this
			// txn) and posts the balanced payout.
			r, err := postEntryTx(ctx, tx, e, nil)
			if err != nil {
				return err
			}
			payoutTxns = append(payoutTxns, r.TxnID)
			netTotal += int64(mn.net)
		}

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

// candidateMerchants returns the distinct merchant account ids that currently
// have pending, unsettled payments, plus the distinct currencies involved.
func candidateMerchants(ctx context.Context, tx pgx.Tx) ([]int64, []money.Currency, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT p.account_id, p.currency
		  FROM transactions t
		  JOIN postings p ON p.txn_id = t.id AND p.amount > 0
		 WHERE t.kind='payment' AND t.pending_settle=true AND t.settled=false
		 ORDER BY p.account_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("candidate merchants: %w", err)
	}
	defer rows.Close()
	var merchants []int64
	curSet := make(map[money.Currency]struct{})
	for rows.Next() {
		var id int64
		var cur money.Currency
		if err := rows.Scan(&id, &cur); err != nil {
			return nil, nil, err
		}
		merchants = append(merchants, id)
		curSet[cur] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	currencies := make([]money.Currency, 0, len(curSet))
	for c := range curSet {
		currencies = append(currencies, c)
	}
	return merchants, currencies, nil
}

// computeNetsLocked returns per-merchant net still-owed and the ids of the
// pending payments, restricted to the given (locked) merchant ids. It must be
// called while those merchant rows are locked FOR UPDATE.
func computeNetsLocked(ctx context.Context, tx pgx.Tx, merchantIDs []int64) ([]merchantNet, int, []int64, error) {
	if len(merchantIDs) == 0 {
		return nil, 0, nil, nil
	}
	rows, err := tx.Query(ctx, `
		WITH pending AS (
		    SELECT t.id AS txn_id, p.account_id AS merchant, p.currency, p.amount AS captured
		      FROM transactions t
		      JOIN postings p ON p.txn_id = t.id AND p.amount > 0
		     WHERE t.kind='payment' AND t.pending_settle=true AND t.settled=false
		       AND p.account_id = ANY($1)
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
		 ORDER BY pending.merchant`, merchantIDs)
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

	idRows, err := tx.Query(ctx, `
		SELECT t.id
		  FROM transactions t
		  JOIN postings p ON p.txn_id = t.id AND p.amount > 0
		 WHERE t.kind='payment' AND t.pending_settle=true AND t.settled=false
		   AND p.account_id = ANY($1)
		 ORDER BY t.id`, merchantIDs)
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

// unionSortedIDs returns the sorted, de-duplicated union of the merchant ids and
// the payout account ids.
func unionSortedIDs(merchants []int64, payouts map[money.Currency]int64) []int64 {
	seen := make(map[int64]struct{}, len(merchants)+len(payouts))
	var ids []int64
	add := func(id int64) {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	for _, m := range merchants {
		add(m)
	}
	for _, p := range payouts {
		add(p)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
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
