package store_test

import (
	"context"
	"testing"

	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

func TestMigrateTwice(t *testing.T) {
	s := storetest.New(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate must be a no-op: %v", err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
