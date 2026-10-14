package store_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/store"
	"github.com/paisans-software/paisans-account-service/internal/store/storetest"
)

func TestSweep(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	old, _ := s.CreateInvite(ctx, hash("old"), "sub", "alice", fresh(), now.Add(-time.Minute), 0)
	app, _ := s.CreateInvite(ctx, hash("app"), "sub", "alice", fresh(), now.Add(time.Hour), 0)
	s.Apply(ctx, hash("app"), now.Add(-20*24*time.Hour), newApp("bob"))
	dec, _ := s.CreateInvite(ctx, hash("d"), "sub", "alice", fresh(), now.Add(time.Hour), 0)
	d, _ := s.Apply(ctx, hash("d"), now, newApp("carol"))
	s.DeclineApplication(ctx, d.ID, "admin", hash("carol"))

	r, err := s.Sweep(ctx, now.Add(200*24*time.Hour), 14*24*time.Hour, 183*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Ran || r.InvitesExpired != 1 || r.ApplicationsExpired != 1 || r.DeclinesDeleted != 1 {
		t.Fatalf("unexpected sweep: %+v", r)
	}
	if pending, _ := s.PendingApplications(ctx); len(pending) != 0 {
		t.Fatal("stale application must be deleted")
	}
	states := map[string]string{}
	for _, inv := range mustList(t, s, "sub") {
		states[fmt.Sprint(inv.ID)] = inv.State
	}
	if want := map[string]string{fmt.Sprint(old.ID): "expired", fmt.Sprint(app.ID): "expired", fmt.Sprint(dec.ID): "declined"}; !reflect.DeepEqual(states, want) {
		t.Fatalf("invite states: got %v, want %v", states, want)
	}
	if got, _ := s.AuditSubjects(ctx, "sweep"); len(got) != 1 || got[0] != "invites=1 applications=1 declines=1 recoveries=0 email_changes=0" {
		t.Fatalf("sweep audit: %v", got)
	}
	// A sweep with nothing to do writes no audit row.
	if r, err := s.Sweep(ctx, now.Add(200*24*time.Hour), 14*24*time.Hour, 183*24*time.Hour); err != nil || !r.Ran {
		t.Fatalf("%+v %v", r, err)
	}
	if got, _ := s.AuditSubjects(ctx, "sweep"); len(got) != 1 {
		t.Fatalf("an empty sweep was audited: %v", got)
	}
}

func mustList(t *testing.T, s *store.Store, sub string) []store.Invite {
	t.Helper()
	list, err := s.ListInvites(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// Only one site sweeps at a time: while another session holds the lock, a
// sweep does nothing and says so.
func TestSweepSkipsWhileLocked(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	inv, _ := s.CreateInvite(ctx, hash("old"), "sub", "alice", fresh(), now.Add(-time.Minute), 0)
	tx, err := storetest.Conn(t, s).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, store.SweepLockKey); err != nil {
		t.Fatal(err)
	}
	r, err := s.Sweep(ctx, now, 14*24*time.Hour, 183*24*time.Hour)
	if err != nil || r.Ran || r.InvitesExpired != 0 {
		t.Fatalf("locked sweep: %+v %v", r, err)
	}
	if list := mustList(t, s, "sub"); list[0].ID != inv.ID || list[0].State != "open" {
		t.Fatalf("a locked sweep changed nothing: %+v", list)
	}
	if got, _ := s.AuditSubjects(ctx, "sweep"); len(got) != 0 {
		t.Fatalf("a locked sweep wrote audit rows: %v", got)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Sweep(ctx, now, 14*24*time.Hour, 183*24*time.Hour); err != nil || !r.Ran || r.InvitesExpired != 1 {
		t.Fatalf("after the lock is released: %+v %v", r, err)
	}
}

func TestSweepLeavesApplicationsBeingApproved(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	old := now.Add(-20 * 24 * time.Hour)
	s.CreateInvite(ctx, hash("a"), "sub", "alice", fresh(), now.Add(time.Hour), 0)
	a, _ := s.Apply(ctx, hash("a"), old, newApp("bob"))
	s.CreateInvite(ctx, hash("b"), "sub", "alice", fresh(), now.Add(time.Hour), 0)
	b, _ := s.Apply(ctx, hash("b"), old, newApp("carol"))
	if _, _, err := s.ClaimApplication(ctx, a.ID, "admin", old); err != nil {
		t.Fatal(err)
	}
	_, claim, err := s.ClaimApplication(ctx, b.ID, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkLinkUnsent(ctx, b.ID, claim, "idp-1", "admin"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Sweep(ctx, now, 14*24*time.Hour, 183*24*time.Hour)
	if err != nil || r.ApplicationsExpired != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if apps, _ := s.PendingApplications(ctx); len(apps) != 2 {
		t.Fatalf("approving and link_unsent applications must survive the sweep: %d", len(apps))
	}
}
