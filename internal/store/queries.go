package store

import (
	"context"
	"fmt"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
)

// RecomputedBalances returns each account's balance computed independently from
// the journal (SUM of its postings). Accounts with no postings are 0.
func (s *Store) RecomputedBalances(ctx context.Context) (map[int64]money.Minor, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.id, COALESCE(SUM(p.amount),0)
		   FROM accounts a LEFT JOIN postings p ON p.account_id = a.id
		  GROUP BY a.id`)
	if err != nil {
		return nil, fmt.Errorf("recompute balances: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]money.Minor)
	for rows.Next() {
		var id int64
		var sum int64
		if err := rows.Scan(&id, &sum); err != nil {
			return nil, err
		}
		out[id] = money.Minor(sum)
	}
	return out, rows.Err()
}

// StoredBalances returns each account's cached balance column.
func (s *Store) StoredBalances(ctx context.Context) (map[int64]money.Minor, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, balance FROM accounts`)
	if err != nil {
		return nil, fmt.Errorf("stored balances: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]money.Minor)
	for rows.Next() {
		var id int64
		var bal int64
		if err := rows.Scan(&id, &bal); err != nil {
			return nil, err
		}
		out[id] = money.Minor(bal)
	}
	return out, rows.Err()
}

// ConservationByCurrency returns the sum of ALL postings grouped by currency.
// For a correct ledger every value here is exactly 0 (money conservation).
func (s *Store) ConservationByCurrency(ctx context.Context) (map[money.Currency]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT currency, SUM(amount) FROM postings GROUP BY currency`)
	if err != nil {
		return nil, fmt.Errorf("conservation: %w", err)
	}
	defer rows.Close()
	out := make(map[money.Currency]int64)
	for rows.Next() {
		var cur money.Currency
		var sum int64
		if err := rows.Scan(&cur, &sum); err != nil {
			return nil, err
		}
		out[cur] = sum
	}
	return out, rows.Err()
}

// SumBalancesByCurrency returns the sum of all cached account balances grouped
// by currency. Because every value movement is a balanced posting funded from a
// world/external account, a correct system nets to exactly 0 per currency — the
// money-conservation invariant read from the cached balances. A single SELECT
// sees a consistent MVCC snapshot, so this can be sampled during a concurrent
// workload to prove the invariant holds continuously.
func (s *Store) SumBalancesByCurrency(ctx context.Context) (map[money.Currency]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT currency, COALESCE(SUM(balance),0) FROM accounts GROUP BY currency`)
	if err != nil {
		return nil, fmt.Errorf("sum balances: %w", err)
	}
	defer rows.Close()
	out := make(map[money.Currency]int64)
	for rows.Next() {
		var cur money.Currency
		var sum int64
		if err := rows.Scan(&cur, &sum); err != nil {
			return nil, err
		}
		out[cur] = sum
	}
	return out, rows.Err()
}

// SumMinBalances returns the sum and minimum cached balance across the given
// account ids in a single consistent snapshot. Used by the load monitor to
// check, continuously, that the shared accounts still hold the seeded total
// (no lost updates) and none is negative (no double-spend).
func (s *Store) SumMinBalances(ctx context.Context, ids []int64) (sum, min int64, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(balance),0), COALESCE(MIN(balance),0) FROM accounts WHERE id = ANY($1)`, ids).
		Scan(&sum, &min)
	if err != nil {
		return 0, 0, fmt.Errorf("sum/min balances: %w", err)
	}
	return sum, min, nil
}

// UnbalancedTxns returns the ids of any transactions whose legs do not sum to
// zero within some currency. A correct ledger returns an empty slice.
func (s *Store) UnbalancedTxns(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT txn_id FROM (
		    SELECT txn_id, currency, SUM(amount) AS s
		      FROM postings GROUP BY txn_id, currency
		 ) t WHERE s <> 0
		 GROUP BY txn_id ORDER BY txn_id`)
	if err != nil {
		return nil, fmt.Errorf("unbalanced txns: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Counts returns the number of accounts and transactions (for reporting).
func (s *Store) Counts(ctx context.Context) (accounts, txns, postings int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM accounts),
		(SELECT count(*) FROM transactions),
		(SELECT count(*) FROM postings)`).Scan(&accounts, &txns, &postings)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("counts: %w", err)
	}
	return accounts, txns, postings, nil
}
