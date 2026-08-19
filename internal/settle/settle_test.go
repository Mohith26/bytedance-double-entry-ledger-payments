package settle_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ops"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/settle"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/testhelp"
)

func bg() context.Context { return context.Background() }

func TestSettlementNetsAndReconcilesZeroDrift(t *testing.T) {
	st := testhelp.NewStore(t)
	svc := ops.New(st)
	infra, err := svc.EnsureInfra(bg(), []money.Currency{"USD"})
	if err != nil {
		t.Fatal(err)
	}
	world := infra["USD"].World

	m1, _ := st.CreateAccount(bg(), "merchant-1", "USD", ledger.KindMerchant, false)
	m2, _ := st.CreateAccount(bg(), "merchant-2", "USD", ledger.KindMerchant, false)

	// Two payments to m1, one to m2, plus a partial refund on m1's first.
	p1, _ := svc.Payment(bg(), "p1", world, m1.ID, 10000, "USD")
	svc.Payment(bg(), "p2", world, m1.ID, 5000, "USD")
	svc.Payment(bg(), "p3", world, m2.ID, 8000, "USD")
	if _, err := svc.Refund(bg(), "rf1", p1.TxnID, 2000); err != nil {
		t.Fatal(err)
	}

	// m1 net owed = (10000-2000) + 5000 = 13000 ; m2 = 8000.
	res, err := settle.Settle(bg(), st)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if res.ItemCount != 3 {
		t.Fatalf("settled item_count = %d, want 3", res.ItemCount)
	}
	if res.NetTotal != 21000 {
		t.Fatalf("net_total = %d, want 21000", res.NetTotal)
	}

	// After payout, merchant balances return to zero.
	b1, _ := st.GetAccount(bg(), m1.ID)
	b2, _ := st.GetAccount(bg(), m2.ID)
	if b1.Balance != 0 || b2.Balance != 0 {
		t.Fatalf("merchant balances after settle: m1=%d m2=%d, want 0/0", b1.Balance, b2.Balance)
	}

	// Reconciliation must prove zero drift.
	rep, err := settle.Reconcile(bg(), st)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("reconcile not OK: %+v", rep)
	}
	if rep.DriftCount != 0 || rep.UnbalancedCount != 0 {
		t.Fatalf("expected zero drift, got drift=%d unbalanced=%d", rep.DriftCount, rep.UnbalancedCount)
	}
	if v := rep.Conservation["USD"]; v != 0 {
		t.Fatalf("conservation USD = %d, want 0", v)
	}

	// A second settlement run has nothing pending.
	res2, err := settle.Settle(bg(), st)
	if err != nil {
		t.Fatal(err)
	}
	if res2.ItemCount != 0 {
		t.Fatalf("second settlement item_count = %d, want 0", res2.ItemCount)
	}
}

// TestConcurrentSettlementPaysExactlyOnce proves the settlement race fix: N
// settlement runs fired simultaneously against the same pending payments pay the
// merchant out exactly once (no double-pay), with money conserved and zero drift.
func TestConcurrentSettlementPaysExactlyOnce(t *testing.T) {
	st := testhelp.NewStore(t)
	svc := ops.New(st)
	infra, err := svc.EnsureInfra(bg(), []money.Currency{"USD"})
	if err != nil {
		t.Fatal(err)
	}
	world := infra["USD"].World
	payout, _ := st.GetAccountByName(bg(), "payout:USD")

	m, _ := st.CreateAccount(bg(), "merchant-conc", "USD", ledger.KindMerchant, false)
	if _, err := svc.Payment(bg(), "p", world, m.ID, 10000, "USD"); err != nil {
		t.Fatal(err)
	}

	const runs = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]store.SettlementResult, 0, runs)
	errs := make([]error, 0)
	wg.Add(runs)
	for i := 0; i < runs; i++ {
		go func() {
			defer wg.Done()
			r, err := settle.Settle(bg(), st)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			results = append(results, r)
		}()
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("unexpected settlement errors: %v", errs)
	}
	// Exactly one run should have paid out; the rest settle nothing.
	paidRuns, totalItems, totalNet := 0, 0, int64(0)
	for _, r := range results {
		if r.ItemCount > 0 {
			paidRuns++
			totalItems += r.ItemCount
			totalNet += r.NetTotal
		}
	}
	if paidRuns != 1 || totalItems != 1 || totalNet != 10000 {
		t.Fatalf("expected exactly one paying run of 1 item/10000, got paidRuns=%d items=%d net=%d",
			paidRuns, totalItems, totalNet)
	}
	// The payout account received the money exactly once; merchant is flat.
	pb, _ := st.GetAccount(bg(), payout.ID)
	mb, _ := st.GetAccount(bg(), m.ID)
	if pb.Balance != 10000 {
		t.Fatalf("payout balance = %d, want 10000 (paid exactly once)", pb.Balance)
	}
	if mb.Balance != 0 {
		t.Fatalf("merchant balance = %d, want 0", mb.Balance)
	}
	rep, _ := settle.Reconcile(bg(), st)
	if !rep.OK {
		t.Fatalf("reconcile not OK after concurrent settlement: %+v", rep)
	}
}

func TestReconcileDetectsInjectedDrift(t *testing.T) {
	// Proves the reconciliation job actually detects a stored-vs-derived
	// mismatch (guards against a vacuous "always OK" reconciler).
	st := testhelp.NewStore(t)
	svc := ops.New(st)
	infra, _ := svc.EnsureInfra(bg(), []money.Currency{"USD"})
	m, _ := st.CreateAccount(bg(), "merchant-x", "USD", ledger.KindMerchant, false)
	svc.Payment(bg(), "p", infra["USD"].World, m.ID, 4200, "USD")

	// Corrupt the cached balance directly (simulating a bug/drift).
	if _, err := st.Pool().Exec(bg(), `UPDATE accounts SET balance = balance + 1 WHERE id=$1`, m.ID); err != nil {
		t.Fatal(err)
	}
	rep, err := settle.Reconcile(bg(), st)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("reconcile should have detected injected drift")
	}
	if rep.DriftCount != 1 {
		t.Fatalf("drift count = %d, want 1", rep.DriftCount)
	}
}
