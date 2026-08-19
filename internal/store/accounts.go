package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/jackc/pgx/v5"
)

// ErrAccountNotFound is returned when an account id/name does not exist.
var ErrAccountNotFound = errors.New("store: account not found")

// CreateAccount inserts a new single-currency account and returns it.
func (s *Store) CreateAccount(ctx context.Context, name string, cur money.Currency, kind ledger.AccountKind, allowNegative bool) (ledger.Account, error) {
	if name == "" {
		return ledger.Account{}, errors.New("store: account name required")
	}
	if !money.IsSupported(cur) {
		return ledger.Account{}, fmt.Errorf("%w: %q", money.ErrUnknownCurrency, cur)
	}
	var a ledger.Account
	err := s.pool.QueryRow(ctx,
		`INSERT INTO accounts (name, currency, kind, allow_negative, balance)
		 VALUES ($1,$2,$3,$4,0)
		 RETURNING id, name, currency, kind, allow_negative, balance`,
		name, string(cur), string(kind), allowNegative,
	).Scan(&a.ID, &a.Name, &a.Currency, &a.Kind, &a.AllowNegative, &a.Balance)
	if err != nil {
		return ledger.Account{}, fmt.Errorf("create account: %w", err)
	}
	return a, nil
}

// EnsureAccount creates the named account if it does not already exist and
// returns it (idempotent provisioning helper).
func (s *Store) EnsureAccount(ctx context.Context, name string, cur money.Currency, kind ledger.AccountKind, allowNegative bool) (ledger.Account, error) {
	if !money.IsSupported(cur) {
		return ledger.Account{}, fmt.Errorf("%w: %q", money.ErrUnknownCurrency, cur)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO accounts (name, currency, kind, allow_negative, balance)
		 VALUES ($1,$2,$3,$4,0) ON CONFLICT (name) DO NOTHING`,
		name, string(cur), string(kind), allowNegative)
	if err != nil {
		return ledger.Account{}, fmt.Errorf("ensure account: %w", err)
	}
	return s.GetAccountByName(ctx, name)
}

// GetAccount returns an account by id.
func (s *Store) GetAccount(ctx context.Context, id int64) (ledger.Account, error) {
	return scanAccount(s.pool.QueryRow(ctx,
		`SELECT id, name, currency, kind, allow_negative, balance FROM accounts WHERE id=$1`, id))
}

// GetAccountByName returns an account by unique name.
func (s *Store) GetAccountByName(ctx context.Context, name string) (ledger.Account, error) {
	return scanAccount(s.pool.QueryRow(ctx,
		`SELECT id, name, currency, kind, allow_negative, balance FROM accounts WHERE name=$1`, name))
}

// scanAccount is a shared row scanner.
func scanAccount(row pgx.Row) (ledger.Account, error) {
	var a ledger.Account
	err := row.Scan(&a.ID, &a.Name, &a.Currency, &a.Kind, &a.AllowNegative, &a.Balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Account{}, ErrAccountNotFound
	}
	if err != nil {
		return ledger.Account{}, fmt.Errorf("scan account: %w", err)
	}
	return a, nil
}

// LedgerLine is one posting as seen in an account statement.
type LedgerLine struct {
	PostingID int64          `json:"posting_id"`
	TxnID     int64          `json:"txn_id"`
	Kind      ledger.TxnKind `json:"kind"`
	Currency  money.Currency `json:"currency"`
	Amount    money.Minor    `json:"amount_minor"`
}

// AccountLedger returns the postings for an account, newest first, with the
// (cached) current balance.
func (s *Store) AccountLedger(ctx context.Context, accountID int64, limit int) ([]LedgerLine, money.Minor, error) {
	acc, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT p.id, p.txn_id, t.kind, p.currency, p.amount
		   FROM postings p JOIN transactions t ON t.id = p.txn_id
		  WHERE p.account_id = $1
		  ORDER BY p.id DESC
		  LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("query ledger: %w", err)
	}
	defer rows.Close()
	var out []LedgerLine
	for rows.Next() {
		var l LedgerLine
		if err := rows.Scan(&l.PostingID, &l.TxnID, &l.Kind, &l.Currency, &l.Amount); err != nil {
			return nil, 0, fmt.Errorf("scan ledger: %w", err)
		}
		out = append(out, l)
	}
	return out, acc.Balance, rows.Err()
}
