package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/store"
	"github.com/paisans-software/paisans-account-service/internal/store/storetest"
)

// A registration has no invite, and goes through every application step an
// invited one does.
func TestRegistrationHasNoInvite(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	a, err := s.Register(ctx, now, newApp("bob"))
	if err != nil || a.InviteID != 0 || a.InviterUsername != "" || a.State != "pending" {
		t.Fatalf("%+v %v", a, err)
	}
	auditOnce(t, s, "application.create", a.ID)
	if _, err := s.Register(ctx, now, newApp("BOB")); !errors.Is(err, store.ErrUsernameTaken) {
		t.Fatalf("username clash: %v", err)
	}
	if p, _ := s.PendingApplications(ctx); len(p) != 1 || p[0].ID != a.ID {
		t.Fatalf("pending: %+v", p)
	}
	_, claim, err := s.ClaimApplication(ctx, a.ID, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLinkUnsent(ctx, a.ID, claim, "idp-1", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteApplication(ctx, a.ID, claim, "admin"); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Register(ctx, now, newApp("dan"))
	if err := s.DeclineApplication(ctx, d.ID, "admin", hash("dan")); err != nil {
		t.Fatal(err)
	}
	x, _ := s.Register(ctx, now, newApp("xena"))
	_, claim, _ = s.ClaimApplication(ctx, x.ID, "admin", now)
	s.MarkLinkUnsent(ctx, x.ID, claim, "idp-2", "admin")
	s.ReleaseClaim(ctx, x.ID, claim, "admin")
	if err := s.DismissApplication(ctx, x.ID, "admin", now); err != nil {
		t.Fatal(err)
	}
	s.Register(ctx, now.Add(-20*24*time.Hour), newApp("old"))
	r, err := s.Sweep(ctx, now, 14*24*time.Hour, 183*24*time.Hour)
	if err != nil || r.ApplicationsExpired != 1 {
		t.Fatalf("sweep: %+v %v", r, err)
	}
	if p, _ := s.PendingApplications(ctx); len(p) != 0 {
		t.Fatalf("left: %+v", p)
	}
}

func TestBans(t *testing.T) {
	s := storetest.New(t)
	b, err := s.RecordBan(ctx, "u1", "bob", hash("bob@x.org"), "spam", "s-root", "root")
	if err != nil || !b.Current() || b.Reason != "spam" || b.BannedBy != "root" {
		t.Fatalf("%+v %v", b, err)
	}
	if got, _ := s.AuditSubjects(ctx, "ban.create"); len(got) != 1 || got[0] != fmt.Sprint(b.ID)+" user=u1" {
		t.Fatalf("audit: %v", got)
	}
	if _, err := s.RecordBan(ctx, "u1", "bob", hash("bob@x.org"), "again", "s-root", "root"); !errors.Is(err, store.ErrAlreadyBanned) {
		t.Fatalf("second current ban: %v", err)
	}
	if _, err := s.RecordBan(ctx, "u2", "eve", nil, "", "s-root", "root"); err == nil {
		t.Fatal("an empty reason must be refused")
	}
	if banned, _ := s.EmailBanned(ctx, hash("bob@x.org")); !banned {
		t.Fatal("banned address not matched")
	}
	if cur, _ := s.CurrentBans(ctx); len(cur) != 1 || cur["u1"].ID != b.ID {
		t.Fatalf("current: %v", cur)
	}
	l, err := s.LiftBan(ctx, b.ID, "s-ann", "ann")
	if err != nil || l.Current() || l.LiftedBy != "ann" {
		t.Fatalf("%+v %v", l, err)
	}
	if got, _ := s.AuditSubjects(ctx, "ban.lift"); len(got) != 1 {
		t.Fatalf("lift audit: %v", got)
	}
	if _, err := s.LiftBan(ctx, b.ID, "s-ann", "ann"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second lift: %v", err)
	}
	if banned, _ := s.EmailBanned(ctx, hash("bob@x.org")); banned {
		t.Fatal("a lifted ban still matches")
	}
	if _, err := s.RecordBan(ctx, "u1", "bob", hash("bob@x.org"), "back again", "s-root", "root"); err != nil {
		t.Fatalf("a lifted user can be banned again: %v", err)
	}
	if all, _ := s.Bans(ctx); len(all) != 2 || all[0].Reason != "back again" {
		t.Fatalf("list: %+v", all)
	}
}

func TestEmailChangeAttempts(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	if _, err := s.TakeEmailChangeAttempt(ctx, "u1", now, 5); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("none pending: %v", err)
	}
	if err := s.StartEmailChange(ctx, "u1", "new@x.org", []byte("h"), now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if addr, ok, _ := s.PendingEmailChange(ctx, "u1", now); !ok || addr != "new@x.org" {
		t.Fatalf("pending: %q %v", addr, ok)
	}
	for i := 1; i <= 5; i++ {
		c, err := s.TakeEmailChangeAttempt(ctx, "u1", now, 5)
		if err != nil || c.Attempts != i || c.NewEmail != "new@x.org" || string(c.CodeHash) != "h" {
			t.Fatalf("attempt %d: %+v %v", i, c, err)
		}
	}
	if _, err := s.TakeEmailChangeAttempt(ctx, "u1", now, 5); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sixth attempt: %v", err)
	}
	// A new request starts the count again.
	s.StartEmailChange(ctx, "u1", "other@x.org", []byte("h2"), now.Add(15*time.Minute))
	if c, err := s.TakeEmailChangeAttempt(ctx, "u1", now, 5); err != nil || c.Attempts != 1 || c.NewEmail != "other@x.org" {
		t.Fatalf("restarted: %+v %v", c, err)
	}
	if _, err := s.TakeEmailChangeAttempt(ctx, "u1", now.Add(16*time.Minute), 5); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
	if err := s.FinishEmailChange(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishEmailChange(ctx, "u1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("finished twice: %v", err)
	}
	if got, _ := s.AuditSubjects(ctx, "email_change.request"); len(got) != 2 || got[0] != "u1" {
		t.Fatalf("audit: %v", got)
	}
	if got, _ := s.AuditSubjects(ctx, "email_change.verified"); len(got) != 1 {
		t.Fatalf("audit: %v", got)
	}
	s.StartEmailChange(ctx, "u2", "z@x.org", []byte("h"), now.Add(time.Minute))
	if r, err := s.Sweep(ctx, now.Add(time.Hour), 14*24*time.Hour, 183*24*time.Hour); err != nil || r.EmailChangesDeleted != 1 {
		t.Fatalf("sweep: %+v %v", r, err)
	}
}

func openRecovery(t *testing.T, s *store.Store, user string, now time.Time) store.Recovery {
	t.Helper()
	r, err := s.OpenRecovery(ctx, store.Recovery{UserID: user, Username: user, NewEmail: user + "@new.org", Reason: "video call",
		OpenedBySub: "s-ann", OpenedBy: "ann", OpenedAt: now, ExpiresAt: now.Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRecoveryRequests(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	r := openRecovery(t, s, "u1", now)
	if r.State != "open" {
		t.Fatal(r.State)
	}
	if _, err := s.OpenRecovery(ctx, store.Recovery{UserID: "u1", Username: "u1", NewEmail: "x@y.org", Reason: "r",
		OpenedBySub: "s-bo", OpenedBy: "bo", OpenedAt: now, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("second open request: %v", err)
	}
	if _, err := s.ClaimRecovery(ctx, r.ID, "s-ann", now); !errors.Is(err, store.ErrSameAdmin) {
		t.Fatalf("opener approving: %v", err)
	}
	if _, err := s.ClaimRecovery(ctx, r.ID, "s-bo", now.Add(49*time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
	c, err := s.ClaimRecovery(ctx, r.ID, "s-bo", now)
	if err != nil || c.State != "approving" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := s.ClaimRecovery(ctx, r.ID, "s-cy", now); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("second claim: %v", err)
	}
	if err := s.CancelRecovery(ctx, r.ID, "s-ann"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancel while approving: %v", err)
	}
	if err := s.ReleaseRecovery(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimRecovery(ctx, r.ID, "s-bo", now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRecovery(ctx, r.ID, "s-bo", "bo"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Recovery(ctx, r.ID); got.State != "approved" || got.ApprovedBy != "bo" {
		t.Fatalf("%+v", got)
	}
	if _, err := s.ClaimRecovery(ctx, r.ID, "s-cy", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approved twice: %v", err)
	}
	// A closed request frees the user for a new one.
	r2 := openRecovery(t, s, "u1", now)
	if err := s.CancelRecovery(ctx, r2.ID, "s-bo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimRecovery(ctx, r2.ID, "s-bo", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancelled: %v", err)
	}
	for action, n := range map[string]int{"recovery.open": 2, "recovery.claim": 2, "recovery.approve": 1, "recovery.cancel": 1} {
		if got, _ := s.AuditSubjects(ctx, action); len(got) != n {
			t.Errorf("%s: %v", action, got)
		}
	}
	r3 := openRecovery(t, s, "u3", now)
	sw, err := s.Sweep(ctx, now.Add(49*time.Hour), 14*24*time.Hour, 183*24*time.Hour)
	if err != nil || sw.RecoveriesExpired != 1 {
		t.Fatalf("sweep: %+v %v", sw, err)
	}
	if got, _ := s.Recovery(ctx, r3.ID); got.State != "expired" {
		t.Fatal(got.State)
	}
	if list, _ := s.Recoveries(ctx); len(list) != 3 || list[0].ID != r3.ID {
		t.Fatalf("list: %+v", list)
	}
}

// A claim left by a crash may be taken over after ten minutes.
func TestRecoveryStaleClaim(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	r := openRecovery(t, s, "u1", now)
	if _, err := s.ClaimRecovery(ctx, r.ID, "s-bo", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimRecovery(ctx, r.ID, "s-cy", now.Add(11*time.Minute)); err != nil {
		t.Fatalf("stale claim: %v", err)
	}
}
