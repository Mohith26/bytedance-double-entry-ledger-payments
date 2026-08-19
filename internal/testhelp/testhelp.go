// Package testhelp provides a shared Postgres-backed store for integration
// tests. It connects to the test database, applies the schema, and truncates
// all tables so each test starts from a clean, reproducible state.
package testhelp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Mohith26/bytedance-double-entry-ledger-payments/internal/store"
	"github.com/Mohith26/bytedance-double-entry-ledger-payments/migrations"
)

// TestURL returns TEST_DATABASE_URL or the local native-Postgres default.
func TestURL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://localhost:5432/ledgercore_test?sslmode=disable"
}

// NewStore opens a migrated, freshly-truncated store for a test. It skips the
// test (rather than failing) if Postgres is unreachable, so unit-only runs
// still work; CI/reproduce commands document that Postgres must be up.
func NewStore(t *testing.T) *store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := store.Open(ctx, TestURL())
	if err != nil {
		t.Skipf("skipping: cannot reach test Postgres at %s: %v", TestURL(), err)
	}
	if err := st.Migrate(ctx, migrations.SchemaSQL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}
