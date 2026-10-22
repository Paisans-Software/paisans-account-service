package flow_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/idp"
	"github.com/paisans-software/paisans-account-service/internal/idp/idptest"
	"github.com/paisans-software/paisans-account-service/internal/mail/mailtest"
	"github.com/paisans-software/paisans-account-service/internal/store"
	"github.com/paisans-software/paisans-account-service/internal/store/storetest"
)

// people adds a member bob, an operator Opal, and a second admin ann.
func people(f *idptest.Fake) {
	f.Users = append(f.Users,
		idp.User{ID: "b", Username: "bob", Email: "bob@x.org", DisplayName: "Bob Builder"},
		idp.User{ID: "o", Username: "opal", Email: "opal@x.org", DisplayName: "Opal"},
		idp.User{ID: "a", Username: "ann", Email: "ann@x.org", DisplayName: "Ann"},
	)
	f.Groups["admins"] = append(f.Groups["admins"], "ann")
}

var ann = flow.Member{Sub: "a", Username: "ann", Admin: true}

// rootR is root as their own session names them: their sub is their user ID.
var rootR = flow.Member{Sub: "r", Username: "root", Admin: true}

func usernames(rows []flow.MemberRow) string {
	var got []string
	for _, r := range rows {
		got = append(got, r.Username)
	}
	return strings.Join(got, ",")
}

func TestMembersSearch(t *testing.T) {
	s, f, _ := setup(t, true)
	people(f)
	for q, want := range map[string]string{"BOB": "bob", "builder": "bob", "opal@X": "opal", "  ": "ann,bob,opal,root", "zzz": ""} {
		p, err := s.Members(ctx, root, flow.MembersQuery{Q: q})
		if err != nil || usernames(p.Rows) != want {
			t.Errorf("%q: %v %v", q, usernames(p.Rows), err)
		}
	}
	p, _ := s.Members(ctx, rootR, flow.MembersQuery{Q: "o"})
	if len(p.Rows) != 4 || p.Total != 4 {
		t.Fatalf("%+v", p)
	}
	for _, r := range p.Rows {
		// Not the operator, not oneself; another admin may be banned.
		if want := r.Username == "bob" || r.Username == "ann"; r.Bannable() != want {
			t.Errorf("%s bannable %v", r.Username, r.Bannable())
		}
	}
	if _, err := s.Members(ctx, alice, flow.MembersQuery{Q: "bob"}); !errors.Is(err, flow.ErrForbidden) {
		t.Fatal("members cannot list members")
	}
}

// With no query every user is listed, by name unless told otherwise; an
// unknown column is the default one.
func TestMembersSort(t *testing.T) {
	s, f, _ := setupFull(t)
	people(f)
	f.Users[0].DisplayName = "zed"    // root
	f.Users[2].Email = "a-opal@x.org" // opal
	f.Users = append(f.Users, idp.User{ID: "c", Username: "cy", Email: "cy@x.org", DisplayName: "cy", Disabled: true})
	if _, err := s.Ban(ctx, root, "b", "spam"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		q    flow.MembersQuery
		sort string
		want string
	}{
		{flow.MembersQuery{}, flow.SortName, "ann,bob,cy,opal,root"},
		{flow.MembersQuery{Sort: "nope"}, flow.SortName, "ann,bob,cy,opal,root"},
		{flow.MembersQuery{Sort: flow.SortName, Desc: true}, flow.SortName, "root,opal,cy,bob,ann"},
		{flow.MembersQuery{Sort: flow.SortUsername}, flow.SortUsername, "ann,bob,cy,opal,root"},
		{flow.MembersQuery{Sort: flow.SortEmail}, flow.SortEmail, "opal,ann,bob,cy,root"},
		{flow.MembersQuery{Sort: flow.SortStatus}, flow.SortStatus, "ann,opal,root,cy,bob"},
		{flow.MembersQuery{Sort: flow.SortStatus, Desc: true}, flow.SortStatus, "bob,cy,root,opal,ann"},
		{flow.MembersQuery{Q: "x.org", Sort: flow.SortEmail, Desc: true}, flow.SortEmail, "root,cy,bob,ann,opal"},
	} {
		p, err := s.Members(ctx, root, c.q)
		if err != nil || usernames(p.Rows) != c.want || p.Sort != c.sort || p.Desc != c.q.Desc {
			t.Errorf("%+v: %s %s %v %v", c.q, usernames(p.Rows), p.Sort, p.Desc, err)
		}
	}
	p, _ := s.Members(ctx, root, flow.MembersQuery{Sort: flow.SortStatus})
	for _, r := range p.Rows {
		want := map[string]string{"bob": "banned", "cy": "disabled"}[r.Username]
		if want == "" {
			want = "active"
		}
		if r.Status() != want {
			t.Errorf("%s: %s", r.Username, r.Status())
		}
	}
}

// A page outside the list is its nearest page.
func TestMembersPages(t *testing.T) {
	s, f, _ := setup(t, true)
	for i := range 2*flow.MembersPerPage + 9 {
		n := fmt.Sprintf("u%03d", i)
		f.Users = append(f.Users, idp.User{ID: n, Username: n, Email: n + "@x.org"})
	}
	total := 2*flow.MembersPerPage + 10 // and root
	for _, c := range []struct{ page, want, rows int }{
		{0, 1, flow.MembersPerPage}, {-3, 1, flow.MembersPerPage}, {1, 1, flow.MembersPerPage},
		{2, 2, flow.MembersPerPage}, {3, 3, 10}, {4, 3, 10}, {1 << 40, 3, 10},
	} {
		p, err := s.Members(ctx, root, flow.MembersQuery{Sort: flow.SortUsername, Page: c.page})
		if err != nil || p.Page != c.want || len(p.Rows) != c.rows || p.Pages != 3 || p.Total != total {
			t.Errorf("page %d: page %d of %d, %d rows of %d, %v", c.page, p.Page, p.Pages, len(p.Rows), p.Total, err)
		}
	}
	p, _ := s.Members(ctx, root, flow.MembersQuery{Sort: flow.SortUsername, Page: 2})
	// root sorts first, so page 2 starts one short of MembersPerPage.
	if p.Rows[0].Username != fmt.Sprintf("u%03d", flow.MembersPerPage-1) {
		t.Errorf("page 2 starts at %s", p.Rows[0].Username)
	}
	if p, _ := s.Members(ctx, root, flow.MembersQuery{Q: "zzz", Page: 5}); p.Page != 1 || p.Pages != 1 || len(p.Rows) != 0 {
		t.Errorf("no match: %+v", p)
	}
}

func TestBanAndLift(t *testing.T) {
	s, f, st := setupFull(t)
	people(f)
	if _, err := s.Ban(ctx, root, "b", "  "); !errors.Is(err, flow.ErrReasonRequired) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := s.Ban(ctx, root, "b", strings.Repeat("x", 1001)); !errors.Is(err, flow.ErrReasonRequired) {
		t.Fatalf("long reason: %v", err)
	}
	if _, err := s.Ban(ctx, root, "o", "nope"); !errors.Is(err, flow.ErrOperator) {
		t.Fatalf("operator: %v", err)
	}
	if _, err := s.Ban(ctx, rootR, "r", "nope"); !errors.Is(err, flow.ErrSelf) {
		t.Fatalf("self: %v", err)
	}
	if _, err := s.Ban(ctx, alice, "b", "nope"); !errors.Is(err, flow.ErrForbidden) {
		t.Fatalf("member: %v", err)
	}
	if _, err := s.Ban(ctx, root, "nobody", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if f.User("o").Disabled || f.User("r").Disabled {
		t.Fatal("a refused ban disabled someone")
	}
	b, err := s.Ban(ctx, root, "b", "spam")
	if err != nil || !f.User("b").Disabled || b.Username != "bob" || b.BannedBy != "root" {
		t.Fatalf("%+v %v", b, err)
	}
	if banned, _ := st.EmailBanned(ctx, hashOf("bob@x.org")); !banned {
		t.Fatal("the address hash was not recorded")
	}
	if _, err := s.Ban(ctx, root, "b", "again"); !errors.Is(err, store.ErrAlreadyBanned) {
		t.Fatalf("twice: %v", err)
	}
	// Another admin may be banned.
	if _, err := s.Ban(ctx, root, "a", "abuse"); err != nil {
		t.Fatalf("admin: %v", err)
	}
	if _, err := s.Lift(ctx, ann, b.ID); err != nil || f.User("b").Disabled {
		t.Fatalf("lift: %v %v", err, f.User("b").Disabled)
	}
	if _, err := s.Lift(ctx, root, b.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lift twice: %v", err)
	}
	if all, _ := s.Bans(ctx, root); len(all) != 2 {
		t.Fatalf("bans: %+v", all)
	}
}

// A ban the database cannot record is undone at the provider.
func TestUnrecordedBanIsUndone(t *testing.T) {
	s, f, st := setupFull(t)
	people(f)
	if _, err := storetest.Conn(t, st).Exec(ctx, `ALTER TABLE bans ADD CONSTRAINT refuse CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ban(ctx, root, "b", "spam"); err == nil {
		t.Fatal("want an error")
	}
	if f.User("b").Disabled {
		t.Fatal("left disabled without a record")
	}
}

func codeIn(t *testing.T, m *mailtest.Recorder, to string) string {
	t.Helper()
	got := m.To(to)
	if len(got) == 0 {
		t.Fatalf("no mail to %s", to)
	}
	c := regexp.MustCompile(`\n(\d{6})\n`).FindStringSubmatch(got[len(got)-1].Body)
	if c == nil {
		t.Fatalf("no code in %q", got[len(got)-1].Body)
	}
	return c[1]
}

var bobM = flow.Member{Sub: "b", Username: "bob"}

func TestEmailChange(t *testing.T) {
	s, f, m := setup(t, true)
	people(f)
	if err := s.RequestEmailChange(ctx, bobM, "nope"); !errors.Is(err, flow.ErrInvalidEmail) {
		t.Fatal(err)
	}
	if err := s.RequestEmailChange(ctx, bobM, "BOB@x.org"); !errors.Is(err, flow.ErrSameAddress) {
		t.Fatal(err)
	}
	if err := s.RequestEmailChange(ctx, bobM, "new@x.org"); err != nil {
		t.Fatal(err)
	}
	if len(m.To("bob@x.org")) != 0 {
		t.Fatal("the code went to the old address")
	}
	code := codeIn(t, m, "new@x.org")
	if addr, ok, _ := s.PendingEmailChange(ctx, bobM); !ok || addr != "new@x.org" {
		t.Fatal("pending change not reported")
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	if err := s.ConfirmEmailChange(ctx, bobM, wrong); !errors.Is(err, flow.ErrCodeWrong) {
		t.Fatalf("wrong code: %v", err)
	}
	if err := s.ConfirmEmailChange(ctx, bobM, " "+code+" "); err != nil {
		t.Fatal(err)
	}
	if f.User("b").Email != "new@x.org" {
		t.Fatal("address not set")
	}
	old := m.To("bob@x.org")
	if len(old) != 1 || old[0].Subject != "Your email address was changed" {
		t.Fatalf("old address not told: %+v", old)
	}
	if err := s.ConfirmEmailChange(ctx, bobM, code); !errors.Is(err, flow.ErrCodeExpired) {
		t.Fatalf("a code is used once: %v", err)
	}
}

func TestEmailChangeAttemptsAreLimited(t *testing.T) {
	s, f, m := setup(t, true)
	people(f)
	s.RequestEmailChange(ctx, bobM, "new@x.org")
	code := codeIn(t, m, "new@x.org")
	for i := 0; i < flow.MaxCodeAttempts; i++ {
		if err := s.ConfirmEmailChange(ctx, bobM, "x"); !errors.Is(err, flow.ErrCodeWrong) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := s.ConfirmEmailChange(ctx, bobM, code); !errors.Is(err, flow.ErrCodeExpired) {
		t.Fatalf("the right code after the limit: %v", err)
	}
	if f.User("b").Email != "bob@x.org" {
		t.Fatal("changed")
	}
}

func TestEmailChangeExpires(t *testing.T) {
	clock := time.Now()
	s, f, m, _ := setupClock(t, func() time.Time { return clock })
	people(f)
	s.RequestEmailChange(ctx, bobM, "new@x.org")
	code := codeIn(t, m, "new@x.org")
	clock = clock.Add(flow.CodeLifetime + time.Second)
	if err := s.ConfirmEmailChange(ctx, bobM, code); !errors.Is(err, flow.ErrCodeExpired) {
		t.Fatalf("expired: %v", err)
	}
}

func TestEmailChangeRefusesUsedAndBannedAddresses(t *testing.T) {
	s, f, m := setup(t, true)
	people(f)
	f.Users = append(f.Users, idp.User{ID: "e", Username: "eve", Email: "eve@x.org"})
	if _, err := s.Ban(ctx, root, "e", "spam"); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"ann@x.org", "EVE@x.org"} {
		if err := s.RequestEmailChange(ctx, bobM, addr); err != nil {
			t.Fatal(err)
		}
		if err := s.ConfirmEmailChange(ctx, bobM, codeIn(t, m, addr)); !errors.Is(err, flow.ErrAddressRefused) {
			t.Fatalf("%s: %v", addr, err)
		}
	}
	if f.User("b").Email != "bob@x.org" {
		t.Fatal("changed")
	}
}

func TestOperatorsChangeNothingHere(t *testing.T) {
	s, f, m := setup(t, true)
	people(f)
	op := flow.Member{Sub: "o", Username: "opal"}
	if err := s.RequestEmailChange(ctx, op, "new@x.org"); !errors.Is(err, flow.ErrOperator) {
		t.Fatal(err)
	}
	if err := s.ConfirmEmailChange(ctx, op, "123456"); !errors.Is(err, flow.ErrOperator) {
		t.Fatal(err)
	}
	if err := s.SetName(ctx, op, idp.Name{DisplayName: "X"}); !errors.Is(err, flow.ErrOperator) {
		t.Fatal(err)
	}
	if len(m.Messages) != 0 || f.User("o").DisplayName != "Opal" {
		t.Fatal("something changed")
	}
	if _, isOp, _ := s.Profile(ctx, op); !isOp {
		t.Fatal("profile does not report the operator")
	}
}

func TestSetName(t *testing.T) {
	s, f, _ := setup(t, true)
	people(f)
	for _, bad := range []idp.Name{{DisplayName: " "}, {DisplayName: strings.Repeat("x", 101)}, {DisplayName: "B", FirstName: strings.Repeat("x", 51)}, {DisplayName: "B", LastName: strings.Repeat("x", 51)}} {
		if err := s.SetName(ctx, bobM, bad); !errors.Is(err, flow.ErrInvalidNames) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if err := s.SetName(ctx, bobM, idp.Name{DisplayName: " Robert ", FirstName: "Robert", LastName: "B"}); err != nil {
		t.Fatal(err)
	}
	if u := f.User("b"); u.DisplayName != "Robert" || u.FirstName != "Robert" || u.LastName != "B" || u.Username != "bob" {
		t.Fatalf("%+v", u)
	}
}

func TestRecovery(t *testing.T) {
	s, f, m := setup(t, true)
	people(f)
	if _, err := s.OpenRecovery(ctx, root, "bob", "fresh@x.org", ""); !errors.Is(err, flow.ErrReasonRequired) {
		t.Fatal(err)
	}
	if _, err := s.OpenRecovery(ctx, root, "opal", "fresh@x.org", "call"); !errors.Is(err, flow.ErrOperator) {
		t.Fatal(err)
	}
	if _, err := s.OpenRecovery(ctx, rootR, "root", "fresh@x.org", "call"); !errors.Is(err, flow.ErrSelf) {
		t.Fatal(err)
	}
	if _, err := s.OpenRecovery(ctx, root, "nobody", "fresh@x.org", "call"); !errors.Is(err, flow.ErrNoSuchMember) {
		t.Fatal(err)
	}
	r, err := s.OpenRecovery(ctx, root, "BOB", "fresh@x.org", "video call")
	if err != nil {
		t.Fatal(err)
	}
	if n := m.To("ann@x.org"); len(n) != 1 || n[0].Subject != "A recovery needs a second admin" {
		t.Fatalf("admins not told: %+v", n)
	}
	if err := s.ApproveRecovery(ctx, root, r.ID); !errors.Is(err, store.ErrSameAdmin) {
		t.Fatalf("same admin: %v", err)
	}
	if f.User("b").Email != "bob@x.org" || f.LinksSent != 0 {
		t.Fatal("the opener's approval changed something")
	}
	if err := s.ApproveRecovery(ctx, ann, r.ID); err != nil {
		t.Fatal(err)
	}
	if f.User("b").Email != "fresh@x.org" || f.LinksSent != 1 || f.LinksTo[0] != "b" {
		t.Fatalf("email %q links %v", f.User("b").Email, f.LinksTo)
	}
	if err := s.ApproveRecovery(ctx, ann, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approved twice: %v", err)
	}
}

func TestRecoveryCancelAndExpiry(t *testing.T) {
	clock := time.Now()
	s, f, _, _ := setupClock(t, func() time.Time { return clock })
	people(f)
	r, _ := s.OpenRecovery(ctx, root, "bob", "fresh@x.org", "call")
	if err := s.CancelRecovery(ctx, ann, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveRecovery(ctx, ann, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancelled: %v", err)
	}
	r, _ = s.OpenRecovery(ctx, root, "bob", "fresh@x.org", "call")
	clock = clock.Add(flow.RecoveryLifetime + time.Minute)
	if err := s.ApproveRecovery(ctx, ann, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
	rows, _ := s.Recoveries(ctx, root)
	if len(rows) != 2 || !rows[0].Expired || rows[0].Live() {
		t.Fatalf("%+v", rows)
	}
	if f.LinksSent != 0 || f.User("b").Email != "bob@x.org" {
		t.Fatal("changed")
	}
}

// A request for a member who has since become an operator, or whose new
// address is taken by now, changes nothing and can be approved later.
func TestRecoveryRechecksAtApproval(t *testing.T) {
	s, f, _ := setup(t, true)
	people(f)
	r, _ := s.OpenRecovery(ctx, root, "bob", "ann@x.org", "call")
	if err := s.ApproveRecovery(ctx, ann, r.ID); !errors.Is(err, flow.ErrAddressRefused) {
		t.Fatalf("taken address: %v", err)
	}
	if rows, _ := s.Recoveries(ctx, root); !rows[0].Live() {
		t.Fatal("a refused approval must leave the request open")
	}
	if err := s.ApproveRecovery(ctx, flow.Member{Sub: "b", Username: "bob", Admin: true}, r.ID); !errors.Is(err, flow.ErrSelf) {
		t.Fatalf("the member approving their own: %v", err)
	}
}

// No log line carries a code or an address, whatever the outcome.
func TestLogsCarryNoCodesOrAddresses(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	s, f, m := setup(t, true)
	people(f)
	s.RequestEmailChange(ctx, bobM, "secret.new@x.org")
	code := codeIn(t, m, "secret.new@x.org")
	s.ConfirmEmailChange(ctx, bobM, "999999")
	s.ConfirmEmailChange(ctx, bobM, code)
	s.Ban(ctx, root, "a", "spam")
	a := bob()
	a.Email = "ann@x.org"
	s.Register(ctx, a)
	a.Email = "bob@x.org"
	s.Register(ctx, a)
	s.OpenRecovery(ctx, root, "bob", "secret.recovery@x.org", "call")
	out := buf.String()
	for _, bad := range []string{code, "secret.new", "secret.recovery", "ann@x.org", "bob@x.org", "@x.org"} {
		if strings.Contains(out, bad) {
			t.Errorf("log carries %q:\n%s", bad, out)
		}
	}
}
