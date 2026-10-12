package flow_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/idp"
	"github.com/paisans-software/paisans-invite/internal/idp/idptest"
	"github.com/paisans-software/paisans-invite/internal/mail/mailtest"
	"github.com/paisans-software/paisans-invite/internal/store"
	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

var ctx = context.Background()
var alice = flow.Member{Sub: "s-alice", Username: "alice"}
var root = flow.Member{Sub: "s-root", Username: "root", Admin: true}

func setup(t *testing.T, approval bool) (*flow.Service, *idptest.Fake, *mailtest.Recorder) {
	st := storetest.New(t)
	f := &idptest.Fake{Users: []idp.User{{ID: "r", Username: "root", Email: "root@x.org"}}, Groups: map[string][]string{"admins": {"root"}}}
	m := &mailtest.Recorder{}
	s := flow.New(st, f, m, coc.CoC{Version: "v1"}, flow.Settings{
		Quota: 2, RequireApproval: approval, InviteLifetime: 14 * 24 * time.Hour,
		PublicURL: "https://invite.example.org", EmailHashKey: make([]byte, 32), AdminGroup: "admins",
	}, time.Now)
	return s, f, m
}

func token(t *testing.T, link string) string {
	i := strings.LastIndex(link, "/i/")
	if i < 0 {
		t.Fatalf("bad link %q", link)
	}
	return link[i+3:]
}

func bob() flow.Applicant {
	return flow.Applicant{Username: "bob", Email: "bob@example.org", DisplayName: "Bob", Agreed: true}
}

func TestApprovalFlow(t *testing.T) {
	s, f, m := setup(t, true)
	link, _, err := s.CreateInvite(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	now, err := s.Apply(ctx, token(t, link), bob())
	if err != nil || now {
		t.Fatalf("with approval on, nothing is created yet: %v %v", now, err)
	}
	if f.Created != 0 || len(m.To("root@x.org")) != 1 || len(m.To("bob@example.org")) != 1 {
		t.Fatalf("admins and the invitee are told, no account yet: created=%d", f.Created)
	}
	p, _ := s.Pending(ctx)
	if err := s.Approve(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	if f.Created != 1 || f.LinksSent != 1 {
		t.Fatalf("created=%d links=%d", f.Created, f.LinksSent)
	}
}

func TestNoApprovalCreatesAtOnce(t *testing.T) {
	s, f, _ := setup(t, false)
	link, _, _ := s.CreateInvite(ctx, alice)
	now, err := s.Apply(ctx, token(t, link), bob())
	if err != nil || !now || f.Created != 1 || f.LinksSent != 1 {
		t.Fatalf("now=%v err=%v created=%d", now, err, f.Created)
	}
}

func TestApplyRefusals(t *testing.T) {
	s, f, _ := setup(t, true)
	f.Users = append(f.Users, idp.User{ID: "x", Username: "Taken", Email: "used@example.org"})
	link, _, _ := s.CreateInvite(ctx, alice)
	tok := token(t, link)
	for name, c := range map[string]struct {
		a    flow.Applicant
		want error
	}{
		"not agreed":     {flow.Applicant{Username: "bob", Email: "b@x.org", DisplayName: "B"}, flow.ErrNotAgreed},
		"bad username":   {flow.Applicant{Username: "-bob", Email: "b@x.org", DisplayName: "B", Agreed: true}, flow.ErrInvalidUsername},
		"long username":  {flow.Applicant{Username: strings.Repeat("a", 51), Email: "b@x.org", DisplayName: "B", Agreed: true}, flow.ErrInvalidUsername},
		"taken username": {flow.Applicant{Username: "taken", Email: "b@x.org", DisplayName: "B", Agreed: true}, flow.ErrUsernameTaken},
		"used email":     {flow.Applicant{Username: "bob", Email: "USED@example.org", DisplayName: "B", Agreed: true}, flow.ErrEmailTaken},
		"bad email":      {flow.Applicant{Username: "bob", Email: "nope", DisplayName: "B", Agreed: true}, flow.ErrInvalidEmail},
		"empty name":     {flow.Applicant{Username: "bob", Email: "b@x.org", DisplayName: " ", Agreed: true}, flow.ErrInvalidName},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Apply(ctx, tok, c.a); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
	if _, err := s.OpenInvite(ctx, tok); err != nil {
		t.Fatal("refused applications must leave the invite usable")
	}
}

func TestBadTokensAllLookTheSame(t *testing.T) {
	s, _, _ := setup(t, true)
	for _, tok := range []string{"", "short", strings.Repeat("A", 43), "!!!!"} {
		if _, err := s.OpenInvite(ctx, tok); !errors.Is(err, store.ErrNotUsable) {
			t.Fatalf("%q: %v", tok, err)
		}
	}
}

func TestRetryDoesNotCreateTwice(t *testing.T) {
	s, f, _ := setup(t, true)
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	p, _ := s.Pending(ctx)
	f.FailSendLink = errors.New("smtp down")
	if err := s.Approve(ctx, root, p[0].ID); err == nil {
		t.Fatal("want the send failure reported")
	}
	p, _ = s.Pending(ctx)
	if p[0].State != "link_unsent" {
		t.Fatalf("state %q", p[0].State)
	}
	f.FailSendLink = nil
	if err := s.Approve(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	if f.Created != 1 || f.LinksSent != 1 {
		t.Fatalf("created=%d links=%d", f.Created, f.LinksSent)
	}
}

func TestDeclineRecordsHistoryAndNotifies(t *testing.T) {
	s, f, m := setup(t, true)
	f.Users = append(f.Users, idp.User{ID: "a", Username: "alice", Email: "alice@x.org"})
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	p, _ := s.Pending(ctx)
	if err := s.Decline(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	declined := 0
	for _, msg := range m.To("bob@example.org") {
		if msg.Subject == "About your invitation" {
			declined++
		}
	}
	if declined != 1 || len(m.To("alice@x.org")) != 1 {
		t.Fatal("invitee and inviter are told")
	}
	link2, _, _ := s.CreateInvite(ctx, alice)
	a := bob()
	a.Email = " Bob@Example.org "
	s.Apply(ctx, token(t, link2), a)
	p, _ = s.Pending(ctx)
	if p[0].Declines.Count != 1 {
		t.Fatalf("decline history not shown: %+v", p[0].Declines)
	}
}

func TestApproveRechecksUsername(t *testing.T) {
	s, f, _ := setup(t, true)
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	f.Users = append(f.Users, idp.User{ID: "z", Username: "Bob", Email: "z@x.org"})
	p, _ := s.Pending(ctx)
	if err := s.Approve(ctx, root, p[0].ID); !errors.Is(err, flow.ErrUsernameTaken) || f.Created != 0 {
		t.Fatalf("err=%v created=%d", err, f.Created)
	}
}

func TestNonAdminCannotDecide(t *testing.T) {
	s, _, _ := setup(t, true)
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	p, _ := s.Pending(ctx)
	if err := s.Approve(ctx, alice, p[0].ID); err == nil {
		t.Fatal("members cannot approve")
	}
}

func TestApprovalOffProviderFailureLeavesApplicationPending(t *testing.T) {
	s, f, m := setup(t, false)
	f.FailCreate = errors.New("pocket id down")
	link, _, _ := s.CreateInvite(ctx, alice)
	now, err := s.Apply(ctx, token(t, link), bob())
	if now || err != nil {
		t.Fatalf("now=%v err=%v", now, err)
	}
	if len(m.To("bob@example.org")) != 1 || len(m.To("root@x.org")) != 1 {
		t.Fatal("applicant and admins are told")
	}
	p, _ := s.Pending(ctx)
	if len(p) != 1 {
		t.Fatalf("pending: %d", len(p))
	}
	f.FailCreate = nil
	if err := s.Approve(ctx, root, p[0].ID); err != nil || f.Created != 1 {
		t.Fatalf("err=%v created=%d", err, f.Created)
	}
}

func TestApproveAdoptsAccountItAlreadyCreated(t *testing.T) {
	s, f, _ := setup(t, true)
	link, _, _ := s.CreateInvite(ctx, alice)
	s.Apply(ctx, token(t, link), bob())
	// The account exists with the applicant's email, as after a crash between
	// creating it and recording it.
	f.Users = append(f.Users, idp.User{ID: "prev", Username: "bob", Email: "BOB@example.org"})
	p, _ := s.Pending(ctx)
	if err := s.Approve(ctx, root, p[0].ID); err != nil {
		t.Fatal(err)
	}
	if f.Created != 0 || f.LinksSent != 1 {
		t.Fatalf("created=%d links=%d", f.Created, f.LinksSent)
	}
}

func TestProviderDownAtApplyStoresApplication(t *testing.T) {
	s, f, _ := setup(t, true)
	f.FailLookup = errors.New("pocket id down")
	link, _, _ := s.CreateInvite(ctx, alice)
	if _, err := s.Apply(ctx, token(t, link), bob()); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Pending(ctx)
	if len(p) != 1 {
		t.Fatalf("pending: %d", len(p))
	}
	if err := s.Approve(ctx, root, p[0].ID); err == nil || f.Created != 0 {
		t.Fatalf("approval needs the provider: err=%v created=%d", err, f.Created)
	}
}

func TestQuotaReportsSetting(t *testing.T) {
	s, _, _ := setup(t, true)
	if s.Quota() != 2 {
		t.Fatal(s.Quota())
	}
}

func TestWaitingNoticeGoesToAdminGroupOnly(t *testing.T) {
	s, f, m := setup(t, true)
	f.Users = append(f.Users, idp.User{ID: "a", Username: "alice", Email: "alice@x.org"})
	f.Groups["members"] = []string{"alice"}
	link, _, _ := s.CreateInvite(ctx, alice)
	if _, err := s.Apply(ctx, token(t, link), bob()); err != nil {
		t.Fatal(err)
	}
	if len(m.To("root@x.org")) != 1 || len(m.To("alice@x.org")) != 0 {
		t.Fatal("only the admin group is told an application is waiting")
	}
}
