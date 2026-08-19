package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/settle"
)

// idemKey extracts the Idempotency-Key header (may be empty).
func idemKey(r *http.Request) string { return r.Header.Get("Idempotency-Key") }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	accounts, txns, postings, err := s.st.Counts(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{
		"status": "ok", "mode": string(s.st.Mode()),
		"accounts": accounts, "transactions": txns, "postings": postings,
	})
}

type createAccountReq struct {
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	Kind          string `json:"kind"`
	AllowNegative bool   `json:"allow_negative"`
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var req createAccountReq
	if err := decode(r, &req); err != nil {
		fail(w, err)
		return
	}
	if req.Name == "" || req.Currency == "" {
		fail(w, fmt.Errorf("%w: name and currency are required", errBadRequest))
		return
	}
	kind := ledger.AccountKind(req.Kind)
	if kind == "" {
		kind = ledger.KindCustomer
	}
	acc, err := s.st.CreateAccount(r.Context(), req.Name, money.Currency(req.Currency), kind, req.AllowNegative)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, http.StatusCreated, acc)
}

func (s *Server) handleBalance(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	acc, err := s.st.GetAccount(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	major, _ := money.FormatMajor(acc.Currency, acc.Balance)
	ok(w, http.StatusOK, map[string]any{
		"account_id": acc.ID, "currency": acc.Currency,
		"balance_minor": acc.Balance, "balance": major,
	})
}

func (s *Server) handleLedger(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, err)
		return
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	lines, balance, err := s.st.AccountLedger(r.Context(), id, limit)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"account_id": id, "balance_minor": balance, "postings": lines})
}

type transferReq struct {
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Amount   int64  `json:"amount"` // minor units
	Currency string `json:"currency"`
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	var req transferReq
	if err := decode(r, &req); err != nil {
		fail(w, err)
		return
	}
	res, err := s.svc.Transfer(r.Context(), idemKey(r), req.From, req.To, money.Minor(req.Amount), money.Currency(req.Currency))
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, statusFor(res.Replayed), res)
}

type paymentReq struct {
	Source   int64  `json:"source"`
	Merchant int64  `json:"merchant"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func (s *Server) handlePayment(w http.ResponseWriter, r *http.Request) {
	var req paymentReq
	if err := decode(r, &req); err != nil {
		fail(w, err)
		return
	}
	res, err := s.svc.Payment(r.Context(), idemKey(r), req.Source, req.Merchant, money.Minor(req.Amount), money.Currency(req.Currency))
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, statusFor(res.Replayed), res)
}

type refundReq struct {
	PaymentTxnID int64 `json:"payment_txn_id"`
	Amount       int64 `json:"amount"`
}

func (s *Server) handleRefund(w http.ResponseWriter, r *http.Request) {
	var req refundReq
	if err := decode(r, &req); err != nil {
		fail(w, err)
		return
	}
	res, err := s.svc.Refund(r.Context(), idemKey(r), req.PaymentTxnID, money.Minor(req.Amount))
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, statusFor(res.Replayed), res)
}

type fxReq struct {
	From       int64 `json:"from"`
	To         int64 `json:"to"`
	FromAmount int64 `json:"from_amount"`
	RateNum    int64 `json:"rate_num"`
	RateDen    int64 `json:"rate_den"`
}

func (s *Server) handleFX(w http.ResponseWriter, r *http.Request) {
	var req fxReq
	if err := decode(r, &req); err != nil {
		fail(w, err)
		return
	}
	res, err := s.svc.ConvertFX(r.Context(), idemKey(r), req.From, req.To, money.Minor(req.FromAmount),
		money.Rate{Num: req.RateNum, Den: req.RateDen})
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, statusFor(res.Replayed), res)
}

func (s *Server) handleSettle(w http.ResponseWriter, r *http.Request) {
	res, err := settle.Settle(r.Context(), s.st)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, http.StatusOK, res)
}

func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	rep, err := settle.Reconcile(r.Context(), s.st)
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusOK
	if !rep.OK {
		status = http.StatusConflict // drift detected
	}
	writeJSON(w, status, Envelope{Success: rep.OK, Data: rep})
}

// pathID parses the {id} path value.
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid account id", errBadRequest)
	}
	return id, nil
}

// statusFor returns 200 for a replay and 201 for a freshly-created posting.
func statusFor(replayed bool) int {
	if replayed {
		return http.StatusOK
	}
	return http.StatusCreated
}
