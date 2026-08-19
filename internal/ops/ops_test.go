package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ops"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/settle"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/testhelp"
)

func bg() context.Context { return context.Background() }

// fixture provisions infra + a merchant and returns the ops service.
func fixture(t *testing.T) (*ops.Service, map[money.Currency]ops.Infra, int64) {
	t.Helper()
	st := testhelp.NewStore(t)
	svc := ops.New(st)
	infra, err := svc.EnsureInfra(bg(), []money.Currency{"USD", "EUR"})
	if err != nil {
		t.Fatalf("ensure infra: %v", err)
	}
	merch, err := st.CreateAccount(bg(), "merchant-1", "USD", ledger.KindMerchant, false)
	if err != nil {
		t.Fatal(err)
	}
	return svc, infra, merch.ID
}

func mustReconcileClean(t *testing.T, svc *ops.Service) {
	t.Helper()
	rep, err := settle.Reconcile(bg(), svc.Store())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("reconcile not clean: drift=%d unbalanced=%d conservationOK=%v",
			rep.DriftCount, rep.UnbalancedCount, rep.ConservationOK)
	}
}

func TestPaymentAndRefund(t *testing.T) {
	svc, infra, merch := fixture(t)
	world := infra["USD"].World

	pay, err := svc.Payment(bg(), "pay-1", world, merch, 10000, "USD")
	if err != nil {
		t.Fatalf("payment: %v", err)
	}
	m, _ := svc.Store().GetAccount(bg(), merch)
	if m.Balance != 10000 {
		t.Fatalf("merchant balance = %d, want 10000", m.Balance)
	}

	// Partial refund of 3000.
	if _, err := svc.Refund(bg(), "refund-1", pay.TxnID, 3000); err != nil {
		t.Fatalf("refund: %v", err)
	}
	m, _ = svc.Store().GetAccount(bg(), merch)
	if m.Balance != 7000 {
		t.Fatalf("merchant after refund = %d, want 7000", m.Balance)
	}
	mustReconcileClean(t, svc)
}

func TestRefundNeverExceedsCaptured(t *testing.T) {
	svc, infra, merch := fixture(t)
	world := infra["USD"].World

	pay, err := svc.Payment(bg(), "pay-2", world, merch, 5000, "USD")
	if err != nil {
		t.Fatal(err)
	}
	// Refund 4000 (ok), then 2000 (would exceed 5000 total) -> rejected.
	if _, err := svc.Refund(bg(), "r-a", pay.TxnID, 4000); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	_, err = svc.Refund(bg(), "r-b", pay.TxnID, 2000)
	if !errors.Is(err, ops.ErrRefundExceedsCaptured) {
		t.Fatalf("expected ErrRefundExceedsCaptured, got %v", err)
	}
	// The remaining 1000 is refundable.
	if _, err := svc.Refund(bg(), "r-c", pay.TxnID, 1000); err != nil {
		t.Fatalf("remainder refund: %v", err)
	}
	mustReconcileClean(t, svc)
}

func TestRefundIdempotent(t *testing.T) {
	svc, infra, merch := fixture(t)
	world := infra["USD"].World
	pay, _ := svc.Payment(bg(), "pay-3", world, merch, 5000, "USD")

	r1, err := svc.Refund(bg(), "refund-idem", pay.TxnID, 2000)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.Refund(bg(), "refund-idem", pay.TxnID, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Replayed || r1.TxnID != r2.TxnID {
		t.Fatalf("expected replay of same refund: r1=%+v r2=%+v", r1, r2)
	}
	// Merchant should be debited only once (5000 - 2000 = 3000).
	m, _ := svc.Store().GetAccount(bg(), merch)
	if m.Balance != 3000 {
		t.Fatalf("merchant = %d, want 3000 (refund applied once)", m.Balance)
	}
}

func TestFXConservesPerCurrency(t *testing.T) {
	svc, infra, _ := fixture(t)
	// Fund a USD customer and create an EUR recipient.
	alice, _ := svc.Store().CreateAccount(bg(), "alice-usd", "USD", ledger.KindCustomer, false)
	bob, _ := svc.Store().CreateAccount(bg(), "bob-eur", "EUR", ledger.KindCustomer, false)
	if _, err := svc.Payment(bg(), "fund-alice", infra["USD"].World, alice.ID, 10000, "USD"); err != nil {
		t.Fatal(err)
	}

	// Convert 100.00 USD -> EUR at 0.92.
	fx, err := svc.ConvertFX(bg(), "fx-1", alice.ID, bob.ID, 10000, money.Rate{Num: 92, Den: 100})
	if err != nil {
		t.Fatalf("fx: %v", err)
	}
	if fx.ToAmount != 9200 {
		t.Fatalf("converted = %d, want 9200", fx.ToAmount)
	}
	a, _ := svc.Store().GetAccount(bg(), alice.ID)
	b, _ := svc.Store().GetAccount(bg(), bob.ID)
	if a.Balance != 0 {
		t.Fatalf("alice USD = %d, want 0", a.Balance)
	}
	if b.Balance != 9200 {
		t.Fatalf("bob EUR = %d, want 9200", b.Balance)
	}
	// No money created or destroyed in any currency.
	cons, _ := svc.Store().ConservationByCurrency(bg())
	if cons["USD"] != 0 || cons["EUR"] != 0 {
		t.Fatalf("conservation broken: USD=%d EUR=%d", cons["USD"], cons["EUR"])
	}
	mustReconcileClean(t, svc)
}

func TestTransferIdempotent(t *testing.T) {
	svc, infra, _ := fixture(t)
	a, _ := svc.Store().CreateAccount(bg(), "a", "USD", ledger.KindCustomer, false)
	b, _ := svc.Store().CreateAccount(bg(), "b", "USD", ledger.KindCustomer, false)
	svc.Payment(bg(), "fund-a", infra["USD"].World, a.ID, 5000, "USD")

	if _, err := svc.Transfer(bg(), "t-1", a.ID, b.ID, 1000, "USD"); err != nil {
		t.Fatal(err)
	}
	r2, err := svc.Transfer(bg(), "t-1", a.ID, b.ID, 1000, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Replayed {
		t.Fatal("expected replay")
	}
	ab, _ := svc.Store().GetAccount(bg(), a.ID)
	bb, _ := svc.Store().GetAccount(bg(), b.ID)
	if ab.Balance != 4000 || bb.Balance != 1000 {
		t.Fatalf("balances a=%d b=%d, want 4000/1000 (transfer applied once)", ab.Balance, bb.Balance)
	}
}
