// Package settle provides the settlement batch trigger and the reconciliation
// job that independently proves the books balance with zero drift.
package settle

import (
	"context"
	"sort"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
)

// DriftItem records an account whose cached balance disagrees with the balance
// recomputed from the journal.
type DriftItem struct {
	AccountID int64       `json:"account_id"`
	Stored    money.Minor `json:"stored"`
	Derived   money.Minor `json:"derived"`
}

// ReconcileReport is the output of the reconciliation job.
type ReconcileReport struct {
	OK              bool                     `json:"ok"`
	Accounts        int                      `json:"accounts"`
	Transactions    int                      `json:"transactions"`
	Postings        int                      `json:"postings"`
	UnbalancedTxns  []int64                  `json:"unbalanced_txns"`
	BalanceDrift    []DriftItem              `json:"balance_drift"`
	Conservation    map[money.Currency]int64 `json:"conservation_by_currency"`
	ConservationOK  bool                     `json:"conservation_ok"`
	DriftCount      int                      `json:"drift_count"`
	UnbalancedCount int                      `json:"unbalanced_count"`
}

// Reconcile independently recomputes balances from the journal and asserts:
//
//	(a) every transaction balances (per-currency leg sums == 0),
//	(b) each account's cached balance == the balance derived from postings,
//	(c) system-wide conservation: postings sum to 0 for every currency.
//
// A fully consistent ledger yields OK=true with zero drift and zero unbalanced
// transactions.
func Reconcile(ctx context.Context, st *store.Store) (ReconcileReport, error) {
	var rep ReconcileReport

	unbalanced, err := st.UnbalancedTxns(ctx)
	if err != nil {
		return rep, err
	}
	rep.UnbalancedTxns = unbalanced
	rep.UnbalancedCount = len(unbalanced)

	stored, err := st.StoredBalances(ctx)
	if err != nil {
		return rep, err
	}
	derived, err := st.RecomputedBalances(ctx)
	if err != nil {
		return rep, err
	}
	for id, s := range stored {
		d := derived[id]
		if s != d {
			rep.BalanceDrift = append(rep.BalanceDrift, DriftItem{AccountID: id, Stored: s, Derived: d})
		}
	}
	sort.Slice(rep.BalanceDrift, func(i, j int) bool {
		return rep.BalanceDrift[i].AccountID < rep.BalanceDrift[j].AccountID
	})
	rep.DriftCount = len(rep.BalanceDrift)

	cons, err := st.ConservationByCurrency(ctx)
	if err != nil {
		return rep, err
	}
	rep.Conservation = cons
	rep.ConservationOK = true
	for _, v := range cons {
		if v != 0 {
			rep.ConservationOK = false
		}
	}

	rep.Accounts, rep.Transactions, rep.Postings, err = st.Counts(ctx)
	if err != nil {
		return rep, err
	}

	rep.OK = rep.UnbalancedCount == 0 && rep.DriftCount == 0 && rep.ConservationOK
	return rep, nil
}

// Settle runs a settlement batch (see store.RunSettlement).
func Settle(ctx context.Context, st *store.Store) (store.SettlementResult, error) {
	return st.RunSettlement(ctx)
}
