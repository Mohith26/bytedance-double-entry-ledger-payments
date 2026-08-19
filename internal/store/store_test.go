package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/testhelp"
)

func ctx() context.Context { return context.Background() }

// mkAccount is a small helper.
func mkAccount(t *testing.T, st *store.Store, name string, kind ledger.AccountKind, allowNeg bool) ledger.Account {
	t.Helper()
	a, err := st.CreateAccount(ctx(), name, "USD", kind, allowNeg)
	if err != nil {
		t.Fatalf("create account %s: %v", name, err)
	}
	return a
}

func TestTransferBalancesAndDerivesFromPostings(t *testing.T) {
	st := testhelp.NewStore(t)
	world := mkAccount(t, st, "world:USD", ledger.KindExternal, true)
	alice := mkAccount(t, st, "alice", ledger.KindCustomer, false)

	// Fund alice from the world (world may go negative).
	fund := ledger.Entry{Kind: ledger.TxnTransfer, Legs: []ledger.Leg{
		{AccountID: world.ID, Currency: "USD", Amount: -10000},
		{AccountID: alice.ID, Currency: "USD", Amount: 10000},
	}}
	if _, err := st.PostEntry(ctx(), fund); err != nil {
		t.Fatalf("fund: %v", err)
	}

	got, err := st.GetAccount(ctx(), alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Balance != 10000 {
		t.Fatalf("alice balance = %d, want 10000", got.Balance)
	}

	// Derived balance (sum of postings) must equal the cached balance.
	derived, _ := st.RecomputedBalances(ctx())
	if derived[alice.ID] != 10000 {
		t.Fatalf("derived alice = %d, want 10000", derived[alice.ID])
	}
	if derived[world.ID] != -10000 {
		t.Fatalf("derived world = %d, want -10000", derived[world.ID])
	}
}

func TestUnbalancedEntryRejectedAtomically(t *testing.T) {
	st := testhelp.NewStore(t)
	a := mkAccount(t, st, "a", ledger.KindExternal, true)
	b := mkAccount(t, st, "b", ledger.KindExternal, true)

	bad := ledger.Entry{Kind: ledger.TxnTransfer, Legs: []ledger.Leg{
		{AccountID: a.ID, Currency: "USD", Amount: -500},
		{AccountID: b.ID, Currency: "USD", Amount: 499}, // does not balance
	}}
	if _, err := st.PostEntry(ctx(), bad); !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("expected ErrUnbalanced, got %v", err)
	}
	// No transaction/posting should have been written.
	accN, txN, postN, err := st.Counts(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if txN != 0 || postN != 0 {
		t.Fatalf("expected 0 txns/postings after rejection, got txns=%d postings=%d (accounts=%d)", txN, postN, accN)
	}
}

func TestInsufficientFundsRejected(t *testing.T) {
	st := testhelp.NewStore(t)
	world := mkAccount(t, st, "world:USD", ledger.KindExternal, true)
	alice := mkAccount(t, st, "alice", ledger.KindCustomer, false) // no-negative

	// alice has 0; try to move 100 out -> would go negative -> rejected.
	e := ledger.Entry{Kind: ledger.TxnTransfer, Legs: []ledger.Leg{
		{AccountID: alice.ID, Currency: "USD", Amount: -100},
		{AccountID: world.ID, Currency: "USD", Amount: 100},
	}}
	if _, err := st.PostEntry(ctx(), e); !errors.Is(err, store.ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
}

func TestCurrencyMismatchRejected(t *testing.T) {
	st := testhelp.NewStore(t)
	usd := mkAccount(t, st, "usd", ledger.KindExternal, true)
	eur, err := st.CreateAccount(ctx(), "eur", "EUR", ledger.KindExternal, true)
	if err != nil {
		t.Fatal(err)
	}
	// Post an EUR leg against a USD account.
	e := ledger.Entry{Kind: ledger.TxnTransfer, Legs: []ledger.Leg{
		{AccountID: usd.ID, Currency: "EUR", Amount: -100},
		{AccountID: eur.ID, Currency: "EUR", Amount: 100},
	}}
	if _, err := st.PostEntry(ctx(), e); !errors.Is(err, store.ErrCurrencyMismatch) {
		t.Fatalf("expected ErrCurrencyMismatch, got %v", err)
	}
}
