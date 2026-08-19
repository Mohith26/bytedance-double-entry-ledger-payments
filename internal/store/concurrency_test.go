package store_test

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/testhelp"
)

// TestConcurrentTransfersConserveMoney is the headline concurrency test. It
// runs many goroutines issuing random transfers among a shared set of
// no-negative accounts and asserts, after the storm:
//   - money is conserved (sum of balances == the seeded total),
//   - no cached balance drifted from the journal-derived balance,
//   - no account went negative (no double-spend / disallowed negative),
//   - no lost updates (conservation would fail if any update were lost).
//
// Run with `go test -race` to also prove the Go code is data-race free.
func TestConcurrentTransfersConserveMoney(t *testing.T) {
	st := testhelp.NewStore(t)

	const (
		numAccounts  = 8
		initialEach  = 100_000 // $1,000.00 each
		workers      = 16
		opsPerWorker = 400
	)
	total := int64(numAccounts) * initialEach

	// Create and fund the shared accounts from a world account.
	world := mkAccount(t, st, "world:USD", ledger.KindExternal, true)
	ids := make([]int64, numAccounts)
	for i := 0; i < numAccounts; i++ {
		a := mkAccount(t, st, fmt.Sprintf("acct-%d", i), ledger.KindCustomer, false)
		ids[i] = a.ID
		fund := ledger.Entry{Kind: ledger.TxnTransfer, Legs: []ledger.Leg{
			{AccountID: world.ID, Currency: "USD", Amount: money.Minor(-initialEach)},
			{AccountID: a.ID, Currency: "USD", Amount: money.Minor(initialEach)},
		}}
		if _, err := st.PostEntry(ctx(), fund); err != nil {
			t.Fatalf("fund acct-%d: %v", i, err)
		}
	}

	var ok, insufficient, other int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < opsPerWorker; i++ {
				fi := rng.Intn(numAccounts)
				ti := rng.Intn(numAccounts)
				if fi == ti {
					ti = (ti + 1) % numAccounts
				}
				amt := money.Minor(rng.Intn(2000) + 1) // 1..2000 minor units
				e := ledger.Entry{Kind: ledger.TxnTransfer, Legs: []ledger.Leg{
					{AccountID: ids[fi], Currency: "USD", Amount: -amt},
					{AccountID: ids[ti], Currency: "USD", Amount: amt},
				}}
				_, err := st.PostEntry(ctx(), e)
				switch {
				case err == nil:
					atomic.AddInt64(&ok, 1)
				case isInsufficient(err):
					atomic.AddInt64(&insufficient, 1)
				default:
					atomic.AddInt64(&other, 1)
					t.Errorf("unexpected transfer error: %v", err)
				}
			}
		}(int64(w) + 1)
	}
	wg.Wait()

	if other != 0 {
		t.Fatalf("%d unexpected errors during concurrent transfers", other)
	}
	t.Logf("concurrent transfers: ok=%d insufficient=%d (workers=%d)", ok, insufficient, workers)

	// 1. Conservation among the shared accounts: their balances must still sum
	//    to the seeded total (world holds the negation).
	var sum int64
	for _, id := range ids {
		a, err := st.GetAccount(ctx(), id)
		if err != nil {
			t.Fatal(err)
		}
		if a.Balance < 0 {
			t.Fatalf("account %d went negative: %d (double-spend / lost update)", id, a.Balance)
		}
		sum += int64(a.Balance)
	}
	if sum != total {
		t.Fatalf("money NOT conserved: sum(balances)=%d, want %d (delta=%d)", sum, total, sum-total)
	}

	// 2. Cached balance == journal-derived balance for every account (no drift).
	stored, _ := st.StoredBalances(ctx())
	derived, _ := st.RecomputedBalances(ctx())
	for id, s := range stored {
		if s != derived[id] {
			t.Fatalf("balance drift on account %d: stored=%d derived=%d", id, s, derived[id])
		}
	}

	// 3. System-wide conservation per currency == 0.
	cons, _ := st.ConservationByCurrency(ctx())
	for cur, v := range cons {
		if v != 0 {
			t.Fatalf("conservation violated for %s: sum(postings)=%d, want 0", cur, v)
		}
	}
}

func isInsufficient(err error) bool {
	return errors.Is(err, store.ErrInsufficientFunds)
}
