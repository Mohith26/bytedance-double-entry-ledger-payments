# LedgerCore — Double-Entry Ledger + Idempotent Payments/Settlement (Go + Postgres)

The beating heart of a payments backend: an **immutable double-entry ledger**
where **money is never created or destroyed** — every transaction posts balanced
legs (**debits == credits, always**), money is **integer minor units (no floats)**,
and balances are **derived from the journal**. On top of it: **idempotent**
transfers / payments / refunds (exactly-once under retries), **concurrency-safe**
balance updates (no double-spend, no lost updates, no disallowed negatives),
**multi-currency FX** posted as balanced legs, and a **settlement + reconciliation**
job that proves the books balance with **zero drift**.

Built for the **ByteDance — SWE Intern, Global Payment (backend)** problem space
(payments, refunds, settlement, high-volume financial transactions) in **Go** with
Postgres row-locking and a **`go test -race`-clean** concurrency proof.

> Every number in [RESULTS.md](RESULTS.md) / [BULLETS.md](BULLETS.md) comes from a
> real run on this machine. Nothing is invented. Machine-specific figures
> (throughput / latency) are tagged as such.

---

## The invariants (what "correct" means here)

| Invariant | Enforcement | Proven by |
|---|---|---|
| **Balanced** — every txn's legs sum to 0 per currency | pure `Entry.Validate()` before any write | unit tests + reconciliation |
| **Integer money** — no floats, ever | `money.Minor int64`; FX via exact `big.Int` rational math | `internal/money` tests |
| **Derived == stored** — cached balance == Σ postings | updated in the same row-locked txn as the postings | reconciliation zero-drift |
| **No double-spend / disallowed-negative** | `SELECT … FOR UPDATE` + post-lock constraint check | 64-worker drain test (31k rejections, 0 negatives) |
| **No lost updates** | deterministic lock ordering, atomic commit | conservation sampled 17k× during the storm, 0 violations |
| **Exactly-once** — replayed key ⇒ one posting | `INSERT … ON CONFLICT` claim inside the posting txn | 16,000 concurrent duplicate submissions, 0 dup postings |
| **Conservation** — Σ balances == 0 per currency | balanced legs funded from a world/external account | reconciliation `net == 0` |

## Architecture

```
internal/money/     integer minor-units Money + exact-rational FX rate (no floats)
internal/ledger/    domain types + the pure balancing invariant (Entry.Validate)
internal/store/     Postgres via pgx: row-locked posting, idempotency, settlement,
                    reconciliation queries  (the single write path is postEntryTx)
internal/ops/       transfer / payment / refund / FX  -> balanced journal entries
internal/settle/    settlement batch trigger + the reconciliation (zero-drift) job
internal/api/       net/http service (Go 1.22 method+path routing), JSON envelope
internal/loadgen/   concurrent load + invariant-verification harness (metrics)
cmd/ledgerd/        the HTTP server
cmd/loadgen/        the load harness -> results/*.json
migrations/         embedded SQL schema (accounts, transactions, postings, …)
```

### Concurrency model (documented)

Every mutation runs in one Postgres transaction that:

1. validates the balancing invariant in memory (reject unbalanced up front),
2. `SELECT … FOR UPDATE`s **all involved accounts in ascending id order**
   (deterministic ordering ⇒ no deadlocks between opposite-direction transfers),
3. runs any operation guard (e.g. refund ≤ captured) **while holding the locks**,
4. checks the no-negative constraint on the *new* balances,
5. inserts the immutable txn + legs and updates the cached balances,
6. commits atomically.

Default isolation is **READ COMMITTED + row locks**. A **SERIALIZABLE** mode is
also available (`-mode serializable`) with automatic retry on `40001`/`40P01`; a
head-to-head comparison is in [RESULTS.md](RESULTS.md).

### FX model (no implicit money creation)

A cross-currency move posts **four balanced legs**, two per currency, through a
per-currency FX position account:

```
from (USD): -10000     fx:USD: +10000      # USD nets to 0
fx:EUR:      -9200     to (EUR): +9200     # EUR nets to 0
```

`toAmount = round(fromAmount × rate.Num / rate.Den)` is computed with exact
`big.Int` arithmetic. Rounding only ever shifts a residual into the FX desk's
multi-currency position — it can **never** create or destroy money within a
currency. Conservation therefore holds **per currency**.

### Idempotency (exactly-once)

The mutating request carries an `Idempotency-Key`. Inside the posting
transaction we `INSERT … ON CONFLICT (key) DO NOTHING RETURNING`. Postgres makes
a concurrent duplicate **block** until the first owner commits or aborts, so a
winner posts exactly once and every retry returns the stored response — no
duplicate journal entry, no double decrement. An aborted attempt leaves no key
row, so a genuine retry can re-claim it.

---

## Quick start

Requires **Go 1.22+** and **Postgres**. The committed results used a native
Homebrew Postgres 16.14; `docker compose up -d` is the portable equivalent.

```bash
# Option A — native Postgres already running on :5432
createdb ledgercore && createdb ledgercore_test
export DATABASE_URL='postgres://localhost:5432/ledgercore?sslmode=disable'
export TEST_DATABASE_URL='postgres://localhost:5432/ledgercore_test?sslmode=disable'

# Option B — Docker
docker compose up -d
export DATABASE_URL='postgres://ledger:ledger@localhost:5432/ledgercore?sslmode=disable'
export TEST_DATABASE_URL='postgres://ledger:ledger@localhost:5432/ledgercore_test?sslmode=disable'

# Tests (with the race detector; -p 1 serializes packages sharing the test DB)
go test -race -p 1 -count=1 ./...

# Load + invariant harness -> results/load.json, results/summary.json
go run ./cmd/loadgen -workers 32 -ops 3000 -accounts 32 -seed 42

# HTTP service
go run ./cmd/ledgerd     # listens on :8080
```

### HTTP API

Consistent envelope `{ "success": bool, "data": …, "error": … }`. Amounts are
integer **minor units** (e.g. `15000` == `$150.00`) — the API never accepts a
float for money.

| Method & path | Body / notes |
|---|---|
| `GET /health` | counts + isolation mode |
| `POST /accounts` | `{name,currency,kind,allow_negative}` |
| `GET /accounts/{id}/balance` | minor units + formatted major |
| `GET /accounts/{id}/ledger?limit=` | postings, newest first |
| `POST /transactions/transfer` | `{from,to,amount,currency}` + `Idempotency-Key` |
| `POST /transactions/payment` | `{source,merchant,amount,currency}` + key |
| `POST /transactions/refund` | `{payment_txn_id,amount}` + key (≤ captured) |
| `POST /transactions/fx` | `{from,to,from_amount,rate_num,rate_den}` + key |
| `POST /settle` | run a settlement batch (net + payout) |
| `GET /reconcile` | zero-drift report (`200` OK / `409` if drift) |

```bash
curl -s -XPOST localhost:8080/transactions/transfer \
  -H 'Idempotency-Key: t-42' -H 'content-type: application/json' \
  -d '{"from":1,"to":2,"amount":2500,"currency":"USD"}'
curl -s localhost:8080/reconcile | jq .data.ok      # -> true, zero drift
```

---

## Scope

**Built (Must-have v1):** double-entry ledger, integer money, derived balances,
transfer/payment/refund + FX, idempotency keys, row-locked concurrency safety,
settlement batch + reconciliation, HTTP API, load harness + measured metrics.
**Should-have included:** SERIALIZABLE-vs-row-lock comparison.
**Out of scope (not built):** real card networks / PSPs / bank rails, KYC/fraud,
a UI, multi-region sharding, hold/capture authorization, hash-chained audit log,
outbox events.

See [RESULTS.md](RESULTS.md) for measured numbers and exact reproduce commands.
