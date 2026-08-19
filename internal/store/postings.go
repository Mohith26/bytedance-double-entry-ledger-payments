package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrInsufficientFunds means a posting would drive a no-negative account
	// below zero; the whole transaction is rejected atomically.
	ErrInsufficientFunds = errors.New("store: insufficient funds (would breach no-negative constraint)")
	// ErrCurrencyMismatch means a leg's currency differs from its account's.
	ErrCurrencyMismatch = errors.New("store: leg currency does not match account currency")
)

// PostResult is the outcome of a successful posting.
type PostResult struct {
	TxnID    int64
	Balances map[int64]money.Minor // post-commit cached balance per touched account
}

// Guard is an optional check that runs AFTER the involved accounts are locked
// FOR UPDATE but BEFORE any posting is written. It lets operations enforce
// extra invariants (e.g. "refund <= captured") atomically under the same locks.
type Guard func(ctx context.Context, tx pgx.Tx) error

// PostEntry validates and posts a balanced journal entry atomically under row
// locks. It is the single write path for all ledger movement.
func (s *Store) PostEntry(ctx context.Context, e ledger.Entry) (PostResult, error) {
	return s.PostEntryGuarded(ctx, e, nil)
}

// PostEntryGuarded is PostEntry with an extra post-lock guard.
func (s *Store) PostEntryGuarded(ctx context.Context, e ledger.Entry, guard Guard) (PostResult, error) {
	var res PostResult
	err := s.runInTx(ctx, func(tx pgx.Tx) error {
		r, err := postEntryTx(ctx, tx, e, guard)
		if err != nil {
			return err
		}
		res = r
		return nil
	})
	return res, err
}

// postEntryTx performs the posting inside an existing transaction. It is shared
// by PostEntry and the idempotent path so the invariant logic lives in one place.
func postEntryTx(ctx context.Context, tx pgx.Tx, e ledger.Entry, guard Guard) (PostResult, error) {
	// 1. Balancing invariant (pure, in-memory) — reject unbalanced up front.
	if err := e.Validate(); err != nil {
		return PostResult{}, err
	}

	// 2. Lock every involved account FOR UPDATE in a deterministic (ascending
	//    id) order. Deterministic ordering prevents deadlocks between concurrent
	//    transfers that touch the same pair of accounts in opposite directions.
	ids := distinctSortedAccountIDs(e.Legs)
	accounts, err := lockAccounts(ctx, tx, ids)
	if err != nil {
		return PostResult{}, err
	}

	// 2b. Optional guard, evaluated while holding the account locks.
	if guard != nil {
		if err := guard(ctx, tx); err != nil {
			return PostResult{}, err
		}
	}

	// 3. Validate currencies and compute per-account deltas.
	deltas := make(map[int64]int64, len(ids))
	for _, l := range e.Legs {
		acc, ok := accounts[l.AccountID]
		if !ok {
			return PostResult{}, fmt.Errorf("%w: id %d", ErrAccountNotFound, l.AccountID)
		}
		if l.Currency != acc.Currency {
			return PostResult{}, fmt.Errorf("%w: leg %s vs account %s", ErrCurrencyMismatch, l.Currency, acc.Currency)
		}
		deltas[l.AccountID] += int64(l.Amount)
	}

	// 4. Enforce the no-negative constraint on the NEW balances.
	newBalances := make(map[int64]money.Minor, len(ids))
	for id, acc := range accounts {
		nb := int64(acc.Balance) + deltas[id]
		if nb < 0 && !acc.AllowNegative {
			return PostResult{}, fmt.Errorf("%w: account %d (%s) balance %d -> %d",
				ErrInsufficientFunds, id, acc.Name, acc.Balance, nb)
		}
		newBalances[id] = money.Minor(nb)
	}

	// 5. Insert the immutable journal entry header.
	var txnID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO transactions (kind, ref_txn_id, pending_settle, memo)
		 VALUES ($1,$2,$3,$4) RETURNING id`,
		string(e.Kind), e.RefTxnID, e.PendingSettle, e.Memo,
	).Scan(&txnID)
	if err != nil {
		return PostResult{}, fmt.Errorf("insert txn: %w", err)
	}

	// 6. Insert the legs and update the cached balances.
	for _, l := range e.Legs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO postings (txn_id, account_id, currency, amount) VALUES ($1,$2,$3,$4)`,
			txnID, l.AccountID, string(l.Currency), int64(l.Amount)); err != nil {
			return PostResult{}, fmt.Errorf("insert posting: %w", err)
		}
	}
	for id, nb := range newBalances {
		if _, err := tx.Exec(ctx, `UPDATE accounts SET balance=$1 WHERE id=$2`, int64(nb), id); err != nil {
			return PostResult{}, fmt.Errorf("update balance: %w", err)
		}
	}

	return PostResult{TxnID: txnID, Balances: newBalances}, nil
}

// lockedAccount is the minimal projection needed while holding the row lock.
type lockedAccount struct {
	ID            int64
	Name          string
	Currency      money.Currency
	AllowNegative bool
	Balance       money.Minor
}

// lockAccounts locks the given account ids FOR UPDATE, returning them by id.
// The ORDER BY id in the query guarantees a consistent lock-acquisition order.
func lockAccounts(ctx context.Context, tx pgx.Tx, ids []int64) (map[int64]lockedAccount, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, name, currency, allow_negative, balance
		   FROM accounts WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock accounts: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]lockedAccount, len(ids))
	for rows.Next() {
		var a lockedAccount
		if err := rows.Scan(&a.ID, &a.Name, &a.Currency, &a.AllowNegative, &a.Balance); err != nil {
			return nil, fmt.Errorf("scan locked account: %w", err)
		}
		out[a.ID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != len(ids) {
		return nil, fmt.Errorf("%w: locked %d of %d accounts", ErrAccountNotFound, len(out), len(ids))
	}
	return out, nil
}

// distinctSortedAccountIDs returns the unique account ids in a stable ascending
// order for deterministic lock acquisition.
func distinctSortedAccountIDs(legs []ledger.Leg) []int64 {
	seen := make(map[int64]struct{}, len(legs))
	var ids []int64
	for _, l := range legs {
		if _, ok := seen[l.AccountID]; !ok {
			seen[l.AccountID] = struct{}{}
			ids = append(ids, l.AccountID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
