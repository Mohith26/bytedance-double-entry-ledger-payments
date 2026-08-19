// Package api exposes the ledger operations over HTTP with a consistent
// response envelope, input validation, and Idempotency-Key support.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ops"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
)

// Server holds the dependencies for the HTTP handlers.
type Server struct {
	svc *ops.Service
	st  *store.Store
}

// New builds a Server.
func New(svc *ops.Service) *Server {
	return &Server{svc: svc, st: svc.Store()}
}

// Handler returns the configured http.Handler (Go 1.22 method+path routing).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /accounts", s.handleCreateAccount)
	mux.HandleFunc("GET /accounts/{id}/balance", s.handleBalance)
	mux.HandleFunc("GET /accounts/{id}/ledger", s.handleLedger)
	mux.HandleFunc("POST /transactions/transfer", s.handleTransfer)
	mux.HandleFunc("POST /transactions/payment", s.handlePayment)
	mux.HandleFunc("POST /transactions/refund", s.handleRefund)
	mux.HandleFunc("POST /transactions/fx", s.handleFX)
	mux.HandleFunc("POST /settle", s.handleSettle)
	mux.HandleFunc("GET /reconcile", s.handleReconcile)
	return mux
}

// Envelope is the consistent response wrapper for every endpoint.
type Envelope struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

func ok(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, Envelope{Success: true, Data: data})
}

// fail maps a domain error to an HTTP status and a safe message.
func fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrAccountNotFound), errors.Is(err, store.ErrNotAPayment):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrInsufficientFunds),
		errors.Is(err, ops.ErrRefundExceedsCaptured),
		errors.Is(err, ops.ErrNonPositive),
		errors.Is(err, store.ErrCurrencyMismatch),
		errors.Is(err, money.ErrUnknownCurrency),
		errors.Is(err, money.ErrBadRate),
		errors.Is(err, errBadRequest):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrIdempotencyConflict):
		status = http.StatusConflict
	}
	writeJSON(w, status, Envelope{Success: false, Error: err.Error()})
}

var errBadRequest = errors.New("bad request")

// decode parses a JSON body into v, rejecting unknown fields.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.Join(errBadRequest, err)
	}
	return nil
}
