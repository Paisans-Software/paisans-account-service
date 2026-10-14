package flow_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/idp/idptest"
	"github.com/paisans-software/paisans-account-service/internal/store"
)

// Open registration, from the form to the sign-in link, against the fake
// provider: no invite, and the queue shows no inviter.
func TestRegisterEndToEnd(t *testing.T) {
	s, f, m := setup(t, true)
	now, err := s.Register(ctx, bob())
	if err != nil || now {
		t.Fatalf("%v %v", now, err)
	}
	p, err := s.Pending(ctx)
	if err != nil || len(p) != 1 || p[0].InviteID != 0 || p[0].InviterUsername != "" {
		t.Fatalf("%+v %v", p, err)
	}
	if notice := m.To("root@x.org"); len(notice) != 1 || notice[0].Subject != "An application is waiting" {
		t.Fatalf("admin notice: %+v", notice)
	}
	if err := s.Approve(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	if f.Created != 1 || f.LinksSent != 1 {
		t.Fatalf("created=%d links=%d", f.Created, f.LinksSent)
	}
	if p, _ := s.Pending(ctx); len(p) != 0 {
		t.Fatal("still pending")
	}
}

func TestRegisterWithoutApprovalCreatesAtOnce(t *testing.T) {
	s, f, _ := setup(t, false)
	now, err := s.Register(ctx, bob())
	if err != nil || !now || f.Created != 1 || f.LinksSent != 1 {
		t.Fatalf("now=%v err=%v created=%d", now, err, f.Created)
	}
}

func TestRegisterDeclineHasNoInviter(t *testing.T) {
	s, _, m := setup(t, true)
	s.Register(ctx, bob())
	p, _ := s.Pending(ctx)
	if err := s.Decline(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := m.To("bob@example.org"); len(got) != 2 || got[1].Subject != "About your application" {
		t.Fatalf("%+v", got)
	}
}

func banAddress(t *testing.T, s *flow.Service, f *idptest.Fake, email string) {
	t.Helper()
	f.Users = append(f.Users, idp.User{ID: "banned-" + email, Username: "b" + strings.ReplaceAll(strings.Split(email, "@")[0], ".", ""), Email: email})
	if _, err := s.Ban(ctx, root, "banned-"+email, "spam"); err != nil {
		t.Fatal(err)
	}
}

// A banned address is refused in both modes, in any case or spacing, and the
// answer is the one an accepted application gets: only the address's inbox
// learns otherwise.
func TestBannedAddressRefusedInBothModes(t *testing.T) {
	for _, approval := range []bool{true, false} {
		s, f, m := setup(t, approval)
		banAddress(t, s, f, "bob@example.org")
		a := bob()
		a.Email = " Bob@EXAMPLE.org "
		link, _, _ := s.CreateInvite(ctx, alice, flow.NewNonce())
		for name, submit := range map[string]func() (bool, error){
			"invite": func() (bool, error) { return s.Apply(ctx, token(t, link), a) },
			"open":   func() (bool, error) { return s.Register(ctx, a) },
		} {
			now, err := submit()
			if err != nil || now != !approval {
				t.Fatalf("%s approval=%v: %v %v", name, approval, now, err)
			}
		}
		if p, _ := s.Pending(ctx); len(p) != 0 || f.Created != 0 {
			t.Fatalf("approval=%v: something was recorded: %d pending, %d created", approval, len(p), f.Created)
		}
		got := m.To("Bob@EXAMPLE.org")
		if len(got) != 2 || got[0].Subject != "About your application" || !strings.Contains(got[0].Body, "can't be accepted") {
			t.Fatalf("approval=%v: %+v", approval, got)
		}
		if len(m.To("root@x.org")) != 0 {
			t.Fatal("admins were told of a refused signup")
		}
		if _, err := s.OpenInvite(ctx, token(t, link)); err != nil {
			t.Fatal("a refused address leaves the invite usable")
		}
	}
}

// An address with an account is answered as accepted, and its inbox is told
// how to sign in.
func TestAddressInUseIsNotRevealed(t *testing.T) {
	s, f, m := setup(t, true)
	f.Users = append(f.Users, idp.User{ID: "x", Username: "other", Email: "used@example.org"})
	a := bob()
	a.Email = "USED@example.org"
	now, err := s.Register(ctx, a)
	if err != nil || now {
		t.Fatalf("%v %v", now, err)
	}
	got := m.To("USED@example.org")
	if len(got) != 1 || got[0].Subject != "You already have an account" || !strings.Contains(got[0].Body, "https://id.example.org") {
		t.Fatalf("%+v", got)
	}
	if p, _ := s.Pending(ctx); len(p) != 0 {
		t.Fatal("recorded")
	}
}

// A dead invite is never answered as accepted, even for a refused address.
func TestRefusedAddressWithDeadInviteIsUnusable(t *testing.T) {
	s, f, _ := setup(t, true)
	banAddress(t, s, f, "bob@example.org")
	if _, err := s.Apply(ctx, strings.Repeat("A", 43), bob()); !errors.Is(err, store.ErrNotUsable) {
		t.Fatalf("%v", err)
	}
}

// A ban made after the application stops its approval.
func TestBanAfterApplicationStopsApproval(t *testing.T) {
	s, f, _ := setup(t, true)
	s.Register(ctx, bob())
	banAddress(t, s, f, "bob@example.org")
	p, _ := s.Pending(ctx)
	if err := s.Approve(ctx, root, p[0].ID); !errors.Is(err, flow.ErrBanned) {
		t.Fatalf("%v", err)
	}
	if f.Created != 0 {
		t.Fatal("created")
	}
	if p, _ := s.Pending(ctx); len(p) != 1 || p[0].State != "pending" {
		t.Fatalf("the claim must be released: %+v", p)
	}
}
