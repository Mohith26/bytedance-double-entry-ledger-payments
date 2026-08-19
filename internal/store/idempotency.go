package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/jackc/pgx/v5"
)

// ErrIdempotencyConflict means the same Idempotency-Key was reused with a
// different request payload.
var ErrIdempotencyConflict = errors.New("store: idempotency key reused with a different request")

// IdemResult carries the posting result plus whether it was a replay.
type IdemResult struct {
	TxnID    int64           `json:"txn_id"`
	Replayed bool            `json:"replayed"`
	Response json.RawMessage `json:"response"`
}

// HashRequest returns a stable hash of an arbitrary request payload, used to
// detect key reuse with a different body.
func HashRequest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PostEntryIdempotent posts a balanced entry exactly once for the given key.
//
// Exactly-once mechanism (relies on Postgres' documented ON CONFLICT waiting
// behaviour): the whole operation runs in ONE transaction. We attempt to claim
// the key with `INSERT ... ON CONFLICT DO NOTHING RETURNING`. A concurrent
// duplicate INSERT of the same key BLOCKS until the first owner commits or
// aborts, so:
//   - if we get a row back, we OWN the key: we post the entry and store the
//     response, then commit (status becomes 'completed').
//   - if we get no row, another request already committed this key: we read and
//     return its stored response (a replay) and post nothing new.
//
// Because posting and key-claim share one transaction, an aborted attempt
// leaves NO key row behind, so a retry can re-claim it. No duplicate journal
// entry and no double decrement is ever possible.
func (s *Store) PostEntryIdempotent(ctx context.Context, key, requestHash string, e ledger.Entry, response any) (IdemResult, error) {
	return s.PostEntryIdempotentGuarded(ctx, key, requestHash, e, response, nil)
}

// PostEntryIdempotentGuarded is PostEntryIdempotent with an extra post-lock
// guard (see store.Guard), used by refund to bound the reversal amount.
func (s *Store) PostEntryIdempotentGuarded(ctx context.Context, key, requestHash string, e ledger.Entry, response any, guard Guard) (IdemResult, error) {
	if key == "" {
		return IdemResult{}, errors.New("store: idempotency key required")
	}
	respJSON, err := json.Marshal(response)
	if err != nil {
		return IdemResult{}, fmt.Errorf("marshal response: %w", err)
	}
	var result IdemResult
	err = s.runInTx(ctx, func(tx pgx.Tx) error {
		claimed, err := claimKey(ctx, tx, key, requestHash)
		if err != nil {
			return err
		}
		if claimed {
			r, err := postEntryTx(ctx, tx, e, guard)
			if err != nil {
				return err
			}
			// Bind the stored response to the real txn id.
			finalResp := bindTxnID(respJSON, r.TxnID)
			if _, err := tx.Exec(ctx,
				`UPDATE idempotency_keys SET txn_id=$1, response=$2, status='completed' WHERE key=$3`,
				r.TxnID, finalResp, key); err != nil {
				return fmt.Errorf("finalize key: %w", err)
			}
			result = IdemResult{TxnID: r.TxnID, Replayed: false, Response: finalResp}
			return nil
		}
		// Replay path: the key already exists and committed.
		return readReplay(ctx, tx, key, requestHash, &result)
	})
	return result, err
}

// claimKey tries to insert the key row; returns true if we own it.
func claimKey(ctx context.Context, tx pgx.Tx, key, requestHash string) (bool, error) {
	var claimedKey string
	err := tx.QueryRow(ctx,
		`INSERT INTO idempotency_keys (key, request_hash, status)
		 VALUES ($1,$2,'in_progress')
		 ON CONFLICT (key) DO NOTHING
		 RETURNING key`, key, requestHash).Scan(&claimedKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // conflict: someone else owns it
	}
	if err != nil {
		return false, fmt.Errorf("claim key: %w", err)
	}
	return true, nil
}

// readReplay loads the stored result for an already-committed key.
func readReplay(ctx context.Context, tx pgx.Tx, key, requestHash string, out *IdemResult) error {
	var (
		storedHash string
		txnID      *int64
		resp       []byte
		status     string
	)
	err := tx.QueryRow(ctx,
		`SELECT request_hash, txn_id, response, status FROM idempotency_keys WHERE key=$1`, key).
		Scan(&storedHash, &txnID, &resp, &status)
	if err != nil {
		return fmt.Errorf("read replay: %w", err)
	}
	if storedHash != requestHash {
		return ErrIdempotencyConflict
	}
	if status != "completed" || txnID == nil {
		// Should not happen: keys only persist committed as 'completed'.
		return fmt.Errorf("store: idempotency key %q in unexpected state %q", key, status)
	}
	out.TxnID = *txnID
	out.Replayed = true
	out.Response = resp
	return nil
}

// bindTxnID injects the real txn id into the stored response object so replays
// return an identical body. It expects response to be a JSON object.
func bindTxnID(respJSON []byte, txnID int64) []byte {
	var m map[string]any
	if err := json.Unmarshal(respJSON, &m); err != nil || m == nil {
		return respJSON
	}
	m["txn_id"] = txnID
	out, err := json.Marshal(m)
	if err != nil {
		return respJSON
	}
	return out
}
