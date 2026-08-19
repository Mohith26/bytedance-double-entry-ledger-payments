-- LedgerCore schema: immutable double-entry journal + derived/cached balances.
--
-- Invariants enforced by the application layer within row-locked transactions,
-- and independently re-verified by the reconciliation job:
--   (1) every transaction's postings sum to zero PER CURRENCY (balanced),
--   (2) an account's cached `balance` equals the sum of its postings,
--   (3) system-wide, postings sum to zero per currency (conservation).
--
-- Money is ALWAYS an integer count of minor units (BIGINT). No floats anywhere.

CREATE TABLE IF NOT EXISTS accounts (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT        NOT NULL UNIQUE,
    currency      CHAR(3)     NOT NULL,
    kind          TEXT        NOT NULL,           -- customer | merchant | external | fx | payout
    allow_negative BOOLEAN    NOT NULL DEFAULT FALSE,
    -- Cached balance in minor units, maintained inside the same transaction as
    -- the postings, under SELECT ... FOR UPDATE. Reconciliation proves this
    -- equals SUM(postings.amount) for the account (zero drift).
    balance       BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A transaction is an immutable journal entry header. Its legs live in postings.
CREATE TABLE IF NOT EXISTS transactions (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind            TEXT        NOT NULL,          -- transfer|payment|refund|fx|settlement
    -- For refunds: the payment transaction being (partially) reversed.
    ref_txn_id      BIGINT      REFERENCES transactions(id),
    -- Settlement lifecycle for payments: TRUE until a settlement batch pays out.
    pending_settle  BOOLEAN     NOT NULL DEFAULT FALSE,
    settled         BOOLEAN     NOT NULL DEFAULT FALSE,
    settle_batch_id BIGINT,
    memo            TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Postings are the immutable legs. A leg's currency must equal its account's
-- currency (enforced in the app layer). Positive credits the account, negative
-- debits it; the ledger's convention is simply "signed amounts sum to zero".
CREATE TABLE IF NOT EXISTS postings (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    txn_id      BIGINT      NOT NULL REFERENCES transactions(id),
    account_id  BIGINT      NOT NULL REFERENCES accounts(id),
    currency    CHAR(3)     NOT NULL,
    amount      BIGINT      NOT NULL,             -- signed minor units
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS postings_account_idx ON postings(account_id);
CREATE INDEX IF NOT EXISTS postings_txn_idx     ON postings(txn_id);

-- Exactly-once store. The unique key blocks duplicate submissions; a replayed
-- key returns the stored response and posts nothing new.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key           TEXT        PRIMARY KEY,
    request_hash  TEXT        NOT NULL,
    txn_id        BIGINT      REFERENCES transactions(id),
    response      JSONB,
    status        TEXT        NOT NULL DEFAULT 'in_progress', -- in_progress|completed
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Settlement batches: a run that nets pending payments per merchant/currency
-- and pays out the net to the merchant's payout account.
CREATE TABLE IF NOT EXISTS settlement_batches (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    item_count  INT         NOT NULL DEFAULT 0,
    net_total   BIGINT      NOT NULL DEFAULT 0     -- sum across merchants (minor units)
);
