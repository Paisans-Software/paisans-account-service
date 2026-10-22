package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/config"
	"github.com/paisans-software/paisans-account-service/internal/flow"
	"github.com/paisans-software/paisans-account-service/internal/idp"
)

var ann = Session{Sub: "s-ann", Username: "ann", Admin: true, CSRF: "tok", Exp: time.Now().Add(time.Hour).Unix()}
var opal = Session{Sub: "s-opal", Username: "opal", Admin: true, CSRF: "tok", Exp: time.Now().Add(time.Hour).Unix()}

func withAnn(h harness) harness {
	h.fake.Users = append(h.fake.Users, idp.User{ID: "s-ann", Username: "ann", Email: "ann@x.org"})
	return h
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func registerForm(t *testing.T, h harness) url.Values {
	t.Helper()
	rec := h.get(joinHost, "/")
	m := csrfField.FindStringSubmatch(rec.Body.String())
	if rec.Code != 200 || m == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	f := form(strings.ReplaceAll(m[1], "&#43;", "+"))
	return f
}

func TestRegisterThroughTheJoinHost(t *testing.T) {
	h := newHarnessSignup(t, config.SignupOpen)
	f := registerForm(t, h)
	if !strings.Contains(h.get(joinHost, "/").Body.String(), "Be kind") {
		t.Fatal("no code of conduct")
	}
	rec := post(h, "/", f)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<h1>Thanks for asking to join</h1>") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if body := h.as(admin, "GET", "/admin", nil).Body.String(); !strings.Contains(body, "registered") || strings.Contains(body, "invited by") {
		t.Fatalf("queue: %s", body)
	}
	bad := registerForm(t, h)
	bad.Set("username", "-x")
	if rec := post(h, "/", bad); rec.Code != 422 || !strings.Contains(rec.Body.String(), "Username must") {
		t.Fatalf("validation: %d", rec.Code)
	}
	for name, c := range map[string]string{"missing": "", "forged": "x.y", "invitee": inviteeCSRF(make([]byte, 32), make([]byte, 32))} {
		g := registerForm(t, h)
		g.Set("csrf", c)
		g.Set("username", "carol")
		if rec := post(h, "/", g); rec.Code != 403 {
			t.Errorf("%s csrf: %d", name, rec.Code)
		}
	}
	expired, _ := sign(make([]byte, 32), "register", registerToken{N: "n", Exp: time.Now().Add(-time.Minute).Unix()})
	g := registerForm(t, h)
	g.Set("csrf", expired)
	if rec := post(h, "/", g); rec.Code != 403 {
		t.Errorf("expired csrf: %d", rec.Code)
	}
}

// A banned address and an address in use get the page an accepted
// application gets, word for word.
func TestRefusedSignupPageIsTheAcceptedOne(t *testing.T) {
	h := newHarnessSignup(t, config.SignupOpen)
	if _, err := h.flow.Ban(context.Background(), admin.member(), "s-alice", "spam"); err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{}
	for name, addr := range map[string]string{"banned": "ALICE@x.org", "in use": "root@x.org", "fresh": "new@x.org"} {
		f := registerForm(t, h)
		f.Set("email", addr)
		f.Set("username", strings.ReplaceAll(name, " ", ""))
		rec := post(h, "/", f)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", name, rec.Code)
		}
		pages[name] = rec.Body.String()
	}
	if pages["banned"] != pages["fresh"] || pages["in use"] != pages["fresh"] {
		t.Fatalf("pages differ:\n%s\n%s\n%s", pages["banned"], pages["in use"], pages["fresh"])
	}
}

func TestEveryNewPostNeedsCSRF(t *testing.T) {
	h := withAnn(newHarness(t, true))
	r, err := h.flow.OpenRecovery(context.Background(), admin.member(), "alice", "a2@x.org", "call")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := h.flow.Ban(context.Background(), admin.member(), "s-ann", "spam")
	for _, c := range []struct {
		s    Session
		path string
	}{
		{member, "/account/email"},
		{member, "/account/email/confirm"},
		{member, "/account/name"},
		{admin, "/admin/members/s-alice/ban"},
		{admin, "/admin/members/s-alice/groups"},
		{admin, "/admin/bans/" + itoa(b.ID) + "/lift"},
		{admin, "/admin/recovery"},
		{ann, "/admin/recovery/" + itoa(r.ID) + "/approve"},
		{admin, "/admin/recovery/" + itoa(r.ID) + "/cancel"},
	} {
		f := url.Values{"csrf": {"wrong"}, "reason": {"x"}, "email": {"n@x.org"}, "display_name": {"N"}, "code": {"1"}, "username": {"alice"}}
		if rec := h.as(c.s, "POST", c.path, f); rec.Code != 403 {
			t.Errorf("%s: %d", c.path, rec.Code)
		}
	}
	if h.fake.User("s-alice").Disabled || !h.fake.User("s-ann").Disabled || h.fake.LinksSent != 0 || len(h.mail.To("n@x.org")) != 0 {
		t.Fatal("a refused post acted")
	}
	if rows, _ := h.flow.Recoveries(context.Background(), admin.member()); len(rows) != 1 || !rows[0].Live() {
		t.Fatal("recovery changed")
	}
}

func TestMembersCannotReachAdminPages(t *testing.T) {
	h := newHarness(t, true)
	for _, p := range []string{"/admin/members?q=a", "/admin/members/s-root/ban", "/admin/bans", "/admin/recovery"} {
		if rec := h.as(member, "GET", p, nil); rec.Code != 403 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := h.as(member, "POST", "/admin/members/s-root/ban", url.Values{"csrf": {"tok"}, "reason": {"x"}}); rec.Code != 403 || h.fake.User("s-root").Disabled {
		t.Fatalf("%d", rec.Code)
	}
}

func TestBanFromMembersPage(t *testing.T) {
	h := newHarness(t, true)
	body := h.as(admin, "GET", "/admin/members?q=x.org", nil).Body.String()
	if !strings.Contains(body, `action="/admin/members/s-alice/ban"`) ||
		strings.Contains(body, `action="/admin/members/s-opal/ban"`) || strings.Contains(body, `action="/admin/members/s-root/ban"`) {
		t.Fatalf("ban buttons: %s", body)
	}
	page := h.as(admin, "GET", "/admin/members/s-alice/ban", nil).Body.String()
	if op := h.as(admin, "GET", "/admin/members/s-opal/ban", nil).Body.String(); !strings.Contains(op, "<p>opal is an operator. Operators can only be managed in the Paisan Stack deployment configuration.</p>") {
		t.Errorf("operator ban page: %s", op)
	}
	if !strings.Contains(page, "keeps them signed in until that session expires") {
		t.Fatalf("no session notice: %s", page)
	}
	if rec := h.as(admin, "POST", "/admin/members/s-alice/ban", url.Values{"csrf": {"tok"}, "reason": {" "}}); rec.Code != 422 || h.fake.User("s-alice").Disabled {
		t.Fatalf("no reason: %d", rec.Code)
	}
	rec := h.as(admin, "POST", "/admin/members/s-alice/ban", url.Values{"csrf": {"tok"}, "reason": {hostile}})
	if rec.Code != 200 || !h.fake.User("s-alice").Disabled || !strings.Contains(rec.Body.String(), "keeps them signed in until that session expires") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	bans := h.as(admin, "GET", "/admin/bans", nil).Body.String()
	if strings.Contains(bans, "<script>") || !strings.Contains(bans, "&lt;script&gt;") {
		t.Fatalf("reason not escaped: %s", bans)
	}
	for who, id := range map[string]string{"operator": "s-opal", "self": "s-root"} {
		rec := h.as(admin, "POST", "/admin/members/"+id+"/ban", url.Values{"csrf": {"tok"}, "reason": {"x"}})
		if rec.Code != 422 || h.fake.User(id).Disabled {
			t.Errorf("%s: %d", who, rec.Code)
		}
	}
	if rec := h.as(admin, "GET", "/admin/members/nobody/ban", nil); rec.Code != 404 {
		t.Errorf("missing member: %d", rec.Code)
	}
	b, _ := h.st.Bans(context.Background())
	rec = h.as(admin, "POST", "/admin/bans/"+itoa(b[0].ID)+"/lift", url.Values{"csrf": {"tok"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/admin/bans?done=lifted" || h.fake.User("s-alice").Disabled {
		t.Fatalf("lift: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(h.as(admin, "GET", "/admin/bans?done=lifted", nil).Body.String(), "The ban is lifted") {
		t.Fatal("no notice")
	}
	if strings.Contains(h.as(admin, "GET", "/admin/bans?done=evil+text", nil).Body.String(), "evil") {
		t.Fatal("notice text from the link")
	}
}

func TestChangeEmailThroughThePages(t *testing.T) {
	h := newHarness(t, true)
	rec := h.as(member, "POST", "/account/email", url.Values{"csrf": {"tok"}, "email": {"new@x.org"}})
	if rec.Code != 303 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	page := h.as(member, "GET", "/account/email", nil).Body.String()
	if !strings.Contains(page, "We sent a six-digit code to new@x.org") {
		t.Fatalf("%s", page)
	}
	msgs := h.mail.To("new@x.org")
	code := regexp.MustCompile(`\n(\d{6})\n`).FindStringSubmatch(msgs[0].Body)[1]
	if rec := h.as(member, "POST", "/account/email/confirm", url.Values{"csrf": {"tok"}, "code": {"abc"}}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "That code is not right.") {
		t.Fatalf("wrong: %d", rec.Code)
	}
	rec = h.as(member, "POST", "/account/email/confirm", url.Values{"csrf": {"tok"}, "code": {code}})
	if rec.Code != 303 || h.fake.User("s-alice").Email != "new@x.org" || len(h.mail.To("alice@x.org")) != 1 {
		t.Fatalf("%d %q", rec.Code, h.fake.User("s-alice").Email)
	}
	op := h.as(opal, "GET", "/account/email", nil).Body.String()
	if strings.Contains(op, `action="/account/email"`) || !strings.Contains(op, "<p>You are an operator. Operators can only change their address in the Paisan Stack deployment configuration.</p>") {
		t.Fatalf("operator page: %s", op)
	}
	if rec := h.as(opal, "POST", "/account/email", url.Values{"csrf": {"tok"}, "email": {"o2@x.org"}}); rec.Code != 422 || len(h.mail.To("o2@x.org")) != 0 {
		t.Fatalf("operator post: %d", rec.Code)
	}
}

func TestChangeNameThroughThePages(t *testing.T) {
	h := newHarness(t, true)
	if page := h.as(member, "GET", "/account/name", nil).Body.String(); !strings.Contains(page, `value="Alice"`) {
		t.Fatal(page)
	}
	rec := h.as(member, "POST", "/account/name", url.Values{"csrf": {"tok"}, "display_name": {hostile}, "first_name": {"Al"}, "last_name": {"Ice"}, "username": {"mallory"}})
	if rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	u := h.fake.User("s-alice")
	if u.DisplayName != hostile || u.FirstName != "Al" || u.Username != "alice" {
		t.Fatalf("%+v", u)
	}
	if page := h.as(member, "GET", "/account/name", nil).Body.String(); strings.Contains(page, "<script>") {
		t.Fatal("not escaped")
	}
	if rec := h.as(opal, "POST", "/account/name", url.Values{"csrf": {"tok"}, "display_name": {"X"}}); rec.Code != 422 || h.fake.User("s-opal").DisplayName != "Opal" {
		t.Fatalf("operator: %d", rec.Code)
	}
	if op := h.as(opal, "GET", "/account/name", nil).Body.String(); strings.Contains(op, `name="display_name"`) ||
		!strings.Contains(op, "<p>You are an operator. Operators can only change their name in the Paisan Stack deployment configuration.</p>") {
		t.Fatalf("operator page: %s", op)
	}
}

func TestRecoveryThroughThePages(t *testing.T) {
	h := withAnn(newHarness(t, true))
	h.fake.Groups["admins"] = append(h.fake.Groups["admins"], "ann")
	rec := h.as(admin, "POST", "/admin/recovery", url.Values{"csrf": {"tok"}, "username": {"alice"}, "email": {"fresh@x.org"}, "reason": {"video call"}})
	if rec.Code != 303 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := h.as(admin, "POST", "/admin/recovery", url.Values{"csrf": {"tok"}, "username": {"opal"}, "email": {"o@x.org"}, "reason": {"x"}}); rec.Code != 422 {
		t.Fatalf("operator: %d", rec.Code)
	}
	rows, _ := h.flow.Recoveries(context.Background(), admin.member())
	id := itoa(rows[0].ID)
	if body := h.as(admin, "GET", "/admin/recovery", nil).Body.String(); strings.Contains(body, `action="/admin/recovery/`+id+`/approve"`) || !strings.Contains(body, "Waiting for another admin") {
		t.Fatalf("the opener is offered approval: %s", body)
	}
	rec = h.as(admin, "POST", "/admin/recovery/"+id+"/approve", url.Values{"csrf": {"tok"}})
	if rec.Header().Get("Location") != "/admin/recovery?error=same" || h.fake.LinksSent != 0 {
		t.Fatalf("same admin: %s", rec.Header().Get("Location"))
	}
	if body := h.as(ann, "GET", "/admin/recovery", nil).Body.String(); !strings.Contains(body, `action="/admin/recovery/`+id+`/approve"`) {
		t.Fatalf("no approve for the second admin: %s", body)
	}
	rec = h.as(ann, "POST", "/admin/recovery/"+id+"/approve", url.Values{"csrf": {"tok"}})
	if rec.Header().Get("Location") != "/admin/recovery?done=approved" || h.fake.User("s-alice").Email != "fresh@x.org" || h.fake.LinksSent != 1 {
		t.Fatalf("approve: %s", rec.Header().Get("Location"))
	}
}

// An operator who is the only admin is told the recovery happens on submit,
// and it does; anyone else sees the two-admin wording.
func TestSoleOperatorRecoveryThroughThePages(t *testing.T) {
	h := newHarness(t, true)
	h.fake.Groups["admins"] = []string{"opal"}
	const now = "the recovery happens as soon as you submit"
	if body := h.as(opal, "GET", "/admin/recovery", nil).Body.String(); !strings.Contains(body, now) || strings.Contains(body, "Another admin must approve") {
		t.Fatalf("sole operator-admin page: %s", body)
	}
	rec := h.as(opal, "POST", "/admin/recovery", url.Values{"csrf": {"tok"}, "username": {"alice"}, "email": {"fresh@x.org"}, "reason": {"video call"}})
	if rec.Header().Get("Location") != "/admin/recovery?done=recovered" || h.fake.User("s-alice").Email != "fresh@x.org" || h.fake.LinksSent != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	if len(h.mail.To("opal@x.org")) != 0 {
		t.Fatal("the sole admin was asked for a second admin")
	}
	body := h.as(opal, "GET", "/admin/recovery?done=recovered", nil).Body.String()
	if !strings.Contains(body, "The account is recovered") || !strings.Contains(body, "recovered by opal alone, as the sole operator-admin") {
		t.Fatalf("after: %s", body)
	}

	h.fake.Groups["admins"] = []string{"opal", "root"}
	if body := h.as(opal, "GET", "/admin/recovery", nil).Body.String(); strings.Contains(body, now) || !strings.Contains(body, "Another admin must approve") {
		t.Fatalf("operator with another admin: %s", body)
	}
	h.fake.Groups["admins"] = []string{"root"}
	if body := h.as(admin, "GET", "/admin/recovery", nil).Body.String(); strings.Contains(body, now) {
		t.Fatalf("sole admin who is not an operator: %s", body)
	}
}

// Code entry is limited per client address, beyond the attempts each change
// allows.
func TestCodeEntryIsRateLimited(t *testing.T) {
	h := newHarness(t, true)
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		rec := httptest.NewRecorder()
		h.s.write(rec, member)
		req := httptest.NewRequest("POST", "http://"+accountHost+"/account/email/confirm", strings.NewReader("csrf=tok&code=1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "198.51.100.7:1"
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		out := httptest.NewRecorder()
		h.h.ServeHTTP(out, req)
		limited = out.Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("never limited")
	}
}

// memberRowRE matches a members table row's username cell.
var memberRowRE = regexp.MustCompile(`<td><code>([^<]*)</code></td>`)

// rowOrder is the order the members table lists the usernames in.
func rowOrder(body string) string {
	var got []string
	for _, m := range memberRowRE.FindAllStringSubmatch(body, -1) {
		got = append(got, m[1])
	}
	return strings.Join(got, ",")
}

// With no query the members page lists everyone, by name, with each row's
// status.
func TestMembersPageListsEveryone(t *testing.T) {
	h := newHarness(t, true)
	h.fake.Users = append(h.fake.Users, idp.User{ID: "s-dan", Username: "dan", Email: "dan@x.org", DisplayName: "Dan", Disabled: true})
	if _, err := h.flow.Ban(context.Background(), admin.member(), "s-alice", "spam"); err != nil {
		t.Fatal(err)
	}
	rec := h.as(admin, "GET", "/admin/members", nil)
	body := rec.Body.String()
	if rec.Code != 200 || rowOrder(body) != "alice,dan,opal,root" {
		t.Fatalf("%d %s\n%s", rec.Code, rowOrder(body), body)
	}
	for _, want := range []string{
		`<td>banned</td>`, `<td>disabled</td>`, `<td>active</td>`, `<a href="/admin/bans">Banned</a>`,
		`<span class="muted">Operator</span>`, `<span class="muted">You</span>`, `action="/admin/members/s-dan/ban"`,
		`<th aria-sort="ascending"><a href="/admin/members?dir=desc">Name ↑</a></th>`,
		`<th aria-sort="none"><a href="/admin/members?sort=status">Status</a></th>`,
		`4 members`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %s", want)
		}
	}
	if strings.Contains(body, `class="pager"`) {
		t.Error("one page has a pager")
	}
}

// A column's header sorts by it, and again reverses it; the search, and the
// sort in the search form, are kept. Unknown values are the defaults.
func TestMembersPageSorts(t *testing.T) {
	h := newHarness(t, true)
	h.fake.Users[0].Email = "z-root@x.org"
	for path, want := range map[string]string{
		"/admin/members?sort=email":                    "alice,opal,root",
		"/admin/members?sort=email&dir=desc":           "root,opal,alice",
		"/admin/members?sort=username&dir=desc":        "root,opal,alice",
		"/admin/members?q=L&sort=username&dir=desc":    "opal,alice",
		"/admin/members?q=l&sort=status&dir=sideways":  "alice,opal",
		"/admin/members?sort=joined&dir=desc":          "root,opal,alice",
		"/admin/members?sort=%3Cscript%3E&page=banana": "alice,opal,root",
	} {
		rec := h.as(admin, "GET", path, nil)
		if got := rowOrder(rec.Body.String()); rec.Code != 200 || got != want {
			t.Errorf("%s: %d %s", path, rec.Code, got)
		}
	}
	body := h.as(admin, "GET", "/admin/members?q=l&sort=username&dir=desc", nil).Body.String()
	for _, want := range []string{
		`<th aria-sort="descending"><a href="/admin/members?q=l&amp;sort=username">Username ↓</a></th>`,
		`<th aria-sort="none"><a href="/admin/members?q=l&amp;sort=email">Email</a></th>`,
		`<th aria-sort="none"><a href="/admin/members?q=l">Name</a></th>`,
		`<input type="hidden" name="sort" value="username">`, `<input type="hidden" name="dir" value="desc">`,
		`value="l"`, `2 members match`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %s", want)
		}
	}
	if body := h.as(admin, "GET", "/admin/members?q=zzz", nil).Body.String(); !strings.Contains(body, "Nobody matches.") {
		t.Errorf("no match: %s", body)
	}
}

// Pages beyond either end show the nearest page; the links keep the search
// and the sort.
func TestMembersPagePages(t *testing.T) {
	h := newHarness(t, true)
	for i := range 2 * flow.MembersPerPage {
		n := fmt.Sprintf("u%03d", i)
		h.fake.Users = append(h.fake.Users, idp.User{ID: "s-" + n, Username: n, Email: n + "@x.org"})
	}
	// All 103 match, so sorted by username: alice, opal, root, u000, ...
	first := "/admin/members?q=x.org&amp;sort=username"
	second := "/admin/members?page=2&amp;q=x.org&amp;sort=username"
	third := "/admin/members?page=3&amp;q=x.org&amp;sort=username"
	for _, c := range []struct{ page, first, prev, next string }{
		{"", "alice", "", second},
		{"1", "alice", "", second},
		{"0", "alice", "", second},
		{"-1", "alice", "", second},
		{"99999999999999999999", "alice", "", second},
		{"2", "u047", first, third},
		{"3", "u097", second, ""},
		{"4", "u097", second, ""},
	} {
		rec := h.as(admin, "GET", "/admin/members?q=x.org&sort=username&page="+c.page, nil)
		body := rec.Body.String()
		rows := strings.Split(rowOrder(body), ",")
		if rec.Code != 200 || rows[0] != c.first {
			t.Errorf("page %q: %d starts at %s", c.page, rec.Code, rows[0])
		}
		for _, l := range []struct{ name, href string }{{"Previous", c.prev}, {"Next", c.next}} {
			link := `<a href="` + l.href + `">` + l.name + `</a>`
			if has := strings.Contains(body, ">"+l.name+"</a>"); has != (l.href != "") || (has && !strings.Contains(body, link)) {
				t.Errorf("page %q: want %q", c.page, link)
			}
		}
		if !strings.Contains(body, "103 members match") || !strings.Contains(body, " of 3") {
			t.Errorf("page %q: no count", c.page)
		}
	}
}
