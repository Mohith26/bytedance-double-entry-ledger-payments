package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/api"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ops"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/testhelp"
)

func newServer(t *testing.T) (*httptest.Server, *ops.Service) {
	t.Helper()
	st := testhelp.NewStore(t)
	svc := ops.New(st)
	if _, err := svc.EnsureInfra(context.Background(), []money.Currency{"USD", "EUR"}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(api.New(svc).Handler())
	t.Cleanup(ts.Close)
	return ts, svc
}

// post issues a JSON POST with an optional Idempotency-Key and returns status +
// decoded envelope.
func post(t *testing.T, url, key string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	json.NewDecoder(resp.Body).Decode(&env)
	return resp.StatusCode, env
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env map[string]any
	json.NewDecoder(resp.Body).Decode(&env)
	return resp.StatusCode, env
}

func TestAPIEndToEnd(t *testing.T) {
	ts, svc := newServer(t)

	// Create a merchant account.
	status, env := post(t, ts.URL+"/accounts", "", map[string]any{
		"name": "merchant-api", "currency": "USD", "kind": "merchant",
	})
	if status != http.StatusCreated || env["success"] != true {
		t.Fatalf("create account: status=%d env=%v", status, env)
	}
	merchID := int64(env["data"].(map[string]any)["id"].(float64))

	// Resolve the world account id via the ops service.
	infra, _ := svc.EnsureInfra(context.Background(), []money.Currency{"USD"})
	world := infra["USD"].World

	// Payment external -> merchant, minor units.
	status, env = post(t, ts.URL+"/transactions/payment", "pay-key-1", map[string]any{
		"source": world, "merchant": merchID, "amount": 15000, "currency": "USD",
	})
	if status != http.StatusCreated {
		t.Fatalf("payment: status=%d env=%v", status, env)
	}

	// Replay same key -> 200 replayed.
	status, env = post(t, ts.URL+"/transactions/payment", "pay-key-1", map[string]any{
		"source": world, "merchant": merchID, "amount": 15000, "currency": "USD",
	})
	if status != http.StatusOK || env["data"].(map[string]any)["replayed"] != true {
		t.Fatalf("replay: status=%d env=%v", status, env)
	}

	// Balance is credited exactly once.
	status, env = getJSON(t, ts.URL+"/accounts/"+itoa(merchID)+"/balance")
	if status != http.StatusOK {
		t.Fatalf("balance: %d", status)
	}
	if got := env["data"].(map[string]any)["balance_minor"].(float64); got != 15000 {
		t.Fatalf("balance_minor = %v, want 15000", got)
	}
	if got := env["data"].(map[string]any)["balance"].(string); got != "150.00" {
		t.Fatalf("balance = %q, want 150.00", got)
	}

	// Reconcile must be clean.
	status, env = getJSON(t, ts.URL+"/reconcile")
	if status != http.StatusOK || env["success"] != true {
		t.Fatalf("reconcile: status=%d env=%v", status, env)
	}
}

func TestAPIValidationErrors(t *testing.T) {
	ts, _ := newServer(t)
	// Missing required fields.
	status, _ := post(t, ts.URL+"/accounts", "", map[string]any{"currency": "USD"})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing name, got %d", status)
	}
	// Unknown currency.
	status, _ = post(t, ts.URL+"/accounts", "", map[string]any{"name": "x", "currency": "ZZZ"})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown currency, got %d", status)
	}
	// Not found account balance.
	status, _ = getJSON(t, ts.URL+"/accounts/999999/balance")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
