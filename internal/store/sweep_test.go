package store_test

import (
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

func TestSweep(t *testing.T) {
	s := storetest.New(t)
	now := time.Now()
	s.CreateInvite(ctx, hash("old"), "sub", "alice", now.Add(-time.Minute), 0)
	s.CreateInvite(ctx, hash("app"), "sub", "alice", now.Add(time.Hour), 0)
	s.Apply(ctx, hash("app"), now.Add(-20*24*time.Hour), newApp("bob"))
	s.CreateInvite(ctx, hash("d"), "sub", "alice", now.Add(time.Hour), 0)
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
}
