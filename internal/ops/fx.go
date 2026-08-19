package ops

import (
	"context"
	"fmt"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
)

// FXResult reports an FX conversion outcome including the converted amount.
type FXResult struct {
	TxnID      int64       `json:"txn_id"`
	Replayed   bool        `json:"replayed"`
	FromAmount money.Minor `json:"from_amount"`
	ToAmount   money.Minor `json:"to_amount"`
}

// FXAccountName is the naming convention for a currency's FX position account.
func FXAccountName(c money.Currency) string { return "fx:" + string(c) }

// ConvertFX moves value across currencies WITHOUT creating or destroying money
// in any single currency. It posts four balanced legs:
//
//	from (curA):  -fromAmount     (debit the sender)
//	fx:curA:      +fromAmount     (FX desk takes the source currency)
//	fx:curB:      -toAmount       (FX desk releases the target currency)
//	to   (curB):  +toAmount       (credit the receiver)
//
// Each currency nets to exactly zero, so conservation holds per currency. The
// FX desk's multi-currency position (its two fx:* accounts) absorbs the trade;
// no implicit money is minted. toAmount is computed with exact integer math.
func (s *Service) ConvertFX(ctx context.Context, key string, from, to int64, fromAmount money.Minor, rate money.Rate) (FXResult, error) {
	if fromAmount <= 0 {
		return FXResult{}, ErrNonPositive
	}
	fromAcc, err := s.st.GetAccount(ctx, from)
	if err != nil {
		return FXResult{}, err
	}
	toAcc, err := s.st.GetAccount(ctx, to)
	if err != nil {
		return FXResult{}, err
	}
	if fromAcc.Currency == toAcc.Currency {
		return FXResult{}, fmt.Errorf("ops: ConvertFX requires different currencies (both %s)", fromAcc.Currency)
	}
	toAmount, err := rate.Convert(fromAmount)
	if err != nil {
		return FXResult{}, err
	}
	if toAmount <= 0 {
		return FXResult{}, fmt.Errorf("ops: converted amount must be positive, got %d", toAmount)
	}
	fxFrom, err := s.st.GetAccountByName(ctx, FXAccountName(fromAcc.Currency))
	if err != nil {
		return FXResult{}, fmt.Errorf("fx account for %s: %w", fromAcc.Currency, err)
	}
	fxTo, err := s.st.GetAccountByName(ctx, FXAccountName(toAcc.Currency))
	if err != nil {
		return FXResult{}, fmt.Errorf("fx account for %s: %w", toAcc.Currency, err)
	}
	e := ledger.Entry{
		Kind: ledger.TxnFX,
		Memo: fmt.Sprintf("fx %s->%s @ %d/%d", fromAcc.Currency, toAcc.Currency, rate.Num, rate.Den),
		Legs: []ledger.Leg{
			{AccountID: from, Currency: fromAcc.Currency, Amount: -fromAmount},
			{AccountID: fxFrom.ID, Currency: fromAcc.Currency, Amount: fromAmount},
			{AccountID: fxTo.ID, Currency: toAcc.Currency, Amount: -toAmount},
			{AccountID: to, Currency: toAcc.Currency, Amount: toAmount},
		},
	}
	req := fxReq{"fx", from, to, int64(fromAmount), rate.Num, rate.Den}
	r, err := s.postIdem(ctx, key, e, req)
	if err != nil {
		return FXResult{}, err
	}
	return FXResult{TxnID: r.TxnID, Replayed: r.Replayed, FromAmount: fromAmount, ToAmount: toAmount}, nil
}

type fxReq struct {
	Kind           string
	From, To       int64
	FromAmount     int64
	RateNum, Rate2 int64
}
