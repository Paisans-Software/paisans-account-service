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
	if err := s.CompleteApplication(ctx, a.ID, "", "admin"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a pending application cannot be completed: %v", err)
	}
	_, claim, _ := s.ClaimApplication(ctx, a.ID, "admin", time.Now())
	if err := s.CompleteApplication(ctx, a.ID, claim, "admin"); err != nil {
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
	if err := s.MarkLinkUnsent(ctx, a.ID, "", "idp-1", "admin"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("an unclaimed application cannot be marked link_unsent: %v", err)
	}
	got, first, err := s.ClaimApplication(ctx, a.ID, "admin", t0)
	if err != nil || got.State != "approving" || got.Username != "bob" || first == "" {
		t.Fatalf("%+v %q %v", got, first, err)
	}
	if _, _, err := s.ClaimApplication(ctx, a.ID, "other", t0.Add(5*time.Minute)); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a live claim must refuse another: %v", err)
	}
	if err := s.DeclineApplication(ctx, a.ID, "other", hash("bob@example.org")); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a claimed application cannot be declined: %v", err)
	}
	// A claim older than ten minutes is a crashed approval and can be taken over.
	_, second, err := s.ClaimApplication(ctx, a.ID, "other", t0.Add(11*time.Minute))
	if err != nil || second == first {
		t.Fatalf("a stale claim must be claimable, with a new token: %v", err)
	}
	if err := s.ReleaseClaim(ctx, a.ID, first, "admin"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a taken-over claim cannot be released by its old holder: %v", err)
	}
	if err := s.ReleaseClaim(ctx, a.ID, second, "other"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseClaim(ctx, a.ID, second, "other"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("only a claimed application can be released: %v", err)
	}
	if err := s.DeclineApplication(ctx, a.ID, "other", hash("bob@example.org")); err != nil {
		t.Fatalf("a released application can be declined: %v", err)
	}
	if _, _, err := s.ClaimApplication(ctx, a.ID, "other", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a missing application: %v", err)
	}
	if err := s.ReleaseClaim(ctx, a.ID, second, "other"); !errors.Is(err, store.ErrNotFound) {
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
			_, _, errs[i] = s.ClaimApplication(ctx, a.ID, "admin", time.Now())
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

func TestDismissApplication(t *testing.T) {
	s := storetest.New(t)
	t0 := time.Now()
	exp := t0.Add(time.Hour)
	inv, _ := s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), exp, 1)
	a, _ := s.Apply(ctx, hash("a"), t0, newApp("bob"))
	if err := s.DismissApplication(ctx, a.ID, "admin", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a pending application is declined, not dismissed: %v", err)
	}
	_, claim, err := s.ClaimApplication(ctx, a.ID, "admin", t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DismissApplication(ctx, a.ID, "admin", t0.Add(5*time.Minute)); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a live claim cannot be dismissed: %v", err)
	}
	if err := s.MarkLinkUnsent(ctx, a.ID, claim, "idp-1", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInvite(ctx, hash("b"), "sub", "alice", fresh(), exp, 1); !errors.Is(err, store.ErrQuota) {
		t.Fatalf("the slot is still in use: %v", err)
	}
	if err := s.DismissApplication(ctx, a.ID, "admin", t0); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a link_unsent application whose link is being sent cannot be dismissed: %v", err)
	}
	if err := s.ReleaseClaim(ctx, a.ID, claim, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissApplication(ctx, a.ID, "admin", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Application(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a dismissed application is deleted")
	}
	if list, _ := s.ListInvites(ctx, "sub"); len(list) != 1 || list[0].ID != inv.ID || list[0].State != "expired" {
		t.Fatalf("the invite is expired: %+v", list)
	}
	if _, err := s.CreateInvite(ctx, hash("b"), "sub", "alice", fresh(), exp, 1); err != nil {
		t.Fatalf("dismissing frees the slot: %v", err)
	}
	if got, _ := s.AuditSubjects(ctx, "application.dismiss"); len(got) != 1 || got[0] != fmt.Sprint(a.ID) {
		t.Fatalf("audit: %v", got)
	}
	if err := s.DismissApplication(ctx, a.ID, "admin", t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a second dismiss: %v", err)
	}
}

// An approval whose claim has outlived the takeover window can be dismissed.
func TestDismissStaleClaim(t *testing.T) {
	s := storetest.New(t)
	t0 := time.Now()
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), t0.Add(time.Hour), 1)
	a, _ := s.Apply(ctx, hash("a"), t0, newApp("bob"))
	if _, _, err := s.ClaimApplication(ctx, a.ID, "admin", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissApplication(ctx, a.ID, "admin", t0.Add(11*time.Minute)); err != nil {
		t.Fatalf("a stale claim can be dismissed: %v", err)
	}
	if _, err := s.Application(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("dismissed application remains")
	}
}

// An approval whose claim was taken over can write nothing more; the
// approval that took it over proceeds.
func TestTakenOverClaimWritesNothing(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), now.Add(time.Hour), 3)
	app, _ := s.Apply(ctx, hash("a"), now, newApp("bob"))
	_, a, err := s.ClaimApplication(ctx, app.ID, "admin-a", now)
	if err != nil {
		t.Fatal(err)
	}
	// A's claim ages past the takeover window, as if A had stalled.
	if _, err := storetest.Conn(t, s).Exec(ctx, `UPDATE applications SET claimed_at = claimed_at - interval '11 minutes' WHERE id=$1`, app.ID); err != nil {
		t.Fatal(err)
	}
	_, b, err := s.ClaimApplication(ctx, app.ID, "admin-b", now)
	if err != nil {
		t.Fatalf("B takes the stale claim over: %v", err)
	}
	if err := s.MarkLinkUnsent(ctx, app.ID, a, "idp-a", "admin-a"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("A's MarkLinkUnsent: %v", err)
	}
	if err := s.ReleaseClaim(ctx, app.ID, a, "admin-a"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("A's ReleaseClaim: %v", err)
	}
	if err := s.CompleteApplication(ctx, app.ID, a, "admin-a"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("A's CompleteApplication: %v", err)
	}
	if err := s.MarkLinkUnsent(ctx, app.ID, b, "idp-b", "admin-b"); err != nil {
		t.Fatalf("B proceeds: %v", err)
	}
	if got, _ := s.Application(ctx, app.ID); got.IDPUserID != "idp-b" {
		t.Fatalf("B's account recorded: %+v", got)
	}
	if err := s.CompleteApplication(ctx, app.ID, b, "admin-b"); err != nil {
		t.Fatal(err)
	}
}

// auditOnce fails unless exactly one audit row for action names id.
func auditOnce(t *testing.T, s *store.Store, action string, id int64) {
	t.Helper()
	got, err := s.AuditSubjects(ctx, action)
	if err != nil || len(got) != 1 || got[0] != fmt.Sprint(id) {
		t.Fatalf("%s: want one row naming %d, got %v %v", action, id, got, err)
	}
}

// Every mutating store method writes its own audit row, naming the record by
// its ID.
func TestEveryWriteIsAudited(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	exp := now.Add(time.Hour)
	inv, _ := s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), exp, 0)
	auditOnce(t, s, "invite.create", inv.ID)

	r, _ := s.CreateInvite(ctx, hash("r"), "other", "carol", fresh(), exp, 0)
	if err := s.RevokeInvite(ctx, r.ID, "other"); err != nil {
		t.Fatal(err)
	}
	auditOnce(t, s, "invite.revoke", r.ID)

	a, err := s.Apply(ctx, hash("a"), now, newApp("bob"))
	if err != nil {
		t.Fatal(err)
	}
	auditOnce(t, s, "application.create", a.ID)

	_, claim, _ := s.ClaimApplication(ctx, a.ID, "admin", now)
	auditOnce(t, s, "application.claim", a.ID)
	if err := s.ReleaseClaim(ctx, a.ID, claim, "admin"); err != nil {
		t.Fatal(err)
	}
	auditOnce(t, s, "application.release", a.ID)

	_, claim, _ = s.ClaimApplication(ctx, a.ID, "admin", now)
	if err := s.MarkLinkUnsent(ctx, a.ID, claim, "idp-1", "admin"); err != nil {
		t.Fatal(err)
	}
	auditOnce(t, s, "application.link_unsent", a.ID)
	if err := s.CompleteApplication(ctx, a.ID, claim, "admin"); err != nil {
		t.Fatal(err)
	}
	auditOnce(t, s, "application.complete", a.ID)

	s.CreateInvite(ctx, hash("d"), "sub", "alice", fresh(), exp, 0)
	d, _ := s.Apply(ctx, hash("d"), now, newApp("dan"))
	if err := s.DeclineApplication(ctx, d.ID, "admin", hash("dan")); err != nil {
		t.Fatal(err)
	}
	auditOnce(t, s, "application.decline", d.ID)

	if err := s.Audit(ctx, "admin", "idp.login_link", "42"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AuditSubjects(ctx, "idp.login_link"); len(got) != 1 || got[0] != "42" {
		t.Fatalf("Audit: %v", got)
	}
}

func TestMarkLinkUnsent(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), now.Add(time.Hour), 3)
	a, _ := s.Apply(ctx, hash("a"), now, newApp("bob"))
	_, claim, _ := s.ClaimApplication(ctx, a.ID, "admin", now)
	if err := s.MarkLinkUnsent(ctx, a.ID, claim, "idp-7", "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Application(ctx, a.ID)
	if err != nil || got.State != "link_unsent" || got.IDPUserID != "idp-7" {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.MarkLinkUnsent(ctx, a.ID, claim, "idp-8", "admin"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a link_unsent application is not marked again: %v", err)
	}
	if got, _ := s.Application(ctx, a.ID); got.IDPUserID != "idp-7" {
		t.Fatalf("recorded account changed: %+v", got)
	}
}

// A retry claims a link_unsent application without changing its state, so
// that only one retry sends the link.
func TestRetryClaim(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), now.Add(time.Hour), 3)
	a, _ := s.Apply(ctx, hash("a"), now, newApp("bob"))
	_, first, _ := s.ClaimApplication(ctx, a.ID, "admin", now)
	s.MarkLinkUnsent(ctx, a.ID, first, "idp-7", "admin")
	if _, _, err := s.ClaimApplication(ctx, a.ID, "other", now.Add(5*time.Minute)); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("the approval sending the link still holds it: %v", err)
	}
	if err := s.ReleaseClaim(ctx, a.ID, first, "admin"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Application(ctx, a.ID); got.State != "link_unsent" || !got.ClaimedAt.IsZero() {
		t.Fatalf("a released link_unsent application stays link_unsent, unclaimed: %+v", got)
	}
	got, retry, err := s.ClaimApplication(ctx, a.ID, "other", now)
	if err != nil || got.State != "link_unsent" || got.IDPUserID != "idp-7" {
		t.Fatalf("retry claim: %+v %v", got, err)
	}
	if _, _, err := s.ClaimApplication(ctx, a.ID, "third", now); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("a second retry: %v", err)
	}
	if err := s.CompleteApplication(ctx, a.ID, first, "admin"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("only the retry's claim completes: %v", err)
	}
	if err := s.CompleteApplication(ctx, a.ID, retry, "other"); err != nil {
		t.Fatal(err)
	}
}

// Rows that share a timestamp come back in a fixed order: invites newest
// first by id, applications oldest first by id.
func TestListOrderIsDeterministic(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	var ids []int64
	for i := 0; i < 4; i++ {
		inv, _ := s.CreateInvite(ctx, hash(fmt.Sprint("o", i)), "sub", "alice", fresh(), now.Add(time.Hour), 0)
		ids = append(ids, inv.ID)
	}
	conn := storetest.Conn(t, s)
	if _, err := conn.Exec(ctx, `UPDATE invites SET created_at = $1`, now); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListInvites(ctx, "sub")
	for i, inv := range list {
		if inv.ID != ids[len(ids)-1-i] {
			t.Fatalf("invites not newest-first by id: %v", list)
		}
	}
	if _, err := conn.Exec(ctx, `UPDATE invites SET created_at = $1 WHERE id=$2`, now.Add(time.Minute), ids[0]); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListInvites(ctx, "sub"); list[0].ID != ids[0] {
		t.Fatalf("the newest invite comes first: %v", list)
	}
	var apps []int64
	for i := 0; i < 3; i++ {
		a, err := s.Apply(ctx, hash(fmt.Sprint("o", i)), now, newApp(fmt.Sprint("u", i)))
		if err != nil {
			t.Fatal(err)
		}
		apps = append(apps, a.ID)
	}
	pending, _ := s.PendingApplications(ctx)
	for i, a := range pending {
		if a.ID != apps[i] {
			t.Fatalf("applications not oldest-first by id: %v", pending)
		}
	}
}

func TestRevokeOnlyOpen(t *testing.T) {
	s := storetest.New(t)
	exp := time.Now().Add(time.Hour)
	applied, _ := s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), exp, 0)
	s.Apply(ctx, hash("a"), time.Now(), newApp("bob"))
	revoked, _ := s.CreateInvite(ctx, hash("r"), "sub", "alice", fresh(), exp, 0)
	if err := s.RevokeInvite(ctx, revoked.ID, "sub"); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]int64{"applied": applied.ID, "revoked": revoked.ID} {
		if err := s.RevokeInvite(ctx, id, "sub"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got, _ := s.AuditSubjects(ctx, "invite.revoke"); len(got) != 1 {
		t.Fatalf("a refused revoke writes no audit row: %v", got)
	}
}
