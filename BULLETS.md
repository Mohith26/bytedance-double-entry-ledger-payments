# Résumé Bullets — LedgerCore (filled strictly from measured results)

> Measured 2026-08-19 on an **Apple M5 Pro (18 cores, 24 GB)** against a native
> **PostgreSQL 16.14** on localhost, with **synthetic seeded** data. Every number
> traces to `results/*.json` and [RESULTS.md](RESULTS.md). Machine-specific
> figures (throughput/latency) are tagged ⚙️. Unmeasured values would be the
> literal `___` — there are none in these three bullets.

## Filled bullets

- Built a **double-entry payments ledger in Go** (PostgreSQL, integer minor-units,
  **no floats for money**) guaranteeing **money conservation across 96,000
  concurrent transactions — 0 violations, debits == credits always** — with
  **0 double-spend / 0 lost updates** under up to **64 concurrent workers**
  (an adversarial 4-account drain withstood **31,158** insufficient-funds
  rejections with **0** negative balances), verified **`go test -race` clean**.
  <br>_(all MEASURED: 96,000 committed transfers; conservation sampled 17,213× mid-storm, 0 violations; contention run 64 workers/4 accounts, 31,158 rejections, 0 negatives; race detector 0 data races. Conservation checked via consistent MVCC-snapshot `SUM(balance)` sampling during the storm + independent journal recomputation.)_

- Made payments/refunds **exactly-once** via idempotency keys —
  **16,000 concurrent duplicate submissions across 500 keys → a single balanced
  posting each (0 duplicate postings)** — with refunds bounded to the captured
  amount (enforced under row locks) and **multi-currency FX** posted as balanced
  legs through an FX position account (**no implicit money creation**;
  USD net = 0, EUR net = 0 after conversion).
  <br>_(all MEASURED: 500/500 keys exactly-once, 16,000 submissions, 0 dup postings; FX conservation per-currency = 0; refund-bound + refund-idempotency unit-tested. Exactly-once via `INSERT … ON CONFLICT` claim inside the posting transaction.)_

- Sustained **9,089 transactions/sec at p99 23.8 ms** ⚙️ under 32 concurrent
  workers, with a **reconciliation job proving zero drift** (journal-recomputed
  balances == stored, system net == 0, 0 unbalanced txns) across settlement,
  verified by **30 passing tests** (`go test -race`).
  <br>_(MEASURED: throughput 9,088.9 tps, p50 1.77 / p99 23.84 / p99.9 68.39 ms; reconciliation drift = 0, net = 0; 30/30 tests pass. ⚙️ throughput/latency are hardware-dependent — Apple M5 Pro, native localhost Postgres, no network hop.)_

## Measured-value ledger

| Placeholder | Value | Status |
|---|---|---|
| concurrent transactions (conservation) | **96,000** (0 violations, 17,213 samples) | MEASURED |
| concurrent workers | **32** headline / **64** adversarial | MEASURED |
| double-spend / lost updates | **0 / 0** | MEASURED |
| insufficient-funds rejections withstood | **31,158** (0 negatives) | MEASURED |
| `go test -race` | **clean, 0 data races** | MEASURED |
| duplicate submissions → single posting | **16,000 / 500 keys → 1 each (0 dup)** | MEASURED |
| FX conservation per currency | **USD 0 / EUR 0** | MEASURED |
| throughput ⚙️ | **9,088.9 txns/sec** | MEASURED (machine-specific) |
| p50 / p99 / p99.9 ⚙️ | **1.77 / 23.84 / 68.39 ms** | MEASURED (machine-specific) |
| reconciliation drift / net | **0 / 0** | MEASURED |
| tests | **30 passed / 0 failed** | MEASURED |

## Honesty tags

- ✅ MEASURED against a live **native Postgres 16.14** (Docker wasn't running; the
  spec's native-Postgres fallback was used and documented). `docker compose up -d`
  is provided as the equivalent.
- ⚙️ **Throughput and p99 are hardware-dependent** (Apple M5 Pro, 18 cores, native
  localhost Postgres — no network hop). Correctness numbers reproduce anywhere;
  perf will vary.
- ⚠️ Conservation "after every op" is shown by high-frequency **consistent-snapshot
  sampling during** the concurrent storm (17k–39k samples) **plus** end-to-end
  reconciliation — not a per-statement synchronous global assertion.
- ⚠️ **Synthetic seeded** data; **2-decimal currencies** only (JPY-style excluded).
- ❌ Not a real payment system: no PSP/card-network/bank rails, no KYC/fraud, no UI.
