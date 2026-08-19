// Command ledgerd runs the LedgerCore HTTP service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/api"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/money"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/ops"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/migrations"
)

func main() {
	ctx := context.Background()
	url := store.DefaultURL()
	st, err := store.Open(ctx, url)
	if err != nil {
		log.Fatalf("open store (%s): %v", url, err)
	}
	defer st.Close()

	if os.Getenv("LEDGER_ISOLATION") == "serializable" {
		st = st.WithMode(store.ModeSerializable)
	}

	if err := st.Migrate(ctx, migrations.SchemaSQL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	svc := ops.New(st)
	if _, err := svc.EnsureInfra(ctx, []money.Currency{"USD", "EUR", "GBP", "CAD", "AUD"}); err != nil {
		log.Fatalf("ensure infra: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           api.New(svc).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("ledgerd listening on :%s (db=%s mode=%s)", port, url, st.Mode())
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
