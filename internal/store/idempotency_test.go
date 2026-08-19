package store_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/testhelp"
)

// fundEntry moves `amt` minor units from world -> dst.
func fundEntry(world, dst int64, amt int64) ledger.Entry {
	return ledger.Entry{Kind: ledger.TxnTransfer, Legs: []ledger.Leg{
		{AccountID: world, Currency: "USD", Amount: money.Minor(-amt)},
		{AccountID: dst, Currency: "USD", Amount: money.Minor(amt)},
	}}
}

func TestIdempotentReplaySequential(t *testing.T) {
	st := testhelp.NewStore(t)
	world := mkAccount(t, st, "world:USD", ledger.KindExternal, true)
	alice := mkAccount(t, st, "alice", ledger.KindCustomer, false)

	e := fundEntry(world.ID, alice.ID, 5000)
	key := "idem-key-1"
	hash := store.HashRequest(map[string]any{"op": "fund", "amt": 5000})

	r1, err := st.PostEntryIdempotent(ctx(), key, hash, e, store.IdemResult{})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if r1.Replayed {
		t.Fatal("first call should not be a replay")
	}
	// Replay the same key several times.
	for i := 0; i < 5; i++ {
		r, err := st.PostEntryIdempotent(ctx(), key, hash, e, store.IdemResult{})
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if !r.Replayed {
			t.Fatalf("replay %d should be a replay", i)
		}
		if r.TxnID != r1.TxnID {
			t.Fatalf("replay %d txnID=%d, want %d", i, r.TxnID, r1.TxnID)
		}
	}
	// Exactly one posting pair and alice credited once.
	got, _ := st.GetAccount(ctx(), alice.ID)
	if got.Balance != 5000 {
		t.Fatalf("alice balance = %d, want 5000 (exactly one posting)", got.Balance)
	}
	_, txN, postN, _ := st.Counts(ctx())
	if txN != 1 || postN != 2 {
		t.Fatalf("expected exactly 1 txn / 2 postings, got txns=%d postings=%d", txN, postN)
	}
}

func TestIdempotentConflictDifferentPayload(t *testing.T) {
	st := testhelp.NewStore(t)
	world := mkAccount(t, st, "world:USD", ledger.KindExternal, true)
	alice := mkAccount(t, st, "alice", ledger.KindCustomer, false)

	e := fundEntry(world.ID, alice.ID, 5000)
	key := "idem-key-2"
	if _, err := st.PostEntryIdempotent(ctx(), key, store.HashRequest("A"), e, store.IdemResult{}); err != nil {
		t.Fatal(err)
	}
	// Same key, different request hash -> conflict.
	_, err := st.PostEntryIdempotent(ctx(), key, store.HashRequest("B"), e, store.IdemResult{})
	if !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

// TestIdempotentReplayConcurrent hammers the SAME key from many goroutines and
// asserts exactly one posting is written (exactly-once under concurrent retry).
func TestIdempotentReplayConcurrent(t *testing.T) {
	st := testhelp.NewStore(t)
	world := mkAccount(t, st, "world:USD", ledger.KindExternal, true)
	alice := mkAccount(t, st, "alice", ledger.KindCustomer, false)

	e := fundEntry(world.ID, alice.ID, 777)
	key := "idem-concurrent"
	hash := store.HashRequest(map[string]any{"op": "fund", "amt": 777})

	const workers = 64
	var wg sync.WaitGroup
	var mu sync.Mutex
	txIDs := map[int64]int{}
	errs := make([]error, 0)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			r, err := st.PostEntryIdempotent(ctx(), key, hash, e, store.IdemResult{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			txIDs[r.TxnID]++
		}()
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(txIDs) != 1 {
		t.Fatalf("expected exactly one distinct txn id, got %d: %v", len(txIDs), txIDs)
	}
	// alice must be credited exactly once despite %d concurrent submissions.
	got, _ := st.GetAccount(ctx(), alice.ID)
	if got.Balance != 777 {
		t.Fatalf("alice balance = %d, want 777 (exactly-once)", got.Balance)
	}
	_, txN, postN, _ := st.Counts(ctx())
	if txN != 1 || postN != 2 {
		t.Fatalf("expected exactly 1 txn / 2 postings, got txns=%d postings=%d", txN, postN)
	}
}
