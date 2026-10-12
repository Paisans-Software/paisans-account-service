// Package storetest gives each test its own migrated schema in the database
// named by INVITE_TEST_DATABASE_URL, and skips when it is unset.
package storetest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/paisans-software/paisans-invite/internal/store"
)

func New(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("INVITE_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("INVITE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("t_%d", rand.Uint64())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	urls.Store(s, u.String())
	t.Cleanup(func() { urls.Delete(s); s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// urls maps each test store to the URL of its own schema.
var urls sync.Map

// Conn opens a separate connection to s's schema, for a test that needs to
// act on the database behind the store's back: to age a row, or to hold a lock.
func Conn(t *testing.T, s *store.Store) *pgx.Conn {
	t.Helper()
	u, ok := urls.Load(s)
	if !ok {
		t.Fatal("storetest.Conn: not a storetest store")
	}
	c, err := pgx.Connect(context.Background(), u.(string))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}
