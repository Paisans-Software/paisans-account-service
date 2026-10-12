package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/store"
	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

var ctx = context.Background()

func hash(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

var nonces atomic.Int64

// fresh is a create-form nonce no other call has used.
func fresh() string { return fmt.Sprint("n", nonces.Add(1)) }

func newApp(u string) store.NewApplication {
	return store.NewApplication{Username: u, Email: u + "@example.org", DisplayName: u, CoCVersion: "v"}
}

func TestQuota(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		if _, err := s.CreateInvite(ctx, hash(fmt.Sprint(i)), "sub", "alice", fresh(), exp, 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateInvite(ctx, hash("x"), "sub", "alice", fresh(), exp, 2); !errors.Is(err, store.ErrQuota) {
		t.Fatalf("want ErrQuota, got %v", err)
	}
	if _, err := s.CreateInvite(ctx, hash("y"), "admin", "root", fresh(), exp, 0); err != nil {
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
			if _, err := s.CreateInvite(ctx, hash(fmt.Sprint("c", i)), "sub", "alice", fresh(), exp, 3); err == nil {
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
	if _, err := s.CreateInvite(ctx, hash("t"), "sub", "alice", fresh(), time.Now().Add(time.Hour), 3); err != nil {
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
	s.CreateInvite(ctx, hash("old"), "sub", "alice", fresh(), time.Now().Add(-time.Minute), 3)
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
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), exp, 3)
	s.CreateInvite(ctx, hash("b"), "sub", "alice", fresh(), exp, 3)
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
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), exp, 1)
	a, _ := s.Apply(ctx, hash("a"), time.Now(), newApp("bob"))
	if err := s.CompleteApplication(ctx, a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Application(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("completed application must be deleted")
	}
	// a used invite returns the slot
	if _, err := s.CreateInvite(ctx, hash("b"), "sub", "alice", fresh(), exp, 1); err != nil {
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
	if _, err := s.CreateInvite(ctx, hash("c"), "sub", "alice", fresh(), exp, 1); err != nil {
		t.Fatalf("declined invite must free the slot: %v", err)
	}
}

func TestRevokeOnlyOwnOpen(t *testing.T) {
	s := storetest.New(t)
	inv, _ := s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), time.Now().Add(time.Hour), 3)
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
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), t0.Add(time.Hour), 3)
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
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), time.Now().Add(time.Hour), 3)
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

func TestCreateNonceOnce(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	if _, err := s.CreateInvite(ctx, hash("a"), "sub", "alice", "n1", exp, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInvite(ctx, hash("b"), "sub", "alice", "n1", exp, 3); !errors.Is(err, store.ErrDuplicateCreate) {
		t.Fatalf("same nonce: %v", err)
	}
	if _, err := s.CreateInvite(ctx, hash("c"), "sub", "alice", "n2", exp, 3); err != nil {
		t.Fatalf("a different nonce makes a second invite: %v", err)
	}
	// Nonces are per member.
	if _, err := s.CreateInvite(ctx, hash("d"), "other", "bob", "n1", exp, 3); err != nil {
		t.Fatalf("another member's nonce: %v", err)
	}
	if inv, _ := s.ListInvites(ctx, "sub"); len(inv) != 2 {
		t.Fatalf("want two invites, got %d", len(inv))
	}
	if got, _ := s.AuditSubjects(ctx, "invite.create"); len(got) != 3 {
		t.Fatalf("a refused duplicate writes no audit row: %v", got)
	}
}

// A duplicate is reported as such even when the member is at quota.
func TestCreateNonceBeforeQuota(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	s.CreateInvite(ctx, hash("a"), "sub", "alice", "n1", exp, 1)
	if _, err := s.CreateInvite(ctx, hash("b"), "sub", "alice", "n1", exp, 1); !errors.Is(err, store.ErrDuplicateCreate) {
		t.Fatalf("want ErrDuplicateCreate, got %v", err)
	}
}

func TestCreateNonceConcurrent(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.CreateInvite(ctx, hash(fmt.Sprint("r", i)), "sub", "alice", "same", exp, 0)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, store.ErrDuplicateCreate) {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if inv, _ := s.ListInvites(ctx, "sub"); ok != 1 || len(inv) != 1 {
		t.Fatalf("want exactly one invite, got %d (%d rows)", ok, len(inv))
	}
}
