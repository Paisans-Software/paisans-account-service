package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/store"
	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

var ctx = context.Background()

func hash(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func newApp(u string) store.NewApplication {
	return store.NewApplication{Username: u, Email: u + "@example.org", DisplayName: u, CoCVersion: "v"}
}

func TestQuota(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		if _, err := s.CreateInvite(ctx, hash(fmt.Sprint(i)), "sub", "alice", exp, 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateInvite(ctx, hash("x"), "sub", "alice", exp, 2); !errors.Is(err, store.ErrQuota) {
		t.Fatalf("want ErrQuota, got %v", err)
	}
	if _, err := s.CreateInvite(ctx, hash("y"), "admin", "root", exp, 0); err != nil {
		t.Fatalf("quota 0 is unlimited: %v", err)
	}
}

func TestQuotaConcurrent(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.CreateInvite(ctx, hash(fmt.Sprint("c", i)), "sub", "alice", exp, 3); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("want exactly 3 invites, got %d", ok)
	}
}

func TestConsumeInviteOnce(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.CreateInvite(ctx, hash("t"), "sub", "alice", time.Now().Add(time.Hour), 3); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Apply(ctx, hash("t"), time.Now(), newApp(fmt.Sprint("u", i)))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, store.ErrNotUsable) {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("want exactly one application, got %d", ok)
	}
}

func TestExpiredInviteNotUsable(t *testing.T) {
	s := storetest.New(t)
	s.CreateInvite(ctx, hash("old"), "sub", "alice", time.Now().Add(-time.Minute), 3)
	if _, err := s.OpenInvite(ctx, hash("old"), time.Now()); !errors.Is(err, store.ErrNotUsable) {
		t.Fatalf("want ErrNotUsable, got %v", err)
	}
	if _, err := s.Apply(ctx, hash("old"), time.Now(), newApp("bob")); !errors.Is(err, store.ErrNotUsable) {
		t.Fatalf("want ErrNotUsable, got %v", err)
	}
}

func TestPendingUsernameCaseInsensitive(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	s.CreateInvite(ctx, hash("a"), "sub", "alice", exp, 3)
	s.CreateInvite(ctx, hash("b"), "sub", "alice", exp, 3)
	if _, err := s.Apply(ctx, hash("a"), time.Now(), newApp("Bob")); err != nil {
		t.Fatal(err)
	}
	if taken, _ := s.PendingUsernameTaken(ctx, "bob"); !taken {
		t.Fatal("bob must be taken by pending Bob")
	}
	if _, err := s.Apply(ctx, hash("b"), time.Now(), newApp("bob")); !errors.Is(err, store.ErrUsernameTaken) {
		t.Fatalf("want ErrUsernameTaken, got %v", err)
	}
	if inv, err := s.OpenInvite(ctx, hash("b"), time.Now()); err != nil || inv.State != "open" {
		t.Fatalf("a refused application must leave the invite open: %v %v", inv, err)
	}
}

func TestCompleteAndDecline(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	s.CreateInvite(ctx, hash("a"), "sub", "alice", exp, 1)
	a, _ := s.Apply(ctx, hash("a"), time.Now(), newApp("bob"))
	if err := s.CompleteApplication(ctx, a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Application(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("completed application must be deleted")
	}
	// a used invite returns the slot
	if _, err := s.CreateInvite(ctx, hash("b"), "sub", "alice", exp, 1); err != nil {
		t.Fatalf("used invite must free the slot: %v", err)
	}
	c, _ := s.Apply(ctx, hash("b"), time.Now(), newApp("carol"))
	h := hash("carol@example.org")
	if err := s.DeclineApplication(ctx, c.ID, "admin", h); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.DeclineHistory(ctx, h); d.Count != 1 {
		t.Fatalf("want one decline, got %+v", d)
	}
	if _, err := s.CreateInvite(ctx, hash("c"), "sub", "alice", exp, 1); err != nil {
		t.Fatalf("declined invite must free the slot: %v", err)
	}
}

func TestRevokeOnlyOwnOpen(t *testing.T) {
	s := storetest.New(t)
	inv, _ := s.CreateInvite(ctx, hash("a"), "sub", "alice", time.Now().Add(time.Hour), 3)
	if err := s.RevokeInvite(ctx, inv.ID, "other"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("must not revoke another member's invite")
	}
	if err := s.RevokeInvite(ctx, inv.ID, "sub"); err != nil {
		t.Fatal(err)
	}
}

func TestClaimApplication(t *testing.T) {
	s := storetest.New(t)
	t0 := time.Now()
	s.CreateInvite(ctx, hash("a"), "sub", "alice", t0.Add(time.Hour), 3)
	a, _ := s.Apply(ctx, hash("a"), t0, newApp("bob"))
	if err := s.MarkLinkUnsent(ctx, a.ID, "idp-1", "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unclaimed application cannot be marked link_unsent: %v", err)
	}
	got, err := s.ClaimApplication(ctx, a.ID, "admin", t0)
	if err != nil || got.State != "approving" || got.Username != "bob" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.ClaimApplication(ctx, a.ID, "other", t0.Add(5*time.Minute)); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a live claim must refuse another: %v", err)
	}
	if err := s.DeclineApplication(ctx, a.ID, "other", hash("bob@example.org")); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a claimed application cannot be declined: %v", err)
	}
	// A claim older than ten minutes is a crashed approval and can be taken over.
	if _, err := s.ClaimApplication(ctx, a.ID, "other", t0.Add(11*time.Minute)); err != nil {
		t.Fatalf("a stale claim must be claimable: %v", err)
	}
	if err := s.ReleaseClaim(ctx, a.ID, "other"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseClaim(ctx, a.ID, "other"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("only a claimed application can be released: %v", err)
	}
	if err := s.DeclineApplication(ctx, a.ID, "other", hash("bob@example.org")); err != nil {
		t.Fatalf("a released application can be declined: %v", err)
	}
	if _, err := s.ClaimApplication(ctx, a.ID, "other", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a missing application: %v", err)
	}
}

func TestClaimConcurrent(t *testing.T) {
	s := storetest.New(t)
	s.CreateInvite(ctx, hash("a"), "sub", "alice", time.Now().Add(time.Hour), 3)
	a, _ := s.Apply(ctx, hash("a"), time.Now(), newApp("bob"))
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.ClaimApplication(ctx, a.ID, "admin", time.Now())
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, store.ErrBusy) {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("want exactly one claim, got %d", ok)
	}
}
