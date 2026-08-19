package ops

import (
	"context"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ledger"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
)

// Infra holds the standard infrastructure account ids for a currency.
type Infra struct {
	// World is the external "outside world" account funding payments (may go
	// negative — it represents everything outside the ledger).
	World int64
	// FX is the currency's FX position account.
	FX int64
	// Payout is the currency's merchant payout rail.
	Payout int64
}

// EnsureInfra provisions the World, FX position, and Payout accounts for each
// currency. It is idempotent and safe to call on every startup.
func (s *Service) EnsureInfra(ctx context.Context, currencies []money.Currency) (map[money.Currency]Infra, error) {
	out := make(map[money.Currency]Infra, len(currencies))
	for _, c := range currencies {
		world, err := s.st.EnsureAccount(ctx, "world:"+string(c), c, ledger.KindExternal, true)
		if err != nil {
			return nil, err
		}
		fx, err := s.st.EnsureAccount(ctx, FXAccountName(c), c, ledger.KindFX, true)
		if err != nil {
			return nil, err
		}
		payout, err := s.st.EnsureAccount(ctx, "payout:"+string(c), c, ledger.KindPayout, true)
		if err != nil {
			return nil, err
		}
		out[c] = Infra{World: world.ID, FX: fx.ID, Payout: payout.ID}
	}
	return out, nil
}
