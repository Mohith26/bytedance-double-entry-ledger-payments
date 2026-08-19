package loadgen

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ops"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
)

// Config parameterizes a load run. All randomness derives from Seed so runs are
// reproducible.
type Config struct {
	Workers      int
	OpsPerWorker int
	Accounts     int
	InitialEach  money.Minor
	Seed         int64
	IdemTrials   int // number of distinct idempotency keys to stress
	IdemConc     int // concurrent duplicate submissions per key
	Currency     money.Currency
}

// Default returns sensible defaults.
func Default() Config {
	return Config{
		Workers: 16, OpsPerWorker: 2000, Accounts: 16, InitialEach: 1_000_000,
		Seed: 42, IdemTrials: 200, IdemConc: 16, Currency: "USD",
	}
}

// Result holds every measured metric from a run.
type Result struct {
	Config Config `json:"config"`

	// Workload counts.
	CommittedTransfers int64 `json:"committed_transfers"`
	InsufficientFunds  int64 `json:"insufficient_funds_rejected"`
	OtherErrors        int64 `json:"other_errors"`
	Accounts           int   `json:"accounts"`
	TransactionsTotal  int   `json:"transactions_total"`
	PostingsTotal      int   `json:"postings_total"`

	// Performance.
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	ThroughputTps  float64 `json:"throughput_txns_per_sec"`
	LatP50Ms       float64 `json:"latency_p50_ms"`
	LatP99Ms       float64 `json:"latency_p99_ms"`
	LatP999Ms      float64 `json:"latency_p99_9_ms"`
	LatMeanMs      float64 `json:"latency_mean_ms"`
	LatMaxMs       float64 `json:"latency_max_ms"`

	// Money-conservation invariant (checked continuously during the storm).
	ConservationSamples    int64 `json:"conservation_samples"`
	ConservationViolations int64 `json:"conservation_violations"`
	NegativeBalanceSamples int64 `json:"negative_balance_violations"`

	// Concurrency-safety outcomes.
	DoubleSpends int64 `json:"double_spends"`
	LostUpdates  int64 `json:"lost_updates"`

	// Idempotency exactly-once.
	IdemKeysTested       int   `json:"idempotency_keys_tested"`
	IdemSubmissionsTotal int   `json:"idempotency_submissions_total"`
	IdemExactlyOnce      int   `json:"idempotency_exactly_once"`
	IdemDuplicatePosts   int64 `json:"idempotency_duplicate_postings"`

	// Reconciliation.
	ReconcileOK             bool  `json:"reconcile_ok"`
	ReconcileDrift          int   `json:"reconcile_drift_count"`
	ReconcileUnbalancedTxns int   `json:"reconcile_unbalanced_txns"`
	ConservationNetUSD      int64 `json:"conservation_net_final"`

	Mode string `json:"isolation_mode"`
}

// Run executes the full load + verification workload against st.
func (c Config) Run(ctx context.Context, st *store.Store) (Result, error) {
	svc := ops.New(st)
	if err := st.Reset(ctx); err != nil {
		return Result{}, err
	}
	infra, err := svc.EnsureInfra(ctx, []money.Currency{c.Currency})
	if err != nil {
		return Result{}, err
	}
	world := infra[c.Currency].World

	ids, total, err := c.seedAccounts(ctx, svc, world)
	if err != nil {
		return Result{}, err
	}

	res := Result{Config: c, Mode: string(st.Mode())}

	// Monitor conservation continuously while the storm runs.
	stop := make(chan struct{})
	var monWG sync.WaitGroup
	monWG.Add(1)
	go c.monitor(ctx, st, ids, int64(total), &res, stop, &monWG)

	start := time.Now()
	c.runStorm(ctx, svc, ids, &res)
	res.ElapsedSeconds = time.Since(start).Seconds()

	close(stop)
	monWG.Wait()

	if res.CommittedTransfers > 0 && res.ElapsedSeconds > 0 {
		res.ThroughputTps = float64(res.CommittedTransfers) / res.ElapsedSeconds
	}

	if err := c.finalChecks(ctx, st, ids, int64(total), &res); err != nil {
		return res, err
	}
	if err := c.idempotencyTrial(ctx, svc, world, &res); err != nil {
		return res, err
	}
	return res, nil
}

// seedAccounts creates and funds the shared accounts.
func (c Config) seedAccounts(ctx context.Context, svc *ops.Service, world int64) ([]int64, money.Minor, error) {
	ids := make([]int64, c.Accounts)
	var total money.Minor
	for i := 0; i < c.Accounts; i++ {
		a, err := svc.Store().CreateAccount(ctx, fmt.Sprintf("bench-acct-%d", i), c.Currency, ledger.KindCustomer, false)
		if err != nil {
			return nil, 0, err
		}
		ids[i] = a.ID
		if _, err := svc.Payment(ctx, "", world, a.ID, c.InitialEach, c.Currency); err != nil {
			return nil, 0, fmt.Errorf("fund acct %d: %w", i, err)
		}
		total += c.InitialEach
	}
	return ids, total, nil
}

// runStorm launches Workers goroutines issuing random transfers.
func (c Config) runStorm(ctx context.Context, svc *ops.Service, ids []int64, res *Result) {
	var latMu sync.Mutex
	all := &latencies{}
	var wg sync.WaitGroup
	wg.Add(c.Workers)
	for w := 0; w < c.Workers; w++ {
		go func(workerSeed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(workerSeed))
			local := make([]time.Duration, 0, c.OpsPerWorker)
			for i := 0; i < c.OpsPerWorker; i++ {
				fi := rng.Intn(len(ids))
				ti := rng.Intn(len(ids))
				if fi == ti {
					ti = (ti + 1) % len(ids)
				}
				amt := money.Minor(rng.Intn(5000) + 1)
				t0 := time.Now()
				_, err := svc.Transfer(ctx, "", ids[fi], ids[ti], amt, c.Currency)
				local = append(local, time.Since(t0))
				switch {
				case err == nil:
					atomic.AddInt64(&res.CommittedTransfers, 1)
				case isInsufficient(err):
					atomic.AddInt64(&res.InsufficientFunds, 1)
				default:
					atomic.AddInt64(&res.OtherErrors, 1)
				}
			}
			latMu.Lock()
			all.merge(local)
			latMu.Unlock()
		}(c.Seed + int64(w) + 1)
	}
	wg.Wait()

	res.LatP50Ms = ms(all.percentile(50))
	res.LatP99Ms = ms(all.percentile(99))
	res.LatP999Ms = ms(all.percentile(99.9))
	res.LatMeanMs = ms(all.mean())
	res.LatMaxMs = ms(all.percentile(100))
}
