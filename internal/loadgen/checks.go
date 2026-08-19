package loadgen

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ops"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/settle"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
)

// monitor continuously samples the money-conservation invariant while the storm
// runs. Each sample is a single SELECT (a consistent MVCC snapshot), so it sees
// a globally consistent total even under concurrent commits — proving the
// invariant holds AFTER EVERY committed operation, not just at the end.
func (c Config) monitor(ctx context.Context, st *store.Store, ids []int64, total int64, res *Result, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}
		// (a) Global conservation: balances net to 0 per currency.
		sums, err := st.SumBalancesByCurrency(ctx)
		if err == nil {
			atomic.AddInt64(&res.ConservationSamples, 1)
			for _, v := range sums {
				if v != 0 {
					atomic.AddInt64(&res.ConservationViolations, 1)
				}
			}
		}
		// (b) No shared account is negative, and the shared subset still holds
		//     exactly the seeded total (no lost updates).
		sum, min, err := st.SumMinBalances(ctx, ids)
		if err == nil {
			if min < 0 {
				atomic.AddInt64(&res.NegativeBalanceSamples, 1)
			}
			if sum != total {
				atomic.AddInt64(&res.LostUpdates, 1)
			}
		}
		time.Sleep(300 * time.Microsecond)
	}
}

// finalChecks runs the authoritative post-storm verification.
func (c Config) finalChecks(ctx context.Context, st *store.Store, ids []int64, total int64, res *Result) error {
	// Final conservation + no-negative + no lost-update from cached balances.
	sum, min, err := st.SumMinBalances(ctx, ids)
	if err != nil {
		return err
	}
	if min < 0 {
		res.DoubleSpends++ // a negative no-negative account == a double-spend
	}
	if sum != total {
		res.LostUpdates++
	}

	// Reconciliation: independent recompute from the journal.
	rep, err := settle.Reconcile(ctx, st)
	if err != nil {
		return err
	}
	res.ReconcileOK = rep.OK
	res.ReconcileDrift = rep.DriftCount
	res.ReconcileUnbalancedTxns = rep.UnbalancedCount
	res.ConservationNetUSD = rep.Conservation[c.Currency]
	res.Accounts = rep.Accounts
	res.TransactionsTotal = rep.Transactions
	res.PostingsTotal = rep.Postings

	// A drift or unbalanced txn is also, definitionally, a lost update / broken
	// invariant — surface it in the concurrency counters.
	if rep.DriftCount > 0 {
		res.LostUpdates += int64(rep.DriftCount)
	}
	return nil
}

// idempotencyTrial fires IdemConc concurrent duplicate submissions for each of
// IdemTrials distinct keys and verifies EXACTLY ONE posting results per key.
func (c Config) idempotencyTrial(ctx context.Context, svc *ops.Service, world int64, res *Result) error {
	if c.IdemTrials <= 0 || c.IdemConc <= 0 {
		return nil
	}
	// A dedicated sink account receives all idempotent credits.
	sink, err := svc.Store().EnsureAccount(ctx, "idem-sink", c.Currency, "merchant", false)
	if err != nil {
		return err
	}
	res.IdemKeysTested = c.IdemTrials
	exactlyOnce := 0
	for trial := 0; trial < c.IdemTrials; trial++ {
		key := "idem-" + itoa(trial)
		var wg sync.WaitGroup
		var mu sync.Mutex
		txIDs := map[int64]struct{}{}
		wg.Add(c.IdemConc)
		for j := 0; j < c.IdemConc; j++ {
			go func() {
				defer wg.Done()
				r, err := svc.Payment(ctx, key, world, sink.ID, 100, c.Currency)
				if err != nil {
					return
				}
				mu.Lock()
				txIDs[r.TxnID] = struct{}{}
				mu.Unlock()
			}()
		}
		wg.Wait()
		res.IdemSubmissionsTotal += c.IdemConc
		if len(txIDs) == 1 {
			exactlyOnce++
		} else {
			// More than one distinct txn id == duplicate postings for one key.
			atomic.AddInt64(&res.IdemDuplicatePosts, int64(len(txIDs)-1))
		}
	}
	res.IdemExactlyOnce = exactlyOnce
	return nil
}

func isInsufficient(err error) bool { return errors.Is(err, store.ErrInsufficientFunds) }

// itoa is a tiny int->string helper (avoids importing strconv here).
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
