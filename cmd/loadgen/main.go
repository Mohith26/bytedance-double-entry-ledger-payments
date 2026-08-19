// Command loadgen runs the concurrent load + invariant-verification harness and
// writes measured metrics to results/*.json.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/loadgen"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/migrations"
)

func main() {
	cfg := loadgen.Default()
	flag.IntVar(&cfg.Workers, "workers", cfg.Workers, "concurrent worker goroutines")
	flag.IntVar(&cfg.OpsPerWorker, "ops", cfg.OpsPerWorker, "transfers per worker")
	flag.IntVar(&cfg.Accounts, "accounts", cfg.Accounts, "shared accounts contended by workers")
	flag.IntVar(&cfg.IdemTrials, "idem-trials", cfg.IdemTrials, "distinct idempotency keys to stress")
	flag.IntVar(&cfg.IdemConc, "idem-conc", cfg.IdemConc, "concurrent duplicate submissions per key")
	initial := flag.Int64("initial", int64(cfg.InitialEach), "initial minor-unit balance per shared account")
	seed := flag.Int64("seed", cfg.Seed, "PRNG seed (reproducible)")
	mode := flag.String("mode", "rowlock", "isolation mode: rowlock | serializable")
	outDir := flag.String("out", "results", "output directory for results JSON")
	flag.Parse()
	cfg.Seed = *seed
	cfg.InitialEach = money.Minor(*initial)

	ctx := context.Background()

	// Size the pool to the concurrency so workers don't queue on connections.
	url := withPoolSize(store.DefaultURL(), cfg.Workers+4)
	st, err := store.Open(ctx, url)
	if err != nil {
		log.Fatalf("open store (%s): %v", url, err)
	}
	defer st.Close()
	if *mode == "serializable" {
		st = st.WithMode(store.ModeSerializable)
	}
	if err := st.Migrate(ctx, migrations.SchemaSQL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	log.Printf("loadgen: workers=%d ops/worker=%d accounts=%d seed=%d mode=%s",
		cfg.Workers, cfg.OpsPerWorker, cfg.Accounts, cfg.Seed, st.Mode())

	res, err := cfg.Run(ctx, st)
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	printSummary(res)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	writeJSON(filepath.Join(*outDir, "load.json"), res)
	writeJSON(filepath.Join(*outDir, "summary.json"), summarize(res))
	log.Printf("wrote %s/load.json and %s/summary.json", *outDir, *outDir)
}

// withPoolSize appends pool_max_conns to a postgres URL if not already present.
func withPoolSize(url string, n int) string {
	sep := "?"
	for _, r := range url {
		if r == '?' {
			sep = "&"
			break
		}
	}
	return fmt.Sprintf("%s%spool_max_conns=%d", url, sep, n)
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		log.Fatalf("write %s: %v", path, err)
	}
}

// machine captures reproducibility context.
type machine struct {
	GoVersion  string `json:"go_version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	NumCPU     int    `json:"num_cpu"`
	MeasuredAt string `json:"measured_at"`
}

func summarize(r loadgen.Result) map[string]any {
	return map[string]any{
		"machine": machine{
			GoVersion:  runtime.Version(),
			GOOS:       runtime.GOOS,
			GOARCH:     runtime.GOARCH,
			NumCPU:     runtime.NumCPU(),
			MeasuredAt: time.Now().UTC().Format(time.RFC3339),
		},
		"workload": map[string]any{
			"workers": r.Config.Workers, "ops_per_worker": r.Config.OpsPerWorker,
			"accounts": r.Accounts, "seed": r.Config.Seed, "isolation_mode": r.Mode,
			"committed_transfers": r.CommittedTransfers,
			"insufficient_funds":  r.InsufficientFunds,
			"transactions_total":  r.TransactionsTotal,
			"postings_total":      r.PostingsTotal,
		},
		"conservation": map[string]any{
			"samples_during_storm":    r.ConservationSamples,
			"violations":              r.ConservationViolations,
			"negative_balance_events": r.NegativeBalanceSamples,
			"final_net":               r.ConservationNetUSD,
		},
		"concurrency": map[string]any{
			"double_spends": r.DoubleSpends,
			"lost_updates":  r.LostUpdates,
		},
		"idempotency": map[string]any{
			"keys_tested":        r.IdemKeysTested,
			"submissions_total":  r.IdemSubmissionsTotal,
			"exactly_once":       r.IdemExactlyOnce,
			"duplicate_postings": r.IdemDuplicatePosts,
		},
		"reconciliation": map[string]any{
			"ok": r.ReconcileOK, "drift_count": r.ReconcileDrift,
			"unbalanced_txns": r.ReconcileUnbalancedTxns,
		},
		"performance": map[string]any{
			"elapsed_seconds":         r.ElapsedSeconds,
			"throughput_txns_per_sec": r.ThroughputTps,
			"latency_p50_ms":          r.LatP50Ms,
			"latency_p99_ms":          r.LatP99Ms,
			"latency_p99_9_ms":        r.LatP999Ms,
			"latency_mean_ms":         r.LatMeanMs,
			"latency_max_ms":          r.LatMaxMs,
		},
	}
}

func printSummary(r loadgen.Result) {
	fmt.Println("──────────────────────────────────────────────")
	fmt.Printf("committed transfers   : %d\n", r.CommittedTransfers)
	fmt.Printf("insufficient rejected : %d\n", r.InsufficientFunds)
	fmt.Printf("other errors          : %d\n", r.OtherErrors)
	fmt.Printf("conservation samples  : %d (violations=%d)\n", r.ConservationSamples, r.ConservationViolations)
	fmt.Printf("negative-balance evts : %d\n", r.NegativeBalanceSamples)
	fmt.Printf("double-spend / lost   : %d / %d\n", r.DoubleSpends, r.LostUpdates)
	fmt.Printf("idempotency exact-once: %d/%d keys (dup postings=%d, submissions=%d)\n",
		r.IdemExactlyOnce, r.IdemKeysTested, r.IdemDuplicatePosts, r.IdemSubmissionsTotal)
	fmt.Printf("reconcile ok / drift  : %v / %d (net=%d, unbalanced=%d)\n",
		r.ReconcileOK, r.ReconcileDrift, r.ConservationNetUSD, r.ReconcileUnbalancedTxns)
	fmt.Printf("throughput            : %.1f txns/sec\n", r.ThroughputTps)
	fmt.Printf("latency p50/p99/p99.9 : %.3f / %.3f / %.3f ms (mean %.3f, max %.3f)\n",
		r.LatP50Ms, r.LatP99Ms, r.LatP999Ms, r.LatMeanMs, r.LatMaxMs)
	fmt.Println("──────────────────────────────────────────────")
}
