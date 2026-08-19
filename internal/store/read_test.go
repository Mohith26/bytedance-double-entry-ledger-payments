package store_test

import (
	"testing"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/testhelp"
)

func TestAccountLedgerAndBalance(t *testing.T) {
	st := testhelp.NewStore(t)
	world := mkAccount(t, st, "world:USD", ledger.KindExternal, true)
	alice := mkAccount(t, st, "alice", ledger.KindCustomer, false)

	// Two fundings so the ledger has two postings for alice.
	for _, amt := range []int64{1000, 2500} {
		if _, err := st.PostEntry(ctx(), fundEntry(world.ID, alice.ID, amt)); err != nil {
			t.Fatal(err)
		}
	}
	lines, bal, err := st.AccountLedger(ctx(), alice.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 3500 {
		t.Fatalf("balance = %d, want 3500", bal)
	}
	if len(lines) != 2 {
		t.Fatalf("ledger lines = %d, want 2", len(lines))
	}
	// Newest first.
	if lines[0].Amount != 2500 {
		t.Fatalf("newest line amount = %d, want 2500", lines[0].Amount)
	}
}

func TestSumMinBalances(t *testing.T) {
	st := testhelp.NewStore(t)
	world := mkAccount(t, st, "world:USD", ledger.KindExternal, true)
	a := mkAccount(t, st, "a", ledger.KindCustomer, false)
	b := mkAccount(t, st, "b", ledger.KindCustomer, false)
	st.PostEntry(ctx(), fundEntry(world.ID, a.ID, 1000))
	st.PostEntry(ctx(), fundEntry(world.ID, b.ID, 4000))

	sum, min, err := st.SumMinBalances(ctx(), []int64{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sum != 5000 {
		t.Fatalf("sum = %d, want 5000", sum)
	}
	if min != 1000 {
		t.Fatalf("min = %d, want 1000", min)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	st := testhelp.NewStore(t)
	if _, err := st.GetAccount(ctx(), 999999); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestGetPaymentLegsRejectsNonPayment(t *testing.T) {
	st := testhelp.NewStore(t)
	a := mkAccount(t, st, "a", ledger.KindExternal, true)
	b := mkAccount(t, st, "b", ledger.KindExternal, true)
	r, err := st.PostEntry(ctx(), ledger.Entry{Kind: ledger.TxnTransfer, Legs: []ledger.Leg{
		{AccountID: a.ID, Currency: "USD", Amount: money.Minor(-100)},
		{AccountID: b.ID, Currency: "USD", Amount: money.Minor(100)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// A transfer is not a payment -> GetPaymentLegs must reject it.
	if _, err := st.GetPaymentLegs(ctx(), r.TxnID); err == nil {
		t.Fatal("expected ErrNotAPayment for a transfer txn")
	}
	_ = store.ErrNotAPayment
}
