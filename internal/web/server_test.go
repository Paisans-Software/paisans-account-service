package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/paisans-software/paisans-invite/internal/coc"
	"github.com/paisans-software/paisans-invite/internal/flow"
	"github.com/paisans-software/paisans-invite/internal/idp"
	"github.com/paisans-software/paisans-invite/internal/idp/idptest"
	"github.com/paisans-software/paisans-invite/internal/mail/mailtest"
	"github.com/paisans-software/paisans-invite/internal/store/storetest"
)

type harness struct {
	h    http.Handler
	s    sessions
	fake *idptest.Fake
	flow *flow.Service
}

func newHarness(t *testing.T, approval bool) harness {
	st := storetest.New(t)
	fake := &idptest.Fake{Users: []idp.User{{ID: "r", Username: "root", Email: "root@x.org", IsAdmin: true}}}
	key := make([]byte, 32)
	f := flow.New(st, fake, &mailtest.Recorder{}, coc.CoC{Version: "v", HTML: "<p>Be kind</p>"}, flow.Settings{
		Quota: 1, RequireApproval: approval, InviteLifetime: time.Hour, PublicURL: "https://invite.example.org", EmailHashKey: key,
	}, time.Now)
	r, _ := newRenderer("", "https://id.example.org")
	s := sessions{key: key}
	return harness{h: NewServer(Options{Flow: f, Sessions: s, Auth: http.NotFoundHandler(), Render: r, Health: st, SessionKey: key}), s: s, fake: fake, flow: f}
}

func (h harness) as(sess Session, method, path string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.s.write(rec, sess)
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	out := httptest.NewRecorder()
	h.h.ServeHTTP(out, req)
	return out
}

var member = Session{Sub: "s-alice", Username: "alice", CSRF: "tok", Exp: time.Now().Add(time.Hour).Unix()}
var admin = Session{Sub: "s-root", Username: "root", Admin: true, CSRF: "tok", Exp: time.Now().Add(time.Hour).Unix()}

func TestAnonymousRedirectsToLogin(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 302 || rec.Header().Get("Location") != "/auth/login" {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCreateInviteShowsLinkOnceAndEnforcesQuota(t *testing.T) {
	h := newHarness(t, true)
	rec := h.as(member, "POST", "/invites", url.Values{"csrf": {"tok"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "https://invite.example.org/i/") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = h.as(member, "POST", "/invites", url.Values{"csrf": {"tok"}})
	if !strings.Contains(rec.Body.String(), "quota") {
		t.Fatal("second invite past a quota of 1 must be refused with a message")
	}
	if rec = h.as(admin, "POST", "/invites", url.Values{"csrf": {"tok"}}); !strings.Contains(rec.Body.String(), "/i/") {
		t.Fatal("admins have no quota")
	}
}

func TestCSRFRequired(t *testing.T) {
	h := newHarness(t, true)
	if rec := h.as(member, "POST", "/invites", url.Values{"csrf": {"wrong"}}); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestMemberCannotSeeAdmin(t *testing.T) {
	h := newHarness(t, true)
	if rec := h.as(member, "GET", "/admin", nil); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := h.as(member, "POST", "/admin/applications/1/approve", url.Values{"csrf": {"tok"}}); rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestAdminApproves(t *testing.T) {
	h := newHarness(t, true)
	link, _, _ := h.flow.CreateInvite(context.Background(), flow.Member{Sub: "s-alice", Username: "alice"})
	tok := link[strings.LastIndex(link, "/")+1:]
	h.flow.Apply(context.Background(), tok, flow.Applicant{Username: "bob", Email: "bob@x.org", DisplayName: "Bob", Agreed: true})
	rec := h.as(admin, "GET", "/admin", nil)
	if !strings.Contains(rec.Body.String(), "bob@x.org") {
		t.Fatal("queue must list the application")
	}
	p, _ := h.flow.Pending(context.Background())
	rec = h.as(admin, "POST", "/admin/applications/"+itoa(p[0].ID)+"/approve", url.Values{"csrf": {"tok"}})
	if rec.Code != 303 || h.fake.Created != 1 {
		t.Fatalf("%d created=%d", rec.Code, h.fake.Created)
	}
}

func TestHealthz(t *testing.T) {
	h := newHarness(t, true)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}
