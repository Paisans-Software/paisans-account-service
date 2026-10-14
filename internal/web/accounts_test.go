package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/config"
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
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<h1>Thanks for applying</h1>") {
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
	if strings.Contains(op, `action="/account/email"`) || !strings.Contains(op, "Operators change their address in the deployment's configuration.") {
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
